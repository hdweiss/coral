// Package logs parses container log lines: Elastic Common Schema (ECS) JSON,
// other structured JSON (zap, logrus, slog, …) and plain text.
package logs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

type Format int

const (
	Plain Format = iota
	JSON
	ECS
)

func (f Format) String() string {
	switch f {
	case JSON:
		return "json"
	case ECS:
		return "ecs"
	}
	return "text"
}

// Entry is one parsed log line.
type Entry struct {
	Raw      string    // the line without the Kubernetes timestamp
	KubeTime time.Time // when the kubelet received the line; zero if unknown
	Format   Format

	Time    time.Time // the entry's own timestamp, else KubeTime
	Level   string    // normalized: TRACE DEBUG INFO WARN ERROR FATAL, or ""
	Message string

	// Fields is the parsed object for JSON and ECS lines, with ECS dotted
	// keys expanded into nested objects ({"log.level": x} → log: {level: x}),
	// so that both spellings are the same field. Plain lines have just
	// "message".
	Fields map[string]any
}

// Parse parses a line as returned by the API with timestamps=true:
// "<RFC3339Nano> <text>". Lines without the timestamp prefix work too.
func Parse(line string) Entry {
	line = strings.TrimRight(line, "\r\n")
	var e Entry
	if ts, rest, ok := strings.Cut(line, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			e.KubeTime, line = t, rest
		}
	}
	e.Time = e.KubeTime

	if obj := parseObject(line); obj != nil {
		e.Raw = Clean(line)
		e.Format = JSON
		if isECS(obj) {
			e.Format = ECS
			obj = expandDots(obj)
		}
		e.Fields = obj
		if t, ok := findTime(obj); ok {
			e.Time = t
		}
		e.Level = findLevel(obj)
		e.Message = findMessage(obj)
		return e
	}

	line = Clean(line)
	e.Raw = line
	e.Message = line
	e.Level = plainLevel(line)
	e.Fields = map[string]any{"message": line}
	return e
}

func parseObject(line string) map[string]any {
	s := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil
	}
	return normalizeNumbers(obj).(map[string]any)
}

// normalizeNumbers turns json.Number into int64 when integral, else float64,
// so that the YAML view and pinned fields print them as written. Strings and
// keys are cleaned of terminal control sequences.
func normalizeNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, c := range t {
			if ck := Clean(k); ck != k {
				delete(t, k)
				k = ck
			}
			t[k] = normalizeNumbers(c)
		}
	case string:
		return Clean(t)
	case []any:
		for i, c := range t {
			t[i] = normalizeNumbers(c)
		}
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	}
	return v
}

// isECS recognizes ECS logs by their version field, or by the @timestamp and
// log.level pair every ECS logging library writes.
func isECS(obj map[string]any) bool {
	if _, ok := obj["ecs.version"]; ok {
		return true
	}
	if ecs, ok := obj["ecs"].(map[string]any); ok && ecs["version"] != nil {
		return true
	}
	_, ts := obj["@timestamp"]
	_, lvl := obj["log.level"]
	if log, ok := obj["log"].(map[string]any); ok {
		_, lvl = log["level"]
	}
	return ts && lvl
}

// expandDots turns dotted keys into nested objects, merging with objects
// that already exist ({"log.level": "info", "log": {"logger": "x"}}). Keys
// without dots are placed first; a dotted key that would overwrite another
// value stays as written, so no field is lost and the result doesn't depend
// on map order.
func expandDots(obj map[string]any) map[string]any {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		di, dj := strings.Contains(keys[i], "."), strings.Contains(keys[j], ".")
		if di != dj {
			return dj
		}
		return keys[i] < keys[j]
	})
	out := make(map[string]any, len(obj))
	for _, k := range keys {
		v := obj[k]
		if m, ok := v.(map[string]any); ok {
			v = expandDots(m)
		}
		parts := strings.Split(k, ".")
		if slices.Contains(parts, "") || !fits(out, parts, v) {
			out[k] = v
			continue
		}
		put(out, parts, v)
	}
	return out
}

// fits reports whether put can place v at path without overwriting a value.
func fits(m map[string]any, path []string, v any) bool {
	for _, p := range path[:len(path)-1] {
		x, ok := m[p]
		if !ok {
			return true
		}
		if m, ok = x.(map[string]any); !ok {
			return false
		}
	}
	x, ok := m[path[len(path)-1]]
	if !ok {
		return true
	}
	xm, ok1 := x.(map[string]any)
	vm, ok2 := v.(map[string]any)
	if !ok1 || !ok2 {
		return false
	}
	for k, c := range vm {
		if !fits(xm, []string{k}, c) {
			return false
		}
	}
	return true
}

