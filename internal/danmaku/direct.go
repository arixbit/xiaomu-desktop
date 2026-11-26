package danmaku

import (
	"bytes"
	"cmp"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/httpx"
)

const (
	directUserAgent        = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	directMaxCandidates    = 8
	directMaxVarietyRows   = 4
	directMaxVarietyYears  = 3
	directSegmentWorkers   = 8
	directSegmentTimeout   = 15 * time.Second
	iqiyiDecodeXOR         = uint64(0x75706971676c)
	iqiyiSegmentSeconds    = 300
	iqiyiMaxSegments       = 60
	mangoSegmentSeconds    = 60
	mangoMaxSegments       = 240
	youkuAppKey            = "24679788"
	youkuDanmuSalt         = "MkmC9SoIw6xCkSKHhJ7b5D2r51kBiREr"
	youkuWindowMinutes     = 2
	youkuWindowSeconds     = youkuWindowMinutes * 60
	youkuMaxWindows        = 120
	youkuFallbackWindows   = 90
	youkuEmptyProbeStop    = 3
	youkuSegmentWorkers    = 3
	detailEpisodeBatchSize = 200
)

var directPlatformOrder = []string{"qq", "qiyi", "youku", "imgo", "bilibili1"}

type directEndpoints struct {
	so360        string
	web360       string
	qqBarrage    string
	iqiyiInfo    string
	iqiyiBullet  string
	youkuShow    string
	youkuBarrage string
	mgtvInfo     string
	mgtvBarrage  string
	mgtvCDNBase  string // optional test override; empty uses https://{cdnHost}
	biliAPI      string
	biliComment  string
}

func defaultDirectEndpoints() directEndpoints {
	return directEndpoints{
		so360:        "https://api.so.360kan.com",
		web360:       "https://api.web.360kan.com",
		qqBarrage:    "https://dm.video.qq.com",
		iqiyiInfo:    "https://pcw-api.iqiyi.com",
		iqiyiBullet:  "https://cmts.iqiyi.com",
		youkuShow:    "https://v.youku.com",
		youkuBarrage: "https://acs.youku.com",
		mgtvInfo:     "https://pcweb.api.mgtv.com",
		mgtvBarrage:  "https://galaxy.bz.mgtv.com",
		biliAPI:      "https://api.bilibili.com",
		biliComment:  "https://comment.bilibili.com",
	}
}

type directProvider struct {
	http      *http.Client
	endpoints directEndpoints
}

func newDirectProvider(httpClient *http.Client) *directProvider {
	return &directProvider{
		http:      httpClient,
		endpoints: defaultDirectEndpoints(),
	}
}

func platformLabel(site string) string {
	switch site {
	case "qq":
		return "腾讯"
	case "qiyi":
		return "爱奇艺"
	case "youku":
		return "优酷"
	case "imgo":
		return "芒果"
	case "bilibili1":
		return "B站"
	default:
		return site
	}
}

func supportedPlatform(site string) bool {
	return slices.Contains(directPlatformOrder, site)
}

func formatDirectAnime(title, year, cat, site string) string {
	title = strings.TrimSpace(title)
	year = strings.TrimSpace(year)
	cat = strings.TrimSpace(cat)
	if cat == "" {
		cat = "影视"
	}
	if year != "" {
		return fmt.Sprintf("%s (%s) 【%s】 from %s", title, year, cat, site)
	}
	return fmt.Sprintf("%s 【%s】 from %s", title, cat, site)
}

type flexString string

func (s *flexString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*s = ""
		return nil
	}
	if data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*s = flexString(text)
		return nil
	}
	*s = flexString(strings.TrimSpace(string(data)))
	return nil
}

type so360Row struct {
	EnID            string          `json:"en_id"`
	TitleTxt        string          `json:"titleTxt"`
	Year            flexString      `json:"year"`
	CatName         string          `json:"cat_name"`
	SeriesSite      string          `json:"seriesSite"`
	SeriesPlaylinks json.RawMessage `json:"seriesPlaylinks"`
	Playlinks       json.RawMessage `json:"playlinks"`
	PlaylinksYear   json.RawMessage `json:"playlinks_year"`
}

