package pan

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
)

const shareListPageSize = 40

type shareListEntry struct {
	Category string
	Name     string
	Pic      string
	Remarks  string
	Refs     []ShareRef
}

// ShareListSession natively executes plaintext cloud-drive share list sites
// (such as csp_QuarkShare, csp_UCShare, csp_AliShare, csp_PushShare) without Android.
type ShareListSession struct {
	categories []catalog.Category
	entries    []shareListEntry
	byCat      map[string][]int
	byID       map[string]int
}

// ShareListURL extracts the plaintext list URL from a $$$-delimited or direct ext.
func ShareListURL(ext string) string {
	ext = strings.TrimSpace(ext)
	if ext == "" {
		return ""
	}
	parts := strings.Split(ext, "$$$")
	// Prefer a non-token list file (.txt or .json) in later segments first.
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		if !isHTTPOrFile(p) {
			continue
		}
		lower := strings.ToLower(p)
		if strings.Contains(lower, "token.json") || strings.Contains(lower, "token.txt") {
			continue
		}
		if strings.HasSuffix(stripQueryAndFrag(lower), ".txt") || strings.HasSuffix(stripQueryAndFrag(lower), ".json") {
			return p
		}
	}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if isHTTPOrFile(p) && !strings.Contains(strings.ToLower(p), "token") {
			return p
		}
	}
	return ""
}

func isHTTPOrFile(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "file://")
}

func stripQueryAndFrag(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		return s[:i]
	}
	return s
}

// OpenShareList loads a plaintext share list site into memory.
func OpenShareList(ctx context.Context, site config.Site) (*ShareListSession, []catalog.Category, error) {
	listURL := ShareListURL(site.Ext)
	if listURL == "" {
		return nil, nil, fmt.Errorf("分享清单站点缺少有效的明文清单地址")
	}
	res, err := config.HTTPFetcher(nil)(ctx, listURL)
	if err != nil {
		return nil, nil, fmt.Errorf("读取分享清单失败：%w", err)
	}
	if res.Status < 200 || res.Status >= 300 {
		return nil, nil, fmt.Errorf("读取分享清单失败：HTTP %d", res.Status)
	}
	body, err := config.Decode(res.Body)
	if err != nil {
		body = res.Body
	}
	defaultProvider := providerFromSiteAPI(site.API)
	entries := parseShareListBody(string(body), defaultProvider)
	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("分享清单中没有解析出有效的网盘分享条目")
	}
	s := &ShareListSession{
		entries: entries,
		byCat:   map[string][]int{},
		byID:    make(map[string]int, len(entries)),
	}
	catOrder := []string{}
	seenCat := map[string]bool{}
	for i, e := range entries {
		id := fmt.Sprintf("share-%d", i)
		s.byID[id] = i
		cat := e.Category
		if cat == "" {
			cat = "全部"
		}
		if !seenCat[cat] {
			seenCat[cat] = true
			catOrder = append(catOrder, cat)
		}
		s.byCat[cat] = append(s.byCat[cat], i)
	}
	for _, cat := range catOrder {
		s.categories = append(s.categories, catalog.Category{ID: catalog.ID(cat), Name: cat})
	}
	return s, s.categories, nil
}

func providerFromSiteAPI(api string) Provider {
	lower := strings.ToLower(strings.TrimSpace(api))
	switch {
	case strings.Contains(lower, "quark"):
		return ProviderQuark
	case strings.Contains(lower, "ucshare") || strings.Contains(lower, "_uc"):
		return ProviderUC
	case strings.Contains(lower, "alishare") || strings.Contains(lower, "_ali"):
		return ProviderAli
	case strings.Contains(lower, "115"):
		return Provider115
	case strings.Contains(lower, "baidu"):
		return ProviderBaidu
	case strings.Contains(lower, "thunder"):
		return ProviderThunder
	default:
		return ""
	}
}

func shareRefFromBareID(token string, provider Provider) (ShareRef, bool) {
	if provider == "" {
		return ShareRef{}, false
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.EqualFold(token, "self") || strings.Contains(token, "://") {
		return ShareRef{}, false
	}
	idPart, queryPart, _ := strings.Cut(token, "?")
	idPart = strings.TrimSpace(idPart)
	if len(idPart) < 6 || len(idPart) > 64 {
		return ShareRef{}, false
	}
	for i := range len(idPart) {
		ch := idPart[i]
		ok := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-'
		if !ok {
			return ShareRef{}, false
		}
	}
	var base string
	switch provider {
	case ProviderQuark:
		base = "https://pan.quark.cn/s/" + idPart
	case ProviderUC:
		base = "https://drive.uc.cn/s/" + idPart
	case ProviderAli:
		base = "https://www.alipan.com/s/" + idPart
	case Provider115:
		base = "https://115.com/s/" + idPart
	case ProviderBaidu:
		base = "https://pan.baidu.com/s/" + idPart
	case ProviderThunder:
		base = "https://pan.xunlei.com/s/" + idPart
	default:
		return ShareRef{}, false
	}
	if queryPart != "" {
		base += "?" + queryPart
	}
	return ParseShareURL(base)
}

func parseShareListBody(raw string, defaultProvider Provider) []shareListEntry {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		if entries := parseShareListJSON(trimmed); len(entries) > 0 {
			return entries
		}
	}
	return parseShareListText(trimmed, defaultProvider)
}

