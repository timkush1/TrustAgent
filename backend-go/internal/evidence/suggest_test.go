package evidence

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

const doc = "Annual plans can be refunded within 30 days of purchase. Monthly plans are not refundable."

func sys(content string) []ScanMessage {
	return []ScanMessage{{Role: "system", Content: content}, {Role: "user", Content: "Can I get a refund?"}}
}

func defaults(string) Settings { return DefaultSettings() }

func TestSuggestByKind(t *testing.T) {
	cases := []struct {
		name      string
		messages  func(i int) []ScanMessage
		settings  func(string) Settings
		kind      string
		label     string
		wouldGain int
	}{
		{"unknown tag in the system prompt", func(i int) []ScanMessage {
			return sys(fmt.Sprintf("You are Acme support.\n<kb_article id=\"a%d\">%s</kb_article>", i, doc))
		}, defaults, SuggestTagPattern, "<kb_article> blocks in the system prompt", 3},
		{"unknown tag in user messages", func(int) []ScanMessage {
			return []ScanMessage{{Role: "user", Content: "<ticket_notes>" + doc + "</ticket_notes>\nCan I get a refund?"}}
		}, defaults, SuggestTagPattern, "<ticket_notes> blocks in user messages", 3},
		{"markdown heading", func(int) []ScanMessage {
			return sys("You are Acme support.\n\n## Refund facts\n" + doc + "\n\n## Tone\nBe brief.")
		}, defaults, SuggestLineHeader, `"## Refund facts" sections in the system prompt`, 3},
		{"colon header", func(int) []ScanMessage {
			return sys("You are Acme support.\n\nRefund facts:\n" + doc + "\n\nAnswer briefly.")
		}, defaults, SuggestLineHeader, `"Refund facts:" sections in the system prompt`, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bodies := []ScanBody{}
			for i := range 3 {
				bodies = append(bodies, ScanBody{TraceID: fmt.Sprintf("t%d", i), Route: "support-bot", Messages: c.messages(i)})
			}
			// A body without the shape gains nothing.
			bodies = append(bodies, ScanBody{TraceID: "plain", Route: "support-bot", Messages: sys("You are Acme support.")})
			got := Suggest(bodies, c.settings)
			if len(got) != 2 {
				t.Fatalf("suggestions = %+v", got)
			}
			s := got[0]
			if s.Kind != c.kind || s.Label != c.label || s.WouldGain != c.wouldGain || s.Sample != 4 || s.ExampleTraceID == nil || *s.ExampleTraceID != "t0" {
				t.Errorf("suggestion = %+v", s)
			}
			if s.Pattern == nil || !ValidSuggestion(TagPattern{Pattern: s.Pattern.Pattern, Flags: s.Pattern.Flags}) {
				t.Errorf("pattern = %+v", s.Pattern)
			}
			// Applying the pattern really gives the scanned requests evidence.
			st := DefaultSettings()
			st.TagPatterns = []TagPattern{{Pattern: s.Pattern.Pattern, Flags: s.Pattern.Flags}}
			res, err := Extract(chatOf(c.messages(0)), &st)
			if err != nil || !res.Trusted() || !strings.Contains(res.Items[0].Text, "30 days") || strings.Contains(res.Items[0].Text, "Be brief") {
				t.Errorf("extraction with the pattern: %+v err %v", res, err)
			}
			ext := got[1]
			if ext.Kind != SuggestExtension || ext.Pattern != nil || ext.WouldGain != 0 || ext.Label != LabelExtensionSuggestion || ext.ExampleTraceID != nil {
				t.Errorf("extension = %+v", ext)
			}
		})
	}
}

