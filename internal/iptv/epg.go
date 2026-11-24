package iptv

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/netproxy"
)

const (
	maxEPGBytes     = 32 << 20 // 32 MiB decompressed XMLTV cap
	maxDIYPEPGBytes = 2 << 20
)

// Program is one scheduled broadcast entry in a channel's EPG.
type Program struct {
	Title string
	Desc  string
	Start time.Time
	End   time.Time
}

// LiveAt reports whether the program is airing at t.
func (p Program) LiveAt(t time.Time) bool {
	return !t.Before(p.Start) && t.Before(p.End)
}

// EndedAt reports whether the program finished before or at t.
func (p Program) EndedAt(t time.Time) bool {
	return !p.End.IsZero() && !p.End.After(t)
}

// TimeRange formats the program's local start and end times (e.g. "19:00–19:30").
func (p Program) TimeRange() string {
	return p.Start.Local().Format("15:04") + "–" + p.End.Local().Format("15:04")
}

// EPG holds parsed program guides indexed for fast channel lookup.
type EPG struct {
	byID       map[string][]Program
	aliasToID  map[string]string
	normToID   map[string]string
}

// IsTemplateEPG reports whether rawURL is a per-channel TVBox/DIYP EPG template
// (e.g. "https://epg.example.com/?ch={name}&date={date}").
func IsTemplateEPG(rawURL string) bool {
	return strings.Contains(rawURL, "{name}") || strings.Contains(rawURL, "{ch}") || strings.Contains(rawURL, "{date}")
}

func defaultEPGClient() *http.Client {
	return &http.Client{Transport: netproxy.Transport(), Timeout: 20 * time.Second}
}

// FetchXMLTV downloads and parses an XMLTV guide (plain .xml or gzipped .xml.gz).
func FetchXMLTV(ctx context.Context, client *http.Client, rawURL string, headers map[string]string) (*EPG, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("EPG 地址无效")
	}
	if client == nil {
		client = defaultEPGClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Xiaomu/1.0")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("EPG 服务返回 HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxEPGBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxEPGBytes {
		return nil, fmt.Errorf("EPG 文件超过上限")
	}
	return ParseXMLTV(raw)
}

// ParseXMLTV parses XMLTV bytes (automatically gunzipping .xml.gz payloads).
func ParseXMLTV(data []byte) (*EPG, error) {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("解压 EPG 失败：%w", err)
		}
		defer gr.Close()
		unpacked, err := io.ReadAll(io.LimitReader(gr, maxEPGBytes+1))
		if err != nil {
			return nil, fmt.Errorf("读取压缩 EPG 失败：%w", err)
		}
		if len(unpacked) > maxEPGBytes {
			return nil, fmt.Errorf("解压后 EPG 超过上限")
		}
		data = unpacked
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	epg := &EPG{
		byID:      make(map[string][]Program),
		aliasToID: make(map[string]string),
		normToID:  make(map[string]string),
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("解析 XMLTV 失败：%w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "channel":
			var ch struct {
				ID           string   `xml:"id,attr"`
				DisplayNames []string `xml:"display-name"`
			}
			if err := dec.DecodeElement(&ch, &se); err != nil {
				continue
			}
			id := strings.ToLower(strings.TrimSpace(ch.ID))
			if id == "" {
				continue
			}
			epg.aliasToID[id] = id
			if norm := normalizeEPGKey(id); norm != "" {
				epg.normToID[norm] = id
			}
			for _, name := range ch.DisplayNames {
				low := strings.ToLower(strings.TrimSpace(name))
				if low == "" {
					continue
				}
				if _, exists := epg.aliasToID[low]; !exists {
					epg.aliasToID[low] = id
				}
				if norm := normalizeEPGKey(low); norm != "" {
					if _, exists := epg.normToID[norm]; !exists {
						epg.normToID[norm] = id
					}
				}
			}
		case "programme":
			var pr struct {
				Start   string `xml:"start,attr"`
				Stop    string `xml:"stop,attr"`
				Channel string `xml:"channel,attr"`
				Title   string `xml:"title"`
				Desc    string `xml:"desc"`
			}
			if err := dec.DecodeElement(&pr, &se); err != nil {
				continue
			}
			chID := strings.ToLower(strings.TrimSpace(pr.Channel))
			title := strings.TrimSpace(pr.Title)
			if chID == "" || title == "" {
				continue
			}
			start, ok1 := parseXMLTVTime(pr.Start)
			end, ok2 := parseXMLTVTime(pr.Stop)
			if !ok1 || !ok2 || !end.After(start) {
				continue
			}
			epg.byID[chID] = append(epg.byID[chID], Program{
				Title: title,
				Desc:  strings.TrimSpace(pr.Desc),
				Start: start,
				End:   end,
			})
			if _, exists := epg.aliasToID[chID]; !exists {
				epg.aliasToID[chID] = chID
			}
			if norm := normalizeEPGKey(chID); norm != "" {
				if _, exists := epg.normToID[norm]; !exists {
					epg.normToID[norm] = chID
				}
			}
		}
	}
	for id, progs := range epg.byID {
		slices.SortFunc(progs, func(a, b Program) int {
			return a.Start.Compare(b.Start)
		})
		epg.byID[id] = progs
	}
	return epg, nil
}

func parseXMLTVTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		"20060102150405 -0700",
		"20060102150405 -07:00",
		"20060102150405-0700",
		"200601021504 -0700",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	for _, layout := range []string{
		"20060102150405",
		"200601021504",
	} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// FetchDIYPChannel queries a TVBox/DIYP per-channel JSON EPG endpoint.
func FetchDIYPChannel(ctx context.Context, client *http.Client, templateURL string, ch Channel, day time.Time, headers map[string]string) ([]Program, error) {
	name := strings.TrimSpace(ch.TVGName)
	if name == "" {
		name = strings.TrimSpace(ch.Name)
	}
	if name == "" {
		return nil, nil
	}
	dateStr := day.Local().Format("2006-01-02")
	expanded := strings.NewReplacer(
		"{name}", url.QueryEscape(name),
		"{ch}", url.QueryEscape(name),
		"{date}", url.QueryEscape(dateStr),
	).Replace(strings.TrimSpace(templateURL))
	u, err := url.Parse(expanded)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("EPG 模板地址无效")
	}
	if client == nil {
		client = defaultEPGClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Xiaomu/1.0")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("EPG 接口返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDIYPEPGBytes))
	if err != nil {
		return nil, err
	}
	return ParseDIYPJSON(body, day)
}

// ParseDIYPJSON parses a TVBox / DIYP JSON EPG payload for one day.
func ParseDIYPJSON(data []byte, fallbackDay time.Time) ([]Program, error) {
	var parsed struct {
		Date    string `json:"date"`
		EPGData []struct {
			Start string `json:"start"`
			End   string `json:"end"`
			Title string `json:"title"`
			Desc  string `json:"desc"`
		} `json:"epg_data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("解析 DIYP EPG 失败：%w", err)
	}
	baseDay := fallbackDay.Local()
	if d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(parsed.Date), time.Local); err == nil {
		baseDay = d
	}
	year, month, day := baseDay.Date()
	out := make([]Program, 0, len(parsed.EPGData))
	for _, item := range parsed.EPGData {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			continue
		}
		sh, sm, ok1 := parseClockHM(item.Start)
		eh, em, ok2 := parseClockHM(item.End)
		if !ok1 || !ok2 {
			continue
		}
		start := time.Date(year, month, day, sh, sm, 0, 0, time.Local)
		end := time.Date(year, month, day, eh, em, 0, 0, time.Local)
		if !end.After(start) {
			end = end.Add(24 * time.Hour)
		}
		out = append(out, Program{
			Title: title,
			Desc:  strings.TrimSpace(item.Desc),
			Start: start,
			End:   end,
		})
	}
	slices.SortFunc(out, func(a, b Program) int {
		return a.Start.Compare(b.Start)
	})
	return out, nil
}

func parseClockHM(raw string) (int, int, bool) {
	hStr, mStr, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(hStr))
	m, err2 := strconv.Atoi(strings.TrimSpace(mStr))
	if err1 != nil || err2 != nil || h < 0 || h > 24 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// lookup finds the program list for ch using TVGID, TVGName, and Name (exact then normalized).
func (e *EPG) lookup(ch Channel) []Program {
	if e == nil {
		return nil
	}
	candidates := []string{ch.TVGID, ch.TVGName, ch.Name}
	for _, raw := range candidates {
		low := strings.ToLower(strings.TrimSpace(raw))
		if low == "" {
			continue
		}
		if id, ok := e.aliasToID[low]; ok {
			if progs := e.byID[id]; len(progs) > 0 {
				return progs
			}
		}
	}
	for _, raw := range candidates {
		norm := normalizeEPGKey(raw)
		if norm == "" {
			continue
		}
		if id, ok := e.normToID[norm]; ok {
			if progs := e.byID[id]; len(progs) > 0 {
				return progs
			}
		}
	}
	return nil
}

// CurrentAndNext returns the currently airing program and the next upcoming
// program for ch at now.
func (e *EPG) CurrentAndNext(ch Channel, now time.Time) (current, next *Program) {
	return CurrentAndNextPrograms(e.lookup(ch), now)
}

// CurrentAndNextPrograms finds the live and upcoming program in a sorted program list.
func CurrentAndNextPrograms(progs []Program, now time.Time) (current, next *Program) {
	for i := range progs {
		p := progs[i]
		if p.LiveAt(now) {
			cp := p
			current = &cp
			if i+1 < len(progs) {
				np := progs[i+1]
				next = &np
			}
			return current, next
		}
		if p.Start.After(now) {
			np := p
			return nil, &np
		}
	}
	return nil, nil
}

// ProgramsForDay returns all programs for ch overlapping the local calendar day of day.
func (e *EPG) ProgramsForDay(ch Channel, day time.Time) []Program {
	progs := e.lookup(ch)
	if len(progs) == 0 {
		return nil
	}
	y, m, d := day.Local().Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	dayEnd := dayStart.Add(24 * time.Hour)
	var out []Program
	for _, p := range progs {
		if p.End.After(dayStart) && p.Start.Before(dayEnd) {
			out = append(out, p)
		}
	}
	return out
}

var epgNoiseSuffixes = []string{
	"4k", "uhd", "fhd", "hd", "超清", "高清", "蓝光", "标清", "综合",
}

func normalizeEPGKey(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '-', '_', '.', '·', '[', ']', '(', ')', '（', '）':
			continue
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	for {
		trimmed := out
		for _, suf := range epgNoiseSuffixes {
			if len(trimmed) > len(suf) && strings.HasSuffix(trimmed, suf) {
				trimmed = strings.TrimSuffix(trimmed, suf)
				break
			}
		}
		if trimmed == out {
			break
		}
		out = trimmed
	}
	return out
}

// --- Catchup / Replay URL Construction ---

var catchupTokenPattern = regexp.MustCompile(`\$\{\(([be])\)([^}]+)\}`)

// SupportsCatchup reports whether ch carries catchup/replay metadata.
func SupportsCatchup(ch Channel) bool {
	if len(ch.Streams) == 0 {
		return false
	}
	if strings.TrimSpace(ch.CatchupSource) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(ch.Catchup)) {
	case "shift", "flussonic":
		return true
	default:
		return false
	}
}

// CatchupURL builds a replay stream URL for a past program on ch's streamIndex.
// Returns ("", false) if the channel does not advertise catchup support.
func CatchupURL(ch Channel, streamIndex int, prog Program, now time.Time) (string, bool) {
	if streamIndex < 0 || streamIndex >= len(ch.Streams) || !prog.End.After(prog.Start) {
		return "", false
	}
	if !SupportsCatchup(ch) {
		return "", false
	}
	if ch.CatchupDays > 0 && now.Sub(prog.Start) > time.Duration(ch.CatchupDays)*24*time.Hour {
		return "", false
	}
	baseStream := strings.TrimSpace(ch.Streams[streamIndex].URL)
	if baseStream == "" {
		return "", false
	}
	mode := strings.ToLower(strings.TrimSpace(ch.Catchup))
	tpl := strings.TrimSpace(ch.CatchupSource)
	if tpl == "" {
		switch mode {
		case "shift":
			tpl = "?utc={utc}&lutc={lutc}"
		case "flussonic":
			tpl = "?utc={utc}&duration={duration}"
		default:
			return "", false
		}
	}
	expanded := expandCatchupTemplate(tpl, prog, now)
	switch {
	case strings.HasPrefix(expanded, "http://") || strings.HasPrefix(expanded, "https://") ||
		strings.HasPrefix(expanded, "rtsp://") || strings.HasPrefix(expanded, "rtmp://"):
		return expanded, true
	case strings.HasPrefix(expanded, "?") || strings.HasPrefix(expanded, "&"):
		sep := "?"
		if strings.Contains(baseStream, "?") {
			sep = "&"
		}
		return baseStream + sep + strings.TrimLeft(expanded, "?&"), true
	case mode == "append" || mode == "shift" || mode == "flussonic":
		if strings.Contains(baseStream, "?") && !strings.HasPrefix(expanded, "/") {
			return baseStream + "&" + expanded, true
		}
		return baseStream + expanded, true
	default:
		return resolve(baseStream, expanded), true
	}
}

func expandCatchupTemplate(tpl string, prog Program, now time.Time) string {
	out := catchupTokenPattern.ReplaceAllStringFunc(tpl, func(m string) string {
		sub := catchupTokenPattern.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		t := prog.Start.Local()
		if sub[1] == "e" {
			t = prog.End.Local()
		}
		spec := sub[2]
		if tzPart, tz, hasTZ := strings.Cut(spec, "|"); hasTZ {
			spec = tzPart
			if strings.EqualFold(strings.TrimSpace(tz), "UTC") {
				t = t.UTC()
			}
		}
		if strings.EqualFold(spec, "timestamp") {
			return strconv.FormatInt(t.Unix(), 10)
		}
		return formatJavaDate(t, spec)
	})
	dur := int64(prog.End.Sub(prog.Start).Seconds())
	if dur < 0 {
		dur = 0
	}
	return strings.NewReplacer(
		"{utc}", strconv.FormatInt(prog.Start.Unix(), 10),
		"{start}", strconv.FormatInt(prog.Start.Unix(), 10),
		"{utcend}", strconv.FormatInt(prog.End.Unix(), 10),
		"{end}", strconv.FormatInt(prog.End.Unix(), 10),
		"{lutc}", strconv.FormatInt(now.Unix(), 10),
		"{duration}", strconv.FormatInt(dur, 10),
	).Replace(out)
}

func formatJavaDate(t time.Time, spec string) string {
	goLayout := strings.NewReplacer(
		"yyyy", "2006",
		"MM", "01",
		"dd", "02",
		"HH", "15",
		"mm", "04",
		"ss", "05",
	).Replace(spec)
	return t.Format(goLayout)
}
