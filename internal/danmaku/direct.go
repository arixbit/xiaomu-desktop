package danmaku

import (
	"bytes"
	"cmp"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
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
	directMaxVarietyRows   = 2
	directSegmentWorkers   = 8
	directSegmentTimeout   = 15 * time.Second
	iqiyiDecodeXOR         = uint64(0x75706971676c)
	iqiyiSegmentSeconds    = 300
	iqiyiMaxSegments       = 60
	mangoSegmentSeconds    = 60
	mangoMaxSegments       = 240
	detailEpisodeBatchSize = 200
)

var directPlatformOrder = []string{"qq", "qiyi", "imgo", "bilibili1"}

type directEndpoints struct {
	so360       string
	web360      string
	qqBarrage   string
	iqiyiInfo   string
	iqiyiBullet string
	mgtvInfo    string
	mgtvBarrage string
	mgtvCDNBase string // optional test override; empty uses https://{cdnHost}
	biliAPI     string
	biliComment string
}

func defaultDirectEndpoints() directEndpoints {
	return directEndpoints{
		so360:       "https://api.so.360kan.com",
		web360:      "https://api.web.360kan.com",
		qqBarrage:   "https://dm.video.qq.com",
		iqiyiInfo:   "https://pcw-api.iqiyi.com",
		iqiyiBullet: "https://cmts.iqiyi.com",
		mgtvInfo:    "https://pcweb.api.mgtv.com",
		mgtvBarrage: "https://galaxy.bz.mgtv.com",
		biliAPI:     "https://api.bilibili.com",
		biliComment: "https://comment.bilibili.com",
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
		row       so360Row
		playlinks map[string]string
		sites     []string
		exact     bool
	}
	queryNorm := normalizeDirectTitle(title)
	var candidates []candidateRow
	for _, row := range parsed.Rows {
		if strings.TrimSpace(row.TitleTxt) == "" {
			continue
		}
		links := parseStringMap(row.Playlinks)
		var sites []string
		for _, site := range directPlatformOrder {
			if links[site] != "" || (row.SeriesSite == site && len(parsePlaylinkList(row.SeriesPlaylinks)) > 0) {
				sites = append(sites, site)
			}
		}
		if len(sites) == 0 {
			continue
		}
		candidates = append(candidates, candidateRow{
			row:       row,
			playlinks: links,
			sites:     sites,
			exact:     normalizeDirectTitle(row.TitleTxt) == queryNorm,
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

	var episodes []Episode
	varietyFetched := 0
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
			if varietyFetched >= directMaxVarietyRows || row.EnID == "" {
				continue
			}
			varietyFetched++
			for _, site := range c.sites {
				items := p.fetchVarietyEpisodes(ctx, row.EnID, site, string(row.Year))
				animeID := row.EnID + ":" + site
				animeLabel := formatDirectAnime(row.TitleTxt, string(row.Year), row.CatName, site)
				label := platformLabel(site)
				for _, item := range items {
					id, ok := directEpisodeID(site, item.URL)
					if !ok {
						continue
					}
					name := strings.TrimSpace(item.Name + " " + item.Period)
					if name == "" {
						name = "正片"
					}
					episodes = append(episodes, Episode{
						ID:      id,
						AnimeID: animeID,
						Anime:   animeLabel,
						Name:    fmt.Sprintf("【%s】 %s", label, name),
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
	Name   string `json:"name"`
	Period string `json:"period"`
	URL    string `json:"url"`
}

func (p *directProvider) fetchVarietyEpisodes(ctx context.Context, entID, site, year string) []varietyEpisode {
	zongyiURL := fmt.Sprintf("%s/episodeszongyi?entid=%s&site=%s&y=%s&count=50&offset=0",
		strings.TrimRight(p.endpoints.so360, "/"),
		url.QueryEscape(entID), url.QueryEscape(site), url.QueryEscape(year))
	body, err := p.get(ctx, zongyiURL, nil)
	if err != nil {
		return nil
	}
	var resp struct {
		Data struct {
			List []varietyEpisode `json:"list"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return nil
	}
	return resp.Data.List
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
	if len(urls) == 0 {
		return nil, nil
	}
	results := make([][]Comment, len(urls))
	errs := make([]error, len(urls))
	sem := make(chan struct{}, min(directSegmentWorkers, len(urls)))
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
	directQQPathVID   = regexp.MustCompile(`^/x/(?:cover/[^/]+/|page/)([a-zA-Z0-9]{6,32})\.html$`)
	directIqiyiVID    = regexp.MustCompile(`v_([a-zA-Z0-9]+)`)
	directMangoIDs    = regexp.MustCompile(`/b/(\d+)/(\d+)`)
	directBiliEP      = regexp.MustCompile(`/ep(\d+)`)
	directBiliBV      = regexp.MustCompile(`/(BV[0-9A-Za-z]+)`)
	directSafeTokenRe = regexp.MustCompile(`^[a-zA-Z0-9]+$`)
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

func normalizeDirectTitle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "《》")
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}

func parseStringMap(raw json.RawMessage) map[string]string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
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