func (p *directProvider) Search(ctx context.Context, title, episode string) ([]Episode, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, nil
	}
	targetEp, _ := strconv.Atoi(strings.TrimSpace(episode))
	searchURL := fmt.Sprintf("%s/index?force_v=1&kw=%s&from=&pageno=1&v_ap=1&tab=all",
		strings.TrimRight(p.endpoints.so360, "/"), url.QueryEscape(title))
	data, err := p.get(ctx, searchURL, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			LongData json.RawMessage `json:"longData"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("弹幕搜索响应不是有效 JSON")
	}
	longData := bytes.TrimSpace(resp.Data.LongData)
	if len(longData) == 0 || longData[0] != '{' {
		return nil, nil
	}
	var parsed struct {
		Rows []so360Row `json:"rows"`
	}
	if err := json.Unmarshal(longData, &parsed); err != nil {
		return nil, fmt.Errorf("弹幕搜索响应格式无效")
	}

	type candidateRow struct {
		row           so360Row
		playlinks     map[string]string
		varietyLinks  map[string][]varietyEpisode
		playlinkYears map[string][]string
		sites         []string
		exact         bool
	}
	queryNorm := normalizeDirectTitle(title)
	var candidates []candidateRow
	for _, row := range parsed.Rows {
		if strings.TrimSpace(row.TitleTxt) == "" {
			continue
		}
		links, varietyLinks := parsePlaylinks(row.Playlinks)
		var sites []string
		for _, site := range directPlatformOrder {
			if links[site] != "" || len(varietyLinks[site]) > 0 || (row.SeriesSite == site && len(parsePlaylinkList(row.SeriesPlaylinks)) > 0) {
				sites = append(sites, site)
			}
		}
		if len(sites) == 0 {
			continue
		}
		candidates = append(candidates, candidateRow{
			row:           row,
			playlinks:     links,
			varietyLinks:  varietyLinks,
			playlinkYears: parsePlaylinksYear(row.PlaylinksYear),
			sites:         sites,
			exact:         normalizeDirectTitle(row.TitleTxt) == queryNorm,
		})
	}
	slices.SortStableFunc(candidates, func(a, b candidateRow) int {
		if a.exact != b.exact {
			if a.exact {
				return -1
			}
			return 1
		}
		return 0
	})
	if len(candidates) > directMaxCandidates {
		candidates = candidates[:directMaxCandidates]
	}

	type v2Key struct {
		cat   string
		entID string
		site  string
	}
	type v2ReqItem struct {
		CatID string `json:"cat_id"`
		EntID string `json:"ent_id"`
		Site  string `json:"site"`
	}
	var batch []v2ReqItem
	batchSeen := map[v2Key]bool{}
	inlineEpisodes := map[v2Key][]string{}
	for _, c := range candidates {
		cat := catIDForName(c.row.CatName)
		if cat == "" || c.row.EnID == "" {
			continue
		}
		if supportedPlatform(c.row.SeriesSite) {
			if urls := parsePlaylinkList(c.row.SeriesPlaylinks); len(urls) > 0 {
				inlineEpisodes[v2Key{cat: cat, entID: c.row.EnID, site: c.row.SeriesSite}] = urls
			}
		}
		for _, site := range c.sites {
			key := v2Key{cat: cat, entID: c.row.EnID, site: site}
			if len(inlineEpisodes[key]) > 0 || batchSeen[key] {
				continue
			}
			batchSeen[key] = true
			batch = append(batch, v2ReqItem{CatID: cat, EntID: c.row.EnID, Site: site})
		}
	}

	fetchedEpisodes := map[v2Key][]string{}
	if len(batch) > 0 {
		rawBatch, _ := json.Marshal(batch)
		v2URL := fmt.Sprintf("%s/episodesv2?v_ap=1&s=%s",
			strings.TrimRight(p.endpoints.so360, "/"), url.QueryEscape(string(rawBatch)))
		if body, err := p.get(ctx, v2URL, nil); err == nil {
			var v2Resp struct {
				Data []struct {
					CatID      flexString `json:"cat_id"`
					EntID      string     `json:"ent_id"`
					Site       string     `json:"site"`
					SeriesHTML struct {
						SeriesPlaylinks json.RawMessage `json:"seriesPlaylinks"`
					} `json:"seriesHTML"`
				} `json:"data"`
			}
			if json.Unmarshal(body, &v2Resp) == nil {
				for _, item := range v2Resp.Data {
					if urls := parsePlaylinkList(item.SeriesHTML.SeriesPlaylinks); len(urls) > 0 {
						fetchedEpisodes[v2Key{cat: string(item.CatID), entID: item.EntID, site: item.Site}] = urls
					}
				}
			}
		}
		// Fall back to v1/detail only for entries that episodesv2 left empty.
		var missing []v2ReqItem
		for _, item := range batch {
			key := v2Key{cat: item.CatID, entID: item.EntID, site: item.Site}
			if len(fetchedEpisodes[key]) == 0 {
				missing = append(missing, item)
			}
		}
		if len(missing) == 1 {
			item := missing[0]
			if urls := p.fetchDetailEpisodes(ctx, item.CatID, item.EntID, item.Site, targetEp); len(urls) > 0 {
				fetchedEpisodes[v2Key{cat: item.CatID, entID: item.EntID, site: item.Site}] = urls
			}
		} else if len(missing) > 1 {
			fallbackURLs := make([][]string, len(missing))
			sem := make(chan struct{}, min(4, len(missing)))
			var wg sync.WaitGroup
			for i, item := range missing {
				if ctx.Err() != nil {
					break
				}
				wg.Add(1)
				sem <- struct{}{}
				go func(idx int, req v2ReqItem) {
					defer wg.Done()
					defer func() { <-sem }()
					fallbackURLs[idx] = p.fetchDetailEpisodes(ctx, req.CatID, req.EntID, req.Site, targetEp)
				}(i, item)
			}
			wg.Wait()
			for i, item := range missing {
				if len(fallbackURLs[i]) > 0 {
					fetchedEpisodes[v2Key{cat: item.CatID, entID: item.EntID, site: item.Site}] = fallbackURLs[i]
				}
			}
		}
	}

	type varietyKey struct {
		entID string
		site  string
	}
	type varietyReq struct {
		key    varietyKey
		years  []string
		year   string
		inline []varietyEpisode
	}
	var varietyReqs []varietyReq
	varietySeen := map[varietyKey]bool{}
	varietyRowsQueued := 0
	for _, c := range candidates {
		if c.row.CatName != "综艺" || c.row.EnID == "" || varietyRowsQueued >= directMaxVarietyRows {
			continue
		}
		varietyRowsQueued++
		for _, site := range c.sites {
			key := varietyKey{entID: c.row.EnID, site: site}
			if varietySeen[key] {
				continue
			}
			varietySeen[key] = true
			varietyReqs = append(varietyReqs, varietyReq{
				key:    key,
				years:  c.playlinkYears[site],
				year:   string(c.row.Year),
				inline: c.varietyLinks[site],
			})
		}
	}
	fetchedVariety := map[varietyKey][]varietyEpisode{}
	if len(varietyReqs) == 1 {
		req := varietyReqs[0]
		fetchedVariety[req.key] = p.fetchVarietyEpisodes(ctx, req.key.entID, req.key.site, req.years, req.year, req.inline)
	} else if len(varietyReqs) > 1 {
		results := make([][]varietyEpisode, len(varietyReqs))
		sem := make(chan struct{}, min(4, len(varietyReqs)))
		var wg sync.WaitGroup
		for i, req := range varietyReqs {
			if ctx.Err() != nil {
				break
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(idx int, r varietyReq) {
				defer wg.Done()
				defer func() { <-sem }()
				results[idx] = p.fetchVarietyEpisodes(ctx, r.key.entID, r.key.site, r.years, r.year, r.inline)
			}(i, req)
		}
		wg.Wait()
		for i, req := range varietyReqs {
			fetchedVariety[req.key] = results[i]
		}
	}

	var episodes []Episode
	varietyEmitted := 0
	for _, c := range candidates {
		row := c.row
		switch row.CatName {
		case "电影":
			for _, site := range c.sites {
				id, ok := directEpisodeID(site, c.playlinks[site])
				if !ok {
					continue
				}
				episodes = append(episodes, Episode{
					ID:      id,
					AnimeID: row.EnID + ":" + site,
					Anime:   formatDirectAnime(row.TitleTxt, string(row.Year), row.CatName, site),
					Name:    fmt.Sprintf("【%s】 正片", platformLabel(site)),
				})
			}
		case "电视剧", "动漫":
			cat := catIDForName(row.CatName)
			for _, site := range c.sites {
				key := v2Key{cat: cat, entID: row.EnID, site: site}
				urls := inlineEpisodes[key]
				if len(urls) == 0 {
					urls = fetchedEpisodes[key]
				}
				if len(urls) == 0 {
					continue
				}
				animeID := row.EnID + ":" + site
				animeLabel := formatDirectAnime(row.TitleTxt, string(row.Year), row.CatName, site)
				label := platformLabel(site)
				for i, epURL := range urls {
					id, ok := directEpisodeID(site, epURL)
					if !ok {
						continue
					}
					episodes = append(episodes, Episode{
						ID:      id,
						AnimeID: animeID,
						Anime:   animeLabel,
						Name:    fmt.Sprintf("【%s】 第%d集", label, i+1),
					})
					if len(episodes) == 3000 {
						return episodes, nil
					}
				}
			}
		case "综艺":
			if varietyEmitted >= directMaxVarietyRows || row.EnID == "" {
				continue
			}
			varietyEmitted++
			for _, site := range c.sites {
				items := fetchedVariety[varietyKey{entID: row.EnID, site: site}]
				animeID := row.EnID + ":" + site
				animeLabel := formatDirectAnime(row.TitleTxt, string(row.Year), row.CatName, site)
				label := platformLabel(site)
				for _, item := range items {
					id, ok := directEpisodeID(site, item.URL)
					if !ok {
						continue
					}
					episodes = append(episodes, Episode{
						ID:      id,
						AnimeID: animeID,
						Anime:   animeLabel,
						Name:    fmt.Sprintf("【%s】 %s", label, formatVarietyEpisodeName(item)),
					})
					if len(episodes) == 3000 {
						return episodes, nil
					}
				}
			}
		}
	}
	return episodes, nil
}

func (p *directProvider) fetchDetailEpisodes(ctx context.Context, cat, entID, site string, targetEp int) []string {
	start, end := 1, detailEpisodeBatchSize
	if targetEp > detailEpisodeBatchSize {
		start = ((targetEp-1)/detailEpisodeBatchSize)*detailEpisodeBatchSize + 1
		end = start + detailEpisodeBatchSize - 1
	}
	detailURL := fmt.Sprintf("%s/v1/detail?cat=%s&id=%s&start=%d&end=%d&site=%s",
		strings.TrimRight(p.endpoints.web360, "/"),
		url.QueryEscape(cat), url.QueryEscape(entID), start, end, url.QueryEscape(site))
	body, err := p.get(ctx, detailURL, nil)
	if err != nil {
		return nil
	}
	var resp struct {
		Data struct {
			AllEpDetail map[string][]struct {
				Num flexString `json:"playlink_num"`
				URL string     `json:"url"`
			} `json:"allepidetail"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return nil
	}
	list := resp.Data.AllEpDetail[site]
	if len(list) == 0 {
		return nil
	}
	// Preserve 1-based episode index alignment when fetching a later page.
	urls := make([]string, 0, start-1+len(list))
	for i := 1; i < start; i++ {
		urls = append(urls, "")
	}
	for _, item := range list {
		urls = append(urls, strings.TrimSpace(item.URL))
	}
	return urls
}

