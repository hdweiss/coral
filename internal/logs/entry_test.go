package logs

import (
	"bufio"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseECS(t *testing.T) {
	e := Parse(`2026-10-03T10:00:00.5Z {"@timestamp":"2026-10-03T09:59:59.123Z","log.level":"warn","message":"slow","ecs.version":"1.6.0","service.name":"cart","log":{"logger":"x.Y"},"http.response.status_code":503,"event.duration":1500000}`)
	if e.Format != ECS {
		t.Fatalf("format = %v, want ecs", e.Format)
	}
	if e.Level != "WARN" || e.Message != "slow" {
		t.Errorf("level, message = %q, %q", e.Level, e.Message)
	}
	if want := time.Date(2026, 10, 3, 9, 59, 59, 123e6, time.UTC); !e.Time.Equal(want) {
		t.Errorf("time = %v, want the entry's own %v", e.Time, want)
	}
	if !e.KubeTime.Equal(time.Date(2026, 10, 3, 10, 0, 0, 5e8, time.UTC)) {
		t.Errorf("kube time = %v", e.KubeTime)
	}
	log := e.Fields["log"].(map[string]any)
	if log["level"] != "warn" || log["logger"] != "x.Y" {
		t.Errorf("dotted keys not merged into log: %v", log)
	}
	status := e.Fields["http"].(map[string]any)["response"].(map[string]any)["status_code"]
	if status != int64(503) {
		t.Errorf("status_code = %#v, want int64 503", status)
	}
}

func TestParseZapJSON(t *testing.T) {
	e := Parse(`{"level":"error","ts":1759485600.25,"msg":"boom","caller":"a.go:1","weird.key":1}`)
	if e.Format != JSON || e.Level != "ERROR" || e.Message != "boom" {
		t.Fatalf("got %v %q %q", e.Format, e.Level, e.Message)
	}
	if e.Time.Unix() != 1759485600 || e.Time.Nanosecond() != 250000000 {
		t.Errorf("time = %v", e.Time)
	}
	if _, ok := e.Fields["weird.key"]; !ok {
		t.Error("plain JSON keys must stay as written")
	}
}

func TestParsePlain(t *testing.T) {
	for line, want := range map[string]string{
		`E1003 12:00:00.000000       1 server.go:1] failed`:  "ERROR",
		`2026/10/03 12:00:00 [warn] 7#7: upstream timed out`: "WARN",
		`10.0.0.1 - - "GET / HTTP/1.1" 200`:                  "",
		`{not json`:                                          "",
	} {
		e := Parse(line)
		if e.Format != Plain || e.Level != want || e.Message != line {
			t.Errorf("%q: format %v level %q message %q", line, e.Format, e.Level, e.Message)
		}
	}
}

func TestCompact(t *testing.T) {
	if got := Compact(map[string]any{"a": "<b>"}); got != `{"a":"<b>"}` {
		t.Errorf("Compact = %s", got)
	}
	if got := Compact("x"); got != "x" {
		t.Errorf("Compact string = %s", got)
	}
}

func TestParseECSVariants(t *testing.T) {
	// Colors and CRLF in the message must not reach the terminal.
	e := Parse("{\"@timestamp\":\"2026-10-03T09:59:59.1234567+00:00\",\"log.level\":\"Information\",\"message\":\"\\u001b[32mstarted\\u001b[0m\\r\\nnext\",\"ecs.version\":\"8.6.0\"}")
	if e.Format != ECS || e.Level != "INFO" || e.Message != "started\nnext" {
		t.Errorf("colored: %v %q %q", e.Format, e.Level, e.Message)
	}

	// Nested ECS without a version, as some libraries write it.
	e = Parse(`{"@timestamp":"2026-10-03T09:59:59Z","log":{"level":"WARN","logger":"x"},"message":"m"}`)
	if e.Format != ECS || e.Level != "WARN" {
		t.Errorf("nested: %v %q", e.Format, e.Level)
	}

	// An error entry without a message shows the error's.
	e = Parse(`{"@timestamp":"2026-10-03T09:59:59Z","log.level":"error","error.message":"boom","ecs.version":"1.6.0"}`)
	if e.Message != "boom" || e.Level != "ERROR" {
		t.Errorf("error entry: %q %q", e.Level, e.Message)
	}

	// A dotted key colliding with a plain value is kept as written, whatever
	// the map order.
	for range 50 {
		e = Parse(`{"@timestamp":"2026-10-03T09:59:59Z","log.level":"info","log":"text","ecs.version":"1.6.0"}`)
		if e.Level != "INFO" || e.Message != "text" || e.Fields["log"] != "text" || e.Fields["log.level"] != "info" {
			t.Fatalf("collision: %q %q %v", e.Level, e.Message, e.Fields)
		}
	}
}

func TestParseJSONVariants(t *testing.T) {
	e := Parse(`{"level":50,"time":1759485600123456,"msg":"pino"}`)
	if e.Level != "ERROR" || e.Time.Year() != 2025 {
		t.Errorf("pino: %q %v", e.Level, e.Time)
	}
	e = Parse("\ufeff" + `{"msg":{"a":1}}`)
	if e.Format != JSON || e.Message != `{"a":1}` {
		t.Errorf("object message: %v %q", e.Format, e.Message)
	}
}

func TestParsePlainColored(t *testing.T) {
	e := Parse("2026-10-03T10:00:00Z \x1b[31mERROR\x1b[0m failed\x07")
	if e.Level != "ERROR" || e.Message != "ERROR failed" || e.KubeTime.IsZero() {
		t.Errorf("got %q %q %v", e.Level, e.Message, e.KubeTime)
	}
}

func TestReadLine(t *testing.T) {
	long := strings.Repeat("x", 300)
	r := bufio.NewReaderSize(strings.NewReader("a\r\n"+long+"\n\nlast"), 16)
	var got []string
	for {
		l, err := ReadLine(r, 100)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, l)
	}
	want := []string{"a", long[:100], "", "last"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("lines = %q", got)
	}
}

func TestParseLogfmt(t *testing.T) {
	e := Parse(`2026-10-09T10:00:00Z logger=http.server t=2026-10-09T09:59:59.5Z level=warn msg="Request Completed" status=200 path="/api/a b" cached`)
	if e.Format != Logfmt || e.Level != "WARN" || e.Message != "Request Completed" {
		t.Fatalf("got %v %q %q", e.Format, e.Level, e.Message)
	}
	if e.Fields["status"] != "200" || e.Fields["path"] != "/api/a b" || e.Fields["cached"] != "true" {
		t.Errorf("fields %v", e.Fields)
	}
	if want := time.Date(2026, 10, 9, 9, 59, 59, 5e8, time.UTC); !e.Time.Equal(want) {
		t.Errorf("time %v", e.Time)
	}
	for _, plain := range []string{
		"Starting server on port=8080",           // prose with a pair
		"user=bob logged in from the office now", // mostly prose
		"a=1",                                    // one pair
		`msg="unterminated value=1`,
		"[INFO] x=1 y=2",
	} {
		if e := Parse(plain); e.Format != Plain {
			t.Errorf("%q parsed as %v", plain, e.Format)
		}
	}
}
