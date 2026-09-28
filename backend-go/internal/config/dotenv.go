package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// EnvFileVar names the dotenv file (SPEC §2.7). Unset or "auto": walk up from the working
// directory to the directory containing VERSION and use the .env there. Set but empty: no dotenv
// loading. Anything else: that file.
const EnvFileVar = "TRUTHTABLE_ENV_FILE"

// maxDotenvBytes bounds a dotenv file; a larger file is refused with a warning.
const maxDotenvBytes = 1 << 20

// dotenvVar is one assignment of a dotenv file.
type dotenvVar struct {
	Name  string
	Value string
	Line  int
}

// dotenvProblem is a line a dotenv file could not parse. It never carries the line's value.
type dotenvProblem struct {
	Line    int
	Message string
}

var dotenvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseDotenv parses KEY=VALUE lines: blank lines and # comments are skipped, an optional
// `export ` prefix is accepted, values may be unquoted (trimmed, a " #" starts a comment),
// single-quoted (literal) or double-quoted (\n, \r, \t, \", \\ and \$ escapes; may span lines).
// ${NAME} in unquoted and double-quoted values expands to an earlier assignment of the same file
// or, failing that, lookup(NAME). A later assignment of the same name wins, as in docker compose.
func parseDotenv(data []byte, lookup func(string) (string, bool)) ([]dotenvVar, []dotenvProblem) {
	var (
		vars     []dotenvVar
		problems []dotenvProblem
		defined  = map[string]string{}
	)
	expand := func(s string) string {
		return expandRefs(s, func(name string) string {
			if v, ok := defined[name]; ok {
				return v
			}
			if lookup != nil {
				if v, ok := lookup(name); ok {
					return v
				}
			}
			return ""
		})
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		lineNo := i + 1
		line := strings.TrimSpace(lines[i])
		if i == 0 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimSpace(rest)
		}
		name, raw, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || !dotenvName.MatchString(name) {
			problems = append(problems, dotenvProblem{Line: lineNo, Message: "expected NAME=value"})
			continue
		}
		raw = strings.TrimLeft(raw, " \t")
		var value string
		switch {
		case strings.HasPrefix(raw, `"`):
			// A double-quoted value may continue on the following lines.
			body := raw[1:]
			for {
				v, rest, closed := scanDoubleQuoted(body)
				if closed {
					if trailing := strings.TrimSpace(rest); trailing != "" && !strings.HasPrefix(trailing, "#") {
						problems = append(problems, dotenvProblem{Line: lineNo, Message: "unexpected text after the closing quote"})
					}
					value = expand(v)
					break
				}
				if i+1 >= len(lines) {
					problems = append(problems, dotenvProblem{Line: lineNo, Message: "unterminated double quote"})
					value = ""
					name = ""
					break
				}
				i++
				body += "\n" + lines[i]
			}
		case strings.HasPrefix(raw, `'`):
			end := strings.IndexByte(raw[1:], '\'')
			if end < 0 {
				problems = append(problems, dotenvProblem{Line: lineNo, Message: "unterminated single quote"})
				continue
			}
			value = raw[1 : end+1]
			if trailing := strings.TrimSpace(raw[end+2:]); trailing != "" && !strings.HasPrefix(trailing, "#") {
				problems = append(problems, dotenvProblem{Line: lineNo, Message: "unexpected text after the closing quote"})
			}
		default:
			if idx := strings.Index(raw, " #"); idx >= 0 {
				raw = raw[:idx]
			}
			if idx := strings.Index(raw, "\t#"); idx >= 0 {
				raw = raw[:idx]
			}
			value = expand(strings.TrimSpace(raw))
		}
		if name == "" {
			continue
		}
		defined[name] = value
		vars = append(vars, dotenvVar{Name: name, Value: value, Line: lineNo})
	}
	return vars, problems
}

// scanDoubleQuoted reads a double-quoted body (after the opening quote) and returns the unescaped
// value, the text after the closing quote and whether the quote was closed.
func scanDoubleQuoted(s string) (string, string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '"', '\\', '$':
				b.WriteByte(s[i])
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
		case c == '"':
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), "", false
}

// refPattern is a ${NAME} reference. A backslash-escaped \$ was already unescaped by the caller
// for double-quoted values; unquoted values expand every ${NAME}.
var refPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandRefs replaces ${NAME} with get(NAME).
func expandRefs(s string, get func(string) string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return refPattern.ReplaceAllStringFunc(s, func(m string) string {
		return get(m[2 : len(m)-1])
	})
}

// envFileChoice resolves TRUTHTABLE_ENV_FILE: the file to read ("" for none) and whether it was
// named explicitly (a missing explicit file is a warning; a missing auto file is not).
func envFileChoice(value string, set bool, workDir string, auto bool) (path string, explicit bool) {
	v := strings.TrimSpace(value)
	switch {
	case set && v == "":
		return "", false
	case !set || strings.EqualFold(v, "auto"):
		if !auto {
			return "", false
		}
		root := findRepoRoot(workDir)
		if root == "" {
			return "", false
		}
		return filepath.Join(root, ".env"), false
	}
	if !filepath.IsAbs(v) && workDir != "" {
		v = filepath.Join(workDir, v)
	}
	return v, true
}

// findRepoRoot returns the nearest directory at or above dir that holds VERSION, or "".
func findRepoRoot(dir string) string {
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	for {
		if fileExists(filepath.Join(dir, "VERSION")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// readDotenv reads a dotenv file. A missing file is (nil, fs.ErrNotExist).
func readDotenv(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- the operator names the dotenv file (TRUTHTABLE_ENV_FILE)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only file
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxDotenvBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxDotenvBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxDotenvBytes)
	}
	return b, nil
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
