package recording

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/truthtable/contracts"
)

// Recordings are published (SPEC §2.13: "sanitized (keys, hosts and emails scrubbed)"). The
// sanitizer rewrites every string value of the manifest and the trace files, so a secret or a host
// is scrubbed the same way in a body, a preview, a claim and a detector finding.
//
// Every replacement keeps the string's length in UTF-16 code units, because recordings carry
// UTF-16 offsets (detector findings, claim and evidence spans) that must still point at the same
// text. Scrubbing covers:
//
//   - credentials matching contracts/rules/secrets.v1.json: all but the first four characters
//     become '*';
//   - email addresses, hosts in URLs (with any user info), bare hostnames under private suffixes
//     (.internal, .local, .lan, .corp, .home, .intranet), and IPv4 and bracketed IPv6 literals:
//     letters and digits become 'x', punctuation stays.
//
// Names under the reserved example domains (RFC 2606 and 6761: example.com, example.net,
// example.org and the .example, .test and .invalid TLDs) and localhost are kept: demo content uses
// them and they reveal nothing.

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,}`)
	// urlPattern captures a URL's authority: [userinfo@]host[:port].
	urlPattern = regexp.MustCompile(`(?i)\b(?:https?|wss?|ftp)://([^/\s?#"'<>\\]+)`)
	// privateHostPattern matches bare hostnames under private-use suffixes.
	privateHostPattern = regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)+(?:internal|local|lan|corp|home|intranet)\b`)
	ipv4Pattern        = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	ipv6Pattern        = regexp.MustCompile(`\[[0-9A-Fa-f:.]{2,45}\]`)
)

// Sanitizer scrubs strings; it is safe for concurrent use after construction.
type Sanitizer struct {
	secrets []*regexp.Regexp
}

// NewSanitizer compiles the credential patterns of contracts/rules/secrets.v1.json.
func NewSanitizer() (*Sanitizer, error) {
	b, err := contracts.FS.ReadFile("rules/secrets.v1.json")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Patterns []struct {
			ID      string `json:"id"`
			Pattern string `json:"pattern"`
			Flags   string `json:"flags"`
		} `json:"patterns"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("secrets.v1.json: %w", err)
	}
	s := &Sanitizer{}
	for _, p := range doc.Patterns {
		src := p.Pattern
		if p.Flags != "" {
			src = "(?" + p.Flags + ")" + src
		}
		re, err := regexp.Compile(src)
		if err != nil {
			return nil, fmt.Errorf("secrets.v1.json %s: %w", p.ID, err)
		}
		s.secrets = append(s.secrets, re)
	}
	return s, nil
}

// String scrubs one string.
func (s *Sanitizer) String(v string) string {
	for _, re := range s.secrets {
		v = re.ReplaceAllStringFunc(v, maskSecret)
	}
	v = emailPattern.ReplaceAllStringFunc(v, func(m string) string {
		at := strings.LastIndexByte(m, '@')
		if reservedHost(m[at+1:]) {
			return m
		}
		return maskAlnum(m)
	})
	v = urlPattern.ReplaceAllStringFunc(v, func(m string) string {
		i := strings.Index(m, "://") + 3
		return m[:i] + maskAuthority(m[i:])
	})
	v = privateHostPattern.ReplaceAllStringFunc(v, maskAlnum)
	v = ipv4Pattern.ReplaceAllStringFunc(v, func(m string) string {
		if net.ParseIP(m) == nil {
			return m // not an address (for example 999.1.1.1)
		}
		return maskAlnum(m)
	})
	v = ipv6Pattern.ReplaceAllStringFunc(v, func(m string) string {
		if net.ParseIP(m[1:len(m)-1]) == nil {
			return m
		}
		return maskAlnum(m)
	})
	return v
}

// maskAuthority scrubs a URL authority: user info always, the host unless reserved; the port stays.
func maskAuthority(a string) string {
	user := ""
	if at := strings.LastIndexByte(a, '@'); at >= 0 {
		user, a = maskAlnum(a[:at])+"@", a[at+1:]
	}
	host, port := a, ""
	if strings.HasPrefix(host, "[") { // IPv6 literal
		if end := strings.IndexByte(host, ']'); end >= 0 {
			host, port = host[:end+1], host[end+1:]
		}
	} else if c := strings.LastIndexByte(host, ':'); c >= 0 {
		host, port = host[:c], host[c:]
	}
	if !reservedHost(host) {
		host = maskAlnum(host)
	}
	return user + host + port
}

// reservedHost reports a name under the reserved example domains, or localhost.
func reservedHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" {
		return true
	}
	for _, d := range []string{"example.com", "example.net", "example.org"} {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	for _, tld := range []string{".example", ".test", ".invalid", ".localhost"} {
		if strings.HasSuffix(h, tld) {
			return true
		}
	}
	return false
}

// maskSecret keeps the first four characters of a credential and replaces the rest with '*'.
func maskSecret(m string) string {
	r := []rune(m)
	keep := min(len(r), 4)
	return string(r[:keep]) + maskRunes(r[keep:], '*', func(rune) bool { return true })
}

// maskAlnum replaces letters and digits with 'x' and keeps punctuation.
func maskAlnum(m string) string {
	return maskRunes([]rune(m), 'x', func(x rune) bool { return unicode.IsLetter(x) || unicode.IsDigit(x) })
}

// maskRunes replaces each rune for which mask is true with as many copies of c as it has UTF-16
// code units, so the result has the same UTF-16 length.
func maskRunes(r []rune, c rune, mask func(rune) bool) string {
	var b strings.Builder
	for _, x := range r {
		if !mask(x) {
			b.WriteRune(x)
			continue
		}
		for range utf16.RuneLen(x) {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// Value scrubs every string in a decoded JSON value (object keys are kept).
func (s *Sanitizer) Value(v any) any {
	switch x := v.(type) {
	case string:
		return s.String(x)
	case []any:
		for i := range x {
			x[i] = s.Value(x[i])
		}
		return x
	case map[string]any:
		for k := range x {
			x[k] = s.Value(x[k])
		}
		return x
	}
	return v
}

// JSON marshals v, scrubs every string in it and returns the indented document. Numbers keep
// their text, and <, > and & are not escaped.
func (s *Sanitizer) JSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.Value(generic)); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
