package client

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-kratos/gateway/constants"
	"github.com/go-kratos/kratos/v2/registry"
)

type watchResult struct {
	services []*registry.ServiceInstance
	err      error
}

type scriptedWatcher struct {
	ctx     context.Context
	results chan watchResult
}

func (w *scriptedWatcher) Next() ([]*registry.ServiceInstance, error) {
	select {
	case result := <-w.results:
		return result.services, result.err
	case <-w.ctx.Done():
		return nil, w.ctx.Err()
	}
}

func (w *scriptedWatcher) Stop() error {
	return nil
}

type scriptedDiscovery struct {
	mu       sync.Mutex
	watchers []*scriptedWatcher
	calls    int
}

func (d *scriptedDiscovery) GetService(context.Context, string) ([]*registry.ServiceInstance, error) {
	return nil, nil
}

func (d *scriptedDiscovery) Watch(ctx context.Context, _ string) (registry.Watcher, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.calls >= len(d.watchers) {
		return nil, errors.New("unexpected watcher creation")
	}
	watcher := d.watchers[d.calls]
	d.calls++
	watcher.ctx = ctx
	return watcher, nil
}

func (d *scriptedDiscovery) watchCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

type callbackApplier struct {
	updates chan []*registry.ServiceInstance
}

func (a *callbackApplier) Callback(instances []*registry.ServiceInstance) error {
	a.updates <- instances
	return nil
}

func (*callbackApplier) Canceled() bool {
	return false
}

func TestServiceWatcherRecreatesWatcherAfterNextError(t *testing.T) {
	originalRetrySteps := constants.DiscoveryRetrySteps
	constants.DiscoveryRetrySteps = []time.Duration{time.Millisecond}
	t.Cleanup(func() {
		constants.DiscoveryRetrySteps = originalRetrySteps
	})

	firstWatcher := &scriptedWatcher{results: make(chan watchResult, 1)}
	firstWatcher.results <- watchResult{err: errors.New("watch connection lost")}

	expected := []*registry.ServiceInstance{{ID: "user-1", Name: "user-identity"}}
	secondWatcher := &scriptedWatcher{results: make(chan watchResult, 1)}
	secondWatcher.results <- watchResult{services: expected}

	discovery := &scriptedDiscovery{watchers: []*scriptedWatcher{firstWatcher, secondWatcher}}
	applier := &callbackApplier{updates: make(chan []*registry.ServiceInstance, 1)}
	watcher := newServiceWatcher()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		watcher.lock.RLock()
		status := watcher.watcherStatus["user-identity"]
		watcher.lock.RUnlock()
		if status != nil {
			status.cancel()
		}
	})

	if existed := watcher.Add(ctx, discovery, "user-identity", applier); existed {
		t.Fatal("Add reported an existing watcher")
	}
	cancel()

	select {
	case instances := <-applier.updates:
		if len(instances) != 1 || instances[0].ID != "user-1" {
			t.Fatalf("applier received %+v, want user-1", instances)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for instances from recreated watcher")
	}

	if calls := discovery.watchCalls(); calls != 2 {
		t.Fatalf("Watch called %d times, want 2", calls)
	}
	instances, ok := watcher.getSelectedCache("user-identity")
	if !ok || len(instances) != 1 || instances[0].ID != "user-1" {
		t.Fatalf("selected cache = %+v, %t, want user-1", instances, ok)
	}
}