type varietyEpisode struct {
	Name     string     `json:"name"`
	Period   string     `json:"period"`
	URL      string     `json:"url"`
	Duration flexString `json:"duration"`
}

func (p *directProvider) fetchVarietyEpisodes(ctx context.Context, entID, site string, years []string, fallbackYear string, inline []varietyEpisode) []varietyEpisode {
	queryYears := make([]string, 0, directMaxVarietyYears)
	yearSeen := map[string]bool{}
	for _, y := range years {
		y = strings.TrimSpace(y)
		if y == "" || yearSeen[y] {
			continue
		}
		yearSeen[y] = true
		queryYears = append(queryYears, y)
		if len(queryYears) == directMaxVarietyYears {
			break
		}
	}
	if len(queryYears) == 0 {
		if y := strings.TrimSpace(fallbackYear); y != "" {
			queryYears = append(queryYears, y)
		}
	}

	var collected []varietyEpisode
	if len(queryYears) > 0 {
		for _, y := range queryYears {
			if ctx.Err() != nil {
				break
			}
			collected = append(collected, p.fetchVarietyDetailYear(ctx, entID, site, y)...)
		}
	} else {
		collected = append(collected, p.fetchVarietyDetailYear(ctx, entID, site, "")...)
	}

	// Legacy endpoint fallback for compatibility with older responses / fixtures.
	if len(collected) == 0 {
		zongyiURL := fmt.Sprintf("%s/episodeszongyi?entid=%s&site=%s&y=%s&count=50&offset=0",
			strings.TrimRight(p.endpoints.so360, "/"),
			url.QueryEscape(entID), url.QueryEscape(site), url.QueryEscape(strings.TrimSpace(fallbackYear)))
		if body, err := p.get(ctx, zongyiURL, nil); err == nil {
			var resp struct {
				Data struct {
					List []varietyEpisode `json:"list"`
				} `json:"data"`
			}
			if json.Unmarshal(body, &resp) == nil {
				collected = append(collected, resp.Data.List...)
			}
		}
	}

	// Inline playlinks fallback when detail endpoints return nothing.
	if len(collected) == 0 && len(inline) > 0 {
		collected = append(collected, inline...)
	}
	return normalizeVarietyEpisodes(site, collected)
}

