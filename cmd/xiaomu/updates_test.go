package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// nearRect allows for sub-pixel differences in text metrics across platforms.
func nearRect(a, b ui.Rect) bool {
	return math.Abs(float64(a.X-b.X)) < 0.5 && math.Abs(float64(a.Y-b.Y)) < 0.5 &&
		math.Abs(float64(a.W-b.W)) < 0.5 && math.Abs(float64(a.H-b.H)) < 0.5
}

func TestSidebarUpdateHint(t *testing.T) {
	a := &app{}
	t.Cleanup(a.close)
	tt := ui.NewTester(a.view, 640, 420)
	before, ok := tt.Find("检查更新")
	if !ok {
		t.Fatal("update entry is missing")
	}
	if _, ok := tt.Find("新版本提示"); ok {
		t.Fatal("an unchecked version already shows an update hint")
	}
	a.setAvailableUpdate("0.8.5")
	tt.Frame()
	button, ok := tt.Find("有新版本")
	if !ok || !nearRect(button, before) {
		t.Fatalf("update hint moved or replaced the sidebar entry: before=%+v after=%+v", before, button)
	}
	dot, ok := tt.Find("新版本提示")
	if !ok || dot.W < 1 || dot.H < 1 || dot.X < button.X || dot.Y < button.Y || dot.X+dot.W > button.X+button.W || dot.Y+dot.H > button.Y+button.H {
		t.Fatalf("update dot is outside its button: button=%+v dot=%+v", button, dot)
	}
	if button.X+button.W > sidebarWidth+20 || button.Y < 210 || button.Y+button.H > 420 {
		t.Fatalf("update hint does not fit the bottom-left sidebar: %+v", button)
	}
	// The updater clears availability for up-to-date, failed and skipped checks.
	a.setAvailableUpdate("")
	tt.Frame()
	if !tt.HasText("检查更新") || tt.HasText("有新版本") {
		t.Fatalf("cleared availability left a stale hint: %v", tt.Texts())
	}
	if _, ok := tt.Find("新版本提示"); ok {
		t.Fatal("cleared availability left a stale dot")
	}
}

func TestAvailableUpdateUsesWindowDispatch(t *testing.T) {
	queued := make(chan func(), 2)
	a := &app{dispatch: func(update func()) { queued <- update }}
	t.Cleanup(a.close)
	go a.setAvailableUpdate("0.8.5")
	var apply func()
	select {
	case apply = <-queued:
	case <-time.After(time.Second):
		t.Fatal("update availability did not dispatch to the UI thread")
	}
	if a.availableUpdate != "" {
		t.Fatal("background update changed UI state before dispatch")
	}
	apply()
	if a.availableUpdate != "0.8.5" {
		t.Fatal("dispatched availability did not reach the sidebar state")
	}
	a.setAvailableUpdate("")
	a.close()
	(<-queued)()
	if a.availableUpdate != "0.8.5" {
		t.Fatal("a queued update changed state after the window closed")
	}
}

func TestUpdateButtonRemainsClickable(t *testing.T) {
	for _, version := range []string{"", "0.8.5"} {
		t.Run("version="+version, func(t *testing.T) {
			clicks := 0
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Box(c).Fill().Children(func() {
					if updateButton(c, version).Clicked() {
						clicks++
					}
				})
			}, sidebarWidth-20, 60)
			label := "检查更新"
			if version != "" {
				label = "有新版本"
			}
			if err := tt.Click(label); err != nil {
				t.Fatal(err)
			}
			if clicks != 1 {
				t.Fatalf("clicking %s activated the button %d times", label, clicks)
			}
			if version != "" {
				dot, ok := tt.Find("新版本提示")
				if !ok {
					t.Fatal("missing update dot")
				}
				tt.ClickAt(dot.X+dot.W/2, dot.Y+dot.H/2)
				if clicks != 2 {
					t.Fatal("update dot swallowed the button click")
				}
			}
		})
	}
}

func TestDarwinArchUpdateSourceRoutesByArchAndFallsBackOn404(t *testing.T) {
	if src := newDarwinArchUpdateSource("ppc64"); src != nil {
		t.Fatalf("unexpected source for unsupported arch: %+v", src)
	}

	for _, arch := range []string{"arm64", "amd64"} {
		t.Run(arch, func(t *testing.T) {
			var gotPath, gotUA string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotUA = r.Header.Get("User-Agent")
				fmt.Fprintf(w, `{"version":"0.9.9","url":"%s/pkg.tar.gz"}`, "https://example.com")
			}))
			defer srv.Close()

			src := &darwinArchUpdateSource{
				baseURL: srv.URL,
				arch:    arch,
				client:  srv.Client(),
				delays:  []time.Duration{time.Millisecond},
			}
			rc, err := src.Manifest(context.Background(), false)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			wantPath := "/update-darwin-" + arch + ".json"
			if gotPath != wantPath {
				t.Fatalf("requested %q, want %q", gotPath, wantPath)
			}
			if gotUA != "xiaomu-updater" {
				t.Fatalf("User-Agent = %q, want ASCII xiaomu-updater", gotUA)
			}
			if !strings.Contains(string(body), `"0.9.9"`) {
				t.Fatalf("body=%s", body)
			}
		})
	}

	// Missing per-arch manifest (404) falls back to universal manifest.
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/update-darwin-amd64.json" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"version":"0.9.8","url":"https://example.com/universal.tar.gz"}`)
	}))
	defer srv.Close()

	src := &darwinArchUpdateSource{
		baseURL: srv.URL,
		arch:    "amd64",
		client:  srv.Client(),
		delays:  []time.Duration{time.Millisecond},
	}
	rc, err := src.Manifest(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if len(paths) != 2 || paths[0] != "/update-darwin-amd64.json" || paths[1] != "/update-darwin-universal.json" {
		t.Fatalf("fallback paths=%v", paths)
	}
	if !strings.Contains(string(data), "universal.tar.gz") {
		t.Fatalf("fallback body=%s", data)
	}
}

