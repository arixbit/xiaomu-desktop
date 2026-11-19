package drpy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestFixScriptCancellation(t *testing.T) {
	const url = "https://example.com/cancel.js"
	for _, method := range []string{"call", "module", "await"} {
		t.Run(method, func(t *testing.T) {
			script := `export default {busy(){const end=Date.now()+200;while(Date.now()<end){};return 1},ok(){return 7}}`
			if method == "module" {
				script = `const end=Date.now()+200;while(Date.now()<end){};` + script
			}
			e, err := Open(Options{Modules: map[string]string{url: script}})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			if method != "module" {
				if err := e.Load(url); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			start := time.Now()
			switch method {
			case "module":
				err = e.LoadContext(ctx, url)
			case "call":
				_, err = e.Call(ctx, "busy")
			case "await":
				_, err = e.CallAwait(ctx, "busy")
			}
			elapsed := time.Since(start)
			t.Logf("20ms deadline / 200ms finite script: elapsed=%v error=%v", elapsed, err)
			if !errors.Is(err, context.DeadlineExceeded) || elapsed > 100*time.Millisecond {
				t.Errorf("cancellation exceeded contract: %v, %v", elapsed, err)
			}
			if method != "module" {
				for range 50 {
					got, err := e.Call(context.Background(), "ok")
					if err != nil || got != "7" {
						t.Fatalf("runtime poisoned: %q %v", got, err)
					}
				}
			}
		})
	}
}

func TestFixScriptCancelAndClose(t *testing.T) {
	const url = "https://example.com/close.js"
	for range 50 {
		entered := make(chan struct{})
		e, err := Open(Options{Modules: map[string]string{url: `export default {busy(){req("https://example.com/entered");const end=Date.now()+200;while(Date.now()<end){}}}`}, Fetch: func(context.Context, Request) (Result, error) { close(entered); return Result{Status: 200}, nil }})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Load(url); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err := e.Call(ctx, "busy")
			if !errors.Is(err, context.Canceled) {
				t.Errorf("call error: %v", err)
			}
		})
		<-entered
		cancel()
		wg.Go(func() {
			if err := e.Close(); err != nil {
				t.Error(err)
			}
		})
		wg.Wait()
	}
}
