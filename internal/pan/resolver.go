package pan

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
)

// CheckAuth verifies the configured credentials for provider.
func CheckAuth(ctx context.Context, p Provider) (AuthStatus, error) {
	switch p {
	case ProviderQuark, ProviderUC:
		return sharedQuarkClient(p).CheckAuth(ctx)
	case ProviderAli:
		return sharedAliClient().CheckAuth(ctx)
	case ProviderBaidu, Provider115:
		if DefaultCredentials().Has(p) {
			return AuthStatus{Provider: p, Valid: true, VIP: "已登录", Summary: p.Label() + "已登录"}, nil
		}
		return AuthStatus{Provider: p}, &AuthRequiredError{Provider: p}
	default:
		return AuthStatus{Provider: p}, fmt.Errorf("暂未内置 %s 解析驱动", p.Label())
	}
}

// CleanupAllTemp removes temporary playback files across all configured providers.
func CleanupAllTemp(ctx context.Context) (int, error) {
	total := 0
	var firstErr error
	for _, p := range []Provider{ProviderQuark, ProviderUC} {
		n, err := sharedQuarkClient(p).CleanupTemp(ctx)
		total += n
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return total, firstErr
}

// ExpandShare expands one share link into naturally sorted video files.
func ExpandShare(ctx context.Context, ref ShareRef) ([]FileItem, error) {
	switch ref.Provider {
	case ProviderQuark, ProviderUC:
		return sharedQuarkClient(ref.Provider).ExpandShare(ctx, ref)
	case ProviderAli:
		return sharedAliClient().ExpandShare(ctx, ref)
	default:
		return nil, fmt.Errorf("暂不支持直接解析%s分享链接（当前已内置夸克、UC、阿里云盘）", ref.Provider.Label())
	}
}

// ExpandShares expands one or more share links concurrently into catalog play lines.
// When a supported provider is not signed in yet (or anonymous expansion fails),
// it still emits a clickable play line so the user can open the detail page and
// trigger interactive official login on click.
func ExpandShares(ctx context.Context, refs []ShareRef) ([]catalog.Source, error) {
	if len(refs) == 0 {
		return nil, fmt.Errorf("没有找到可用的网盘分享链接")
	}
	creds := DefaultCredentials()
	type result struct {
		ref        ShareRef
		files      []FileItem
		needsLogin bool
		err        error
	}
	results := make([]result, len(refs))
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func(idx int, r ShareRef) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[idx] = result{ref: r, err: ctx.Err()}
				return
			}
			defer func() { <-sem }()
			if r.Provider.Supported() && !creds.Has(r.Provider) {
				results[idx] = result{ref: r, needsLogin: true}
				return
			}
			files, err := ExpandShare(ctx, r)
			if err != nil {
				if _, needAuth := AuthRequiredProvider(err); needAuth {
					results[idx] = result{ref: r, needsLogin: true, err: err}
					return
				}
			}
			results[idx] = result{ref: r, files: files, err: err}
		}(i, ref)
	}
	wg.Wait()

	var sources []catalog.Source
	counts := map[Provider]int{}
	var firstErr error
	for _, r := range results {
		if r.err != nil && !r.needsLogin {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		counts[r.ref.Provider]++
		lineName := r.ref.Provider.Label()
		if counts[r.ref.Provider] > 1 {
			lineName = fmt.Sprintf("%s · %d", lineName, counts[r.ref.Provider])
		}
		if r.needsLogin {
			sources = append(sources, catalog.Source{
				Name: lineName,
				Episodes: []catalog.Episode{{
					Name: "登录" + r.ref.Provider.Label() + "后播放",
					ID:   shareEpisodeID(r.ref),
				}},
			})
			continue
		}
		eps := make([]catalog.Episode, 0, len(r.files))
		for _, f := range r.files {
			eps = append(eps, catalog.Episode{
				Name: DisplayEpisodeName(f.Name),
				ID:   EncodeEpisodeID(f),
			})
		}
		if len(eps) > 0 {
			sources = append(sources, catalog.Source{Name: lineName, Episodes: eps})
		}
	}
	if len(sources) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("网盘分享链接中没有可播放视频")
	}
	return sources, nil
}

func shareEpisodeID(ref ShareRef) string {
	if ref.Passcode != "" && !strings.Contains(ref.RawURL, "pwd=") {
		sep := "?"
		if strings.Contains(ref.RawURL, "?") {
			sep = "&"
		}
		return ref.RawURL + sep + "pwd=" + ref.Passcode
	}
	return ref.RawURL
}

