package main

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
)

func TestFixRepeatedResultAllocation(t *testing.T) {
	doc := &config.Config{URL: "https://example.com/config.json"}
	events := make([]library.Event, 100)
	for site := range events {
		entry := library.Entry{Config: doc, Site: config.Site{Key: fmt.Sprint(site)}}
		videos := make([]catalog.Video, 100)
		for i := range videos {
			id := fmt.Sprintf("%d-%d", site, i)
			videos[i] = catalog.Video{ID: catalog.ID(id), Name: "合成 " + id}
		}
		events[site].Result = &library.Result{Target: library.Target{Entry: entry}, Page: catalog.Page{Videos: videos}}
	}
	a := &app{library: libraryBrowser{root: doc, cached: map[string]*config.Config{}}}
	defer a.close()
	s := &a.library.search
	scan := func() {
		a.startScan(s, "合成", false)
		a.applyLibraryEvents(s, events)
		if len(s.items) != 10000 || len(s.groups) != 10000 {
			t.Fatal("repeated scan lost full results")
		}
	}
	scan()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 10 {
		scan()
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("10 repeated 100-site/10000-result aggregations: allocated=%d bytes (%.2f MiB)", allocated, float64(allocated)/(1<<20))
	// Allows independent group snapshots and keys on every scan, while
	// rejecting repeated geometric growth of the full result array.
	if allocated > 128<<20 {
		t.Errorf("repeated result aggregation allocated over 128 MiB: %d", allocated)
	}
}

func TestFixResultSnapshotOwnership(t *testing.T) {
	doc := &config.Config{URL: "https://example.com/config.json"}
	items := []library.Item{
		{Entry: library.Entry{Config: doc}, Video: catalog.Video{ID: "1", Name: "B"}},
		{Entry: library.Entry{Config: doc}, Video: catalog.Video{ID: "2", Name: "A"}},
		{Entry: library.Entry{Config: doc}, Video: catalog.Video{ID: "3", Name: " b "}},
	}
	groups := groupTitles(items)
	if len(groups) != 2 || groups[0].name != "B" || groups[1].name != "A" || len(groups[0].items) != 2 || groups[0].items[1].Video.ID != "3" {
		t.Fatal("group order or source order changed")
	}
	a := &app{}
	defer a.close()
	a.openTitle("B", "", items[:1])
	items[0].Video.Name = "replaced"
	if groups[0].items[0].Video.Name != "B" {
		t.Fatal("group snapshot aliases mutable scan storage")
	}
	if a.title.seeds[0].Video.Name != "B" {
		t.Fatal("open detail aliases mutable scan storage")
	}
	// Appending a candidate must not overwrite the next group's snapshot.
	groups[0].items = append(groups[0].items, items[0])
	if groups[1].items[0].Video.ID != "2" {
		t.Fatal("group append overwrote another group")
	}
}
