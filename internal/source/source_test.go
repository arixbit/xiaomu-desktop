package source

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/config"
)

func TestOpenMisclassifiedDrpyObject(t *testing.T) {
	engine, err := os.ReadFile("../drpy/testdata/engine.js")
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/entry.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(engine)
		case "/rule.js":
			fmt.Fprintf(w, "var rule = {host:%q, class_name:\"电影\", class_url:\"1\", url:\"/list/fyclass/fypage\"};", srv.URL)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s, categories, err := Open(context.Background(), config.Site{Kind: config.KindFongMi, Type: 3, API: srv.URL + "/entry.js", Ext: srv.URL + "/rule.js"}, "../drpy/assets")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(categories) != 1 || categories[0].ID != "1" {
		t.Fatalf("categories: %+v", categories)
	}
}

func TestOpenRejectsHTMLScript(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>blocked</html>")
	}))
	defer srv.Close()
	_, _, err := Open(context.Background(), config.Site{Kind: config.KindFongMi, Type: 3, API: srv.URL + "/entry.js"}, "")
	if err == nil || !strings.Contains(err.Error(), srv.URL+"/entry.js") || !strings.Contains(err.Error(), "HTTP 200") || !strings.Contains(err.Error(), "HTML") {
		t.Fatalf("error: %v", err)
	}
}
