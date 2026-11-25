package source

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCancelScopeBlocksDeepChildrenAtomically(t *testing.T) {
	for range 25 {
		scopeCtx, cancelScope := WithCancelScope(context.Background())

		// Fill all background slots using shallow child contexts.
		var holders sync.WaitGroup
		ready := make(chan struct{}, BackgroundLimit)
		for range BackgroundLimit {
			child, cancelChild := context.WithCancel(scopeCtx)
			defer cancelChild()
			holders.Go(func() {
				release, err := AcquireBackground(child)
				if err != nil {
					return
				}
				ready <- struct{}{}
				<-child.Done()
				release()
			})
		}
		for range BackgroundLimit {
			<-ready
		}

		// Queue waiters with deeply nested child contexts (matching following ->
		// itemTimeout -> scanCancel -> executeTimeout).
		var admitted atomic.Int64
		var waiters sync.WaitGroup
		for range 8 {
			c1, cancel1 := context.WithCancel(scopeCtx)
			c2, cancel2 := context.WithTimeout(c1, 5*time.Second)
			c3, cancel3 := context.WithCancel(c2)
			c4, cancel4 := context.WithTimeout(c3, 5*time.Second)
			defer cancel4()
			defer cancel3()
			defer cancel2()
			defer cancel1()
			waiters.Go(func() {
				release, err := AcquireBackground(c4)
				if err == nil {
					admitted.Add(1)
					release()
				}
			})
		}
		time.Sleep(5 * time.Millisecond)

		// Cancel the shared scope: releasing the 4 held slots must never admit
		// any of the deeply nested waiters.
		cancelScope()
		holders.Wait()
		waiters.Wait()
		if got := admitted.Load(); got != 0 {
			t.Fatalf("canceled scope admitted %d deeply nested waiters after cancel", got)
		}
	}
}

func TestCancelScopeIsolatesIndependentChildCancel(t *testing.T) {
	scopeCtx, cancelScope := WithCancelScope(context.Background())
	defer cancelScope()

	childA, cancelA := context.WithCancel(scopeCtx)
	childB, cancelB := context.WithCancel(scopeCtx)
	defer cancelB()

	cancelA()
	if _, err := AcquireBackground(childA); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled childA err = %v; want context.Canceled", err)
	}
	release, err := AcquireBackground(childB)
	if err != nil {
		t.Fatalf("sibling childB blocked by childA cancel: %v", err)
	}
	release()

	// Nested scopes inherit parent atomic cancellation.
	nestedCtx, cancelNested := WithCancelScope(scopeCtx)
	defer cancelNested()
	cancelScope()
	if _, err := AcquireBackground(nestedCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("nested scope err = %v; want context.Canceled", err)
	}
}
