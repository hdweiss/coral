package k8s

import (
	"encoding/json"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// persists reports whether list k is kept on disk: a context's CRD list.
func (s *Store) persists(k Key) bool {
	return s.cacheDir != "" && k == CRDKey(k.Context)
}

func (s *Store) diskPath(k Key) string {
	return filepath.Join(CacheDir(s.cacheDir, k.Context), "crds.json")
}

type diskList struct {
	Items []map[string]any `json:"items"`
}

// loadDisk reads list k from disk into the cache. It counts as fetched when
// the file was written.
func (s *Store) loadDisk(k Key) (Entry, bool) {
	path := s.diskPath(k)
	fi, err := os.Stat(path)
	if err != nil {
		return Entry{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, false
	}
	var l diskList
	if json.Unmarshal(b, &l) != nil {
		return Entry{}, false
	}
	e := Entry{FetchedAt: fi.ModTime(), Items: make([]unstructured.Unstructured, len(l.Items))}
	for i, it := range l.Items {
		e.Items[i].Object = it
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.entries[k]; ok { // fetched meanwhile
		return cur, true
	}
	s.entries[k] = e
	return e, true
}

// saveDisk writes list k to disk. Failing to is not worth bothering anyone
// with: the list is just fetched again next time.
func (s *Store) saveDisk(k Key, e Entry) {
	l := diskList{Items: make([]map[string]any, len(e.Items))}
	for i := range e.Items {
		l.Items[i] = e.Items[i].Object
	}
	b, err := json.Marshal(l)
	if err != nil {
		return
	}
	path := s.diskPath(k)
	if os.MkdirAll(filepath.Dir(path), 0o750) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".crds-*.json")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return
	}
	if os.Rename(tmp.Name(), path) != nil {
		os.Remove(tmp.Name())
	}
}
