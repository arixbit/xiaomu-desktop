// Package fongmi adapts FongMi class spiders to the catalog data model.
package fongmi

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/drpy"
)

// Session owns one class spider. Its methods serialize access to QuickJS.
type Session struct {
	mu     sync.Mutex
	engine *drpy.Engine
}

// Open loads a FongMi module, initializes its Spider subclass and reads categories.
func Open(ctx context.Context, site config.Site, assets string) (*Session, []catalog.Category, error) {
	if site.Kind != config.KindFongMi {
		return nil, nil, fmt.Errorf("fongmi: unsupported site kind %s", site.Kind)
	}
	if site.API == "" {
		return nil, nil, fmt.Errorf("fongmi: site has no script URL")
	}
	key, _ := json.Marshal(site.Key)
	api, _ := json.Marshal(site.API)
	shim := fmt.Sprintf(catvodModule, key)
	wrapperURL := site.API + "#xiaomu-fongmi"
	modules := map[string]string{
		"catvod":                    shim,
		"assets://js/lib/catvod.js": shim,
		wrapperURL:                  fmt.Sprintf(spiderWrapper, api),
	}
	eng, err := drpy.Open(drpy.Options{AssetsDir: assets, Modules: modules})
	if err != nil {
		return nil, nil, err
	}
	s := &Session{engine: eng}
	if err := eng.LoadContext(ctx, wrapperURL); err != nil {
		s.Close()
		return nil, nil, fmt.Errorf("fongmi: load %s: %w", site.API, err)
	}
	var ext any = site.Ext
	if strings.HasPrefix(strings.TrimSpace(site.Ext), "{") || strings.HasPrefix(strings.TrimSpace(site.Ext), "[") {
		if err := json.Unmarshal([]byte(site.Ext), &ext); err != nil {
			s.Close()
			return nil, nil, fmt.Errorf("fongmi: invalid ext JSON: %w", err)
		}
	}
	if _, err := eng.CallAwait(ctx, "init", ext); err != nil {
		s.Close()
		return nil, nil, fmt.Errorf("fongmi: initialize %s: %w", site.API, err)
	}
	raw, err := eng.CallAwait(ctx, "home", false)
	if err != nil {
		s.Close()
		return nil, nil, fmt.Errorf("fongmi: home %s: %w", site.API, err)
	}
	var home struct {
		Categories []catalog.Category `json:"class"`
	}
	if err := json.Unmarshal([]byte(raw), &home); err != nil {
		s.Close()
		return nil, nil, fmt.Errorf("fongmi: categories %s: %w", site.API, err)
	}
	return s, home.Categories, nil
}

// Close releases the script runtime.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine != nil {
		_, _ = s.engine.CallAwait(context.Background(), "destroy")
		_ = s.engine.Close()
		s.engine = nil
	}
}

func (s *Session) call(ctx context.Context, name string, args ...any) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.engine == nil {
		return "", fmt.Errorf("fongmi: site is closed")
	}
	return s.engine.CallAwait(ctx, name, args...)
}

// Category returns one page of videos.
func (s *Session) Category(ctx context.Context, id string, page int) (catalog.Page, error) {
	raw, err := s.call(ctx, "category", id, page, false, map[string]any{})
	return readPage(raw, page, err)
}

// Search returns one page of matching videos.
func (s *Session) Search(ctx context.Context, query string, page int) (catalog.Page, error) {
	raw, err := s.call(ctx, "search", query, false, page)
	return readPage(raw, page, err)
}

func readPage(raw string, page int, err error) (catalog.Page, error) {
	if err != nil {
		return catalog.Page{}, err
	}
	var doc struct {
		List      []catalog.Video `json:"list"`
		PageCount catalog.ID      `json:"pagecount"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return catalog.Page{}, fmt.Errorf("fongmi: video list: %w", err)
	}
	out := catalog.Page{}
	for _, video := range doc.List {
		if video.ID != "" && video.ID != "no_data" {
			out.Videos = append(out.Videos, video)
		}
	}
	out.HasNext = len(out.Videos) > 0
	if count, err := strconv.Atoi(string(doc.PageCount)); err == nil && count > 0 {
		out.HasNext = page < count
	}
	return out, nil
}

// Detail reads the sources and episodes for one video.
func (s *Session) Detail(ctx context.Context, id string) (catalog.Detail, error) {
	raw, err := s.call(ctx, "detail", id)
	if err != nil {
		return catalog.Detail{}, err
	}
	var doc struct {
		List []struct {
			Name    string `json:"vod_name"`
			Pic     string `json:"vod_pic"`
			Content string `json:"vod_content"`
			Year    string `json:"vod_year"`
			Type    string `json:"vod_class"`
			From    string `json:"vod_play_from"`
			URLs    string `json:"vod_play_url"`
		} `json:"list"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return catalog.Detail{}, fmt.Errorf("fongmi: detail: %w", err)
	}
	if len(doc.List) == 0 {
		return catalog.Detail{}, fmt.Errorf("fongmi: site returned no detail")
	}
	v := doc.List[0]
	out := catalog.Detail{Name: v.Name, Pic: v.Pic, Content: v.Content, Year: v.Year, Type: v.Type}
	names := strings.Split(v.From, "$$$")
	for i, group := range strings.Split(v.URLs, "$$$") {
		source := catalog.Source{}
		if i < len(names) {
			source.Name = names[i]
		}
		for _, item := range strings.Split(group, "#") {
			name, id, ok := strings.Cut(strings.TrimSpace(item), "$")
			if !ok {
				id, name = name, fmt.Sprintf("第 %d 集", len(source.Episodes)+1)
			}
			if strings.TrimSpace(id) != "" {
				source.Episodes = append(source.Episodes, catalog.Episode{Name: name, ID: id})
			}
		}
		if len(source.Episodes) > 0 {
			out.Sources = append(out.Sources, source)
		}
	}
	if len(out.Sources) == 0 {
		return catalog.Detail{}, fmt.Errorf("fongmi: video has no playable episodes")
	}
	return out, nil
}