func parseShareListJSON(raw string) []shareListEntry {
	clean := config.Strip([]byte(raw))
	var entries []shareListEntry
	// Format A: array of objects {name/title, url/link, type/category, pic}
	var arr []struct {
		Category string `json:"category"`
		Type     string `json:"type"`
		TypeName string `json:"type_name"`
		Name     string `json:"name"`
		Title    string `json:"title"`
		VodName  string `json:"vod_name"`
		URL      string `json:"url"`
		Link     string `json:"link"`
		Pic      string `json:"pic"`
		VodPic   string `json:"vod_pic"`
		Remarks  string `json:"remarks"`
	}
	if json.Unmarshal(clean, &arr) == nil && len(arr) > 0 {
		for _, item := range arr {
			refs := ExtractShareURLs(item.URL + " " + item.Link)
			if len(refs) == 0 {
				continue
			}
			name := firstNonEmpty(item.Name, item.Title, item.VodName)
			if name == "" {
				name = refs[0].Provider.Label() + "资源"
			}
			cat := firstNonEmpty(item.Category, item.TypeName, item.Type, "分享资源")
			entries = append(entries, shareListEntry{
				Category: cat,
				Name:     name,
				Pic:      firstNonEmpty(item.Pic, item.VodPic),
				Remarks:  firstNonEmpty(item.Remarks, refs[0].Provider.Label()),
				Refs:     refs,
			})
		}
		return entries
	}
	// Format B: map of category -> map/array of items
	var obj map[string]json.RawMessage
	if json.Unmarshal(clean, &obj) == nil {
		for cat, val := range obj {
			if sub := parseShareListJSON(string(val)); len(sub) > 0 {
				for i := range sub {
					if sub[i].Category == "" || sub[i].Category == "分享资源" {
						sub[i].Category = cat
					}
				}
				entries = append(entries, sub...)
				continue
			}
			var strMap map[string]string
			if json.Unmarshal(val, &strMap) == nil {
				for name, link := range strMap {
					refs := ExtractShareURLs(link)
					if len(refs) == 0 {
						continue
					}
					entries = append(entries, shareListEntry{
						Category: cat,
						Name:     strings.TrimSpace(name),
						Remarks:  refs[0].Provider.Label(),
						Refs:     refs,
					})
				}
			}
		}
	}
	return entries
}

func parseShareListText(raw string, defaultProvider Provider) []shareListEntry {
	var entries []shareListEntry
	byTitle := map[string]int{}
	currentCat := "分享资源"
	if defaultProvider != "" {
		currentCat = defaultProvider.Label()
	}

	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		// Category header syntax: "分类名,#genre#"
		if strings.Contains(line, ",#genre#") {
			if name, _, ok := strings.Cut(line, ","); ok && strings.TrimSpace(name) != "" {
				currentCat = strings.TrimSpace(name)
			}
			continue
		}
		refs := ExtractShareURLs(line)
		cat, name := currentCat, ""
		if len(refs) == 0 {
			// Support bare share-ID lines used by quarkshare.txt / ucshare.txt / alishare.txt:
			//   <shareID> Title
			//   <shareID>?pwd=xxxx Title ...
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if ref, ok := shareRefFromBareID(fields[0], defaultProvider); ok {
					refs = []ShareRef{ref}
					rest := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
					// Optional "Category|Title" notation in alishare lists.
					if cPart, tPart, hasPipe := strings.Cut(rest, "|"); hasPipe && strings.TrimSpace(cPart) != "" && strings.TrimSpace(tPart) != "" {
						cat = strings.TrimSpace(cPart)
						rest = strings.TrimSpace(tPart)
					}
					// Trim trailing numeric/sort metadata tokens like " 1" or " DESC".
					name = trimShareListMeta(rest)
				}
			}
			if len(refs) == 0 {
				continue
			}
		} else {
			// Skip placeholder "self" links like https://pan.quark.cn/s/self.
			filtered := refs[:0]
			for _, r := range refs {
				if !strings.EqualFold(r.ShareID, "self") {
					filtered = append(filtered, r)
				}
			}
			refs = filtered
			if len(refs) == 0 {
				continue
			}
			if strings.Contains(line, "$") {
				left, _, _ := strings.Cut(line, "$")
				name = strings.TrimSpace(left)
			} else if parts := strings.Split(line, ","); len(parts) >= 2 && !strings.Contains(parts[0], "://") {
				if len(parts) >= 3 && !strings.Contains(parts[1], "://") {
					cat = strings.TrimSpace(parts[0])
					name = strings.TrimSpace(parts[1])
				} else {
					name = strings.TrimSpace(parts[0])
				}
			} else {
				// Take prefix before the first URL, or trailing title after the URL.
				if idx := strings.Index(line, "http"); idx > 0 {
					name = strings.TrimSpace(strings.TrimRight(line[:idx], ",:：|-"))
				} else if fields := strings.Fields(line); len(fields) >= 2 {
					name = strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
				}
			}
		}
		if name == "" {
			name = refs[0].Provider.Label() + " · " + refs[0].ShareID
		}
		key := cat + "\x00" + strings.ToLower(name)
		if idx, exists := byTitle[key]; exists {
			entries[idx].Refs = mergeShareRefs(entries[idx].Refs, refs)
			continue
		}
		byTitle[key] = len(entries)
		entries = append(entries, shareListEntry{
			Category: cat,
			Name:     name,
			Remarks:  refs[0].Provider.Label(),
			Refs:     refs,
		})
	}
	return entries
}

