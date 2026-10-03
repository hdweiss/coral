package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand/v2"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Logs serves generated logs for the demo pods. The style depends on the
// app: ECS JSON for the shop services, zap-style JSON for fraud-check,
// nginx/redis/logfmt text for the off-the-shelf images and klog elsewhere.
// With Follow, a new line arrives every few hundred milliseconds.
func (p *demoProvider) Logs(ctx context.Context, kctx string, req LogRequest) (io.ReadCloser, error) {
	c, err := p.Client(kctx)
	if err != nil {
		return nil, err
	}
	pod, err := c.Resource(MustLookup("pods").GVR()).Namespace(req.Namespace).Get(ctx, req.Pod, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	crashing := strings.Contains(fmt.Sprint(pod.Object["status"]), "CrashLoopBackOff")
	if req.Previous && !crashing {
		return nil, fmt.Errorf("previous terminated container %q in pod %q not found", req.Container, req.Pod)
	}
	app := pod.GetLabels()["app"]
	if app == "" {
		app = pod.GetLabels()["app.kubernetes.io/name"]
	}
	h := fnv.New64a()
	h.Write([]byte(req.Pod + "/" + req.Container))
	g := &demoLog{app: app, pod: req.Pod, rng: rand.New(rand.NewPCG(h.Sum64(), 1)), crashing: crashing}

	pr, pw := io.Pipe()
	go func() {
		n := 300
		if req.TailLines > 0 {
			n = min(n, int(req.TailLines))
		}
		now := time.Now()
		write := func(t time.Time, line string) bool {
			_, err := io.WriteString(pw, t.UTC().Format(time.RFC3339Nano)+" "+line+"\n")
			return err == nil
		}
		for i := n; i > 0; i-- {
			t := now.Add(-time.Duration(i)*1700*time.Millisecond - time.Duration(g.rng.IntN(900))*time.Millisecond)
			if !write(t, g.line(t, req.Previous && i < 4)) {
				return
			}
		}
		if req.Previous || crashing || !req.Follow {
			pw.Close()
			return
		}
		for {
			select {
			case <-ctx.Done():
				pw.Close()
				return
			case <-time.After(time.Duration(150+g.rng.IntN(1200)) * time.Millisecond):
				t := time.Now()
				if !write(t, g.line(t, false)) {
					return
				}
			}
		}
	}()
	return pr, nil
}

type demoLog struct {
	app, pod string
	rng      *rand.Rand
	crashing bool
	seq      int
}

func (g *demoLog) pick(s ...string) string { return s[g.rng.IntN(len(s))] }

func (g *demoLog) hex(n int) string {
	var sb strings.Builder
	for range n {
		sb.WriteByte("0123456789abcdef"[g.rng.IntN(16)])
	}
	return sb.String()
}

// line generates one log line at t. dying makes it the last words of a
// crashing container.
func (g *demoLog) line(t time.Time, dying bool) string {
	g.seq++
	switch g.app {
	case "cart", "catalog", "payments-api":
		return g.ecs(t, dying)
	case "fraud-check":
		return g.zap(t)
	case "frontend":
		return g.nginx(t)
	case "redis":
		return fmt.Sprintf("1:M %s * %s", t.Format("02 Jan 2006 15:04:05.000"),
			g.pick("Background saving started by pid 42", "DB saved on disk", "100 changes in 300 seconds. Saving...", "Background saving terminated with success", "Accepted 10.42.1.17:51234"))
	case "grafana", "prometheus":
		lvl := g.pick("info", "info", "info", "warn", "debug")
		return fmt.Sprintf(`logger=%s t=%s level=%s msg=%q status=200 duration=%dms`,
			g.pick("context", "http.server", "cleanup", "sqlstore"), t.Format(time.RFC3339Nano), lvl,
			g.pick("Request Completed", "Completed cleanup jobs", "Database locked, sleeping then retrying", "HTTP Server Listen"), g.rng.IntN(80))
	}
	lvl := g.pick("I", "I", "I", "I", "W", "E")
	return fmt.Sprintf("%s%s %7d %s] %s", lvl, t.Format("0102 15:04:05.000000"), 1,
		g.pick("server.go:123", "reflector.go:561", "proxier.go:799", "health.go:58"),
		g.pick("Watch close - *v1.EndpointSlice total 7 items received", "Syncing iptables rules", "plugin/reload: Running configuration SHA512 = 2a0f…", "Using lease lock"))
}

func (g *demoLog) ecs(t time.Time, dying bool) string {
	m := map[string]any{
		"@timestamp":          t.UTC().Format("2006-01-02T15:04:05.000Z"),
		"ecs.version":         "1.6.0",
		"service.name":        g.app,
		"service.version":     map[string]string{"cart": "1.9.0", "catalog": "3.0.0-rc2", "payments-api": "5.2.0"}[g.app],
		"service.environment": "production",
		"host.hostname":       g.pod,
		"process.thread.name": fmt.Sprintf("http-nio-8080-exec-%d", 1+g.rng.IntN(10)),
		"event.dataset":       g.app + ".log",
	}
	logger := "com.acme." + strings.ReplaceAll(g.app, "-", "") + "."
	r := g.rng.IntN(100)
	switch {
	case dying || (g.crashing && r < 25):
		m["log.level"] = "error"
		m["log.logger"] = logger + "db.ConnectionPool"
		m["message"] = "Failed to obtain JDBC connection"
		m["error.type"] = "java.sql.SQLTransientConnectionException"
		m["error.message"] = "HikariPool-1 - Connection is not available, request timed out after 30000ms."
		m["error.stack_trace"] = "java.sql.SQLTransientConnectionException: HikariPool-1 - Connection is not available\n" +
			"\tat com.zaxxer.hikari.pool.HikariPool.createTimeoutException(HikariPool.java:696)\n" +
			"\tat com.zaxxer.hikari.pool.HikariPool.getConnection(HikariPool.java:181)\n" +
			"\tat " + logger + "db.ConnectionPool.get(ConnectionPool.java:42)\n" +
			"\tat " + logger + "Application.main(Application.java:17)"
		if dying {
			m["log.level"] = "fatal"
			m["message"] = "Application run failed"
		}
	case r < 70:
		status := 200
		switch x := g.rng.IntN(20); {
		case x == 0:
			status = 500
		case x < 3:
			status = 404
		case x < 5:
			status = 201
		}
		method := g.pick("GET", "GET", "GET", "POST", "PUT", "DELETE")
		path := "/api/" + g.app + "/" + fmt.Sprint(1000+g.rng.IntN(9000))
		m["log.level"] = "info"
		if status >= 500 {
			m["log.level"] = "error"
		}
		m["log.logger"] = logger + "web.RequestLogger"
		m["message"] = fmt.Sprintf("%s %s %d", method, path, status)
		m["http.request.method"] = method
		m["url.path"] = path
		m["http.response.status_code"] = status
		m["event.duration"] = int64(1+g.rng.IntN(250)) * 1_000_000
		m["trace.id"] = g.hex(32)
		m["transaction.id"] = g.hex(16)
		m["client.ip"] = fmt.Sprintf("10.42.%d.%d", g.rng.IntN(4), 2+g.rng.IntN(250))
		m["user_agent.original"] = g.pick("Mozilla/5.0 (X11; Linux x86_64)", "okhttp/4.12.0", "kube-probe/1.37")
	case r < 85:
		m["log.level"] = "debug"
		m["log.logger"] = logger + "cache.Redis"
		m["message"] = g.pick("cache hit", "cache miss, loading from database", "evicted 12 entries")
		m["labels"] = map[string]any{"cache_key": g.app + ":" + g.hex(8)}
	default:
		m["log.level"] = "warn"
		m["log.logger"] = logger + "client.Retry"
		m["message"] = g.pick("retrying request to inventory (attempt 2/3)", "slow query took 1240ms", "circuit breaker half-open")
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (g *demoLog) zap(t time.Time) string {
	m := map[string]any{
		"level":      g.pick("info", "info", "info", "debug", "warn", "error"),
		"ts":         float64(t.UnixNano()) / 1e9,
		"caller":     g.pick("server/handler.go:42", "rules/engine.go:118", "score/model.go:77"),
		"msg":        g.pick("scored transaction", "rule matched", "request finished", "model reloaded"),
		"request_id": g.hex(12),
		"score":      float64(g.rng.IntN(1000)) / 1000,
		"latency_ms": g.rng.IntN(40),
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (g *demoLog) nginx(t time.Time) string {
	status := g.pick("200", "200", "200", "200", "304", "404", "502")
	return fmt.Sprintf(`10.42.%d.%d - - [%s] "%s %s HTTP/1.1" %s %d "-" "%s"`,
		g.rng.IntN(4), 2+g.rng.IntN(250), t.Format("02/Jan/2006:15:04:05 -0700"),
		g.pick("GET", "GET", "POST"), g.pick("/", "/cart", "/products/42", "/static/app.js", "/healthz"),
		status, 200+g.rng.IntN(20000), g.pick("Mozilla/5.0 (X11; Linux x86_64)", "curl/8.9.1", "kube-probe/1.37"))
}
