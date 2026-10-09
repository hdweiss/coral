package k8s

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
)

const (
	watchMinBackoff = time.Second
	watchMaxBackoff = 30 * time.Second
)

// errRelisted stops a watch whose list was fetched again meanwhile (r), so
// that it resumes from that list instead of applying older changes to it.
var errRelisted = errors.New("relisted")

// Watch keeps the cached list of k current until stop is called: it lists
// k, then watches from the list's resourceVersion and applies the changes
// to the entry. notify is called, from another goroutine, whenever the entry
// changed. A watch the server ends is resumed from the last version seen; an
// expired version (410 Gone) or a failed watch lists again, with backoff on
// errors.
//
// Starting always lists, even when the entry is fresh: changes made while
// nothing watched are not in it, and resuming from an old version would miss
// the demo's (whose fake watch can't replay deletions).
func (s *Store) Watch(k Key, notify func()) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	go s.watch(ctx, k, notify)
	return cancel
}

func (s *Store) watch(ctx context.Context, k Key, notify func()) {
	wait := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
	backoff := watchMinBackoff
	relist := true
	for ctx.Err() == nil {
		if relist {
			e := s.Fetch(k)
			if ctx.Err() != nil {
				return
			}
			notify()
			if e.Err != nil {
				if !wait(backoff) {
					return
				}
				backoff = min(2*backoff, watchMaxBackoff)
				continue
			}
			relist = false
		}
		start := time.Now()
		err := s.watchOnce(ctx, k, notify)
		switch {
		case ctx.Err() != nil:
			return
		case err == nil || errors.Is(err, errRelisted):
			// The server ended the watch (its timeout); resume. One that
			// ends right away is a misbehaving proxy, not worth a tight loop.
			if time.Since(start) < time.Second && !wait(backoff) {
				return
			}
			backoff = watchMinBackoff
		case apierrors.IsResourceExpired(err) || apierrors.IsGone(err):
			relist = true
		default:
			s.fail(k, err)
			notify()
			if !wait(backoff) {
				return
			}
			backoff = min(2*backoff, watchMaxBackoff)
			relist = true
		}
	}
}

// watchOnce runs one watch request from the entry's resourceVersion until it
// ends, applying its changes in batches.
func (s *Store) watchOnce(ctx context.Context, k Key, notify func()) error {
	e, _ := s.Get(k)
	rv := e.ResourceVersion
	ri, err := s.resource(k, k.Namespace)
	if err != nil {
		return err
	}
	// Like client-go's reflector: the server ends the watch after a random
	// 5–10 minutes, so that clients don't all reconnect at once.
	timeout := int64(300 + rand.IntN(300))
	w, err := ri.Watch(ctx, metav1.ListOptions{
		ResourceVersion: rv, FieldSelector: k.Fields,
		AllowWatchBookmarks: true, TimeoutSeconds: &timeout,
	})
	if err != nil {
		return err
	}
	defer w.Stop()
	for {
		var batch []watch.Event
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.ResultChan():
			if !ok {
				return nil
			}
			batch = append(batch, ev)
		}
		// Whatever else has arrived goes in the same update: a rollout
		// changes many objects at once.
		open := true
	drain:
		for open {
			select {
			case ev, ok := <-w.ResultChan():
				if !ok {
					open = false
					break drain
				}
				batch = append(batch, ev)
			default:
				break drain
			}
		}
		changed, err := s.apply(k, &rv, batch)
		if changed {
			notify()
		}
		if err != nil || !open {
			return err
		}
	}
}

// apply applies watch events to the entry of k, which must still be at
// version *rv, and advances *rv. Views hold on to the old item slice, so the
// changes go to a copy.
func (s *Store) apply(k Key, rv *string, batch []watch.Event) (changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[k]
	if !ok || e.ResourceVersion != *rv {
		return false, errRelisted
	}
	items := e.Items
	cloned := false
	for _, ev := range batch {
		if ev.Type == watch.Error {
			err = apierrors.FromObject(ev.Object)
			break
		}
		obj, ok := ev.Object.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		if v := obj.GetResourceVersion(); v != "" {
			*rv = v
		}
		if ev.Type == watch.Bookmark {
			continue
		}
		if !cloned {
			items, cloned = slices.Clone(items), true
		}
		i := slices.IndexFunc(items, func(u unstructured.Unstructured) bool { return u.GetUID() == obj.GetUID() })
		switch {
		case ev.Type == watch.Deleted:
			if i >= 0 {
				items = slices.Delete(items, i, i+1)
			}
		case i >= 0:
			items[i] = *obj
		default:
			items = append(items, *obj)
		}
	}
	e.Items, e.ResourceVersion, e.FetchedAt, e.Err = items, *rv, time.Now(), nil
	s.entries[k] = e
	return cloned, err
}

// fail records a watch error on the entry of k, keeping its items.
func (s *Store) fail(k Key, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[k]
	e.Err = err
	s.entries[k] = e
}
