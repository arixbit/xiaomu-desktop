// Package probe walks the first N sites of a user-supplied config:
// home, category, detail, then play.
package probe

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/source"
)

// Options selects what to probe.
type Options struct {
	ConfigURL string
	Line      string
	Query     string // optional search term for search-only sites
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
	if site.Kind.Unsupported() {
		return finish("SKIP", "未执行："+string(site.Kind))
	}
	if site.API == "" {
		return finish("FAIL", "没有 api")
	}
	siteCtx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()

	session, categories, err := source.Open(siteCtx, site, opt.AssetsDir)
	if err != nil {
		return finish("FAIL", err.Error())
	}
	defer session.Close()
	if site.Kind == config.KindFongMi {
		if _, ok := session.(*catalog.Session); ok {
			row.Kind = config.KindDRPY2
		}
	}
	if opt.Query == "" && (len(categories) == 0 || categories[0].ID == "") {
		row.Home = "fail"
		return finish("FAIL", "home: 没有分类")
	}
	row.Home = "ok"

	var page catalog.Page
	if opt.Query != "" {
		page, err = session.Search(siteCtx, opt.Query, 1)
	} else {
		page, err = session.Category(siteCtx, string(categories[0].ID), 1)
	}
	if err != nil {
		row.Category = "fail"
		return finish("FAIL", "列表: "+err.Error())
	}
	if len(page.Videos) == 0 || page.Videos[0].ID == "" {
		row.Category = "fail"
		return finish("FAIL", "列表为空")
	}
	row.Category = "ok"

	detail, err := session.Detail(siteCtx, string(page.Videos[0].ID))
	if err != nil {
		row.Detail = "fail"
		return finish("FAIL", "detail: "+err.Error())
	}
	if len(detail.Sources) == 0 || len(detail.Sources[0].Episodes) == 0 {
		row.Detail = "fail"
		return finish("FAIL", "detail: 没有播放分集")
	}
	row.Detail = "ok"

	played, err := session.Play(siteCtx, detail.Sources[0].Name, detail.Sources[0].Episodes[0])
	if err != nil {
		row.Play = "fail"
		return finish("FAIL", "play: "+err.Error())
	}
	row.Play = "ok"
	note := played.URL
	if len(note) > 80 {
		note = note[:80] + "…"
	}
	return finish("PASS", note)
}

// Fprint writes a plain table.
func Fprint(w io.Writer, rows []Result) {
	fmt.Fprintf(w, "%-16s %-8s %-6s %-6s %-6s %-6s %-6s %-10s %s\n",
		"站点", "类型", "结果", "首页", "列表", "详情", "播放", "耗时", "说明")
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
