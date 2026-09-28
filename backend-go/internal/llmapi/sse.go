package llmapi

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
)

// Event is one server-sent event as the event-stream format defines it (WHATWG HTML, "Server-sent
// events": fields event, data, id and retry, comment lines starting with a colon, dispatched at a
// blank line).
type Event struct {
	// Name is the event field; empty when absent (the default "message" type).
	Name string
	// Data is the data lines joined with "\n"; HasData reports whether any data line was present
	// (an event can carry an empty data line).
	Data    []byte
	HasData bool
	// ID is the last id field (one containing NUL is ignored, as the format requires).
	ID    string
	HasID bool
	// Retry is the retry field in milliseconds (only an all-digit value counts).
	Retry    int
	HasRetry bool
	// Comments are the texts of the comment lines, without the colon and one leading space.
	Comments []string
	// Raw is the event exactly as it was read: its lines, their terminators, the blank line that
	// dispatched it and any stray blank lines before it. Concatenating the Raw of every event a
	// Reader returns reproduces its input byte for byte, so a relay can forward what it inspects
	// without re-encoding it. An event built in code has no Raw; Encode serializes it.
	Raw []byte
	// Incomplete marks the tail of a stream that ended (or failed) before the blank line that
	// dispatches an event. Its fields are parsed from the complete lines, plus the unterminated
	// last line.
	Incomplete bool
}

// Type is the event type: the event field, else the "type" member of a JSON object data field
// (Anthropic sends both; the fallback tolerates streams that omit the event lines).
func (e Event) Type() string {
	if e.Name != "" {
		return e.Name
	}
	if !e.HasData {
		return ""
	}
	var v struct {
		Type string `json:"type"`
	}
	if unmarshalObject(e.Data, &v) {
		return v.Type
	}
	return ""
}

// Bytes returns the event's wire bytes: Raw when it was read, else its encoding.
func (e Event) Bytes() []byte {
	if len(e.Raw) > 0 {
		return e.Raw
	}
	return e.Encode()
}

