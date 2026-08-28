package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattwalters/lerp/internal/telemetry"
)

func TestPathSharesDirectoryWithTelemetry(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	uPath, err := Path()
	if err != nil {
		t.Fatalf("update.Path: %v", err)
	}
	telPath, err := telemetry.Path()
	if err != nil {
		t.Fatalf("telemetry.Path: %v", err)
	}

	if filepath.Dir(uPath) != filepath.Dir(telPath) {
		t.Errorf("update dir %q != telemetry dir %q", filepath.Dir(uPath), filepath.Dir(telPath))
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		current string
		latest  string
		want    bool
	}{
		{current: "v0.1.0", latest: "v0.2.0", want: true},
		{current: "v0.1.0", latest: "v0.1.1", want: true},
		{current: "v0.1.0", latest: "v1.0.0", want: true},
		{current: "v9.0.0", latest: "v10.0.0", want: true},
		{current: "v10.0.0", latest: "v9.0.0", want: false},
		{current: "v0.2.0", latest: "v0.2.0", want: false},
		{current: "v0.3.0", latest: "v0.2.0", want: false},
		// Suffix dropped, ahead of base tag -> must not nag.
		{current: "v0.1.0-3-gabc1234-dirty", latest: "v0.1.0", want: false},
		{current: "v0.1.0-3-gabc1234-dirty", latest: "v0.2.0", want: true},
		// Dev / unparseable / pseudo-versions
		{current: "dev", latest: "v0.2.0", want: false},
		{current: "(devel)", latest: "v0.2.0", want: false},
		{current: "v0.0.0-20260827041153-3c6bcf9d86e7+dirty", latest: "v0.2.0", want: false},
		{current: "v0.1.1-0.20260827041153-3c6bcf9d86e7", latest: "v1.0.0", want: false},
		{current: "", latest: "v0.2.0", want: false},
		{current: "garbage", latest: "v0.2.0", want: false},
		{current: "v0.1.0", latest: "garbage", want: false},
		{current: "v0.1.0", latest: "v0.2.0-rc1", want: true},
	}

	for _, c := range cases {
		t.Run(fmt.Sprintf("%s_vs_%s", c.current, c.latest), func(t *testing.T) {
			got := newer(c.current, c.latest)
			if got != c.want {
				t.Errorf("newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
			}
		})
	}
}

func TestCheckFetchesAndAnnounces(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("Accept header = %q, want application/vnd.github+json", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"tag_name": "v0.2.0", "html_url": "https://attacker.example.com"}`)
	}))
	defer srv.Close()

	oldEndpoint := endpoint
	endpoint = srv.URL
	defer func() { endpoint = oldEndpoint }()

	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	notice, err := Check(context.Background(), srv.Client(), now, "v0.1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if notice.Latest != "v0.2.0" {
		t.Errorf("notice.Latest = %q, want v0.2.0", notice.Latest)
	}
	// URL must be derived from tag, never taken from payload
	if want := "https://github.com/mattwalters/lerp/releases/tag/v0.2.0"; notice.URL != want {
		t.Errorf("notice.URL = %q, want %q", notice.URL, want)
	}
	if !notice.Announce {
		t.Errorf("notice.Announce = false on first check, want true")
	}
	if atomic.LoadInt32(&requests) != 1 {
		t.Errorf("requests = %d, want 1", atomic.LoadInt32(&requests))
	}

	// A second check within 24h makes zero network requests and Announce is false.
	later := now.Add(2 * time.Hour)
	notice2, err := Check(context.Background(), srv.Client(), later, "v0.1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&requests) != 1 {
		t.Errorf("second check made network request: total requests = %d, want 1", atomic.LoadInt32(&requests))
	}
	if notice2.Announce {
		t.Errorf("second check notice.Announce = true, want false")
	}
	if notice2.Latest != "v0.2.0" {
		t.Errorf("notice2.Latest = %q, want v0.2.0", notice2.Latest)
	}
}

func TestCheckOptOut(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)
	t.Setenv("LERP_NO_UPDATE_CHECK", "1")

	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
	}))
	defer srv.Close()

	oldEndpoint := endpoint
	endpoint = srv.URL
	defer func() { endpoint = oldEndpoint }()

	notice, err := Check(context.Background(), srv.Client(), time.Now(), "v0.1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notice.Latest != "" || notice.Announce {
		t.Errorf("expected empty notice, got %+v", notice)
	}
	if atomic.LoadInt32(&requests) != 0 {
		t.Errorf("requests = %d, want 0", atomic.LoadInt32(&requests))
	}

	// Opt-out reads and writes no cache file
	path, _ := Path()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cache file created despite opt-out: %v", err)
	}
}

func TestCheckRejectsInvalidTags(t *testing.T) {
	invalidTags := []string{
		"\x1b[2J",
		"../../etc/passwd",
		"v0.1",
		"latest",
		"v1.0.0; rm -rf /",
	}

	for _, bad := range invalidTags {
		t.Run(bad, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("XDG_STATE_HOME", tmp)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				payload, _ := json.Marshal(map[string]string{"tag_name": bad})
				w.Write(payload)
			}))
			defer srv.Close()

			oldEndpoint := endpoint
			endpoint = srv.URL
			defer func() { endpoint = oldEndpoint }()

			notice, err := Check(context.Background(), srv.Client(), time.Now(), "v0.1.0")
			if err == nil {
				t.Fatalf("expected error for bad tag %q, got nil", bad)
			}
			if notice.Latest != "" || notice.Announce {
				t.Errorf("expected empty notice for bad tag %q, got %+v", bad, notice)
			}
		})
	}
}

func TestCheckFailuresWriteCheckedAt(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "403 Forbidden",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
		},
		{
			name: "500 Internal Error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
		},
		{
			name: "Garbage JSON",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("{invalid json"))
			},
		},
		{
			name: "Truncated Body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "1000")
				w.Write([]byte(`{"tag_name":`))
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("XDG_STATE_HOME", tmp)

			srv := httptest.NewServer(c.handler)
			defer srv.Close()

			oldEndpoint := endpoint
			endpoint = srv.URL
			defer func() { endpoint = oldEndpoint }()

			now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
			notice, err := Check(context.Background(), srv.Client(), now, "v0.1.0")
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if notice.Latest != "" {
				t.Errorf("notice = %+v, want zero", notice)
			}

			path, _ := Path()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("failed to read cache: %v", err)
			}
			var cacheData cache
			if err := json.Unmarshal(data, &cacheData); err != nil {
				t.Fatalf("unmarshal cache: %v", err)
			}
			if !cacheData.CheckedAt.Equal(now) {
				t.Errorf("checked_at = %v, want %v", cacheData.CheckedAt, now)
			}
		})
	}
}

func TestUnwritableStateDirStillReturnsNotice(t *testing.T) {
	tmp := t.TempDir()
	// Point to an unwritable directory
	unwritable := filepath.Join(tmp, "unwritable")
	if err := os.MkdirAll(unwritable, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", unwritable)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"tag_name": "v0.2.0"}`)
	}))
	defer srv.Close()

	oldEndpoint := endpoint
	endpoint = srv.URL
	defer func() { endpoint = oldEndpoint }()

	notice, err := Check(context.Background(), srv.Client(), time.Now(), "v0.1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notice.Latest != "v0.2.0" {
		t.Errorf("notice.Latest = %q, want v0.2.0", notice.Latest)
	}
}

func TestCachedLatest(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	// No cache -> empty, no error
	got, err := CachedLatest("v0.1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("CachedLatest = %q, want empty", got)
	}

	// Cache with matching version -> empty
	c := cache{
		CheckedAt: time.Now(),
		Latest:    "v0.1.0",
		Announced: "v0.1.0",
	}
	if err := writeCache(c); err != nil {
		t.Fatal(err)
	}
	got, err = CachedLatest("v0.1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("CachedLatest = %q, want empty", got)
	}

	// Cache with newer version -> returns version
	c.Latest = "v0.2.0"
	if err := writeCache(c); err != nil {
		t.Fatal(err)
	}
	got, err = CachedLatest("v0.1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "v0.2.0" {
		t.Errorf("CachedLatest = %q, want v0.2.0", got)
	}

	// Corrupt cache -> returns error
	path, _ := Path()
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = CachedLatest("v0.1.0")
	if err == nil {
		t.Fatal("expected error on corrupt cache, got nil")
	}
}
