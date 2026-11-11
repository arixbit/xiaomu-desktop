// Package drpy hosts a drpy2-style JavaScript module on QuickJS.
//
// The engine file (the site's api) is loaded as an ES module. Imports of
// assets://js/lib/... resolve to files shipped with this package, or to a
// directory the caller passes. Those shipped files are small stand-ins so
// tests run offline; they are not cheerio, crypto-js, jsencrypt or the
// drpy template. Point AssetsDir at the real files to load an upstream
// engine, and keep that engine's license.
//
// Host globals match the shell drpy2 calls: req, joinUrl, pdfh, pdfa, pd,
// and local.get/set/delete.
package drpy

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/pdfh"
	"github.com/arixbit/xiaomu-desktop/internal/urljoin"
	"modernc.org/quickjs"
)

//go:embed assets
var embedded embed.FS

// Request is one req() call from JavaScript.
type Request struct {
	URL     string
	Method  string
	Headers map[string]string
	Body    string
}

// Result is the object req() returns.
type Result struct {
	Content string
	Headers map[string]string
	Status  int
}

// Fetcher performs req(). Nil uses HTTP for http(s) URLs.
type Fetcher func(ctx context.Context, req Request) (Result, error)

// Options configures an engine.
type Options struct {
	Fetch     Fetcher
	Modules   map[string]string // module URL -> source, used before the network
	AssetsDir string            // overrides assets:// files
	Timeout   time.Duration
}

// Engine is one QuickJS runtime. It is not safe for concurrent use.
type Engine struct {
	vm      *quickjs.VM
	eng     quickjs.Value
	loaded  bool
	fetch   Fetcher
	modules map[string]string
	assets  string
	ctx     context.Context
	store   map[string]string
}

// Open starts a runtime and registers the host globals.
func Open(opt Options) (*Engine, error) {
	vm, err := quickjs.NewVM()
	if err != nil {
		return nil, err
	}
	e := &Engine{
		vm:      vm,
		fetch:   opt.Fetch,
		modules: opt.Modules,
		assets:  opt.AssetsDir,
		store:   map[string]string{},
		ctx:     context.Background(),
	}
	if e.fetch == nil {
		e.fetch = httpFetch
	}
	if e.modules == nil {
		e.modules = map[string]string{}
	}
	to := opt.Timeout
	if to <= 0 {
		to = 20 * time.Second
	}
	if err := vm.SetEvalTimeout(to); err != nil {
		_ = vm.Close()
		return nil, err
	}
	if err := vm.StdAddHelpers(); err != nil {
		_ = vm.Close()
		return nil, err
	}
	vm.SetModuleLoader(e.loadModule, e.normalize)
	if err := e.register(); err != nil {
		_ = vm.Close()
		return nil, err
	}
	if _, err := vm.Eval(prelude, quickjs.EvalGlobal); err != nil {
		_ = vm.Close()
		return nil, fmt.Errorf("drpy: prelude: %w", err)
	}
	return e, nil
}

// Close releases the runtime.
func (e *Engine) Close() error {
	if e.loaded {
		e.eng.Free()
		e.loaded = false
	}
	if e.vm == nil {
		return nil
	}
	err := e.vm.Close()
	e.vm = nil
	return err
}

// Load evaluates the module at moduleURL. Source already in Options.Modules
// is used; otherwise the module loader fetches it.
func (e *Engine) Load(moduleURL string) error {
	quoted, err := jsonQuote(moduleURL)
	if err != nil {
		return err
	}
	src := "import eng from " + quoted + ";\nglobalThis.__xiaomuEngine = eng.default ?? eng;\n"
	if _, err := e.vm.Eval(src, quickjs.EvalModule); err != nil {
		return fmt.Errorf("drpy: load %s: %w", moduleURL, err)
	}
	if _, err := e.vm.ExecutePendingJobs(); err != nil {
		return fmt.Errorf("drpy: load %s: %w", moduleURL, err)
	}
	g := e.vm.GlobalObject()
	defer g.Free()
	atom, err := e.vm.NewAtom("__xiaomuEngine")
	if err != nil {
		return err
	}
	val, err := g.GetPropertyValue(atom)
	if err != nil {
		return err
	}
	if val.IsUndefined() {
		val.Free()
		return fmt.Errorf("drpy: %s did not export an engine", moduleURL)
	}
	if e.loaded {
		e.eng.Free()
	}
	e.eng = val
	e.loaded = true
	return nil
}