// put places v at path, creating objects on the way and merging into an
// existing object. Check fits first.
func put(m map[string]any, path []string, v any) {
	for _, p := range path[:len(path)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	last := path[len(path)-1]
	if xm, ok := m[last].(map[string]any); ok {
		for k, c := range v.(map[string]any) {
			put(xm, []string{k}, c)
		}
		return
	}
	m[last] = v
}

func isMap(v any) bool { _, ok := v.(map[string]any); return ok }

var (
	timePaths    = [][]string{{"@timestamp"}, {"timestamp"}, {"time"}, {"ts"}, {"t"}, {"date"}}
	levelPaths   = [][]string{{"log", "level"}, {"log.level"}, {"level"}, {"lvl"}, {"severity"}, {"levelname"}, {"loglevel"}, {"@l"}}
	messagePaths = [][]string{{"message"}, {"msg"}, {"log"}, {"text"}, {"@m"}, {"@mt"}}
	// Where the message is when the usual fields are missing, as in ECS
	// error entries.
	fallbackMessagePaths = [][]string{{"error", "message"}, {"error.message"}, {"event", "original"}, {"event.original"}}
)

// findLevel reads the level as a word, or as a pino/bunyan number (30 = info).
func findLevel(obj map[string]any) string {
	if s := findString(obj, levelPaths); s != "" {
		return normalizeLevel(s)
	}
	for _, p := range levelPaths {
		v, ok := get(obj, p)
		if !ok {
			continue
		}
		var n int64
		switch t := v.(type) {
		case int64:
			n = t
		case float64:
			n = int64(t)
		default:
			continue
		}
		switch {
		case n >= 60:
			return "FATAL"
		case n >= 50:
			return "ERROR"
		case n >= 40:
			return "WARN"
		case n >= 30:
			return "INFO"
		case n >= 20:
			return "DEBUG"
		case n >= 10:
			return "TRACE"
		}
	}
	return ""
}

// findMessage reads the message, formatting a message that isn't a string
// (an object or number) as compact JSON. An object under "log" is ECS's log
// namespace, not a message.
func findMessage(obj map[string]any) string {
	if s := findString(obj, messagePaths); s != "" {
		return s
	}
	for _, p := range messagePaths {
		if v, ok := get(obj, p); ok && v != nil && !(p[0] == "log" && isMap(v)) {
			if _, isStr := v.(string); !isStr {
				return Compact(v)
			}
		}
	}
	return findString(obj, fallbackMessagePaths)
}

func get(obj map[string]any, path []string) (any, bool) {
	var cur any = obj
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func findString(obj map[string]any, paths [][]string) string {
	for _, p := range paths {
		if v, ok := get(obj, p); ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

func findTime(obj map[string]any) (time.Time, bool) {
	for _, p := range timePaths {
		v, ok := get(obj, p)
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05,999999999"} {
				if tm, err := time.Parse(layout, t); err == nil {
					return tm, true
				}
			}
		case float64:
			return epoch(t), true
		case int64:
			return epoch(float64(t)), true
		}
	}
	return time.Time{}, false
}

// epoch reads seconds, milliseconds, microseconds or nanoseconds since 1970,
// guessing the unit from the magnitude.
func epoch(v float64) time.Time {
	switch {
	case v > 1e17:
		return time.Unix(0, int64(v))
	case v > 1e14:
		return time.UnixMicro(int64(v))
	case v > 1e11:
		return time.UnixMilli(int64(v))
	}
	sec, frac := math.Modf(v)
	return time.Unix(int64(sec), int64(frac*1e9))
}

func normalizeLevel(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "":
		return ""
	case "TRACE", "TRC", "VERBOSE", "VRB", "FINEST", "FINER":
		return "TRACE"
	case "DEBUG", "DBG", "D", "FINE":
		return "DEBUG"
	case "INFO", "INF", "I", "INFORMATION", "INFORMATIONAL", "NOTICE":
		return "INFO"
	case "WARN", "WARNING", "WRN", "W":
		return "WARN"
	case "ERROR", "ERR", "E", "SEVERE", "EROR":
		return "ERROR"
	case "FATAL", "FTL", "CRITICAL", "CRIT", "PANIC", "EMERGENCY", "EMERG", "ALERT", "DPANIC", "F":
		return "FATAL"
	}
	return strings.ToUpper(s)
}

// A level word near the start of a plain line, or a klog prefix like
// "E1003 12:00:00.000000".
var (
	plainLevelRe = regexp.MustCompile(`(?i)\b(trace|debug|info|warn(?:ing)?|error|fatal|panic|critical)\b`)
	klogRe       = regexp.MustCompile(`^([IWEF])\d{4} `)
)

func plainLevel(line string) string {
	if m := klogRe.FindStringSubmatch(line); m != nil {
		return normalizeLevel(m[1])
	}
	head := line
	if len(head) > 48 {
		head = head[:48]
	}
	if m := plainLevelRe.FindString(head); m != "" {
		return normalizeLevel(m)
	}
	return ""
}

// Compact formats a field value for a single line: strings as they are,
// everything else as compact JSON.
func Compact(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return "null"
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	return strings.TrimSpace(b.String())
}

// Clean makes text safe to draw: terminal escape sequences (colors, cursor
// movement) are removed, invalid UTF-8 is replaced and control characters
// other than tab and newline are dropped (so CRLF becomes LF), since the
// terminal would act on them and garble the screen.
func Clean(s string) string {
	if utf8.ValidString(s) && !strings.ContainsFunc(s, isControl) {
		return s
	}
	s = ansi.Strip(strings.ToValidUTF8(s, "\uFFFD"))
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, s)
}

func isControl(r rune) bool {
	return (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r < 0xa0)
}

// ReadLine reads one line without its line break. A line longer than max
// bytes is cut to max, and the rest of it is skipped, rather than failing
// the stream. The last line may come with io.EOF.
func ReadLine(r *bufio.Reader, max int) (string, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if room := max - len(buf); room > 0 {
			buf = append(buf, chunk[:min(len(chunk), room)]...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		buf = bytes.TrimRight(buf, "\r\n")
		if err == io.EOF && len(buf) > 0 {
			err = nil // return the last line now and io.EOF on the next call
		}
		return string(buf), err
	}
}
