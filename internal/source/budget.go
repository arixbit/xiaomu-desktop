package source

import "context"

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

func acquireSlot(ctx context.Context, slots chan struct{}) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
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