// Play resolves one episode to a direct HTTP media URL.
func (s *Session) Play(ctx context.Context, source string, episode catalog.Episode) (catalog.Playback, error) {
	raw, err := s.call(ctx, "play", source, episode.ID, []string{})
	if err != nil {
		return catalog.Playback{}, err
	}
	return catalog.ReadPlayback(raw)
}

const spiderWrapper = `
import { Spider } from 'catvod';
import Entry from %s;
if (typeof Entry !== 'function' || !(Entry.prototype instanceof Spider)) {
  throw new TypeError('FongMi script must export a class extending Spider');
}
const spider = new Entry();
async function output(value) {
  const result = await value;
  if (!result || typeof result !== 'object') throw new TypeError('FongMi method must return an object');
  return JSON.stringify(result);
}
export default {
  async init(ext) { await spider.init(ext); return ''; },
  async home(filter) { return output(spider.homeContent(filter)); },
  async category(id, page, filter, extend) { return output(spider.categoryContent(id, String(page), filter, extend)); },
  async search(query, quick, page) { return output(spider.searchContent(query, quick, String(page))); },
  async detail(id) { return output(spider.detailContent([id])); },
  async play(flag, id, vipFlags) { return output(spider.playerContent(flag, id, vipFlags)); },
  async destroy() { await spider.destroy(); return ''; }
};
`

// This is a small compatibility surface for class spiders using the common
// catvod text/JSON request methods. Native Android services are not emulated.
const catvodModule = `
const siteKey = %s;
const unsupported = name => { throw new Error('FongMi host capability unavailable: ' + name); };
export class Spider {
  constructor() { this.siteKey = siteKey; this.net = net; this.local = local; this.js = js; }
  init(ext) {}
  homeContent(filter) { return {class: []}; }
  homeVideoContent() { return {list: []}; }
  categoryContent(tid, pg, filter, extend) { return {list: []}; }
  detailContent(ids) { return {list: []}; }
  searchContent(key, quick, pg) { return {list: []}; }
  playerContent(flag, id, vipFlags) { return {}; }
  destroy() {}
  proxyUrl() { unsupported('proxyUrl'); }
}
export const net = {
  async req(url, options = {}) {
    try {
      const opts = options || {};
      if (opts.buffer) unsupported('binary response');
      const response = globalThis.req(String(url), {method: opts.method || 'GET', headers: opts.headers || {}, body: opts.body ?? opts.data ?? ''});
      return {code: response.code || 200, headers: response.headers || {}, content: response.content || ''};
    } catch (error) {
      return {code: '', headers: {}, content: '', error: String(error)};
    }
  },
  async json(url, options = {}) {
    const response = await net.req(url, options);
    if (response.error) throw new Error(response.error);
    if (response.code < 200 || response.code >= 300) throw new Error('HTTP ' + response.code + ': ' + url);
    return JSON.parse(response.content);
  },
  getUserAgent() { return 'xiaomu/0.0.1'; },
  sleep() { unsupported('net.sleep'); },
  session() { unsupported('net.session'); }
};
export const local = {
  get(key, fallback = null) {
    const raw = globalThis.local.get(String(key));
    if (!raw) return fallback;
    try { return JSON.parse(raw); } catch (_) { return raw; }
  },
  set(key, value) { globalThis.local.set(String(key), JSON.stringify(value)); },
  delete(key) { globalThis.local.delete(String(key)); }
};
export const js = {eval(source) { return globalThis.eval(String(source)); }};
export const joinUrl = (base, ref) => globalThis.joinUrl(base, ref);
export const page = (items, pg, limit, total) => {
  pg = Math.max(1, Number(pg));
  if (total == null) { total = items.length; items = items.slice((pg - 1) * limit, pg * limit); }
  return {list: items, page: pg, pagecount: Math.max(1, Math.ceil(total / limit)), limit, total};
};
export const htmlQuery = () => unsupported('htmlQuery');
export const md5X = () => unsupported('md5X');
export const sha256X = () => unsupported('sha256X');
export const proxyUrl = () => unsupported('proxyUrl');
export const openWeb = () => unsupported('openWeb');
export const s2t = value => value;
export const t2s = value => value;
export const link = (name, target) => '[a=cr:' + JSON.stringify(target) + '/]' + name + '[/a]';
`
