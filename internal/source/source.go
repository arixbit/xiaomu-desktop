// Package source opens a configured site with the matching script adapter.
package source

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/fongmi"
	"github.com/arixbit/xiaomu-desktop/internal/xbpq"
)

// Session is the shared surface of the drpy2, CMS and FongMi adapters.
type Session interface {
	Home(context.Context) (catalog.Page, error)
	Category(context.Context, string, int) (catalog.Page, error)
	Search(context.Context, string, int) (catalog.Page, error)
	Detail(context.Context, string) (catalog.Detail, error)
	Play(context.Context, string, catalog.Episode) (catalog.Playback, error)
	Close()
}

// Home reads a site's recommendations, falling back to its first categories
// when the site does not provide a home feed. Search never needs this fallback.
func Home(ctx context.Context, session Session, categories []catalog.Category) (catalog.Page, error) {
	page, err := session.Home(ctx)
	if len(page.Videos) > 0 {
		return page, nil
	}
	for _, category := range categories[:min(3, len(categories))] {
		if ctx.Err() != nil {
			return catalog.Page{}, ctx.Err()
		}
		page, err = session.Category(ctx, string(category.ID), 1)
		if len(page.Videos) > 0 {
			return page, nil
		}
	}
	return page, err
}

// Open detects mislabeled type-3 scripts before choosing an adapter. Only
// FongMi candidates are fetched for inspection; other sites retain their kind.
func Open(ctx context.Context, site config.Site, assets string) (Session, []catalog.Category, error) {
	if site.Kind == config.KindXBPQ {
		return xbpq.Open(ctx, site)
	}
	if site.Kind == config.KindFongMi && scriptURL(site.API) {
		res, err := config.HTTPFetcher(nil)(ctx, site.API)
		if err != nil {
			return nil, nil, fmt.Errorf("识别站点脚本 %s 失败：%w", site.API, err)
		}
		if res.Status < 200 || res.Status >= 300 {
			return nil, nil, fmt.Errorf("识别站点脚本 %s 失败：HTTP %d (Content-Type %q)", site.API, res.Status, res.ContentType)
		}
		source := string(res.Body)
		if strings.HasPrefix(strings.TrimLeft(source, "\ufeff \t\r\n"), "<") || strings.Contains(strings.ToLower(res.ContentType), "html") {
			return nil, nil, fmt.Errorf("站点脚本 %s 返回 HTML，不是 JavaScript：HTTP %d (Content-Type %q)", site.API, res.Status, res.ContentType)
		}
		if strings.Contains(strings.ToLower(res.ContentType), "json") {
			return nil, nil, fmt.Errorf("站点脚本 %s 返回 JSON，不是 JavaScript：HTTP %d (Content-Type %q)", site.API, res.Status, res.ContentType)
		}
		kind := config.Classify(site.Type, site.API, site.Ext, source)
		if kind == config.KindDRPY2 || drpyObjectExport(source) {
			site.Kind = config.KindDRPY2
		}
	}
	if site.Kind == config.KindFongMi {
		return fongmi.Open(ctx, site, assets)
	}
	return catalog.Open(ctx, site, assets)
}

func scriptURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(path.Ext(u.Path), ".js")
}

var drpyMethods = regexp.MustCompile(`\b(?:init|home)\s*(?:\(|:)`)
var defaultObject = regexp.MustCompile(`\bexport\s+default\s*\{`)

func drpyObjectExport(source string) bool {
	if strings.Contains(source, "extends Spider") {
		return false
	}
	// Minified drpy2 scripts may encode every method key as a hex escape.
	if defaultObject.MatchString(source) {
		return true
	}
	if !strings.Contains(source, "export default") {
		return false
	}
	// A drpy2 entry exports methods named init/home; a class spider exports
	// homeContent/categoryContent and derives from Spider.
	methods := drpyMethods.FindAllString(source, -1)
	hasInit, hasHome := false, false
	for _, method := range methods {
		if strings.HasPrefix(method, "init") {
			hasInit = true
		}
		if strings.HasPrefix(method, "home") {
			hasHome = true
		}
	}
	return hasInit && hasHome
}
