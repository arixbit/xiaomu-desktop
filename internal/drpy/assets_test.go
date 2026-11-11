package drpy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssetsUseRealDependencies(t *testing.T) {
	const entry = "https://example.com/lib/drpy2.js"
	const asset = "assets://js/lib/cheerio.min.js"
	const engine = `import dependency from "assets://js/lib/cheerio.min.js";
export default { home() { return dependency.value; } };`
	for _, mode := range []string{"remote", "local", "override", "missing"} {
		t.Run(mode, func(t *testing.T) {
			opt := Options{Modules: map[string]string{entry: engine}}
			if mode == "local" {
				opt.AssetsDir = t.TempDir()
				if err := os.WriteFile(filepath.Join(opt.AssetsDir, "cheerio.min.js"), []byte(`export default {value: "local"};`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "override" {
				opt.Modules[asset] = `export default {value: "override"};`
			}
			var fetched []string
			opt.Fetch = func(_ context.Context, req Request) (Result, error) {
				fetched = append(fetched, req.URL)
				if mode == "missing" {
					return Result{Status: 404, Content: "not found"}, nil
				}
				switch req.URL {
				case "https://example.com/lib/cheerio.min.js":
					return Result{Status: 200, Content: `import value from "./helper.js"; export default {value};`}, nil
				case "https://example.com/lib/helper.js":
					return Result{Status: 200, Content: `export default "remote";`}, nil
				default:
					return Result{}, fmt.Errorf("unexpected request: %s", req.URL)
				}
			}
			eng, err := Open(opt)
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			err = eng.Load(entry)
			if mode == "missing" {
				if err == nil || !strings.Contains(err.Error(), "cheerio.min.js") || !strings.Contains(err.Error(), "HTTP 404") {
					t.Fatalf("missing dependency should fail at load time: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := eng.Call(context.Background(), "home")
			if err != nil || got != mode {
				t.Fatalf("home = %q, %v; want %q", got, err, mode)
			}
			if mode != "remote" && len(fetched) != 0 {
				t.Fatalf("local dependency unexpectedly fetched %v", fetched)
			}
		})
	}
}

func TestClassicLibraryGlobals(t *testing.T) {
	for _, tt := range []struct{ file, global string }{
		{"jinja.js", "jinja"},
		{"json5.js", "JSON5"},
		{"pako.min.js", "pako"},
		{"jsencrypt.js", "JSEncrypt"},
		{"node-rsa.js", "NodeRSA"},
	} {
		t.Run(tt.file, func(t *testing.T) {
			const entry = "https://example.com/engine.js"
			eng, err := Open(Options{Modules: map[string]string{
				entry: fmt.Sprintf(`import "./%s"; export default {home() { return globalThis[%q].value; }};`, tt.file, tt.global),
				"https://example.com/" + tt.file: `(function (global, factory) {
  typeof exports === "object" && typeof module !== "undefined" ? factory(exports) : factory(global.library = {});
})(this, function (exports) { exports.value = "loaded"; });`,
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			if err := eng.Load(entry); err != nil {
				t.Fatal(err)
			}
			got, err := eng.Call(context.Background(), "home")
			if err != nil || got != "loaded" {
				t.Fatalf("library global %s = %q, %v", tt.global, got, err)
			}
		})
	}
}
