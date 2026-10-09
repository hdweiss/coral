package logs

import "strings"

// parseLogfmt reads a logfmt line (key=value pairs, values bare or
// "quoted"), as written by Go's slog text handler, logrus, Grafana and
// Prometheus. Prose with the odd key=value in it is not logfmt: the line
// must start with a pair, and at least two thirds of its tokens, and at
// least two, must be pairs. Values stay strings; bare keys are "true".
func parseLogfmt(line string) map[string]any {
	obj := map[string]any{}
	pairs, tokens := 0, 0
	s := strings.TrimSpace(line)
	for s != "" {
		tokens++
		i := 0
		for i < len(s) && isKeyByte(s[i]) {
			i++
		}
		key := s[:i]
		if key == "" {
			return nil // a quote, a bracket, … : not logfmt
		}
		s = s[i:]
		var val string
		switch {
		case strings.HasPrefix(s, "="):
			s = s[1:]
			pairs++
			if strings.HasPrefix(s, `"`) {
				v, rest, ok := unquote(s)
				if !ok {
					return nil
				}
				val, s = v, rest
			} else {
				end := strings.IndexByte(s, ' ')
				if end < 0 {
					end = len(s)
				}
				val, s = s[:end], s[end:]
			}
		case s == "" || s[0] == ' ':
			if tokens == 1 {
				return nil
			}
			val = "true"
		default:
			return nil
		}
		obj[key] = Clean(val)
		s = strings.TrimLeft(s, " ")
	}
	if pairs < 2 || 3*pairs < 2*tokens {
		return nil
	}
	return obj
}

func isKeyByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-' || c == '/' || c == '@'
}

// unquote reads a "quoted" value with backslash escapes from the start of
// s and returns it and the rest of s.
func unquote(s string) (string, string, bool) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			if i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(s[i])
				}
			}
		case '"':
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(c)
		}
	}
	return "", "", false
}
