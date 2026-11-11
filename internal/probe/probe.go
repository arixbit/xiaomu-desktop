// Package probe walks the first N sites of a user-supplied config:
// home, category, detail, then play. Only drpy2 sites are executed.
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/drpy"
)

// Options selects what to probe.
type Options struct {
	ConfigURL string
	Line      string
	N         int
	Timeout   time.Duration
	AssetsDir string
	Fetch     config.Fetcher
}

// Result is one site's row.
type Result struct {
	Name     string
	Key      string
	Kind     config.Kind
	Status   string // PASS, FAIL, SKIP
	Home     string
	Category string
	Detail   string
	Play     string
	Elapsed  time.Duration
	Note     string
}

// Run loads the config and probes up to N sites.
func Run(ctx context.Context, opt Options) ([]Result, error) {
	if opt.N <= 0 {
		opt.N = 1
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 30 * time.Second
	}
	cfg, err := config.Load(ctx, opt.ConfigURL, opt.Line, opt.Fetch)
	if err != nil {
		return nil, err
	}
	n := opt.N
	if n > len(cfg.Sites) {
		n = len(cfg.Sites)
	}
	out := make([]Result, 0, n)
	for _, site := range cfg.Sites[:n] {
		out = append(out, runSite(ctx, site, opt))
	}
	return out, nil
}

func runSite(ctx context.Context, site config.Site, opt Options) Result {
	start := time.Now()
	row := Result{
		Name: site.Name, Key: site.Key, Kind: site.Kind,
		Home: "-", Category: "-", Detail: "-", Play: "-",
	}
	finish := func(status, note string) Result {
		row.Status = status
		row.Note = note
		row.Elapsed = time.Since(start)
		return row
	}
	if site.Kind != config.KindDRPY2 {
		return finish("SKIP", "未执行："+string(site.Kind))
	}
	if site.API == "" {
		return finish("FAIL", "没有 api")
	}
	siteCtx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()

	src, err := getText(siteCtx, site.API, opt.Fetch)
	if err != nil {
		return finish("FAIL", "读取引擎: "+err.Error())
	}
	kind := config.Classify(site.Type, site.API, site.Ext, src)
	row.Kind = kind
	if kind != config.KindDRPY2 {
		return finish("SKIP", "脚本不是 drpy2："+string(kind))
	}
	eng, err := drpy.Open(drpy.Options{
		Modules:   map[string]string{site.API: src},
		AssetsDir: opt.AssetsDir,
		Timeout:   opt.Timeout,
	})
	if err != nil {
		return finish("FAIL", err.Error())
	}
	defer eng.Close()
	if err := eng.Load(site.API); err != nil {
		return finish("FAIL", err.Error())
	}
	if _, err := eng.Call(siteCtx, "init", site.Ext); err != nil {
		return finish("FAIL", "init: "+err.Error())
	}
	home, err := eng.Call(siteCtx, "home", false)
	if err != nil {
		row.Home = "fail"
		return finish("FAIL", "home: "+err.Error())
	}
	tid, _, err := firstClass(home)
	if err != nil {
		row.Home = "fail"
		return finish("FAIL", "home: "+err.Error())
	}
	row.Home = "ok"

	cat, err := eng.Call(siteCtx, "category", tid, 1, map[string]any{}, map[string]any{})
	if err != nil {
		row.Category = "fail"
		return finish("FAIL", "category: "+err.Error())
	}
	vodID, _, err := firstVod(cat)
	if err != nil {
		row.Category = "fail"
		return finish("FAIL", "category: "+err.Error())
	}
	row.Category = "ok"

	detail, err := eng.Call(siteCtx, "detail", vodID)
	if err != nil {
		row.Detail = "fail"
		return finish("FAIL", "detail: "+err.Error())
	}
	flag, playID, err := firstEpisode(detail)
	if err != nil {
		row.Detail = "fail"
		return finish("FAIL", "detail: "+err.Error())
	}
	row.Detail = "ok"

	played, err := eng.Call(siteCtx, "play", flag, playID, []any{})
	if err != nil {
		row.Play = "fail"
		return finish("FAIL", "play: "+err.Error())
	}
	playURL, err := playURLOf(played)
	if err != nil {
		row.Play = "fail"
		return finish("FAIL", "play: "+err.Error())
	}
	row.Play = "ok"
	note := playURL
	if len(note) > 80 {
		note = note[:80] + "…"
	}
	return finish("PASS", note)
}

func getText(ctx context.Context, raw string, fetch config.Fetcher) (string, error) {
	if fetch == nil {
		fetch = config.HTTPFetcher(nil)
	}
	res, err := fetch(ctx, raw)
	if err != nil {
		return "", err
	}
	if res.Status != 0 && (res.Status < 200 || res.Status >= 300) {
		return "", fmt.Errorf("status %d", res.Status)
	}
	return string(res.Body), nil
}

func firstClass(raw string) (id, name string, err error) {
	var doc struct {
		Class []map[string]any `json:"class"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "", "", err
	}
	if len(doc.Class) == 0 {
		return "", "", fmt.Errorf("没有分类")
	}
	id = asString(doc.Class[0]["type_id"])
	name = asString(doc.Class[0]["type_name"])
	if id == "" {
		return "", "", fmt.Errorf("分类没有 type_id")
	}
	return id, name, nil
}

func firstVod(raw string) (id, name string, err error) {
	var doc struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "", "", err
	}
	if len(doc.List) == 0 {
		return "", "", fmt.Errorf("列表为空")
	}
	id = asString(doc.List[0]["vod_id"])
	name = asString(doc.List[0]["vod_name"])
	if id == "" {
		return "", "", fmt.Errorf("没有 vod_id")
	}
	return id, name, nil
}

func firstEpisode(raw string) (flag, id string, err error) {
	var doc struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "", "", err
	}
	if len(doc.List) == 0 {
		return "", "", fmt.Errorf("详情为空")
	}
	item := doc.List[0]
	froms := strings.Split(asString(item["vod_play_from"]), "$$$")
	urls := strings.Split(asString(item["vod_play_url"]), "$$$")
	if len(urls) == 0 || urls[0] == "" {
		return "", "", fmt.Errorf("没有播放地址")
	}
	flag = ""
	if len(froms) > 0 {
		flag = froms[0]
	}
	ep := strings.Split(urls[0], "#")[0]
	parts := strings.SplitN(ep, "$", 2)
	if len(parts) == 2 {
		return flag, parts[1], nil
	}
	if ep == "" {
		return "", "", fmt.Errorf("没有分集")
	}
	return flag, ep, nil
}

func playURLOf(raw string) (string, error) {
	var doc struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "", err
	}
	if strings.TrimSpace(doc.URL) == "" {
		return "", fmt.Errorf("播放结果没有 url")
	}
	return doc.URL, nil
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// Fprint writes a plain table.
func Fprint(w io.Writer, rows []Result) {
	fmt.Fprintf(w, "%-16s %-8s %-6s %-6s %-6s %-6s %-6s %-10s %s\n",
		"站点", "类型", "结果", "首页", "分类", "详情", "播放", "耗时", "说明")
	for _, r := range rows {
		name := r.Name
		if name == "" {
			name = r.Key
		}
		fmt.Fprintf(w, "%-16s %-8s %-6s %-6s %-6s %-6s %-6s %-10s %s\n",
			trimWidth(name, 16), r.Kind, r.Status, r.Home, r.Category, r.Detail, r.Play,
			r.Elapsed.Truncate(time.Millisecond), r.Note)
	}
}

func trimWidth(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