// Encode serializes the event: comment lines, then event, id, retry and one data line per line of
// Data, then the blank line. Line breaks inside comments, names and ids are replaced with spaces,
// and one inside Data starts a new data line, so the output always parses back to the same event.
func (e Event) Encode() []byte {
	var b bytes.Buffer
	for _, c := range e.Comments {
		writeLine(&b, ": ", c)
	}
	if e.Name != "" {
		writeLine(&b, "event: ", e.Name)
	}
	if e.HasID {
		writeLine(&b, "id: ", e.ID)
	}
	if e.HasRetry {
		writeLine(&b, "retry: ", strconv.Itoa(e.Retry))
	}
	if e.HasData {
		data := strings.ReplaceAll(strings.ReplaceAll(string(e.Data), "\r\n", "\n"), "\r", "\n")
		for _, line := range strings.Split(data, "\n") {
			b.WriteString("data: ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	b.WriteByte('\n')
	return b.Bytes()
}

// writeLine writes prefix + s + "\n" with the line breaks of s replaced by spaces.
func writeLine(b *bytes.Buffer, prefix, s string) {
	b.WriteString(prefix)
	b.WriteString(strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(s))
	b.WriteByte('\n')
}

// CommentEvent is an event made of one comment line per line of text: ": <line>".
func CommentEvent(text string) Event {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	return Event{Comments: strings.Split(text, "\n")}
}

// DataEvent is an event with the given event name (may be empty) and data.
func DataEvent(name string, data []byte) Event {
	return Event{Name: name, Data: data, HasData: true}
}

// EncodeEvents concatenates the wire bytes of events.
func EncodeEvents(events []Event) []byte {
	var b bytes.Buffer
	for i := range events {
		b.Write(events[i].Bytes())
	}
	return b.Bytes()
}

// DefaultMaxEventBytes bounds one event (its Raw bytes) unless SetMaxEventBytes says otherwise:
// the default TRUTHTABLE_MAX_UPSTREAM_RESPONSE_BYTES.
const DefaultMaxEventBytes = 8 << 20

// ErrEventTooLarge is returned by Reader.Next when one event grows past the size limit (a stream
// that is not an event stream, or one that never sends a blank line).
var ErrEventTooLarge = errors.New("llmapi: server-sent event exceeds the size limit")

// maxEmptyReads bounds consecutive (0, nil) reads before a reader counts as stuck.
const maxEmptyReads = 100

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Reader reads server-sent events from a stream. Lines may end in CRLF, LF or CR (also when a
// CRLF pair is split across reads), a leading UTF-8 byte order mark is ignored, and an event is
// only interpreted once complete, so a multi-byte character or a JSON document split across
// network chunks is reassembled before anyone looks at it.
//
// A Reader is not safe for concurrent use.
type Reader struct {
	src io.Reader
	// buf[start:end] is read but not yet returned; start is where the current event begins.
	buf        []byte
	start, end int
	max        int
	// skipLF is set after a line that ended in CR at the end of the buffered bytes: an LF that
	// follows belongs to that line.
	skipLF  bool
	started bool // the byte order mark check is done
	err     error
}

// NewReader returns a Reader of src with the default event size limit.
func NewReader(src io.Reader) *Reader {
	return &Reader{src: src, max: DefaultMaxEventBytes}
}

// SetMaxEventBytes sets the event size limit; n <= 0 restores the default.
func (r *Reader) SetMaxEventBytes(n int) {
	if n <= 0 {
		n = DefaultMaxEventBytes
	}
	r.max = n
}

// Next returns the next event. Every event that has at least one line (a field, a comment or an
// unknown field) is returned, comment-only keepalives included, so a relay forwards them without
// delay. At the end of the stream a trailing partial event is returned with Incomplete set and a
// nil error, and bytes that form no event (stray blank lines, or the LF of a CRLF whose CR ended
// the previous event) come back as an event with only Raw, so nothing read is lost; the call after
// that returns io.EOF, or the read error that ended the stream.
func (r *Reader) Next() (Event, error) {
	var (
		ev     Event
		data   bytes.Buffer
		lines  int
		off    int // offset of the next line, relative to r.start
		cursor int // offset up to which the buffer was searched for a terminator
	)
	for {
		if !r.started {
			// A leading byte order mark is ignored (it stays in Raw).
			if r.end-r.start < len(utf8BOM) && r.err == nil {
				if err := r.fill(); err != nil {
					return Event{}, err
				}
				continue
			}
			r.started = true
			if bytes.HasPrefix(r.buf[r.start:r.end], utf8BOM) {
				off, cursor = len(utf8BOM), len(utf8BOM)
			}
		}
		if r.skipLF {
			if r.start+off < r.end {
				r.skipLF = false
				if r.buf[r.start+off] == '\n' {
					off++
					cursor = off
				}
			} else if r.err == nil {
				if err := r.fill(); err != nil {
					return Event{}, err
				}
				continue
			} else {
				r.skipLF = false
			}
		}
		pending := r.buf[r.start+cursor : r.end]
		i := bytes.IndexAny(pending, "\r\n")
		if i < 0 {
			if r.err != nil {
				return r.finish(&ev, &data, lines, off)
			}
			cursor = r.end - r.start
			if err := r.fill(); err != nil {
				return Event{}, err
			}
			continue
		}
		lineEnd := cursor + i
		next := lineEnd + 1
		if r.buf[r.start+lineEnd] == '\r' {
			switch {
			case r.start+next < r.end:
				if r.buf[r.start+next] == '\n' {
					next++
				}
			case r.err == nil:
				r.skipLF = true
			}
		}
		line := r.buf[r.start+off : r.start+lineEnd]
		off, cursor = next, next
		if len(line) == 0 {
			if lines == 0 {
				continue // a stray blank line: it travels with the next event
			}
			return r.dispatch(&ev, &data, off, false), nil
		}
		lines++
		r.parseLine(&ev, &data, line)
	}
}

// finish returns what is left at the end of the stream: a partial event, or the stream's error.
func (r *Reader) finish(ev *Event, data *bytes.Buffer, lines, off int) (Event, error) {
	rest := r.buf[r.start+off : r.end]
	if len(rest) > 0 {
		lines++
		r.parseLine(ev, data, rest)
	}
	if r.end == r.start {
		return Event{}, r.err
	}
	return r.dispatch(ev, data, r.end-r.start, lines > 0), nil
}

// dispatch completes ev with the n bytes from r.start as its Raw and consumes them.
func (r *Reader) dispatch(ev *Event, data *bytes.Buffer, n int, incomplete bool) Event {
	ev.Raw = bytes.Clone(r.buf[r.start : r.start+n])
	r.start += n
	if ev.HasData {
		ev.Data = bytes.Clone(bytes.TrimSuffix(data.Bytes(), []byte("\n")))
	}
	ev.Incomplete = incomplete
	return *ev
}

// parseLine applies one non-blank line to ev.
func (r *Reader) parseLine(ev *Event, data *bytes.Buffer, line []byte) {
	if len(line) > 0 && line[0] == ':' {
		ev.Comments = append(ev.Comments, string(trimOneSpace(line[1:])))
		return
	}
	field, value := line, []byte(nil)
	if i := bytes.IndexByte(line, ':'); i >= 0 {
		field, value = line[:i], trimOneSpace(line[i+1:])
	}
	switch string(field) {
	case "event":
		ev.Name = string(value)
	case "data":
		ev.HasData = true
		data.Write(value)
		data.WriteByte('\n')
	case "id":
		if bytes.IndexByte(value, 0) < 0 {
			ev.ID, ev.HasID = string(value), true
		}
	case "retry":
		if n, ok := atoiDigits(value); ok {
			ev.Retry, ev.HasRetry = n, true
		}
	}
}

// fill reads more input after the buffered bytes, first moving the current event to the front of
// the buffer. It returns ErrEventTooLarge once the event would outgrow the limit; a read error is
// kept for Next to act on once the buffered bytes are used up.
func (r *Reader) fill() error {
	if r.end-r.start >= r.max {
		return ErrEventTooLarge
	}
	if r.start > 0 {
		n := copy(r.buf, r.buf[r.start:r.end])
		r.start, r.end = 0, n
	}
	if r.end == len(r.buf) {
		size := max(2*len(r.buf), 4096)
		size = min(size, r.max+1)
		grown := make([]byte, size)
		copy(grown, r.buf[:r.end])
		r.buf = grown
	}
	for range maxEmptyReads {
		n, err := r.src.Read(r.buf[r.end:])
		r.end += n
		if err != nil {
			r.err = err
			return nil
		}
		if n > 0 {
			return nil
		}
	}
	r.err = io.ErrNoProgress
	return nil
}

func trimOneSpace(b []byte) []byte {
	if len(b) > 0 && b[0] == ' ' {
		return b[1:]
	}
	return b
}

// atoiDigits parses a non-empty run of ASCII digits that fits an int.
func atoiDigits(b []byte) (int, bool) {
	if len(b) == 0 {
		return 0, false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(string(b))
	return n, err == nil
}
