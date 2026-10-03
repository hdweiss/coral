// Package logs parses container log lines: Elastic Common Schema (ECS) JSON,
// other structured JSON (zap, logrus, slog, …) and plain text.
package logs

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"time"
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
	e.Raw = line
	e.Time = e.KubeTime

	if obj := parseObject(line); obj != nil {
		e.Format = JSON
		if isECS(obj) {
			e.Format = ECS
			obj = expandDots(obj)
		}
		e.Fields = obj
		if t, ok := findTime(obj); ok {
			e.Time = t
		}
		e.Level = normalizeLevel(findString(obj, levelPaths))
		e.Message = findString(obj, messagePaths)
		return e
	}

	e.Message = line
	e.Level = plainLevel(line)
	e.Fields = map[string]any{"message": line}
	return e
}

func parseObject(line string) map[string]any {
	s := strings.TrimSpace(line)
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
// so that the YAML view and pinned fields print them as written.
func normalizeNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, c := range t {
			t[k] = normalizeNumbers(c)
		}
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
	return ts && lvl
}

// expandDots turns dotted keys into nested objects, merging with objects
// that already exist ({"log.level": "info", "log": {"logger": "x"}}).
func expandDots(obj map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range obj {
		if m, ok := v.(map[string]any); ok {
			v = expandDots(m)
		}
		parts := strings.Split(k, ".")
		if k == "" || slicesContainsEmpty(parts) {
			out[k] = v
			continue
		}
		cur := out
		for _, p := range parts[:len(parts)-1] {
			next, ok := cur[p].(map[string]any)
			if !ok {
				if _, taken := cur[p]; taken {
					next = nil
				} else {
					next = map[string]any{}
					cur[p] = next
				}
			}
			if next == nil { // a scalar is in the way; keep the dotted key
				cur = nil
				break
			}
			cur = next
		}
		last := parts[len(parts)-1]
		switch {
		case cur == nil:
			out[k] = v
		case isMap(cur[last]) && isMap(v):
			for ck, cv := range v.(map[string]any) {
				cur[last].(map[string]any)[ck] = cv
			}
		default:
			cur[last] = v
		}
	}
	return out
}

func slicesContainsEmpty(parts []string) bool {
	for _, p := range parts {
		if p == "" {
			return true
		}
	}
	return false
}

func isMap(v any) bool { _, ok := v.(map[string]any); return ok }

var (
	timePaths    = [][]string{{"@timestamp"}, {"timestamp"}, {"time"}, {"ts"}, {"t"}, {"date"}}
	levelPaths   = [][]string{{"log", "level"}, {"log.level"}, {"level"}, {"lvl"}, {"severity"}, {"levelname"}, {"loglevel"}}
	messagePaths = [][]string{{"message"}, {"msg"}, {"log"}, {"text"}}
)

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
			if s, ok := v.(string); ok {
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
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999"} {
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

// epoch reads seconds, milliseconds or nanoseconds since 1970, guessing the
// unit from the magnitude.
func epoch(v float64) time.Time {
	switch {
	case v > 1e17:
		return time.Unix(0, int64(v))
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
	case "TRACE", "TRC":
		return "TRACE"
	case "DEBUG", "DBG", "D":
		return "DEBUG"
	case "INFO", "INF", "I", "INFORMATION", "NOTICE":
		return "INFO"
	case "WARN", "WARNING", "WRN", "W":
		return "WARN"
	case "ERROR", "ERR", "E":
		return "ERROR"
	case "FATAL", "CRITICAL", "CRIT", "PANIC", "EMERGENCY", "ALERT", "DPANIC", "F":
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