func trimShareListMeta(s string) string {
	fields := strings.Fields(s)
	for len(fields) > 1 {
		last := strings.ToUpper(fields[len(fields)-1])
		if last == "1" || last == "0" || last == "DESC" || last == "ASC" {
			fields = fields[:len(fields)-1]
			continue
		}
		break
	}
	return strings.Join(fields, " ")
}

func mergeShareRefs(dst, src []ShareRef) []ShareRef {
	seen := make(map[string]bool, len(dst))
	for _, r := range dst {
		seen[r.Key()] = true
	}
	for _, r := range src {
		if !seen[r.Key()] {
			seen[r.Key()] = true
			dst = append(dst, r)
		}
	}
	return dst
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func (s *ShareListSession) videoOf(idx int) catalog.Video {
	e := s.entries[idx]
	return catalog.Video{
		ID:      catalog.ID(fmt.Sprintf("share-%d", idx)),
		Name:    e.Name,
		Pic:     e.Pic,
		Remarks: e.Remarks,
		Type:    e.Category,
	}
}

func (s *ShareListSession) pageFromIndices(indices []int, page int) catalog.Page {
	if page < 1 {
		page = 1
	}
	start := (page - 1) * shareListPageSize
	if start >= len(indices) {
		return catalog.Page{}
	}
	end := min(start+shareListPageSize, len(indices))
	videos := make([]catalog.Video, 0, end-start)
	for _, idx := range indices[start:end] {
		videos = append(videos, s.videoOf(idx))
	}
	return catalog.Page{Videos: videos, HasNext: end < len(indices)}
}

func (s *ShareListSession) Home(context.Context) (catalog.Page, error) {
	all := make([]int, min(shareListPageSize, len(s.entries)))
	for i := range all {
		all[i] = i
	}
	return s.pageFromIndices(all, 1), nil
}

func (s *ShareListSession) Category(_ context.Context, id string, page int) (catalog.Page, error) {
	return s.pageFromIndices(s.byCat[id], page), nil
}

func (s *ShareListSession) Search(_ context.Context, query string, page int) (catalog.Page, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return catalog.Page{}, nil
	}
	var matched []int
	for i, e := range s.entries {
		if strings.Contains(strings.ToLower(e.Name), q) {
			matched = append(matched, i)
		}
	}
	return s.pageFromIndices(matched, page), nil
}

func (s *ShareListSession) Detail(ctx context.Context, id string) (catalog.Detail, error) {
	idx, ok := s.byID[id]
	if !ok || idx < 0 || idx >= len(s.entries) {
		return catalog.Detail{}, fmt.Errorf("未找到对应的分享条目")
	}
	e := s.entries[idx]
	sources, err := ExpandShares(ctx, e.Refs)
	if err != nil {
		return catalog.Detail{}, err
	}
	return catalog.Detail{
		Name:    e.Name,
		Pic:     e.Pic,
		Type:    e.Category,
		Sources: sources,
	}, nil
}

func (s *ShareListSession) Play(ctx context.Context, _ string, ep catalog.Episode) (catalog.Playback, error) {
	return ResolveEpisode(ctx, ep.ID)
}

func (s *ShareListSession) Close() {}