func TestSuggestLeavesOutWhatGainsNothing(t *testing.T) {
	bodies := []ScanBody{}
	for i := range 3 {
		bodies = append(bodies, ScanBody{TraceID: fmt.Sprintf("t%d", i), Route: "r", Messages: sys(
			"<rules>" + doc + "</rules>\n## Examples\n" + doc + "\n<kb_article>" + doc + "</kb_article>")})
	}
	// Instruction-like shapes are never suggested.
	for _, s := range Suggest(bodies, defaults) {
		if strings.Contains(s.Label, "rules") || strings.Contains(s.Label, "Examples") {
			t.Errorf("instruction-like shape suggested: %+v", s)
		}
	}
	// A route that reads nothing from the system prompt gains nothing from system-prompt blocks.
	off := func(string) Settings { s := DefaultSettings(); s.SystemMode = SystemModeOff; return s }
	if got := Suggest(bodies, off); len(got) != 1 || got[0].Kind != SuggestExtension {
		t.Errorf("system mode off: %+v", got)
	}
	// A shape seen in one body of many is not a repeated shape.
	once := append([]ScanBody{{TraceID: "x", Route: "r", Messages: sys("<faq_entry>" + doc + "</faq_entry>")}},
		ScanBody{TraceID: "y", Route: "r", Messages: sys("You are Acme support.")})
	if got := Suggest(once, defaults); len(got) != 1 {
		t.Errorf("single occurrence: %+v", got)
	}
	// Too little text in the block is not a document.
	short := []ScanBody{{TraceID: "a", Route: "r", Messages: sys("<note>hi</note>")}, {TraceID: "b", Route: "r", Messages: sys("<note>hi</note>")}}
	if got := Suggest(short, defaults); len(got) != 1 {
		t.Errorf("short blocks: %+v", got)
	}
	if got := Suggest(nil, defaults); len(got) != 0 {
		t.Errorf("nothing scanned: %+v", got)
	}
}

func TestSuggestScanCap(t *testing.T) {
	bodies := make([]ScanBody, MaxSuggestScan+100)
	for i := range bodies {
		bodies[i] = ScanBody{TraceID: fmt.Sprintf("t%d", i), Route: "r", Messages: sys("<kb_article>" + doc + "</kb_article>")}
	}
	got := Suggest(bodies, defaults)
	if len(got) != 2 || got[0].Sample != MaxSuggestScan || got[0].WouldGain != MaxSuggestScan {
		t.Errorf("suggestions = %+v", got)
	}
}

func TestSuggestCapsTheList(t *testing.T) {
	var b strings.Builder
	for k := range 15 {
		fmt.Fprintf(&b, "<doc_%02d>%s</doc_%02d>\n", k, doc, k)
	}
	bodies := []ScanBody{{TraceID: "a", Route: "r", Messages: sys(b.String())}, {TraceID: "b", Route: "r", Messages: sys(b.String())}}
	got := Suggest(bodies, defaults)
	if len(got) != MaxSuggestions || got[len(got)-1].Kind != SuggestExtension {
		t.Errorf("%d suggestions, last %+v", len(got), got[len(got)-1])
	}
}

func TestValidSuggestion(t *testing.T) {
	for _, c := range []struct {
		p    TagPattern
		want bool
	}{
		{TagPattern{Pattern: "<a>(.*?)</a>", Flags: "is"}, true},
		{TagPattern{Pattern: "<a>(.*?)</a>", Flags: "x"}, false},
		{TagPattern{Pattern: "<a>(.*?)</a>", Flags: "U"}, false},
		{TagPattern{Pattern: "(unclosed", Flags: ""}, false},
		{TagPattern{Pattern: "", Flags: ""}, false},
		{TagPattern{Pattern: strings.Repeat("a", MaxSuggestPattern+1)}, false},
		{TagPattern{Pattern: strings.Repeat("a", MaxSuggestPattern)}, true},
	} {
		if got := ValidSuggestion(c.p); got != c.want {
			t.Errorf("%+v: %v", c.p, got)
		}
	}
}

