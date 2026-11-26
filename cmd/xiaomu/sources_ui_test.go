package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/config"
	"github.com/arixbit/xiaomu-desktop/internal/iptv"
	"github.com/arixbit/xiaomu-desktop/internal/library"
	"github.com/arixbit/xiaomu-desktop/internal/pan"
	"github.com/egoist/mygo/ui"
)

func TestSourcesS2TopTabsAndEmbeddedPanSection(t *testing.T) {
	doc := &config.Config{
		URL: "https://example.com/vod/config.json",
		Sites: []config.Site{
			{Key: "pan1", Name: "星河影库", Type: 3, API: "csp_Wogg", Ext: "https://example.com/pan1", Kind: config.KindPanWeb},
			{Key: "pan2", Name: "极光云盘", Type: 3, API: "csp_Alist", Ext: "https://example.com/alist", Kind: config.KindAList},
			{Key: "cms1", Name: "青空动漫", Type: 1, API: "https://example.com/cms1", Kind: config.KindJSON},
			{Key: "cms2", Name: "白昼剧场", Type: 1, API: "https://example.com/cms2", Kind: config.KindJSON},
			{Key: "dead1", Name: "故障站点", Type: 1, API: "https://example.com/dead", Kind: config.KindJSON},
			{Key: "jar1", Name: "安卓插件站", Type: 3, API: "csp_Android", Kind: config.KindCSP},
		},
	}

	a := &app{nav: []routeKind{routeSources}}
	defer a.close()
	a.browser.configURL = doc.URL
	a.library.root = doc
	a.library.documents = []libraryDocument{{doc: doc, line: "主仓精选", open: true}}
	a.markSiteFailed(library.Entry{Site: doc.Sites[4], Config: doc, Line: "主仓精选"}.Key())
	_ = a.latency.record(library.Entry{Site: doc.Sites[0], Config: doc, Line: "主仓精选"}.Key(), 142*time.Millisecond)
	_ = a.latency.record(library.Entry{Site: doc.Sites[2], Config: doc, Line: "主仓精选"}.Key(), 640*time.Millisecond)
	_ = a.latency.record(library.Entry{Site: doc.Sites[3], Config: doc, Line: "主仓精选"}.Key(), 1800*time.Millisecond)
	a.panUI.creds = pan.Credentials{QuarkCookie: "k=v"}

	a.live.url = "https://example.com/iptv/channels.m3u"
	a.live.selectedSource = 0
	a.live.sources = []iptv.Source{
		{
			Name: "高清主源",
			URL:  "https://example.com/iptv/main.m3u",
			EPG:  "https://example.com/iptv/epg.xml",
			Channels: []iptv.Channel{
				{Name: "频道一", Streams: []iptv.Stream{{URL: "https://example.com/1.m3u8"}}},
				{Name: "频道二", Streams: []iptv.Stream{{URL: "https://example.com/2.m3u8"}}},
			},
		},
		{
			Name: "卫视精选",
			URL:  "https://example.com/iptv/sat.m3u",
			Channels: []iptv.Channel{
				{Name: "频道三", Streams: []iptv.Stream{{URL: "https://example.com/3.m3u8"}}},
			},
		},
	}
	a.live.channels = a.live.sources[0].Channels

	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()

	// Tab 0 (影视点播) active by default with counts on both top pillar tabs.
	if a.sourcesTab != 0 {
		t.Fatalf("default sourcesTab = %d, want 0", a.sourcesTab)
	}
	for _, want := range []string{
		"影视点播（4 站）",
		"直播源（3 频道）",
		"重新测速",
		"更换片源",
		"当前影视配置",
		"网盘原画授权",
		"夸克网盘",
		"阿里云盘",
		"UC网盘",
		"百度网盘",
		"清理临时文件",
		"可用站点",
		"全部 (4)",
		"网盘站 (2)",
		"常规站 (2)",
		"星河影库",
		"142ms",
		"640ms",
		"1.8s",
	} {
		if !tt.HasText(want) {
			t.Fatalf("VOD tab missing %q: %v", want, tt.Texts())
		}
	}
	// Embedded pan section must not duplicate the old "已识别的网盘站点" list,
	// and Live tab cards must stay hidden while on Tab 0.
	for _, forbidden := range []string{"已识别的网盘站点", "当前直播源配置", "已识别的直播来源"} {
		if tt.HasText(forbidden) {
			t.Fatalf("VOD tab unexpectedly rendered %q: %v", forbidden, tt.Texts())
		}
	}

	// Switch to Tab 1 (直播源).
	if err := tt.Click("直播源"); err != nil {
		t.Fatalf("click 直播源 tab: %v (texts=%v)", err, tt.Texts())
	}
	if a.sourcesTab != 1 {
		t.Fatalf("sourcesTab = %d, want 1", a.sourcesTab)
	}
	for _, want := range []string{
		"刷新直播",
		"更换直播源",
		"当前直播源配置",
		"3 频道",
		"https://example.com/iptv/channels.m3u",
		"已识别的直播来源（2 个）",
		"高清主源",
		"当前源",
		"含 EPG 节目单",
		"卫视精选",
		"前往直播 ›",
	} {
		if !tt.HasText(want) {
			t.Fatalf("Live tab missing %q: %v", want, tt.Texts())
		}
	}
	for _, forbidden := range []string{"当前影视配置", "可用站点", "更换片源", "重新测速"} {
		if tt.HasText(forbidden) {
			t.Fatalf("Live tab unexpectedly rendered VOD element %q: %v", forbidden, tt.Texts())
		}
	}

	// Switch back to Tab 0 (影视点播).
	if err := tt.Click("影视点播"); err != nil {
		t.Fatalf("click 影视点播 tab: %v (texts=%v)", err, tt.Texts())
	}
	if a.sourcesTab != 0 || !tt.HasText("当前影视配置") || !tt.HasText("可用站点") {
		t.Fatalf("did not switch back to VOD tab: tab=%d texts=%v", a.sourcesTab, tt.Texts())
	}
}

