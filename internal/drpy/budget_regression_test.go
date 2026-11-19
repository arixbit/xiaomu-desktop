package drpy

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFixScriptSharedLimit(t *testing.T) {
	const url = "https://example.com/budget.js"
	const script = `export default {busy(){req("https://example.com/start");const end=Date.now()+80;while(Date.now()<end){};req("https://example.com/end");return 1}}`
	var active, peak atomic.Int64
	engines := make([]*Engine, 12)
	for i := range engines {
		e, err := Open(Options{Modules: map[string]string{url: script}, Fetch: func(_ context.Context, r Request) (Result, error) {
			if strings.HasSuffix(r.URL, "start") {
				n := active.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
			} else {
				active.Add(-1)
			}
			return Result{Status: 200}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Load(url); err != nil {
			t.Fatal(err)
		}
		engines[i] = e
		defer e.Close()
	}
	var wg sync.WaitGroup
	for _, e := range engines {
		wg.Go(func() {
			if _, err := e.Call(context.Background(), "busy"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	t.Logf("actual simultaneous script bodies=%d remaining=%d", peak.Load(), active.Load())
	if peak.Load() > 4 || active.Load() != 0 {
		t.Fatal("shared script budget exceeded")
	}
}

func TestFixScriptMemoryLimit(t *testing.T) {
	const url = "https://example.com/memory.js"
	e, err := Open(Options{Modules: map[string]string{url: `export default {normal(){return new Uint8Array(4*1024*1024).length},large(){return new Uint8Array(96*1024*1024).length}}`}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.Load(url); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Call(context.Background(), "normal"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Call(context.Background(), "large"); err == nil {
		t.Fatal("96MiB allocation exceeded 64MiB VM budget without error")
	}
}