// A character whose lower case is longer in bytes (Ⱥ, 2 bytes, lowers to ⱥ, 3 bytes) inside a
// block once made the scan slice past the text and panic, failing every suggestions request whose
// window held that body. The closing tag is still found in either case, at the right offset.
func TestShapesKeepOffsetsWithCaseChangingText(t *testing.T) {
	block := strings.Repeat("Ⱥ", 60)
	for _, text := range []string{
		"<kb_note>" + block + "</kb_note>",
		"<KB_Note>" + block + "</kb_note>",
		"<kb_note>short</kb_note> " + block,
	} {
		var got []*shape
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%q: panic %v", text[:12], r)
				}
			}()
			got = shapesOf(text)
		}()
		wantShape := !strings.Contains(text, "short")
		if (len(got) == 1) != wantShape {
			t.Errorf("%q: shapes = %d, want a shape %v", text[:12], len(got), wantShape)
		}
	}
	if got := closingTags("xⱥ</KB_NOTE></kb_not"); len(got) != 1 || !slices.Equal(got["kb_note"], []int{len("xⱥ")}) {
		t.Errorf("closingTags = %v", got)
	}
}

// Many open tags of one name before a single far closing tag all close there, and the shape is
// returned once.
func TestShapesManyOpenTagsShareOneClose(t *testing.T) {
	text := strings.Repeat("<note>", 20000) + doc + "</note>"
	got := shapesOf(text)
	if len(got) != 1 || got[0].kind != SuggestTagPattern || got[0].label != "<note> blocks" {
		t.Fatalf("shapes = %+v", got)
	}
}

// A message made to be slow to scan (any client writes request content): tens of thousands of
// distinct unclosed tags, or of "Title:" lines with no blank line between them. Searching the
// rest of the text for each one took about 3 s for 128 KiB of tags; the indexed scan is linear.
// The bound is generous (race detector, a loaded machine); the quadratic scan of these 256 KiB
// took minutes under -race.
func TestShapesScanAdversarialTextInLinearTime(t *testing.T) {
	var tags, titles strings.Builder
	for i := 0; tags.Len() < 256<<10; i++ {
		fmt.Fprintf(&tags, "<t%05d>", i)
	}
	for i := 0; titles.Len() < 256<<10; i++ {
		fmt.Fprintf(&titles, "Ab%03d:\n", i%1000)
	}
	start := time.Now()
	for _, c := range []struct {
		text string
		want int
	}{
		{tags.String(), 0},      // nothing closes
		{titles.String(), 1000}, // Ab000: to Ab999:, each followed by the rest of the text
		// One far close after a long run of white space, which every open tag's block ends with.
		{strings.Repeat("<note> ", 30000) + strings.Repeat(" ", 30000) + "x</note>", 1},
	} {
		if got := shapesOf(c.text); len(got) != c.want {
			t.Errorf("%q...: %d shapes, want %d", c.text[:16], len(got), c.want)
		}
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the scans took %v", d)
	}
}

func TestTextIndexBlock(t *testing.T) {
	text := "  \n" + strings.Repeat("a", minBlockChars-1) + "  \n" + "b" + "\t "
	ix := newTextIndex(text)
	end := strings.Index(text, "b")
	for _, c := range []struct {
		from, to int
		want     bool
	}{
		{0, len(text), true},              // 39 a's, white space, b: 39+3+1 bytes trimmed
		{0, end, false},                   // the 39 a's alone
		{0, end + 1, true},                // up to and with the b
		{3, 3 + minBlockChars - 1, false}, // 39 bytes
		{len(text), len(text), false},     // empty at the end
		{end + 1, len(text), false},       // white space only
		{0, 3, false},                     // white space only
		{1, len(text) - 1, true},          // trimmed both sides
	} {
		if got := ix.block(c.from, c.to); got != c.want {
			t.Errorf("block(%d, %d) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
	if got := occurrences("a\n\n\nb\n\n", "\n\n"); !slices.Equal(got, []int{1, 2, 5}) {
		t.Errorf("occurrences = %v", got)
	}
	for _, c := range []struct{ from, want int }{{0, 2}, {2, 2}, {3, 9}, {10, -1}} {
		if got := firstAtOrAfter([]int{2, 9}, c.from); got != c.want {
			t.Errorf("firstAtOrAfter(%d) = %d, want %d", c.from, got, c.want)
		}
	}
	if got := sectionEnd([]int{4, 9}, 4, 20); got != 9 {
		t.Errorf("a stop at the section start belongs to the line before: %d", got)
	}
	if got := sectionEnd([]int{4}, 5, 20); got != 20 {
		t.Errorf("no stop after: %d", got)
	}
}