func (p *directProvider) fetchVarietyDetailYear(ctx context.Context, entID, site, year string) []varietyEpisode {
	detailURL := fmt.Sprintf("%s/v1/detail?cat=3&id=%s&site=%s",
		strings.TrimRight(p.endpoints.web360, "/"),
		url.QueryEscape(entID), url.QueryEscape(site))
	if year != "" {
		detailURL += "&year=" + url.QueryEscape(year)
	}
	body, err := p.get(ctx, detailURL, nil)
	if err != nil {
		return nil
	}
	var resp struct {
		Data struct {
			DefaultEpisode []varietyEpisode `json:"defaultepisode"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return nil
	}
	return resp.Data.DefaultEpisode
}

func normalizeVarietyEpisodes(site string, raw []varietyEpisode) []varietyEpisode {
	if len(raw) == 0 {
		return nil
	}
	type rankedEpisode struct {
		item varietyEpisode
		rank int
	}
	seen := make(map[string]bool, len(raw))
	ranked := make([]rankedEpisode, 0, len(raw))
	for _, item := range raw {
		id, ok := directEpisodeID(site, item.URL)
		if !ok || seen[id] {
			continue
		}
		rank, drop := classifyVarietyEpisode(item)
		if drop {
			continue
		}
		seen[id] = true
		ranked = append(ranked, rankedEpisode{item: item, rank: rank})
	}
	slices.SortStableFunc(ranked, func(a, b rankedEpisode) int {
		return cmp.Compare(a.rank, b.rank)
	})
	out := make([]varietyEpisode, len(ranked))
	for i, r := range ranked {
		out[i] = r.item
	}
	return out
}

// classifyVarietyEpisode separates main episodes (0), extended/plus cuts (1),
// clips/behind-the-scenes (2), and trailers (3) so full episodes appear first
// while short trailers (<5 min) do not clutter episode selection.
func classifyVarietyEpisode(item varietyEpisode) (rank int, drop bool) {
	lower := strings.ToLower(strings.TrimSpace(item.Name))
	durSec := parseClockSeconds(string(item.Duration))
	isTrailer := strings.Contains(lower, "预告") || strings.Contains(lower, "片花") || strings.Contains(lower, "抢先看")
	if isTrailer {
		if durSec > 0 && durSec < 300 {
			return 3, true
		}
		return 3, false
	}
	for _, kw := range []string{"纯享", "直拍"} {
		if strings.Contains(lower, kw) {
			return 2, false
		}
	}
	for _, kw := range []string{"加更", "plus", "会员版", "先导", "超前"} {
		if strings.Contains(lower, kw) {
			return 1, false
		}
	}
	for _, kw := range []string{"花絮", "幕后", "专访", "看点", "彩蛋", "vlog", "片段", "速看", "未播"} {
		if strings.Contains(lower, kw) {
			return 2, false
		}
	}
	for _, kw := range []string{"特辑", "企划", "衍生", "陪看", "回顾", "番外", "特别篇"} {
		if strings.Contains(lower, kw) {
			return 1, false
		}
	}
	if durSec > 0 && durSec < 300 {
		return 2, false
	}
	return 0, false
}

func formatVarietyEpisodeName(item varietyEpisode) string {
	name := strings.TrimSpace(item.Name)
	period := strings.TrimSpace(item.Period)
	if name == "" && period == "" {
		return "正片"
	}
	if name == "" {
		return period
	}
	if period == "" {
		return name
	}
	barePeriod := strings.TrimSuffix(period, "期")
	if strings.Contains(name, period) || (barePeriod != "" && strings.Contains(name, barePeriod)) {
		return name
	}
	return name + " " + period
}

func (p *directProvider) Comments(ctx context.Context, id string) ([]Comment, error) {
	if !validID(id) {
		return nil, fmt.Errorf("弹幕分集 ID 无效")
	}
	parts := strings.Split(id, ":")
	switch parts[0] {
	case "qq":
		if len(parts) != 2 || parts[1] == "" {
			return nil, fmt.Errorf("腾讯弹幕分集 ID 无效")
		}
		return p.tencentComments(ctx, parts[1])
	case "qiyi":
		if len(parts) != 2 || parts[1] == "" {
			return nil, fmt.Errorf("爱奇艺弹幕分集 ID 无效")
		}
		return p.iqiyiComments(ctx, parts[1])
	case "youku":
		if len(parts) != 2 || !directYoukuSafeVID.MatchString(parts[1]) {
			return nil, fmt.Errorf("优酷弹幕分集 ID 无效")
		}
		return p.youkuComments(ctx, parts[1])
	case "imgo":
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			return nil, fmt.Errorf("芒果弹幕分集 ID 无效")
		}
		return p.mangoComments(ctx, parts[1], parts[2])
	case "bili":
		if len(parts) != 3 || parts[2] == "" {
			return nil, fmt.Errorf("B站弹幕分集 ID 无效")
		}
		return p.bilibiliComments(ctx, parts[1], parts[2])
	default:
		return nil, fmt.Errorf("不支持的内置弹幕分集 ID")
	}
}

func (p *directProvider) tencentComments(ctx context.Context, vid string) ([]Comment, error) {
	baseURL := fmt.Sprintf("%s/barrage/base/%s", strings.TrimRight(p.endpoints.qqBarrage, "/"), url.PathEscape(vid))
	body, err := p.get(ctx, baseURL, nil)
	if err != nil {
		if errors.Is(err, errHTTPNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var base struct {
		SegmentIndex map[string]struct {
			Start flexString `json:"segment_start"`
			Name  string     `json:"segment_name"`
		} `json:"segment_index"`
	}
	if err := json.Unmarshal(body, &base); err != nil {
		return nil, fmt.Errorf("解析腾讯弹幕索引失败")
	}
	type segment struct {
		start int64
		url   string
	}
	segments := make([]segment, 0, len(base.SegmentIndex))
	prefix := fmt.Sprintf("%s/barrage/segment/%s/", strings.TrimRight(p.endpoints.qqBarrage, "/"), url.PathEscape(vid))
	for _, item := range base.SegmentIndex {
		if strings.TrimSpace(item.Name) == "" {
			continue
		}
		start, _ := strconv.ParseInt(strings.TrimSpace(string(item.Start)), 10, 64)
		segments = append(segments, segment{start: start, url: prefix + strings.TrimLeft(item.Name, "/")})
	}
	if len(segments) == 0 {
		return nil, nil
	}
	slices.SortFunc(segments, func(a, b segment) int { return cmp.Compare(a.start, b.start) })
	urls := make([]string, len(segments))
	for i, seg := range segments {
		urls[i] = seg.url
	}
	return p.fetchSegments(ctx, urls, func(segCtx context.Context, segURL string) ([]Comment, error) {
		raw, err := p.get(segCtx, segURL, nil)
		if err != nil {
			return nil, err
		}
		var doc struct {
			List []struct {
				TimeOffset   flexString `json:"time_offset"`
				Content      string     `json:"content"`
				ContentStyle string     `json:"content_style"`
			} `json:"barrage_list"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		out := make([]Comment, 0, len(doc.List))
		for _, item := range doc.List {
			ms, err := strconv.ParseFloat(strings.TrimSpace(string(item.TimeOffset)), 64)
			if err != nil || ms < 0 || ms > 24*60*60*1000 {
				continue
			}
			text := cleanCommentText(item.Content)
			if text == "" {
				continue
			}
			mode, color := 1, uint32(0xffffff)
			if item.ContentStyle != "" {
				var style struct {
					Color          string   `json:"color"`
					GradientColors []string `json:"gradient_colors"`
					Position       int      `json:"position"`
				}
				if json.Unmarshal([]byte(item.ContentStyle), &style) == nil {
					if len(style.GradientColors) > 0 {
						color = parseHexColor(style.GradientColors[0], color)
					} else if style.Color != "" {
						color = parseHexColor(style.Color, color)
					}
					switch style.Position {
					case 2:
						mode = 5
					case 3:
						mode = 4
					}
				}
			}
			out = append(out, Comment{
				Time:  time.Duration(ms * float64(time.Millisecond)),
				Mode:  mode,
				Color: color,
				Text:  text,
			})
		}
		return out, nil
	})
}