// Call runs a method of the default export and returns its JSON text.
func (e *Engine) Call(ctx context.Context, name string, args ...any) (string, error) {
	if !e.loaded {
		return "", fmt.Errorf("drpy: engine not loaded")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.ctx = ctx
	atom, err := e.vm.NewAtom(name)
	if err != nil {
		return "", err
	}
	fn, err := e.eng.GetPropertyValue(atom)
	if err != nil {
		return "", err
	}
	defer fn.Free()
	if fn.IsUndefined() {
		return "", fmt.Errorf("drpy: no function %s", name)
	}
	res, err := fn.Call(e.eng, args...)
	if err != nil {
		return "", err
	}
	if _, err := e.vm.ExecutePendingJobs(); err != nil {
		return "", err
	}
	return stringify(res), nil
}

func (e *Engine) register() error {
	binds := []struct {
		name string
		fn   quickjs.HostFunc
	}{
		{"req", e.hostReq},
		{"joinUrl", e.hostJoin},
		{"pdfh", e.hostPDFH},
		{"pdfa", e.hostPDFA},
		{"pd", e.hostPD},
		{"__xiaomuLocalGet", e.hostGet},
		{"__xiaomuLocalSet", e.hostSet},
		{"__xiaomuLocalDel", e.hostDel},
	}
	for _, b := range binds {
		if err := e.vm.RegisterHostFunc(b.name, b.fn); err != nil {
			return err
		}
	}
	return nil
}

const prelude = `
globalThis.local = {
  get(a, b) {
    if (arguments.length >= 2) return __xiaomuLocalGet(String(a ?? ""), String(b ?? ""));
    return __xiaomuLocalGet("", a == null ? "" : String(a));
  },
  set(a, b, c) {
    if (arguments.length >= 3) return __xiaomuLocalSet(String(a ?? ""), String(b ?? ""), c == null ? "" : String(c));
    return __xiaomuLocalSet("", a == null ? "" : String(a), b == null ? "" : String(b));
  },
  delete(a, b) {
    if (arguments.length >= 2) return __xiaomuLocalDel(String(a ?? ""), String(b ?? ""));
    return __xiaomuLocalDel("", a == null ? "" : String(a));
  }
};
`

func (e *Engine) hostReq(args []any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("req: missing url")
	}
	raw := argString(args[0])
	method := http.MethodGet
	headers := map[string]string{}
	body := ""
	if len(args) >= 2 && args[1] != nil {
		if obj, ok := args[1].(*quickjs.Object); ok && obj != nil {
			var parsed struct {
				Method  string         `json:"method"`
				Headers map[string]any `json:"headers"`
				Body    any            `json:"body"`
			}
			if err := obj.Into(&parsed); err != nil {
				return nil, err
			}
			if parsed.Method != "" {
				method = strings.ToUpper(parsed.Method)
			}
			for k, v := range parsed.Headers {
				headers[k] = fmt.Sprint(v)
			}
			if parsed.Body != nil {
				switch b := parsed.Body.(type) {
				case string:
					body = b
				default:
					buf, err := json.Marshal(b)
					if err != nil {
						return nil, err
					}
					body = string(buf)
				}
			}
		}
	}
	res, err := e.fetch(e.ctx, Request{URL: raw, Method: method, Headers: headers, Body: body})
	if err != nil {
		return nil, err
	}
	if res.Headers == nil {
		res.Headers = map[string]string{}
	}
	return struct {
		Content string            `json:"content"`
		Headers map[string]string `json:"headers"`
	}{Content: res.Content, Headers: res.Headers}, nil
}

func (e *Engine) hostJoin(args []any) (any, error) {
	return urljoin.Join(argString(argAt(args, 0)), argString(argAt(args, 1))), nil
}

func (e *Engine) hostPDFH(args []any) (any, error) {
	return pdfh.PDFH(argString(argAt(args, 0)), argString(argAt(args, 1))), nil
}

func (e *Engine) hostPDFA(args []any) (any, error) {
	got := pdfh.PDFA(argString(argAt(args, 0)), argString(argAt(args, 1)))
	if got == nil {
		got = []string{}
	}
	return got, nil
}

func (e *Engine) hostPD(args []any) (any, error) {
	return pdfh.PD(argString(argAt(args, 0)), argString(argAt(args, 1)), argString(argAt(args, 2))), nil
}

func (e *Engine) hostGet(args []any) (any, error) {
	return e.store[storeKey(argString(argAt(args, 0)), argString(argAt(args, 1)))], nil
}