func TestDarwinArchUpdateSourceRetriesTransientErrorsAndRequiresHTTPS(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/update-darwin-universal.json" {
			t.Error("transient 503 must not fall back to universal manifest")
		}
		if attempts.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"version":"0.9.9"}`)
	}))
	defer srv.Close()

	src := &darwinArchUpdateSource{
		baseURL: srv.URL,
		arch:    "arm64",
		client:  srv.Client(),
		delays:  []time.Duration{time.Millisecond},
	}
	rc, err := src.Manifest(context.Background(), false)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	rc.Close()
	if attempts.Load() != 2 {
		t.Fatalf("attempts=%d, want 2", attempts.Load())
	}

	if _, err := src.Open(context.Background(), "http://example.com/xiaomu.tar.gz"); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("plain HTTP update URL was not rejected: %v", err)
	}

	// Loopback HTTP is allowed for local tests.
	rc, err = src.Open(context.Background(), srv.URL+"/update-darwin-arm64.json")
	if err != nil {
		t.Fatalf("loopback Open failed: %v", err)
	}
	rc.Close()
}

func TestDarwinArchUpdateSourceKeepsHTTPSAcrossRedirects(t *testing.T) {
	var plainHits atomic.Int32
	var rejectHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		plainHits.Add(1)
		fmt.Fprint(w, "plain")
	}))
	defer plain.Close()

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/download":
			http.Redirect(w, r, "/asset", http.StatusFound)
		case "/asset":
			fmt.Fprint(w, "asset")
		default:
			rejectHits.Add(1)
			http.Redirect(w, r, plain.URL+"/asset", http.StatusFound)
		}
	}))
	defer secure.Close()
	upgrade := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, secure.URL+"/reject", http.StatusFound)
	}))
	defer upgrade.Close()

	src := &darwinArchUpdateSource{client: secure.Client(), delays: []time.Duration{time.Millisecond}}
	body, err := src.Open(context.Background(), secure.URL+"/download")
	if err != nil {
		t.Fatalf("HTTPS redirect failed: %v", err)
	}
	data, err := io.ReadAll(body)
	body.Close()
	if err != nil || string(data) != "asset" {
		t.Fatalf("HTTPS redirect response = %q, %v", data, err)
	}
	for _, start := range []string{secure.URL + "/reject", upgrade.URL} {
		body, err = src.Open(context.Background(), start)
		if body != nil {
			body.Close()
		}
		if !errors.Is(err, errInsecureUpdateRedirect) || plainHits.Load() != 0 {
			t.Fatalf("HTTPS download followed a plain HTTP redirect: err=%v, requests=%d", err, plainHits.Load())
		}
	}
	if rejectHits.Load() != 2 {
		t.Fatalf("insecure redirect should not trigger retries: requests=%d", rejectHits.Load())
	}
}

func TestUniversalBridgeManifestValidForTwoHopMigration(t *testing.T) {
	bridgePath := filepath.Join("..", "..", "scripts", "update-darwin-universal-bridge.json")
	raw, err := os.ReadFile(bridgePath)
	if err != nil {
		t.Fatalf("read bridge manifest: %v", err)
	}
	var manifest struct {
		Version   string `json:"version"`
		Notes     string `json:"notes"`
		URL       string `json:"url"`
		Size      int64  `json:"size"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse bridge manifest: %v", err)
	}
	if manifest.Version != "0.9.8" {
		t.Fatalf("bridge manifest version = %q, want 0.9.8", manifest.Version)
	}
	const wantURL = "https://github.com/arixbit/xiaomu-desktop/releases/download/v0.9.8/xiaomu-0.9.8-darwin-universal.tar.gz"
	if manifest.URL != wantURL {
		t.Fatalf("bridge manifest url = %q, want %q", manifest.URL, wantURL)
	}
	if manifest.Size <= 0 {
		t.Fatalf("bridge manifest size = %d, want > 0", manifest.Size)
	}
	if strings.TrimSpace(manifest.Notes) == "" {
		t.Fatal("bridge manifest notes must not be empty")
	}
	sig, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		t.Fatalf("invalid bridge manifest signature: len=%d err=%v", len(sig), err)
	}

	// Ensure mygo.json still carries a valid Ed25519 public key for updater verification.
	cfgRaw, err := os.ReadFile(filepath.Join("..", "..", "mygo.json"))
	if err != nil {
		t.Fatalf("read mygo.json: %v", err)
	}
	var cfg struct {
		Updates struct {
			PublicKey string `json:"publicKey"`
		} `json:"updates"`
	}
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		t.Fatalf("parse mygo.json: %v", err)
	}
	pub, err := base64.StdEncoding.DecodeString(cfg.Updates.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("invalid mygo.json updates.publicKey: len=%d err=%v", len(pub), err)
	}
}
