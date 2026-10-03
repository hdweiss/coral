package logs

import (
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
		`E1003 12:00:00.000000       1 server.go:1] failed`:         "ERROR",
		`2026/10/03 12:00:00 [warn] 7#7: upstream timed out`:        "WARN",
		`logger=context t=2026-10-03T12:00:00Z level=info msg="ok"`: "INFO",
		`10.0.0.1 - - "GET / HTTP/1.1" 200`:                         "",
		`{not json`:                                                 "",
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
