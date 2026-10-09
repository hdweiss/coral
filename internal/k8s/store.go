package k8s

import (
	"context"
	"slices"
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
	// Fields is a field selector, e.g. "type=Warning". The demo's fake
	// clients ignore it, so callers filter the items again.
	Fields string
}

// Entry is a cached list result. A failed refresh keeps the previous items.
type Entry struct {
	Items     []unstructured.Unstructured
	FetchedAt time.Time
	Err       error
	// ResourceVersion is the list's, or that of the last change a watch
	// applied; a watch resumes from it. "" for lists read from disk.
	ResourceVersion string
}

// Store caches list results so that views render instantly from memory and
// refresh in the background.
type Store struct {
	provider Provider
	timeout  time.Duration

	mu         sync.RWMutex
	entries    map[Key]Entry
	inflight   map[Key]*call
	registries map[string]registryAt

	cacheDir string // where CRD lists persist; "" keeps them in memory
}

// registryAt is a context's registry, built from the CRD list fetched at at.
type registryAt struct {
	at  time.Time
	reg *Registry
}

// call is a list request in flight, which later fetches of the same key wait
// for instead of sending their own.
type call struct {
	done  chan struct{}
	entry Entry
}

func NewStore(p Provider, timeout time.Duration) *Store {
	return &Store{provider: p, timeout: timeout, entries: map[Key]Entry{}, inflight: map[Key]*call{},
		registries: map[string]registryAt{}}
}

// SetCacheDir keeps the contexts' CRD lists on disk under dir, so that a
// restart shows the custom resources right away and lists them again only
// once they are older than CRDMaxAge.
func (s *Store) SetCacheDir(dir string) { s.cacheDir = dir }

func (s *Store) Provider() Provider { return s.provider }

func (s *Store) Get(k Key) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[k]
	return e, ok
}

// Registry returns the resources of a context, with the custom resources of
// its cached CRD list; nil until that has been listed.
func (s *Store) Registry(ctx string) *Registry {
	e, ok := s.Get(CRDKey(ctx))
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.registries[ctx]; ok && r.at.Equal(e.FetchedAt) {
		return r.reg
	}
	reg := NewRegistry(CustomResources(e.Items), e.Err)
	s.registries[ctx] = registryAt{at: e.FetchedAt, reg: reg}
	return reg
}

// Fetch lists the resource from the cluster and caches the result. It is
// safe to call from any goroutine; a fetch of a key that is already being
// fetched waits for that request and returns its result.
func (s *Store) Fetch(k Key) Entry {
	s.mu.Lock()
	if c, ok := s.inflight[k]; ok {
		s.mu.Unlock()
		<-c.done
		return c.entry
	}
	c := &call{done: make(chan struct{})}
	s.inflight[k] = c
	s.mu.Unlock()

	e := Entry{FetchedAt: time.Now()}
	items, rv, err := s.list(k)
	if err != nil {
		e.Err = err
		if old, ok := s.Get(k); ok {
			e.Items, e.ResourceVersion = old.Items, old.ResourceVersion
		}
	} else {
		e.Items, e.ResourceVersion = items, rv
	}
	s.mu.Lock()
	s.entries[k] = e
	delete(s.inflight, k)
	s.mu.Unlock()
	if e.Err == nil && s.persists(k) {
		s.saveDisk(k, e)
	}
	c.entry = e
	close(c.done)
	return e
}

// Cached returns the cached list of k when it is younger than maxAge, and
// fetches it otherwise. Lists kept on disk are looked for there first.
func (s *Store) Cached(k Key, maxAge time.Duration) Entry {
	e, ok := s.Get(k)
	if !ok && s.persists(k) {
		e, ok = s.loadDisk(k)
	}
	if ok && time.Since(e.FetchedAt) <= maxAge {
		return e
	}
	return s.Fetch(k)
}

func (s *Store) list(k Key) ([]unstructured.Unstructured, string, error) {
	client, err := s.provider.Client(k.Context)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	var ri dynamic.ResourceInterface = client.Resource(k.GVR)
	if k.Namespace != "" {
		ri = client.Resource(k.GVR).Namespace(k.Namespace)
	}
	// ResourceVersion "0" lets the API server answer from its watch cache
	// instead of reading through to etcd. The list may be a moment stale,
	// which a refreshing view doesn't mind.
	list, err := ri.List(ctx, metav1.ListOptions{ResourceVersion: "0", FieldSelector: k.Fields})
	if err != nil {
		return nil, "", err
	}
	return list.Items, list.GetResourceVersion(), nil
}

func (s *Store) resource(k Key, namespace string) (dynamic.ResourceInterface, error) {
	client, err := s.provider.Client(k.Context)
	if err != nil {
		return nil, err
	}
	if namespace != "" {
		return client.Resource(k.GVR).Namespace(namespace), nil
	}
	return client.Resource(k.GVR), nil
}

// GetObject reads one object of k's resource fresh from the cluster.
func (s *Store) GetObject(k Key, namespace, name string) (*unstructured.Unstructured, error) {
	ri, err := s.resource(k, namespace)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return ri.Get(ctx, name, metav1.GetOptions{})
}

// Update replaces an object of k's resource. The object's resourceVersion
// makes the server reject it if someone else changed the object meanwhile.
func (s *Store) Update(k Key, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	ri, err := s.resource(k, obj.GetNamespace())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	u, err := ri.Update(ctx, obj, metav1.UpdateOptions{FieldManager: "coralctl"})
	if err == nil {
		s.replace(k, u)
	}
	return u, err
}

// replace puts obj in place of the object with its uid in the cached lists
// of its resource, so that views show the change without relisting: a list
// from the watch cache could still miss it.
func (s *Store) replace(k Key, obj *unstructured.Unstructured) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, e := range s.entries {
		if key.Context != k.Context || key.GVR != k.GVR {
			continue
		}
		for i := range e.Items {
			if e.Items[i].GetUID() == obj.GetUID() {
				// Copy: views hold on to the old slice.
				e.Items = slices.Clone(e.Items)
				e.Items[i] = *obj.DeepCopy()
				s.entries[key] = e
				break
			}
		}
	}
}

// Delete deletes obj, an object of k's resource. The uid precondition makes
// sure it is still the object that was shown, not a new one of that name.
// Cached lists keep it: a real cluster may only mark it Terminating, and the
// next list (or a watch) shows what happened.
func (s *Store) Delete(k Key, obj *unstructured.Unstructured, opts metav1.DeleteOptions) error {
	ri, err := s.resource(k, obj.GetNamespace())
	if err != nil {
		return err
	}
	uid := obj.GetUID()
	opts.Preconditions = &metav1.Preconditions{UID: &uid}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return ri.Delete(ctx, obj.GetName(), opts)
}
