package main

import (
	"fmt"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFixSpeedStaleReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"class":[{"type_id":"1","type_name":"合成"}],"list":[]}`)
	}))
	defer srv.Close()
	doc := &config.Config{URL: srv.URL + "/old", Sites: []config.Site{{Key: "old", API: srv.URL, Kind: config.KindJSON, Type: 1}}}
	q := make(chan func(), 100)
	a := &app{dispatch: func(f func()) { q <- f }}
	defer a.close()
	a.library = libraryBrowser{root: doc, documents: []libraryDocument{{doc: doc}}, cached: map[string]*config.Config{}}
	a.startSourceSpeedTest()
	var reply func()
	select {
	case reply = <-q:
	case <-time.After(time.Second):
		t.Fatal("no speed result queued")
	}
	a.resetLibrary(nil)
	reply()
	time.Sleep(25 * time.Millisecond)
	for len(q) > 0 {
		(<-q)()
	}
	key := (library.Entry{Config: doc, Site: doc.Sites[0]}).Key()
	if _, ok := a.latency.get(key); ok {
		t.Fatal("queued old configuration result wrote current latency state")
	}
	if a.speedTest.running || a.speedTest.done != 0 {
		t.Fatalf("old result changed current progress: %+v", a.speedTest)
	}
}