func TestSourcesS2SiteFilterTabsAndOpenSite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ac") == "list" {
			fmt.Fprint(w, `{"class":[{"type_id":1,"type_name":"电影"}]}`)
			return
		}
		fmt.Fprint(w, `{"pagecount":1,"list":[{"vod_id":"1","vod_name":"演示影片"}]}`)
	}))
	defer srv.Close()

	line1 := &config.Config{
		URL: srv.URL + "/line1.json",
		Sites: []config.Site{
			{Key: "pan1", Name: "极速网盘", Type: 3, API: "csp_Wogg", Ext: "https://example.com/wogg", Kind: config.KindPanWeb},
			{Key: "cms1", Name: "极速影视", Type: 1, API: srv.URL + "/cms1", Kind: config.KindJSON},
		},
	}
	line2 := &config.Config{
		URL: srv.URL + "/line2.json",
		Sites: []config.Site{
			{Key: "cms2", Name: "备用常规站", Type: 1, API: srv.URL + "/cms2", Kind: config.KindJSON},
			{Key: "pan2", Name: "折叠网盘站", Type: 3, API: "csp_Alist", Ext: "https://example.com/alist", Kind: config.KindAList},
		},
	}

	a := &app{nav: []routeKind{routeSources}}
	defer a.close()
	a.browser.configURL = line1.URL
	a.library.root = line1
	a.library.documents = []libraryDocument{
		{doc: line1, line: "主线路", open: true},
		{doc: line2, line: "备用线路", open: false},
	}

	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()

	if !tt.HasText("极速网盘") || !tt.HasText("极速影视") {
		t.Fatalf("all filter missing open line sites: %v", tt.Texts())
	}

	// Filter to pan sites only: reveals pan sites across both lines (even collapsed line2)
	// while hiding regular sites.
	if err := tt.Click("网盘站"); err != nil {
		t.Fatalf("click 网盘站: %v (texts=%v)", err, tt.Texts())
	}
	if a.sourcesSiteFilter != 1 {
		t.Fatalf("sourcesSiteFilter = %d, want 1", a.sourcesSiteFilter)
	}
	if !tt.HasText("极速网盘") || !tt.HasText("折叠网盘站") || tt.HasText("极速影视") || tt.HasText("备用常规站") {
		t.Fatalf("pan filter mismatch: %v", tt.Texts())
	}

	// Filter to regular sites only: pan sites disappear, both regular sites show.
	if err := tt.Click("常规站"); err != nil {
		t.Fatalf("click 常规站: %v (texts=%v)", err, tt.Texts())
	}
	if a.sourcesSiteFilter != 2 {
		t.Fatalf("sourcesSiteFilter = %d, want 2", a.sourcesSiteFilter)
	}
	if tt.HasText("极速网盘") || tt.HasText("折叠网盘站") || !tt.HasText("极速影视") || !tt.HasText("备用常规站") {
		t.Fatalf("regular filter mismatch: %v", tt.Texts())
	}

	// Return to all and open a site chip.
	if err := tt.Click("全部"); err != nil {
		t.Fatalf("click 全部: %v", err)
	}
	if a.sourcesSiteFilter != 0 || !tt.HasText("极速网盘") {
		t.Fatalf("all filter did not restore all sites: filter=%d texts=%v", a.sourcesSiteFilter, tt.Texts())
	}
	if err := tt.Click("极速影视"); err != nil {
		t.Fatalf("click site chip 极速影视: %v (texts=%v)", err, tt.Texts())
	}
	if a.route() != routeSite || a.browser.session == nil {
		t.Fatalf("clicking site chip did not open site view: route=%v session=%v status=%q", a.route(), a.browser.session != nil, a.status)
	}
}

