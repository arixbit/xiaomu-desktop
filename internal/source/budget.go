package source

import (
	"context"
	"sync/atomic"
)

const (
	// BackgroundLimit bounds quiet background tasks (home feed scans, follow
	// refreshes and speed tests) so they stay lightweight.
	BackgroundLimit = 4
	// InteractiveLimit bounds foreground user-initiated searches and detail
	// lookups independently of quiet background scans.
	InteractiveLimit = 10
	// ScriptLimit bounds concurrent QuickJS/XBPQ script evaluations across all
	// foreground and background tasks so heavy JS sites never spike CPU or RAM.
	ScriptLimit = 4
)

var (
	backgroundSlots  = make(chan struct{}, BackgroundLimit)
	interactiveSlots = make(chan struct{}, InteractiveLimit)
	scriptSlots      = make(chan struct{}, ScriptLimit)
)

type cancelScopeKey struct{}

type cancelScope struct {
	parent   *cancelScope
	canceled atomic.Bool
}

func (s *cancelScope) isCanceled() bool {
	for cur := s; cur != nil; cur = cur.parent {
		if cur.canceled.Load() {
			return true
		}
	}
	return false
}

// WithCancelScope returns a cancellable context that marks the scope canceled
// atomically before propagating Go's per-child context cancellation. Workers
// sharing a concurrency pool check this flag on acquire so a slot released by
// one canceled worker cannot admit another worker in the same scope before its
// deeper child context observes cancellation.
func WithCancelScope(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	prev, _ := parent.Value(cancelScopeKey{}).(*cancelScope)
	scope := &cancelScope{parent: prev}
	ctx, cancel := context.WithCancel(context.WithValue(parent, cancelScopeKey{}, scope))
	return ctx, func() {
		scope.canceled.Store(true)
		cancel()
	}
}

func scopeErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if scope, _ := ctx.Value(cancelScopeKey{}).(*cancelScope); scope != nil && scope.isCanceled() {
		return context.Canceled
	}
	return nil
}

func acquireSlot(ctx context.Context, slots chan struct{}) (release func(), err error) {
	if err := scopeErr(ctx); err != nil {
		return nil, err
	}
	select {
	case slots <- struct{}{}:
		if err := scopeErr(ctx); err != nil {
			<-slots
			return nil, err
		}
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// AcquireBackground must only be called from work goroutines. The returned
// release belongs to one finite background site operation, never to a cached UI session.
func AcquireBackground(ctx context.Context) (release func(), err error) {
	return acquireSlot(ctx, backgroundSlots)
}

// AcquireInteractive acquires a slot for foreground user-initiated searches so
// they never queue behind quiet background scans.
func AcquireInteractive(ctx context.Context) (release func(), err error) {
	return acquireSlot(ctx, interactiveSlots)
}

// AcquireScript bounds concurrent script engine evaluations. Callers that also
// hold a background or interactive slot must acquire it before AcquireScript so
// lock ordering stays consistent everywhere.
func AcquireScript(ctx context.Context) (release func(), err error) {
	return acquireSlot(ctx, scriptSlots)
}
