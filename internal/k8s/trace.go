package k8s

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	k8stesting "k8s.io/client-go/testing"
)

// Tracer logs API requests, one line each (coral --trace-api). A nil Tracer
// logs nothing.
type Tracer struct {
	mu sync.Mutex
	w  io.Writer
}

func NewTracer(w io.Writer) *Tracer { return &Tracer{w: w} }

func (t *Tracer) logf(format string, args ...any) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.w, time.Now().Format("15:04:05.000")+" "+format+"\n", args...)
}

// WrapTransport logs every request of a real cluster: verb, path with query,
// status, response bytes, duration, and whether the response came gzipped.
// The line is written when the body is closed, so streams (logs) log when
// they end.
func (t *Tracer) WrapTransport(rt http.RoundTripper) http.RoundTripper {
	if t == nil {
		return rt
	}
	return traceRT{t: t, next: rt}
}

type traceRT struct {
	t    *Tracer
	next http.RoundTripper
}

func (rt traceRT) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := rt.next.RoundTrip(req)
	if err != nil {
		rt.t.logf("%s %s error %v %s", req.Method, req.URL.RequestURI(), err, time.Since(start).Round(time.Millisecond))
		return resp, err
	}
	if req.URL.Query().Get("watch") == "true" {
		// Its line is written when it ends, minutes later.
		rt.t.logf("%s %s %d watch opened", req.Method, req.URL.RequestURI(), resp.StatusCode)
	}
	resp.Body = &traceBody{ReadCloser: resp.Body, done: func(n int64) {
		enc := ""
		// Go's transport asks for gzip itself and decompresses
		// transparently; Uncompressed says it did.
		if resp.Uncompressed || resp.Header.Get("Content-Encoding") == "gzip" {
			enc = " gzip"
		}
		rt.t.logf("%s %s %d %s %s%s", req.Method, req.URL.RequestURI(), resp.StatusCode,
			formatBytes(n), time.Since(start).Round(time.Millisecond), enc)
	}}
	return resp, nil
}

// traceBody counts the bytes read and reports them once, on close.
type traceBody struct {
	io.ReadCloser
	n    int64
	once sync.Once
	done func(n int64)
}

func (b *traceBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	return n, err
}

func (b *traceBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.done(b.n) })
	return err
}

func formatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fkB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(n)/1024/1024)
}

// reactor logs the requests of a demo cluster: verb, resource, namespace and
// selectors. It never handles the action itself.
func (t *Tracer) reactor(cluster string) k8stesting.ReactionFunc {
	return func(action k8stesting.Action) (bool, runtime.Object, error) {
		t.logAction(cluster, action)
		return false, nil, nil
	}
}

// watchReactor logs the watches of a demo cluster, like reactor.
func (t *Tracer) watchReactor(cluster string) k8stesting.WatchReactionFunc {
	return func(action k8stesting.Action) (bool, watch.Interface, error) {
		t.logAction(cluster, action)
		return false, nil, nil
	}
}

func (t *Tracer) logAction(cluster string, action k8stesting.Action) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s", cluster, strings.ToUpper(action.GetVerb()), action.GetResource().Resource)
	if g := action.GetResource().Group; g != "" {
		b.WriteString("." + g)
	}
	if ns := action.GetNamespace(); ns != "" {
		b.WriteString(" ns=" + ns)
	}
	if l, ok := action.(k8stesting.ListAction); ok {
		if f := l.GetListRestrictions().Fields; f != nil && !f.Empty() {
			b.WriteString(" fields=" + f.String())
		}
	}
	if w, ok := action.(k8stesting.WatchAction); ok {
		if f := w.GetWatchRestrictions().Fields; f != nil && !f.Empty() {
			b.WriteString(" fields=" + f.String())
		}
		b.WriteString(" rv=" + w.GetWatchRestrictions().ResourceVersion)
	}
	t.logf("%s", b.String())
}
