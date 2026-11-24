package alist

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arixbit/xiaomu-desktop/internal/config"
)

func TestAListBrowseSearchDetailAndPlay(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			var req struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Path == "/movies" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 200,
					"data": map[string]any{
						"total": 3,
						"content": []map[string]any{
							{"name": "示例剧集", "is_dir": true},
							{"name": "示例4K电影.mkv", "is_dir": false, "size": 8000000000},
							{"name": "说明.nfo", "is_dir": false},
						},
					},
				})
				return
			}
			if req.Path == "/movies/示例剧集" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 200,
					"data": map[string]any{
						"total": 2,
						"content": []map[string]any{
							{"name": "S01E10.mp4", "is_dir": false, "size": 1000},
							{"name": "S01E02.mp4", "is_dir": false, "size": 1000},
						},
					},
				})
				return
			}
		case "/api/fs/search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{
					"total": 1,
					"content": []map[string]any{
						{"name": "示例4K电影.mkv", "parent": "/movies", "is_dir": false, "size": 8000000000},
					},
				},
			})
		case "/api/fs/get":
			var req struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Path == "/movies/示例4K电影.mkv" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 200,
					"data": map[string]any{
						"name":    "示例4K电影.mkv",
						"raw_url": "https://cdn.example.com/4k-movie.mkv",
						"size":    8000000000,
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{
					"name":    "S01E02.mp4",
					"raw_url": "https://cdn.example.com/s01e02.mp4",
					"size":    1000,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer good.Close()

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer broken.Close()

	// Test shorthand map format with one working drive and one broken drive.
	extMap, _ := json.Marshal(map[string]string{
		"坏盘": broken.URL,
		"好盘": good.URL + "/movies",
	})
	sess, cats, err := Open(context.Background(), config.Site{
		Kind: config.KindAList,
		Ext:  string(extMap),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if len(cats) != 2 {
		t.Fatalf("cats = %+v", cats)
	}

	// Home should skip the broken drive and return items from the good drive.
	home, err := sess.Home(context.Background())
	if err != nil || len(home.Videos) != 2 {
		t.Fatalf("home = %+v err=%v", home, err)
	}

	// Search across drives should ignore the broken drive and find the 4K MKV.
	found, err := sess.Search(context.Background(), "示例", 1)
	if err != nil || len(found.Videos) != 1 {
		t.Fatalf("search = %+v err=%v", found, err)
	}

	// Single MKV detail & play marks NativePlayable=false for non-AVFoundation containers.
	detailMKV, err := sess.Detail(context.Background(), string(found.Videos[0].ID))
	if err != nil || len(detailMKV.Sources) != 1 || len(detailMKV.Sources[0].Episodes) != 1 {
		t.Fatalf("mkv detail = %+v err=%v", detailMKV, err)
	}
	playMKV, err := sess.Play(context.Background(), detailMKV.Sources[0].Name, detailMKV.Sources[0].Episodes[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(playMKV.Streams) != 1 || playMKV.Streams[0].NativePlayable || playMKV.Filename != "示例4K电影.mkv" {
		t.Fatalf("unexpected MKV playback: %+v", playMKV)
	}

	// Directory detail should expand and naturally sort episodes, and MP4 should be NativePlayable=true.
	var dirID string
	for _, v := range home.Videos {
		if v.Name == "示例剧集" {
			dirID = string(v.ID)
		}
	}
	detailDir, err := sess.Detail(context.Background(), dirID)
	if err != nil {
		t.Fatal(err)
	}
	eps := detailDir.Sources[0].Episodes
	if len(eps) != 2 || eps[0].Name != "S01E02" || eps[1].Name != "S01E10" {
		t.Fatalf("unexpected dir episodes: %+v", eps)
	}
	playMP4, err := sess.Play(context.Background(), detailDir.Sources[0].Name, eps[0])
	if err != nil || len(playMP4.Streams) != 1 || !playMP4.Streams[0].NativePlayable {
		t.Fatalf("unexpected MP4 playback: %+v err=%v", playMP4, err)
	}
}
