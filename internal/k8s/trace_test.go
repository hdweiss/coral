package k8s

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTraceTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			gz.Write([]byte(strings.Repeat("x", 4096)))
			gz.Close()
			return
		}
		w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer srv.Close()

	var log strings.Builder
	client := &http.Client{Transport: NewTracer(&log).WrapTransport(http.DefaultTransport)}
	resp, err := client.Get(srv.URL + "/api/v1/pods?resourceVersion=0")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	line := log.String()
	for _, want := range []string{"GET /api/v1/pods?resourceVersion=0 200 4.0kB", " gzip"} {
		if !strings.Contains(line, want) {
			t.Errorf("trace %q lacks %q", line, want)
		}
	}
}
