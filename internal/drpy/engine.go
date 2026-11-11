// Package drpy hosts a drpy2-style JavaScript module on QuickJS.
//
// The engine file (the site's api) is loaded as an ES module. Imports of
// assets://js/lib/... resolve to the caller's AssetsDir, or to dependencies
// alongside the selected engine. Libraries and site rules are not bundled.
//
// Host globals match the shell drpy2 calls: req, joinUrl, pdfh, pdfa, pd,
// and local.get/set/delete.
package drpy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/pdfh"
	"github.com/arixbit/xiaomu-desktop/internal/urljoin"
	"github.com/egoist/mygo/netproxy"
	"modernc.org/quickjs"
)

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
	AssetsDir string            // overrides assets:// files; modules.json maps module URLs to local files
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
	entry   string
	ctx     context.Context
	store   map[string]string
}

// Open starts a runtime and registers the host globals.
func Open(opt Options) (*Engine, error) {
	modules, err := readModuleOverrides(opt.AssetsDir)
	if err != nil {
		return nil, err
	}
	for name, src := range opt.Modules {
		modules[name] = src
	}
	vm, err := quickjs.NewVM()
	if err != nil {
		return nil, err
	}
	e := &Engine{
		vm:      vm,
		fetch:   opt.Fetch,
		modules: modules,
		assets:  opt.AssetsDir,
		store:   map[string]string{},
		ctx:     context.Background(),
	}
	if e.fetch == nil {
		e.fetch = httpFetch
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
	return e.LoadContext(context.Background(), moduleURL)
}

// LoadContext loads a module with cancellation for its network requests.
func (e *Engine) LoadContext(ctx context.Context, moduleURL string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.ctx = ctx
	e.entry = moduleURL
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

// CallAwait runs a method that returns a Promise. FongMi class spiders may
// implement their methods asynchronously; drpy2's synchronous calls still use Call.
func (e *Engine) CallAwait(ctx context.Context, name string, args ...any) (string, error) {
	if !e.loaded {
		return "", fmt.Errorf("drpy: engine not loaded")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	e.ctx = ctx
	if ctx.Done() != nil {
		done := make(chan struct{})
		vm := e.vm
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				vm.Interrupt()
			case <-done:
			}
		}()
	}
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
	res, err := fn.CallValue(e.eng, args...)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	defer res.Free()
	var result string
	var settled bool
	var failure error
	chained, err := res.Then(func(v quickjs.Value) {
		value, convertErr := v.Any()
		if convertErr != nil {
			failure = convertErr
		} else {
			result = stringify(value)
		}
		settled = true
	}, func(v quickjs.Value) {
		failure = fmt.Errorf("drpy: %s: %s", name, jsReason(e.vm, v))
		settled = true
	})
	if err != nil {
		return "", fmt.Errorf("drpy: %s did not return a Promise: %w", name, err)
	}
	defer chained.Free()
	for !settled {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := e.vm.ExecutePendingJobs()
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("drpy: %s: %w", name, err)
		}
		if n == 0 && !settled {
			return "", fmt.Errorf("drpy: %s: Promise did not settle", name)
		}
	}
	if failure != nil {
		return "", failure
	}
	return result, nil
}