// ResolveEpisode resolves a pan:// episode ID or direct share URL into playback streams.
func ResolveEpisode(ctx context.Context, episodeID string) (catalog.Playback, error) {
	if item, ok := DecodeEpisodeID(episodeID); ok {
		switch item.Provider {
		case ProviderQuark, ProviderUC:
			return sharedQuarkClient(item.Provider).ResolvePlayback(ctx, item)
		case ProviderAli:
			return sharedAliClient().ResolvePlayback(ctx, item)
		default:
			return catalog.Playback{}, fmt.Errorf("暂不支持%s在线解析", item.Provider.Label())
		}
	}
	if ref, ok := ParseShareURL(episodeID); ok {
		if ref.Provider.Supported() && !DefaultCredentials().Has(ref.Provider) {
			return catalog.Playback{}, &AuthRequiredError{
				Provider: ref.Provider,
				Reason:   fmt.Sprintf("播放%s资源需要先登录账号", ref.Provider.Label()),
			}
		}
		files, err := ExpandShare(ctx, ref)
		if err != nil {
			return catalog.Playback{}, err
		}
		switch ref.Provider {
		case ProviderQuark, ProviderUC:
			return sharedQuarkClient(ref.Provider).ResolvePlayback(ctx, files[0])
		case ProviderAli:
			return sharedAliClient().ResolvePlayback(ctx, files[0])
		default:
			return catalog.Playback{}, fmt.Errorf("暂不支持%s在线解析", ref.Provider.Label())
		}
	}
	return catalog.Playback{}, fmt.Errorf("无法识别的网盘分集地址")
}

// ExpandDetailShares checks whether a site detail returned raw share links (e.g. from
// drpy2 pan-search scripts) as episode IDs and expands them into playable pan:// episodes.
func ExpandDetailShares(ctx context.Context, detail catalog.Detail) (catalog.Detail, bool, error) {
	var refs []ShareRef
	seen := map[string]bool{}
	hasNonShare := false
	for _, src := range detail.Sources {
		for _, ep := range src.Episodes {
			if _, ok := DecodeEpisodeID(ep.ID); ok {
				hasNonShare = true
				continue
			}
			if ref, ok := ParseShareURL(ep.ID); ok {
				if !seen[ref.Key()] {
					seen[ref.Key()] = true
					refs = append(refs, ref)
				}
				continue
			}
			hasNonShare = true
		}
	}
	if len(refs) == 0 {
		return detail, false, nil
	}
	expanded, err := ExpandShares(ctx, refs)
	if err != nil {
		if hasNonShare {
			return detail, false, nil
		}
		return catalog.Detail{}, true, err
	}
	if hasNonShare {
		// Keep non-share lines and replace share-only lines with expanded files.
		var merged []catalog.Source
		for _, src := range detail.Sources {
			var kept []catalog.Episode
			for _, ep := range src.Episodes {
				if _, ok := ParseShareURL(ep.ID); !ok {
					kept = append(kept, ep)
				}
			}
			if len(kept) > 0 {
				src.Episodes = kept
				merged = append(merged, src)
			}
		}
		detail.Sources = append(merged, expanded...)
	} else {
		detail.Sources = expanded
	}
	return detail, true, nil
}

// ShareSession adapts a single pasted share link into a source.Session so Xiaomu's
// detail page, episode selector, danmaku and player work identically.
type ShareSession struct {
	ref ShareRef
}

// OpenShareSession creates a session backed by one cloud-drive share link.
func OpenShareSession(ref ShareRef) *ShareSession {
	return &ShareSession{ref: ref}
}

func (s *ShareSession) Home(context.Context) (catalog.Page, error) {
	return catalog.Page{}, nil
}

func (s *ShareSession) Category(context.Context, string, int) (catalog.Page, error) {
	return catalog.Page{}, nil
}

func (s *ShareSession) Search(context.Context, string, int) (catalog.Page, error) {
	return catalog.Page{}, nil
}

func (s *ShareSession) Detail(ctx context.Context, _ string) (catalog.Detail, error) {
	sources, err := ExpandShares(ctx, []ShareRef{s.ref})
	if err != nil {
		return catalog.Detail{}, err
	}
	title := s.ref.Provider.Label() + "分享"
	if len(sources) > 0 && len(sources[0].Episodes) > 0 {
		title = guessTitleFromEpisode(sources[0].Episodes[0].Name, title)
	}
	return catalog.Detail{
		Name:    title,
		Type:    s.ref.Provider.Label(),
		Sources: sources,
	}, nil
}

func (s *ShareSession) Play(ctx context.Context, _ string, ep catalog.Episode) (catalog.Playback, error) {
	return ResolveEpisode(ctx, ep.ID)
}

func (s *ShareSession) Close() {}

func guessTitleFromEpisode(epName, fallback string) string {
	epName = strings.TrimSpace(epName)
	if epName == "" {
		return fallback
	}
	return epName
}
