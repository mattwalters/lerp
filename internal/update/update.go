package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Notice reports an update available to announce or display.
type Notice struct {
	Latest   string
	URL      string
	Announce bool
}

const (
	subdir   = "lerp"
	fileName = "update.json"
)

// Path is where the update cache lives: $XDG_STATE_HOME/lerp/update.json,
// falling back to ~/.local/state/lerp/update.json on every platform.
func Path() (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locating the user home directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, subdir, fileName), nil
}

type cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest,omitempty"`
	Announced string    `json:"announced,omitempty"`
}

func readCache(path string) (cache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cache{}, err
	}
	var c cache
	if err := json.Unmarshal(data, &c); err != nil {
		return cache{}, fmt.Errorf("decoding %s: %w", path, err)
	}
	return c, nil
}

func writeCache(c cache) error {
	path, err := Path()
	if err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding update cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating update directory: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// CachedLatest returns the latest version tag from the local cache if it is
// valid and newer than current. It never makes a network request. If no cache
// file exists, it returns empty with no error.
func CachedLatest(current string) (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	c, err := readCache(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if c.Latest == "" || !validTag(c.Latest) {
		return "", nil
	}
	if !newer(current, c.Latest) {
		return "", nil
	}
	return c.Latest, nil
}

// endpoint is the GitHub releases API URL. Unexported package var so tests can
// point it at an httptest server.
var endpoint = "https://api.github.com/repos/mattwalters/lerp/releases/latest"

var (
	tagRegex      = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	pseudoPattern = regexp.MustCompile(`(^v0\.0\.0-)|(\d{14}-[0-9a-fA-F]+)`)
)

func validTag(tag string) bool {
	return tagRegex.MatchString(tag)
}

func releaseURL(tag string) string {
	return "https://github.com/mattwalters/lerp/releases/tag/" + tag
}

// Check queries GitHub's releases API for the latest version tag, at most once
// per 24 hours, cached in the state directory. It fails silently by design: an
// update check must never break the tool.
func Check(ctx context.Context, hc *http.Client, now time.Time, current string) (Notice, error) {
	if os.Getenv("LERP_NO_UPDATE_CHECK") != "" {
		return Notice{}, nil
	}

	path, pathErr := Path()
	var c cache
	if pathErr == nil {
		if loaded, err := readCache(path); err == nil {
			c = loaded
		}
	}

	// Cache younger than 24h -> answer from it, zero requests.
	if !c.CheckedAt.IsZero() && now.Sub(c.CheckedAt) < 24*time.Hour && now.Sub(c.CheckedAt) >= 0 {
		if c.Latest != "" && validTag(c.Latest) && newer(current, c.Latest) {
			announce := (c.Announced != c.Latest)
			if announce {
				c.Announced = c.Latest
				_ = writeCache(c)
			}
			return Notice{
				Latest:   c.Latest,
				URL:      releaseURL(c.Latest),
				Announce: announce,
			}, nil
		}
		return Notice{}, nil
	}

	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		c.CheckedAt = now
		_ = writeCache(c)
		return Notice{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "lerp")

	resp, err := hc.Do(req)
	if err != nil {
		c.CheckedAt = now
		_ = writeCache(c)
		return Notice{}, fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.CheckedAt = now
		_ = writeCache(c)
		return Notice{}, fmt.Errorf("GitHub releases API returned %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		c.CheckedAt = now
		_ = writeCache(c)
		return Notice{}, fmt.Errorf("reading response: %w", err)
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		c.CheckedAt = now
		_ = writeCache(c)
		return Notice{}, fmt.Errorf("decoding release payload: %w", err)
	}

	tag := payload.TagName
	if !validTag(tag) {
		c.CheckedAt = now
		_ = writeCache(c)
		return Notice{}, fmt.Errorf("invalid release tag %q", tag)
	}

	c.CheckedAt = now
	c.Latest = tag
	if !newer(current, tag) {
		c.Announced = tag
		_ = writeCache(c)
		return Notice{}, nil
	}

	announce := (c.Announced != tag)
	c.Announced = tag
	_ = writeCache(c)

	return Notice{
		Latest:   tag,
		URL:      releaseURL(tag),
		Announce: announce,
	}, nil
}

// newer reports whether latest represents a newer version than current.
// Leading vX.Y.Z of each are compared as three integers. Any suffix on current
// is dropped (e.g. git describe output like v0.1.0-3-gabc1234-dirty is ahead of
// v0.1.0 and must not nag). dev, (devel), pseudo-versions, or anything
// unparseable always return false.
func newer(current, latest string) bool {
	if current == "" || current == "dev" || current == "(devel)" {
		return false
	}
	if pseudoPattern.MatchString(current) {
		return false
	}
	if !validTag(latest) {
		return false
	}
	cMaj, cMin, cPatch, cOK := parseVersion(current, true)
	if !cOK {
		return false
	}
	if cMaj == 0 && cMin == 0 && cPatch == 0 {
		return false
	}
	lMaj, lMin, lPatch, lOK := parseVersion(latest, false)
	if !lOK {
		return false
	}
	if lMaj != cMaj {
		return lMaj > cMaj
	}
	if lMin != cMin {
		return lMin > cMin
	}
	return lPatch > cPatch
}

func parseVersion(s string, stripSuffix bool) (maj, min, patch int, ok bool) {
	if !strings.HasPrefix(s, "v") {
		return 0, 0, 0, false
	}
	s = s[1:]
	if idx := strings.IndexAny(s, "-+"); idx != -1 {
		s = s[:idx]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var err error
	maj, err = strconv.Atoi(parts[0])
	if err != nil || maj < 0 {
		return 0, 0, 0, false
	}
	min, err = strconv.Atoi(parts[1])
	if err != nil || min < 0 {
		return 0, 0, 0, false
	}
	patch, err = strconv.Atoi(parts[2])
	if err != nil || patch < 0 {
		return 0, 0, 0, false
	}
	return maj, min, patch, true
}