func (p *directProvider) iqiyiComments(ctx context.Context, tvid string) ([]Comment, error) {
	infoURL := fmt.Sprintf("%s/video/video/baseinfo/%s", strings.TrimRight(p.endpoints.iqiyiInfo, "/"), url.PathEscape(tvid))
	body, err := p.get(ctx, infoURL, nil)
	if err != nil {
		return nil, err
	}
	var info struct {
		Data struct {
			DurationSec    float64 `json:"durationSec"`
			DisplayBarrage *bool   `json:"displayBarrage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("解析爱奇艺视频信息失败")
	}
	if info.Data.DisplayBarrage != nil && !*info.Data.DisplayBarrage {
		return nil, nil
	}
	duration := int(math.Ceil(info.Data.DurationSec))
	if duration <= 0 {
		return nil, nil
	}
	pages := min((duration+iqiyiSegmentSeconds-1)/iqiyiSegmentSeconds, iqiyiMaxSegments)
	padded := "0000" + tvid
	sub := padded[len(padded)-4:len(padded)-2] + "/" + padded[len(padded)-2:]
	urls := make([]string, pages)
	base := strings.TrimRight(p.endpoints.iqiyiBullet, "/")
	for i := range pages {
		urls[i] = fmt.Sprintf("%s/bullet/%s/%s_%d_%d.z", base, sub, tvid, iqiyiSegmentSeconds, i+1)
	}
	return p.fetchSegments(ctx, urls, func(segCtx context.Context, segURL string) ([]Comment, error) {
		raw, err := p.get(segCtx, segURL, nil)
		if err != nil {
			if errors.Is(err, errHTTPNotFound) {
				return nil, nil
			}
			return nil, err
		}
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		xmlBytes, err := io.ReadAll(io.LimitReader(zr, maxResponseBytes+1))
		if err != nil {
			return nil, err
		}
		if len(xmlBytes) > maxResponseBytes {
			return nil, fmt.Errorf("弹幕分片解压后超过限制")
		}
		return parseIqiyiBulletXML(xmlBytes)
	})
}

func parseIqiyiBulletXML(data []byte) ([]Comment, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var out []Comment
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "bulletInfo" {
			continue
		}
		var item struct {
			Content  string `xml:"content"`
			ShowTime string `xml:"showTime"`
			Color    string `xml:"color"`
			Position int    `xml:"position"`
		}
		if err := decoder.DecodeElement(&item, &start); err != nil {
			return nil, err
		}
		seconds, err := strconv.ParseFloat(strings.TrimSpace(item.ShowTime), 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > 24*60*60 {
			continue
		}
		text := cleanCommentText(item.Content)
		if text == "" {
			continue
		}
		mode := 1
		switch item.Position {
		case 1:
			mode = 5
		case 2:
			mode = 4
		}
		out = append(out, Comment{
			Time:  time.Duration(seconds * float64(time.Second)),
			Mode:  mode,
			Color: parseHexColor(item.Color, 0xffffff),
			Text:  text,
		})
	}
	return out, nil
}

func (p *directProvider) mangoComments(ctx context.Context, cid, vid string) ([]Comment, error) {
	infoURL := fmt.Sprintf("%s/video/info?cid=%s&vid=%s",
		strings.TrimRight(p.endpoints.mgtvInfo, "/"), url.QueryEscape(cid), url.QueryEscape(vid))
	body, err := p.get(ctx, infoURL, nil)
	if err != nil {
		if errors.Is(err, errHTTPNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var info struct {
		Data struct {
			Info struct {
				Time string `json:"time"`
			} `json:"info"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("解析芒果视频信息失败")
	}
	duration := parseClockSeconds(info.Data.Info.Time)
	if duration <= 0 {
		return nil, nil
	}
	pages := min((duration+mangoSegmentSeconds-1)/mangoSegmentSeconds, mangoMaxSegments)

	ctlURL := fmt.Sprintf("%s/getctlbarrage?version=8.1.39&abroad=0&uuid=&os=10.15.7&platform=0&mac=&vid=%s&pid=&cid=%s&ticket=",
		strings.TrimRight(p.endpoints.mgtvBarrage, "/"), url.QueryEscape(vid), url.QueryEscape(cid))
	var cdnHost, cdnVersion string
	if ctlBody, ctlErr := p.get(ctx, ctlURL, nil); ctlErr == nil {
		var ctl struct {
			Data struct {
				CDNList    string `json:"cdn_list"`
				CDNVersion string `json:"cdn_version"`
			} `json:"data"`
		}
		if json.Unmarshal(ctlBody, &ctl) == nil && ctl.Data.CDNList != "" && ctl.Data.CDNVersion != "" {
			cdnHost = strings.TrimSpace(strings.Split(ctl.Data.CDNList, ",")[0])
			cdnVersion = strings.Trim(ctl.Data.CDNVersion, "/")
		}
	}

	urls := make([]string, pages)
	if cdnHost != "" && cdnVersion != "" {
		base := p.endpoints.mgtvCDNBase
		if base == "" {
			base = "https://" + cdnHost
		}
		base = strings.TrimRight(base, "/")
		for i := range pages {
			urls[i] = fmt.Sprintf("%s/%s/%d.json", base, cdnVersion, i)
		}
	} else {
		base := strings.TrimRight(p.endpoints.mgtvBarrage, "/")
		for i := range pages {
			urls[i] = fmt.Sprintf("%s/rdbarrage?vid=%s&cid=%s&time=%d", base, url.QueryEscape(vid), url.QueryEscape(cid), i*mangoSegmentSeconds*1000)
		}
	}

	return p.fetchSegments(ctx, urls, func(segCtx context.Context, segURL string) ([]Comment, error) {
		raw, err := p.get(segCtx, segURL, nil)
		if err != nil {
			if errors.Is(err, errHTTPNotFound) {
				return nil, nil
			}
			return nil, err
		}
		var doc struct {
			Data struct {
				Items []struct {
					Time       float64         `json:"time"`
					Content    string          `json:"content"`
					V2Position int             `json:"v2_position"`
					V2Color    json.RawMessage `json:"v2_color"`
				} `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		out := make([]Comment, 0, len(doc.Data.Items))
		for _, item := range doc.Data.Items {
			if item.Time < 0 || item.Time > 24*60*60*1000 {
				continue
			}
			text := cleanCommentText(item.Content)
			if text == "" {
				continue
			}
			mode := 1
			switch item.V2Position {
			case 1:
				mode = 5
			case 2:
				mode = 4
			}
			out = append(out, Comment{
				Time:  time.Duration(item.Time * float64(time.Millisecond)),
				Mode:  mode,
				Color: parseMangoColor(item.V2Color),
				Text:  text,
			})
		}
		return out, nil
	})
}

func (p *directProvider) bilibiliComments(ctx context.Context, kind, value string) ([]Comment, error) {
	var cid int64
	switch kind {
	case "ep":
		epID, err := strconv.ParseInt(value, 10, 64)
		if err != nil || epID <= 0 {
			return nil, fmt.Errorf("B站分集 ID 无效")
		}
		seasonURL := fmt.Sprintf("%s/pgc/view/web/season?ep_id=%d", strings.TrimRight(p.endpoints.biliAPI, "/"), epID)
		body, err := p.get(ctx, seasonURL, map[string]string{"Referer": "https://www.bilibili.com/"})
		if err != nil {
			return nil, err
		}
		var resp struct {
			Code   int `json:"code"`
			Result struct {
				Episodes []struct {
					ID  int64 `json:"id"`
					CID int64 `json:"cid"`
				} `json:"episodes"`
				Section []struct {
					Episodes []struct {
						ID  int64 `json:"id"`
						CID int64 `json:"cid"`
					} `json:"episodes"`
				} `json:"section"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || resp.Code != 0 {
			return nil, fmt.Errorf("获取B站番剧信息失败")
		}
		for _, ep := range resp.Result.Episodes {
			if ep.ID == epID && ep.CID > 0 {
				cid = ep.CID
				break
			}
		}
		if cid == 0 {
			for _, sec := range resp.Result.Section {
				for _, ep := range sec.Episodes {
					if ep.ID == epID && ep.CID > 0 {
						cid = ep.CID
						break
					}
				}
				if cid > 0 {
					break
				}
			}
		}
	case "bv":
		viewURL := fmt.Sprintf("%s/x/web-interface/view?bvid=%s", strings.TrimRight(p.endpoints.biliAPI, "/"), url.QueryEscape(value))
		body, err := p.get(ctx, viewURL, map[string]string{"Referer": "https://www.bilibili.com/"})
		if err != nil {
			return nil, err
		}
		var resp struct {
			Code int `json:"code"`
			Data struct {
				CID int64 `json:"cid"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || resp.Code != 0 {
			return nil, fmt.Errorf("获取B站视频信息失败")
		}
		cid = resp.Data.CID
	default:
		return nil, fmt.Errorf("不支持的B站分集类型")
	}
	if cid <= 0 {
		return nil, nil
	}
	commentURL := fmt.Sprintf("%s/%d.xml", strings.TrimRight(p.endpoints.biliComment, "/"), cid)
	raw, err := p.get(ctx, commentURL, map[string]string{"Referer": "https://www.bilibili.com/"})
	if err != nil {
		return nil, err
	}
	xmlBytes, err := decompressMaybeDeflate(raw)
	if err != nil {
		return nil, fmt.Errorf("解压B站弹幕失败：%w", err)
	}
	return Parse(xmlBytes)
}

type youkuDanmuMsg struct {
	PID    int    `json:"pid"`
	CType  int    `json:"ctype"`
	SVer   string `json:"sver"`
	CVer   string `json:"cver"`
	CTime  int64  `json:"ctime"`
	GUID   string `json:"guid"`
	LID    string `json:"lid"`
	AID    string `json:"aid"`
	Mat    int    `json:"mat"`
	MCount int    `json:"mcount"`
	Type   int    `json:"type"`
	VID    string `json:"vid"`
	Msg    string `json:"msg,omitempty"`
	Sign   string `json:"sign,omitempty"`
}

func (p *directProvider) youkuComments(ctx context.Context, vid string) ([]Comment, error) {
	var (
		tk, tkEnc string
		tokenErr  error
		duration  int
	)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		tk, tkEnc, tokenErr = p.youkuAcquireToken(ctx)
	}()
	go func() {
		defer wg.Done()
		duration = p.youkuDuration(ctx, vid)
	}()
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if tokenErr != nil {
		return nil, tokenErr
	}
	if duration > 0 {
		windows := min((duration+youkuWindowSeconds-1)/youkuWindowSeconds, youkuMaxWindows)
		mats := make([]string, windows)
		for i := range windows {
			mats[i] = strconv.Itoa(i * youkuWindowMinutes)
		}
		return p.fetchSegmentsWithWorkers(ctx, mats, youkuSegmentWorkers, func(segCtx context.Context, matStr string) ([]Comment, error) {
			mat, _ := strconv.Atoi(matStr)
			return p.youkuFetchWindow(segCtx, vid, mat, youkuWindowMinutes, tk, tkEnc)
		})
	}
	return p.youkuProbeComments(ctx, vid, tk, tkEnc)
}

func (p *directProvider) youkuProbeComments(ctx context.Context, vid, tk, tkEnc string) ([]Comment, error) {
	var (
		all        []Comment
		firstErr   error
		succeeded  int
		emptyRun   int
		foundAny   bool
	)
	for i := range youkuFallbackWindows {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		segCtx, cancel := context.WithTimeout(ctx, directSegmentTimeout)
		items, err := p.youkuFetchWindow(segCtx, vid, i*youkuWindowMinutes, youkuWindowMinutes, tk, tkEnc)
		cancel()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		succeeded++
		if len(items) == 0 {
			emptyRun++
			if (foundAny && emptyRun >= youkuEmptyProbeStop) || (!foundAny && i+1 >= youkuEmptyProbeStop+2) {
				break
			}
			continue
		}
		foundAny = true
		emptyRun = 0
		all = append(all, items...)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if succeeded == 0 && firstErr != nil {
		return nil, firstErr
	}
	slices.SortStableFunc(all, func(a, b Comment) int {
		return cmp.Compare(a.Time, b.Time)
	})
	return sampleComments(all, maxComments), nil
}

func (p *directProvider) youkuAcquireToken(ctx context.Context) (tk, tkEnc string, err error) {
	u := fmt.Sprintf("%s/h5/mopen.youku.danmu.list/1.0/?jsv=2.5.1&appKey=%s&t=%d&sign=0&api=mopen.youku.danmu.list&v=1.0&type=originaljson&dataType=jsonp&data=%%7B%%7D",
		strings.TrimRight(p.endpoints.youkuBarrage, "/"), youkuAppKey, time.Now().UnixMilli())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", fmt.Errorf("弹幕请求地址无效")
	}
	req.Header.Set("User-Agent", directUserAgent)
	req.Header.Set("Referer", "https://v.youku.com/")
	req.Header.Set("Accept", "application/json, */*;q=0.8")
	resp, err := p.http.Do(req)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return "", "", fmt.Errorf("弹幕请求失败：%w", err)
	}
	defer httpx.DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("弹幕服务返回 HTTP %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		switch c.Name {
		case "_m_h5_tk":
			tk = strings.TrimSpace(c.Value)
		case "_m_h5_tk_enc":
			tkEnc = strings.TrimSpace(c.Value)
		}
	}
	if tk == "" || tkEnc == "" || strings.ContainsAny(tk+tkEnc, "\r\n;") {
		return "", "", fmt.Errorf("获取优酷弹幕令牌失败")
	}
	return tk, tkEnc, nil
}

func (p *directProvider) youkuDuration(ctx context.Context, vid string) int {
	pageURL := fmt.Sprintf("%s/v_show/id_%s.html", strings.TrimRight(p.endpoints.youkuShow, "/"), url.PathEscape(vid))
	body, err := p.get(ctx, pageURL, nil)
	if err != nil {
		return 0
	}
	m := directYoukuDurationRe.FindSubmatch(body)
	if len(m) != 2 {
		return 0
	}
	sec, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil || math.IsNaN(sec) || math.IsInf(sec, 0) || sec <= 0 || sec > 24*60*60 {
		return 0
	}
	return int(math.Ceil(sec))
}

func (p *directProvider) youkuFetchWindow(ctx context.Context, vid string, mat, mcount int, tk, tkEnc string) ([]Comment, error) {
	nowMS := time.Now().UnixMilli()
	msg := youkuDanmuMsg{
		PID:    0,
		CType:  10004,
		SVer:   "3.1.0",
		CVer:   "v1.0",
		CTime:  nowMS,
		GUID:   "0",
		LID:    "0",
		AID:    "0",
		Mat:    mat,
		MCount: mcount,
		Type:   1,
		VID:    vid,
	}
	rawMsg, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	msgB64 := base64.StdEncoding.EncodeToString(rawMsg)
	msg.Msg = msgB64
	msg.Sign = md5Hex(msgB64 + youkuDanmuSalt)
	dataBytes, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	dataStr := string(dataBytes)
	token, _, _ := strings.Cut(tk, "_")
	tStr := strconv.FormatInt(nowMS, 10)
	sign := md5Hex(token + "&" + tStr + "&" + youkuAppKey + "&" + dataStr)

	q := url.Values{}
	q.Set("jsv", "2.5.6")
	q.Set("appKey", youkuAppKey)
	q.Set("t", tStr)
	q.Set("sign", sign)
	q.Set("api", "mopen.youku.danmu.list")
	q.Set("v", "1.0")
	q.Set("type", "originaljson")
	q.Set("dataType", "jsonp")
	q.Set("timeout", "20000")
	reqURL := fmt.Sprintf("%s/h5/mopen.youku.danmu.list/1.0/?%s", strings.TrimRight(p.endpoints.youkuBarrage, "/"), q.Encode())

	form := url.Values{}
	form.Set("data", dataStr)
	raw, err := p.postForm(ctx, reqURL, form.Encode(), map[string]string{
		"Referer": "https://v.youku.com/",
		"Cookie":  fmt.Sprintf("_m_h5_tk=%s; _m_h5_tk_enc=%s", tk, tkEnc),
	})
	if err != nil {
		if errors.Is(err, errHTTPNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var outer struct {
		Ret  []string `json:"ret"`
		Data struct {
			Result string `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, fmt.Errorf("解析优酷弹幕响应失败")
	}
	if len(outer.Ret) > 0 && !strings.HasPrefix(outer.Ret[0], "SUCCESS::") {
		return nil, fmt.Errorf("优酷弹幕接口拒绝请求")
	}
	if strings.TrimSpace(outer.Data.Result) == "" {
		return nil, nil
	}
	var inner struct {
		Code flexString `json:"code"`
		Data struct {
			Result []struct {
				PlayAt    flexString `json:"playat"`
				Content   string     `json:"content"`
				Propertis string     `json:"propertis"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(outer.Data.Result), &inner); err != nil {
		return nil, fmt.Errorf("解析优酷弹幕数据失败")
	}
	if string(inner.Code) == "-1" || len(inner.Data.Result) == 0 {
		return nil, nil
	}
	out := make([]Comment, 0, len(inner.Data.Result))
	for _, item := range inner.Data.Result {
		ms, err := strconv.ParseFloat(strings.TrimSpace(string(item.PlayAt)), 64)
		if err != nil || math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 || ms > 24*60*60*1000 {
			continue
		}
		text := cleanCommentText(item.Content)
		if text == "" {
			continue
		}
		mode, color := 1, uint32(0xffffff)
		if item.Propertis != "" {
			var prop struct {
				Color flexString `json:"color"`
				Pos   flexString `json:"pos"`
			}
			if json.Unmarshal([]byte(item.Propertis), &prop) == nil {
				if rawColor := strings.TrimSpace(string(prop.Color)); rawColor != "" {
					if v, err := strconv.ParseUint(rawColor, 10, 24); err == nil {
						color = uint32(v)
					}
				}
				switch strings.TrimSpace(string(prop.Pos)) {
				case "1":
					mode = 5
				case "2":
					mode = 4
				}
			}
		}
		out = append(out, Comment{
			Time:  time.Duration(ms * float64(time.Millisecond)),
			Mode:  mode,
			Color: color,
			Text:  text,
		})
	}
	return out, nil
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (p *directProvider) postForm(ctx context.Context, rawURL, formBody string, extraHeaders map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(formBody))
	if err != nil {
		return nil, fmt.Errorf("弹幕请求地址无效")
	}
	req.Header.Set("User-Agent", directUserAgent)
	req.Header.Set("Accept", "application/json, */*;q=0.8")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	response, err := p.http.Do(req)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return nil, fmt.Errorf("弹幕请求失败：%w", err)
	}
	defer httpx.DrainAndClose(response.Body)
	if response.StatusCode == http.StatusNotFound {
		return nil, errHTTPNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("弹幕服务返回 HTTP %d", response.StatusCode)
	}
	data, exceeded, err := httpx.ReadLimit(response.Body, response.ContentLength, maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("读取弹幕响应失败：%w", err)
	}
	if exceeded {
		return nil, fmt.Errorf("弹幕响应超过 16 MB")
	}
	return data, nil
}

func decompressMaybeDeflate(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}))
	if bytes.HasPrefix(trimmed, []byte("<")) {
		return raw, nil
	}
	// comment.bilibili.com sends raw deflate; try raw deflate, zlib, then gzip.
	if r := flate.NewReader(bytes.NewReader(raw)); r != nil {
		defer r.Close()
		if data, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1)); err == nil && len(data) <= maxResponseBytes && bytes.Contains(data[:min(len(data), 128)], []byte("<")) {
			return data, nil
		}
	}
	if r, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
		defer r.Close()
		if data, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1)); err == nil && len(data) <= maxResponseBytes {
			return data, nil
		}
	}
	if r, err := gzip.NewReader(bytes.NewReader(raw)); err == nil {
		defer r.Close()
		if data, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1)); err == nil && len(data) <= maxResponseBytes {
			return data, nil
		}
	}
	return raw, nil
}

func (p *directProvider) fetchSegments(ctx context.Context, urls []string, fetch func(context.Context, string) ([]Comment, error)) ([]Comment, error) {
	return p.fetchSegmentsWithWorkers(ctx, urls, directSegmentWorkers, fetch)
}

func (p *directProvider) fetchSegmentsWithWorkers(ctx context.Context, urls []string, maxWorkers int, fetch func(context.Context, string) ([]Comment, error)) ([]Comment, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	if maxWorkers <= 0 {
		maxWorkers = 1
	}
	results := make([][]Comment, len(urls))
	errs := make([]error, len(urls))
	sem := make(chan struct{}, min(maxWorkers, len(urls)))
	var wg sync.WaitGroup
	for i, segURL := range urls {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, target string) {
			defer wg.Done()
			defer func() { <-sem }()
			segCtx, cancel := context.WithTimeout(ctx, directSegmentTimeout)
			defer cancel()
			results[idx], errs[idx] = fetch(segCtx, target)
		}(i, segURL)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	succeeded := 0
	total := 0
	var firstErr error
	for i := range urls {
		if errs[i] == nil {
			succeeded++
			total += len(results[i])
		} else if firstErr == nil {
			firstErr = errs[i]
		}
	}
	if succeeded == 0 && firstErr != nil {
		return nil, firstErr
	}
	all := make([]Comment, 0, total)
	for i := range urls {
		if errs[i] == nil && len(results[i]) > 0 {
			all = append(all, results[i]...)
			results[i] = nil
		}
	}
	slices.SortStableFunc(all, func(a, b Comment) int {
		return cmp.Compare(a.Time, b.Time)
	})
	return sampleComments(all, maxComments), nil
}

// sampleComments keeps comments evenly distributed across the full episode when
// a hot title exceeds maxComments, instead of leaving the ending empty.
func sampleComments(comments []Comment, limit int) []Comment {
	if limit <= 0 || len(comments) <= limit {
		return comments
	}
	out := make([]Comment, limit)
	n := len(comments)
	for i := range limit {
		out[i] = comments[i*n/limit]
	}
	return out
}

var errHTTPNotFound = errors.New("HTTP 404")

func (p *directProvider) get(ctx context.Context, rawURL string, extraHeaders map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("弹幕请求地址无效")
	}
	req.Header.Set("User-Agent", directUserAgent)
	req.Header.Set("Accept", "application/json, application/xml;q=0.9, */*;q=0.8")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	response, err := p.http.Do(req)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return nil, fmt.Errorf("弹幕请求失败：%w", err)
	}
	defer httpx.DrainAndClose(response.Body)
	if response.StatusCode == http.StatusNotFound {
		return nil, errHTTPNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("弹幕服务返回 HTTP %d", response.StatusCode)
	}
	data, exceeded, err := httpx.ReadLimit(response.Body, response.ContentLength, maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("读取弹幕响应失败：%w", err)
	}
	if exceeded {
		return nil, fmt.Errorf("弹幕响应超过 16 MB")
	}
	return data, nil
}

var (
	directQQPathVID       = regexp.MustCompile(`^/x/(?:cover/[^/]+/|page/)([a-zA-Z0-9]{6,32})\.html$`)
	directIqiyiVID        = regexp.MustCompile(`v_([a-zA-Z0-9]+)`)
	directYoukuPathVID    = regexp.MustCompile(`/id_([a-zA-Z0-9]{6,64}={0,2})(?:\.html)?$`)
	directYoukuSafeVID    = regexp.MustCompile(`^[a-zA-Z0-9]{6,64}={0,2}$`)
	directYoukuDurationRe = regexp.MustCompile(`"duration"\s*:\s*"?([0-9]+(?:\.[0-9]+)?)"?`)
	directMangoIDs        = regexp.MustCompile(`/b/(\d+)/(\d+)`)
	directBiliEP          = regexp.MustCompile(`/ep(\d+)`)
	directBiliBV          = regexp.MustCompile(`/(BV[0-9A-Za-z]+)`)
	directSafeTokenRe     = regexp.MustCompile(`^[a-zA-Z0-9]+$`)
)

func directEpisodeID(site, rawURL string) (string, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	switch site {
	case "qq":
		if vid := strings.TrimSpace(u.Query().Get("vid")); len(vid) >= 6 && len(vid) <= 32 && directSafeTokenRe.MatchString(vid) {
			return "qq:" + vid, true
		}
		if m := directQQPathVID.FindStringSubmatch(u.Path); len(m) == 2 {
			return "qq:" + m[1], true
		}
	case "qiyi":
		if m := directIqiyiVID.FindStringSubmatch(u.Path); len(m) == 2 {
			val, err := strconv.ParseUint(m[1], 36, 64)
			if err == nil {
				tvid := val ^ iqiyiDecodeXOR
				if tvid > 0 {
					return "qiyi:" + strconv.FormatUint(tvid, 10), true
				}
			}
		}
	case "youku":
		if vid := strings.TrimSpace(u.Query().Get("vid")); directYoukuSafeVID.MatchString(vid) {
			return "youku:" + vid, true
		}
		if m := directYoukuPathVID.FindStringSubmatch(u.Path); len(m) == 2 {
			return "youku:" + m[1], true
		}
	case "imgo":
		if m := directMangoIDs.FindStringSubmatch(u.Path); len(m) == 3 {
			return "imgo:" + m[1] + ":" + m[2], true
		}
	case "bilibili1":
		if m := directBiliEP.FindStringSubmatch(u.Path); len(m) == 2 {
			return "bili:ep:" + m[1], true
		}
		if m := directBiliBV.FindStringSubmatch(u.Path); len(m) == 2 {
			return "bili:bv:" + m[1], true
		}
	}
	return "", false
}

func catIDForName(name string) string {
	switch name {
	case "电视剧":
		return "2"
	case "动漫":
		return "4"
	default:
		return ""
	}
}

var chineseSeasonRe = regexp.MustCompile(`第\s*([一二三四五六七八九十两零〇]+)\s*([季部])`)

// NormalizeSeasonTitle converts Chinese season/part numerals such as "第四季"
// into Arabic digits ("第4季") so title comparison matches across sources.
func NormalizeSeasonTitle(s string) string {
	return chineseSeasonRe.ReplaceAllStringFunc(s, func(match string) string {
		parts := chineseSeasonRe.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		n, ok := ParseChineseInt(parts[1])
		if !ok || n <= 0 {
			return match
		}
		return fmt.Sprintf("第%d%s", n, parts[2])
	})
}

// ParseChineseInt parses common Chinese numerals from 1 to 999 (e.g. "四", "十二", "二十三").
func ParseChineseInt(raw string) (int, bool) {
	digits := map[rune]int{
		'零': 0, '〇': 0,
		'一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
		'五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
	}
	total, section, num := 0, 0, 0
	hasAny := false
	for _, r := range raw {
		if d, ok := digits[r]; ok {
			num = d
			hasAny = true
			continue
		}
		switch r {
		case '十':
			if num == 0 {
				num = 1
			}
			section += num * 10
			num = 0
			hasAny = true
		case '百':
			if num == 0 {
				return 0, false
			}
			section += num * 100
			num = 0
			hasAny = true
		default:
			return 0, false
		}
	}
	if !hasAny {
		return 0, false
	}
	total += section + num
	return total, total > 0
}

func normalizeDirectTitle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "《》")
	s = NormalizeSeasonTitle(s)
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}

func parsePlaylinks(raw json.RawMessage) (map[string]string, map[string][]varietyEpisode) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, nil
	}
	var stringLinks map[string]string
	var varietyLinks map[string][]varietyEpisode
	for site, val := range fields {
		val = bytes.TrimSpace(val)
		if len(val) == 0 {
			continue
		}
		switch val[0] {
		case '"':
			var link string
			if json.Unmarshal(val, &link) == nil && strings.TrimSpace(link) != "" {
				if stringLinks == nil {
					stringLinks = make(map[string]string)
				}
				stringLinks[site] = strings.TrimSpace(link)
			}
		case '[':
			var list []varietyEpisode
			if json.Unmarshal(val, &list) == nil && len(list) > 0 {
				if varietyLinks == nil {
					varietyLinks = make(map[string][]varietyEpisode)
				}
				varietyLinks[site] = list
			}
		}
	}
	return stringLinks, varietyLinks
}

func parsePlaylinksYear(raw json.RawMessage) map[string][]string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var rawMap map[string][]flexString
	if json.Unmarshal(raw, &rawMap) != nil {
		return nil
	}
	out := make(map[string][]string, len(rawMap))
	for site, list := range rawMap {
		years := make([]string, 0, len(list))
		for _, y := range list {
			if s := strings.TrimSpace(string(y)); s != "" {
				years = append(years, s)
			}
		}
		if len(years) > 0 {
			out[site] = years
		}
	}
	return out
}

func parsePlaylinkList(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []string
	for _, item := range items {
		item = bytes.TrimSpace(item)
		if len(item) == 0 || item[0] != '{' {
			continue
		}
		var obj struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(item, &obj) == nil && strings.TrimSpace(obj.URL) != "" {
			out = append(out, strings.TrimSpace(obj.URL))
		}
	}
	return out
}

func parseHexColor(raw string, fallback uint32) uint32 {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "#")
	if len(raw) != 6 {
		return fallback
	}
	v, err := strconv.ParseUint(raw, 16, 24)
	if err != nil {
		return fallback
	}
	return uint32(v)
}

func parseMangoColor(raw json.RawMessage) uint32 {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0xffffff
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return parseHexColor(text, 0xffffff)
		}
		return 0xffffff
	}
	var obj struct {
		ColorLeft struct {
			R int `json:"r"`
			G int `json:"g"`
			B int `json:"b"`
		} `json:"color_left"`
	}
	if json.Unmarshal(raw, &obj) == nil && (obj.ColorLeft.R > 0 || obj.ColorLeft.G > 0 || obj.ColorLeft.B > 0) {
		r := uint32(min(max(obj.ColorLeft.R, 0), 255))
		g := uint32(min(max(obj.ColorLeft.G, 0), 255))
		b := uint32(min(max(obj.ColorLeft.B, 0), 255))
		return r<<16 | g<<8 | b
	}
	return 0xffffff
}

func parseClockSeconds(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return secs
	}
	parts := strings.Split(raw, ":")
	total := 0
	for _, part := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || v < 0 {
			return 0
		}
		total = total*60 + v
	}
	return total
}
