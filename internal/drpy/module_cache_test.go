package drpy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteModuleCacheDeduplicatesConcurrentFetchesAndRespectsCancel(t *testing.T) {
	cache := &remoteModuleCache{}
	var calls atomic.Int32
	gate := make(chan struct{})
	started := make(chan struct{}, 1)

	fetch := func(ctx context.Context, name string) (string, error) {
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-gate:
			return "export default 42;", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	var wg sync.WaitGroup
	results := make([]string, 4)
	errs := make([]error, 4)
	for i := range 4 {
		wg.Go(func() {
			results[i], errs[i] = cache.getOrFetch(context.Background(), "https://cdn.example.com/lib/cheerio.min.js", fetch)
		})
	}
	<-started
	time.Sleep(10 * time.Millisecond)
	close(gate)
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("expected 1 coalesced fetch for 4 concurrent callers, got %d", calls.Load())
	}
	for i := range 4 {
		if errs[i] != nil || results[i] != "export default 42;" {
			t.Fatalf("caller %d got (%q, %v)", i, results[i], errs[i])
		}
	}

	// Subsequent call hits in-memory cache with 0 network calls.
	got, err := cache.getOrFetch(context.Background(), "https://cdn.example.com/lib/cheerio.min.js", fetch)
	if err != nil || got != "export default 42;" || calls.Load() != 1 {
		t.Fatalf("cache hit failed: got=%q err=%v calls=%d", got, err, calls.Load())
	}

	// Failed fetches must not be cached.
	var failCalls atomic.Int32
	_, err = cache.getOrFetch(context.Background(), "https://cdn.example.com/lib/broken.js", func(context.Context, string) (string, error) {
		failCalls.Add(1)
		return "", errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	_, err = cache.getOrFetch(context.Background(), "https://cdn.example.com/lib/broken.js", func(context.Context, string) (string, error) {
		failCalls.Add(1)
		return "export default 1;", nil
	})
	if err != nil || failCalls.Load() != 2 {
		t.Fatalf("failed module should not stay cached: err=%v calls=%d", err, failCalls.Load())
	}
}

func TestPreludeSilencesConsoleLogWithoutThrowing(t *testing.T) {
	eng, err := Open(Options{
		Modules: map[string]string{
			"https://cdn.example.com/test.js": `
				console.log("noisy", { a: 1 });
				print("noisy print");
				export default {
					ping() { return JSON.stringify({ ok: true }); }
				};
			`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.LoadContext(context.Background(), "https://cdn.example.com/test.js"); err != nil {
		t.Fatal(err)
	}
	if got, err := eng.Call(context.Background(), "ping"); err != nil || got != `{"ok":true}` {
		t.Fatalf("ping = (%q, %v)", got, err)
	}
}

func TestRemoteModuleCacheCachesNotFoundAndGoneButNotServerErrors(t *testing.T) {
	cache := &remoteModuleCache{}

	// 1. Concurrent 404 requests coalesce and subsequent calls hit negative cache.
	var notFoundCalls atomic.Int32
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	fetch404 := func(ctx context.Context, name string) (string, error) {
		notFoundCalls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-gate
		return "", &remoteModuleStatusError{status: 404, msg: "drpy: module " + name + ": HTTP 404"}
	}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range 4 {
		wg.Go(func() {
			_, errs[i] = cache.getOrFetch(context.Background(), "https://cdn.example.com/lib/missing.js", fetch404)
		})
	}
	<-started
	time.Sleep(10 * time.Millisecond)
	close(gate)
	wg.Wait()
	if notFoundCalls.Load() != 1 {
		t.Fatalf("expected 1 coalesced 404 fetch, got %d", notFoundCalls.Load())
	}
	for i, err := range errs {
		if err == nil {
			t.Fatalf("caller %d unexpectedly succeeded", i)
		}
	}
	// Follow-up sequential call must also hit negative cache without fetching.
	if _, err := cache.getOrFetch(context.Background(), "https://cdn.example.com/lib/missing.js", fetch404); err == nil {
		t.Fatal("expected cached 404 error")
	}
	if notFoundCalls.Load() != 1 {
		t.Fatalf("cached 404 should not re-fetch: got %d calls", notFoundCalls.Load())
	}

	// 2. HTTP 410 Gone is also cached.
	var goneCalls atomic.Int32
	fetch410 := func(context.Context, string) (string, error) {
		goneCalls.Add(1)
		return "", &remoteModuleStatusError{status: 410, msg: "HTTP 410"}
	}
	_, _ = cache.getOrFetch(context.Background(), "https://cdn.example.com/lib/gone.js", fetch410)
	_, _ = cache.getOrFetch(context.Background(), "https://cdn.example.com/lib/gone.js", fetch410)
	if goneCalls.Load() != 1 {
		t.Fatalf("cached 410 should not re-fetch: got %d calls", goneCalls.Load())
	}

	// 3. Transient HTTP 500 must NOT be cached.
	var err500Calls atomic.Int32
	fetch500 := func(context.Context, string) (string, error) {
		if err500Calls.Add(1) == 1 {
			return "", &remoteModuleStatusError{status: 500, msg: "HTTP 500"}
		}
		return "export default 99;", nil
	}
	if _, err := cache.getOrFetch(context.Background(), "https://cdn.example.com/lib/flaky.js", fetch500); err == nil {
		t.Fatal("expected first 500 error")
	}
	got, err := cache.getOrFetch(context.Background(), "https://cdn.example.com/lib/flaky.js", fetch500)
	if err != nil || got != "export default 99;" || err500Calls.Load() != 2 {
		t.Fatalf("HTTP 500 should not be cached: got=%q err=%v calls=%d", got, err, err500Calls.Load())
	}
}