func jsReason(vm *quickjs.VM, v quickjs.Value) string {
	atom, err := vm.NewAtom("message")
	if err == nil {
		message, getErr := v.GetPropertyValue(atom)
		if getErr == nil {
			defer message.Free()
			if !message.IsUndefined() {
				if value, convertErr := message.Any(); convertErr == nil {
					return fmt.Sprint(value)
				}
			}
		}
	}
	if value, err := v.Any(); err == nil {
		return fmt.Sprint(value)
	}
	return "unknown JavaScript rejection"
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
// Timers run as QuickJS microtasks when the host drains pending jobs. The
// delay is intentionally ignored; callbacks never enter QuickJS from Go threads.
(() => {
  let nextTimer = 1;
  const pending = new Map();
  globalThis.setTimeout = (callback, delay, ...args) => {
    if (typeof callback !== 'function') throw new TypeError('setTimeout callback must be a function');
    const id = nextTimer++;
    pending.set(id, {callback, args});
    Promise.resolve().then(() => {
      const task = pending.get(id);
      if (!task) return;
      pending.delete(id);
      task.callback(...task.args);
    });
    return id;
  };
  globalThis.clearTimeout = id => { pending.delete(Number(id)); };
})();
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
	if res.Status >= 400 {
		contentType := ""
		for name, value := range res.Headers {
			if strings.EqualFold(name, "Content-Type") {
				contentType = value
				break
			}
		}
		if strings.Contains(strings.ToLower(contentType), "html") || strings.HasPrefix(strings.TrimSpace(res.Content), "<") {
			return nil, fmt.Errorf("req %s: HTTP %d (Content-Type %q): 返回 HTML，站点接口可能已失效", raw, res.Status, contentType)
		}
	}
	if res.Headers == nil {
		res.Headers = map[string]string{}
	}
	return struct {
		Content string            `json:"content"`
		Headers map[string]string `json:"headers"`
		Code    int               `json:"code"`
	}{Content: res.Content, Headers: res.Headers, Code: res.Status}, nil
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
	if _, ok := e.modules[name]; ok {
		return name, nil
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
		return asModule(name, src), nil
	}
	if strings.HasPrefix(name, "assets://") {
		key := strings.TrimPrefix(name, "assets://")
		src, err := e.readAsset(key)
		if err == nil {
			return asModule(name, src), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if strings.HasPrefix(key, "js/lib/") && (strings.HasPrefix(e.entry, "https://") || strings.HasPrefix(e.entry, "http://") || strings.HasPrefix(e.entry, "file://")) {
			src, err = e.loadModule(nil, urljoin.Join(e.entry, strings.TrimPrefix(key, "js/lib/")))
			if err == nil {
				return src, nil
			}
		}
		return "", fmt.Errorf("drpy: 依赖 %s 加载失败，请在脚本目录提供真实的 %s：%w", name, key, err)
	}
	if strings.HasPrefix(name, "file://") {
		b, err := os.ReadFile(strings.TrimPrefix(name, "file://"))
		if err != nil {
			return "", err
		}
		return asModule(name, string(b)), nil
	}
	if strings.HasPrefix(name, "http://") || strings.HasPrefix(name, "https://") {
		res, err := e.fetch(e.ctx, Request{URL: name, Method: http.MethodGet})
		if err != nil {
			return "", fmt.Errorf("drpy: module %s: %w", name, err)
		}
		contentType := ""
		for k, v := range res.Headers {
			if strings.EqualFold(k, "Content-Type") {
				contentType = v
				break
			}
		}
		if res.Status != 0 && (res.Status < 200 || res.Status >= 300) {
			return "", fmt.Errorf("drpy: module %s: HTTP %d (Content-Type %q)", name, res.Status, contentType)
		}
		// Raw file hosts also serve JavaScript as plain text or binary data.
		mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
		switch mediaType {
		case "", "text/plain", "application/octet-stream",
			"text/javascript", "application/javascript", "application/x-javascript",
			"text/ecmascript", "application/ecmascript":
		default:
			return "", fmt.Errorf("drpy: module %s: HTTP %d: expected JavaScript, got Content-Type %q", name, res.Status, contentType)
		}
		if strings.HasPrefix(strings.TrimLeft(res.Content, "\ufeff \t\r\n"), "<") {
			return "", fmt.Errorf("drpy: module %s: HTTP %d: expected JavaScript, got HTML/XML (Content-Type %q)", name, res.Status, contentType)
		}
		return asModule(name, res.Content), nil
	}
	return "", fmt.Errorf("drpy: cannot load module %s", name)
}

// asModule lets a classic script (crypto-js, jsencrypt) load as an ES module.
// Files that already export are returned unchanged. Others get a module and
// exports binding so their UMD wrapper can run. Publish the browser globals
// that drpy expects when those wrappers choose their CommonJS branch.
func asModule(name, src string) string {
	if isESModule(src) {
		return src
	}
	footer := scriptFooter
	u, _ := url.Parse(name)
	if u != nil {
		global := map[string]string{
			"jinja.js": "jinja", "json5.js": "JSON5",
			"pako.min.js": "pako", "pako.js": "pako",
			"jsencrypt.js": "JSEncrypt", "jsencrypt.min.js": "JSEncrypt",
			"node-rsa.js": "NodeRSA",
		}[strings.ToLower(path.Base(u.Path))]
		if global != "" {
			footer += fmt.Sprintf("\nif(module.exports && (typeof module.exports === 'function' || Object.keys(module.exports).length)) globalThis[%q] = module.exports[%q] || module.exports.default || module.exports;\n", global, global)
		}
	}
	return scriptPrelude + src + footer
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
	if !filepath.IsLocal(key) {
		return "", fmt.Errorf("drpy: invalid asset path %q", key)
	}
	if e.assets != "" {
		p := filepath.Join(e.assets, filepath.FromSlash(key))
		if b, err := os.ReadFile(p); err == nil {
			return string(b), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if b, err := os.ReadFile(filepath.Join(e.assets, filepath.Base(key))); err == nil {
			return string(b), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("drpy: asset %s: %w", key, os.ErrNotExist)
}

// readModuleOverrides keeps URL-specific replacements in the caller's assets
// directory. Modules retain their original URL so relative imports still work.
func readModuleOverrides(dir string) (map[string]string, error) {
	modules := map[string]string{}
	if dir == "" {
		return modules, nil
	}
	manifest := filepath.Join(dir, "modules.json")
	b, err := os.ReadFile(manifest)
	if os.IsNotExist(err) {
		return modules, nil
	}
	if err != nil {
		return nil, fmt.Errorf("drpy: module overrides %s: %w", manifest, err)
	}
	var files map[string]string
	if err := json.Unmarshal(b, &files); err != nil {
		return nil, fmt.Errorf("drpy: module overrides %s: %w", manifest, err)
	}
	for name, file := range files {
		if !filepath.IsLocal(file) {
			return nil, fmt.Errorf("drpy: module %s: override path %q must be relative to %s", name, file, dir)
		}
		src, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			return nil, fmt.Errorf("drpy: module %s: %w", name, err)
		}
		modules[name] = string(src)
	}
	return modules, nil
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
	client := &http.Client{Transport: netproxy.Transport(), Timeout: 20 * time.Second}
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