func (e *Engine) hostSet(args []any) (any, error) {
	k := storeKey(argString(argAt(args, 0)), argString(argAt(args, 1)))
	v := argString(argAt(args, 2))
	e.store[k] = v
	return v, nil
}

func (e *Engine) hostDel(args []any) (any, error) {
	delete(e.store, storeKey(argString(argAt(args, 0)), argString(argAt(args, 1))))
	return true, nil
}

func storeKey(ns, key string) string { return ns + "\x00" + key }

func (e *Engine) normalize(_ *quickjs.VM, base, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("drpy: empty module specifier")
	}
	if strings.HasPrefix(name, "assets://") || strings.Contains(name, "://") {
		return name, nil
	}
	if base == "" || !strings.Contains(base, "://") {
		return name, nil
	}
	bu, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	ru, err := url.Parse(name)
	if err != nil {
		return "", err
	}
	return bu.ResolveReference(ru).String(), nil
}

func (e *Engine) loadModule(_ *quickjs.VM, name string) (string, error) {
	if src, ok := e.modules[name]; ok {
		return asModule(src), nil
	}
	if strings.HasPrefix(name, "assets://") {
		src, err := e.readAsset(strings.TrimPrefix(name, "assets://"))
		if err != nil {
			return "", err
		}
		return asModule(src), nil
	}
	if strings.HasPrefix(name, "file://") {
		b, err := os.ReadFile(strings.TrimPrefix(name, "file://"))
		if err != nil {
			return "", err
		}
		return asModule(string(b)), nil
	}
	if strings.HasPrefix(name, "http://") || strings.HasPrefix(name, "https://") {
		res, err := e.fetch(e.ctx, Request{URL: name, Method: http.MethodGet})
		if err != nil {
			return "", err
		}
		return asModule(res.Content), nil
	}
	return "", fmt.Errorf("drpy: cannot load module %s", name)
}

// asModule lets a classic script (crypto-js, jsencrypt) load as an ES module.
// Files that already export are returned unchanged. Others get a module and
// exports binding so their UMD wrapper can run, and CryptoJS is published
// on globalThis when the script provides it.
func asModule(src string) string {
	if isESModule(src) {
		return src
	}
	return scriptPrelude + src + scriptFooter
}

func isESModule(src string) bool {
	for _, mark := range []string{
		"export{", "export {", "export default", "export function",
		"export class", "export const", "export let", "export var", "export async",
	} {
		if strings.Contains(src, mark) {
			return true
		}
	}
	return false
}

const scriptPrelude = "var module={exports:{}};var exports=module.exports;var window=globalThis;var self=globalThis;var global=globalThis;\n"
const scriptFooter = "\n;try{var __xiaomuOut=module.exports;if(__xiaomuOut&&__xiaomuOut.AES){globalThis.CryptoJS=globalThis.CryptoJS||__xiaomuOut;}}catch(e){}\n"

func (e *Engine) readAsset(key string) (string, error) {
	key = strings.TrimPrefix(key, "/")
	if e.assets != "" {
		p := filepath.Join(e.assets, filepath.FromSlash(key))
		if b, err := os.ReadFile(p); err == nil {
			return string(b), nil
		}
		if b, err := os.ReadFile(filepath.Join(e.assets, filepath.Base(key))); err == nil {
			return string(b), nil
		}
	}
	b, err := embedded.ReadFile("assets/" + key)
	if err != nil {
		return "", fmt.Errorf("drpy: asset %s: %w", key, err)
	}
	return string(b), nil
}

func httpFetch(ctx context.Context, req Request) (Result, error) {
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	var body io.Reader
	if req.Body != "" {
		body = strings.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("User-Agent", "xiaomu/0.0.1")
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return Result{}, err
	}
	h := map[string]string{}
	for k, v := range resp.Header {
		if len(v) > 0 {
			h[k] = strings.Join(v, ", ")
		}
	}
	return Result{Content: string(buf), Headers: h, Status: resp.StatusCode}, nil
}

func argAt(args []any, i int) any {
	if i < 0 || i >= len(args) {
		return nil
	}
	return args[i]
}

func argString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case quickjs.Undefined:
		return ""
	case *quickjs.Object:
		if t == nil {
			return ""
		}
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case quickjs.Undefined:
		return ""
	case *quickjs.Object:
		if t == nil {
			return ""
		}
		return t.String()
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func jsonQuote(s string) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
