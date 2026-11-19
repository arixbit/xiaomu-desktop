package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFixLatencyWriterFlushAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			q := make(chan func(), 100)
			a := &app{dispatch: func(f func()) { q <- f }}
			path := filepath.Join(t.TempDir(), "latency.json")
			a.latency.path = path
			if fail {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for i := range 1000 {
				a.recordLatency(fmt.Sprint(i), time.Duration(i+1)*time.Millisecond)
			}
			err := a.latency.flush()
			if fail {
				if err == nil || len(a.latency.entries) != 0 {
					t.Fatalf("failed write appeared saved: %v entries=%d", err, len(a.latency.entries))
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				saved, err := readLatency(path)
				if err != nil || len(saved) != 256 || saved["999"].Ms != 1000 {
					t.Fatalf("close flush lost latest valid data: %d %v", len(saved), err)
				}
			}
			a.close()
			for len(q) > 0 {
				(<-q)()
			}
		})
	}
}
