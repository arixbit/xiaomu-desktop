package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	searchHistoryLimit    = 10 // recent searches kept for suggestions
	searchSuggestionLimit = 8  // rows in the suggestions popup
)

// searchHistoryStore keeps the viewer's recent searches, newest first, only
// on this Mac. It never holds a config URL or a media address.
type searchHistoryStore struct {
	queries []string
	path    string
}

// record puts the query first, dropping an older spelling of the same title,
// and saves before updating the UI so a failed save cannot appear saved.
func (s *searchHistoryStore) record(query string) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	kept := []string{query}
	for _, old := range s.queries {
		if titleKey(old) == titleKey(query) {
			continue
		}
		kept = append(kept, old)
		if len(kept) == searchHistoryLimit {
			break
		}
	}
	if s.path != "" {
		if err := writeJSONFile(s.path, kept); err != nil {
			return err
		}
	}
	s.queries = kept
	return nil
}

func readSearchHistory(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var queries []string
	if err := json.Unmarshal(data, &queries); err != nil {
		return nil, fmt.Errorf("读取搜索记录：%w", err)
	}
	return queries, nil
}

func (a *app) restoreSearchHistory() {
	sourcePath, err := savedSourcePath()
	if err != nil {
		return
	}
	a.searchHistory.path = filepath.Join(filepath.Dir(sourcePath), "search-history.json")
	queries, err := readSearchHistory(a.searchHistory.path)
	if err != nil {
		a.status = err.Error()
		return
	}
	a.searchHistory.queries = queries
}

// searchSuggestions are the titles the toolbar offers while typing: the
// viewer's own recent searches first, then the Douban hot titles already
// cached for the home page. Nothing here touches the network.
func (a *app) searchSuggestions() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[titleKey(name)] {
			return
		}
		seen[titleKey(name)] = true
		out = append(out, name)
	}
	for _, query := range a.searchHistory.queries {
		add(query)
	}
	for _, list := range []*discoveryList{&a.discovery.movie, &a.discovery.tv} {
		for _, subject := range list.feed.Subjects {
			add(subject.Title)
		}
	}
	return out
}

// suggestMatches keeps the candidates containing the typed text: those
// starting with it first, then the rest, in their original order, each title
// once. An empty query shows the first candidates, as when the field is
// only clicked.
func suggestMatches(candidates []string, query string) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []string
	seen := map[string]bool{}
	add := func(candidate string) {
		if seen[titleKey(candidate)] {
			return
		}
		seen[titleKey(candidate)] = true
		out = append(out, candidate)
	}
	if q == "" {
		for _, candidate := range candidates {
			add(candidate)
		}
	} else {
		var starts, contains []string
		for _, candidate := range candidates {
			if strings.EqualFold(candidate, query) {
				continue
			}
			switch lower := strings.ToLower(candidate); {
			case strings.HasPrefix(lower, q):
				starts = append(starts, candidate)
			case strings.Contains(lower, q):
				contains = append(contains, candidate)
			}
		}
		for _, candidate := range starts {
			add(candidate)
		}
		for _, candidate := range contains {
			add(candidate)
		}
	}
	if len(out) > searchSuggestionLimit {
		out = out[:searchSuggestionLimit]
	}
	return out
}
