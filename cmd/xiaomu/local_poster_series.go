package main

import (
	"path/filepath"

	"github.com/arixbit/xiaomu-desktop/internal/tmdb"
)

// localPosterSeriesID marks a remote poster shareable only when the filename
// independently identifies its TV series and agrees with the chosen match.
func localPosterSeriesID(path string, match tmdb.Match) int {
	if match.ID <= 0 || match.Kind != tmdb.TV {
		return 0
	}
	title, year, kind := localPosterFileQuery(path)
	key := titleKey(title)
	if kind != tmdb.TV || key == "" || (year != "" && year != match.Year) {
		return 0
	}
	if key != titleKey(match.Title) && key != titleKey(match.OriginalTitle) {
		return 0
	}
	return match.ID
}

// findLocalSeriesPoster finds a previously identified episode with the same
// parsed title and year, within the video's own directory. Disagreeing IDs
// leave the series unresolved so an ambiguous folder can still be searched.
func (h *historyStore) findLocalSeriesPoster(path, title, year string) (historyEntry, bool) {
	key := titleKey(title)
	if key == "" {
		return historyEntry{}, false
	}
	file, dir := filepath.Clean(path), filepath.Dir(path)
	var chosen historyEntry
	for _, entry := range h.entries {
		if !entry.isLocal() || entry.PosterSeriesID <= 0 || entry.LocalPoster == "" {
			continue
		}
		if filepath.Clean(entry.VideoID) == file || filepath.Dir(entry.VideoID) != dir {
			continue
		}
		entryTitle, entryYear, kind := localPosterFileQuery(entry.VideoID)
		if kind != tmdb.TV || titleKey(entryTitle) != key || entryYear != year {
			continue
		}
		if chosen.PosterSeriesID == 0 {
			chosen = entry
		} else if chosen.PosterSeriesID != entry.PosterSeriesID {
			return historyEntry{}, false
		}
	}
	return chosen, chosen.PosterSeriesID != 0
}