func TestSourcesS2PanSectionCollapsesWhenNoPanSitesAndNoCreds(t *testing.T) {
	doc := &config.Config{
		URL: "https://example.com/regular-only.json",
		Sites: []config.Site{
			{Key: "cms1", Name: "常规影视站", Type: 1, API: "https://example.com/cms1", Kind: config.KindJSON},
		},
	}
	a := &app{nav: []routeKind{routeSources}}
	defer a.close()
	a.browser.configURL = doc.URL
	a.library.root = doc
	a.library.documents = []libraryDocument{{doc: doc, line: "常规线路", open: true}}

	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()

	// Compact collapsible row shown by default; 4-slot pan tiles stay collapsed.
	if !tt.HasText("网盘原画授权（播放网盘影片时自动按需授权）") {
		t.Fatalf("collapsed pan disclosure missing: %v", tt.Texts())
	}
	if tt.HasText("夸克网盘") {
		t.Fatalf("pan account slots should stay collapsed when no pan sites and no creds: %v", tt.Texts())
	}

	// Expanding reveals all 4 pan provider tiles.
	if err := tt.Click("网盘原画授权（播放网盘影片时自动按需授权）"); err != nil {
		t.Fatalf("expand pan disclosure: %v (texts=%v)", err, tt.Texts())
	}
	for _, want := range []string{"夸克网盘", "阿里云盘", "UC网盘", "百度网盘", "登录"} {
		if !tt.HasText(want) {
			t.Fatalf("expanded pan section missing %q: %v", want, tt.Texts())
		}
	}
}

func TestSourcesS2LiveTabListEnterLiveAndEmptyState(t *testing.T) {
	// 1. Empty live tab offers friendly guidance and opens the live dialog.
	a := &app{nav: []routeKind{routeSources}, sourcesTab: 1}
	defer a.close()
	tt := ui.NewTester(a.view, 1120, 760)
	tt.Frame()

	if !tt.HasText("还没有添加") || !tt.HasText("还没有可用的直播来源") {
		t.Fatalf("empty live tab missing guidance: %v", tt.Texts())
	}
	if err := tt.Click("空态添加直播源"); err != nil {
		t.Fatalf("click empty state add live button: %v (texts=%v)", err, tt.Texts())
	}
	if !a.live.importOpen {
		t.Fatal("clicking empty state add live button did not open live dialog")
	}
	a.live.importOpen = false

	// 2. Populated live sources from VOD config: no remove button, clicking "进入直播"
	// switches to the chosen source and navigates to routeLive.
	a.live.sources = []iptv.Source{
		{
			Name: "默认直播源",
			Channels: []iptv.Channel{
				{Name: "频道甲", Streams: []iptv.Stream{{URL: "https://example.com/a.m3u8"}}},
			},
		},
		{
			Name: "备用直播源",
			Channels: []iptv.Channel{
				{Name: "频道乙", Streams: []iptv.Stream{{URL: "https://example.com/b.m3u8"}}},
				{Name: "频道丙", Streams: []iptv.Stream{{URL: "https://example.com/c.m3u8"}}},
			},
		},
	}
	a.live.selectedSource = 0
	a.live.channels = a.live.sources[0].Channels
	tt.Frame()

	if !tt.HasText("来自影视配置 · 2 个直播来源") || !tt.HasText("影视配置附带") || tt.HasText("移除直播源") {
		t.Fatalf("VOD-inherited live tab state unexpected: %v", tt.Texts())
	}
	if err := tt.Click("进入直播 备用直播源"); err != nil {
		t.Fatalf("click 进入直播 备用直播源: %v (texts=%v)", err, tt.Texts())
	}
	if a.route() != routeLive || a.live.selectedSource != 1 || len(a.live.channels) != 2 || a.live.channels[0].Name != "频道乙" {
		t.Fatalf("entering live source 1 failed: route=%v selected=%d channels=%+v", a.route(), a.live.selectedSource, a.live.channels)
	}

	// Returning to Sources live tab and clicking "前往直播" switches back to routeLive.
	a.switchTab(routeSources)
	a.sourcesTab = 1
	tt.Frame()
	if err := tt.Click("前往直播"); err != nil {
		t.Fatalf("click 前往直播: %v (texts=%v)", err, tt.Texts())
	}
	if a.route() != routeLive {
		t.Fatalf("clicking 前往直播 did not switch to routeLive: route=%v", a.route())
	}
}
