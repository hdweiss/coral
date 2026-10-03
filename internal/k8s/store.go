package k8s

import (
	"context"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Key identifies one cached list.
type Key struct {
	Context   string
	GVR       schema.GroupVersionResource
	Namespace string // "" means all namespaces, or a cluster-scoped resource
}

// Entry is a cached list result. A failed refresh keeps the previous items.
type Entry struct {
	Items     []unstructured.Unstructured
	FetchedAt time.Time
	Err       error
}

// Store caches list results so that views render instantly from memory and
// refresh in the background.
type Store struct {
	provider Provider
	timeout  time.Duration

	mu      sync.RWMutex
	entries map[Key]Entry
}

func NewStore(p Provider, timeout time.Duration) *Store {
	return &Store{provider: p, timeout: timeout, entries: map[Key]Entry{}}
}

func (s *Store) Provider() Provider { return s.provider }

func (s *Store) Get(k Key) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[k]
	return e, ok
}

// Fetch lists the resource from the cluster and caches the result. It is
// safe to call from any goroutine.
func (s *Store) Fetch(k Key) Entry {
	e := Entry{FetchedAt: time.Now()}
	items, err := s.list(k)
	if err != nil {
		e.Err = err
		if old, ok := s.Get(k); ok {
			e.Items = old.Items
		}
	} else {
		e.Items = items
	}
	s.mu.Lock()
	s.entries[k] = e
	s.mu.Unlock()
	return e
}

func (s *Store) list(k Key) ([]unstructured.Unstructured, error) {
	client, err := s.provider.Client(k.Context)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	var ri dynamic.ResourceInterface = client.Resource(k.GVR)
	if k.Namespace != "" {
		ri = client.Resource(k.GVR).Namespace(k.Namespace)
	}
	list, err := ri.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}
