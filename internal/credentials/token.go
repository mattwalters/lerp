package credentials

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Where the token lives: os.UserConfigDir()/lerp/token.json. Outside the
// clone deliberately — it is the operator's credential, not the clone's
// state, so it is neither in the repo nor in .lerp/ (SCOPE invariant 1).
const (
	configSubdir = "lerp"
	tokenFile    = "token.json"
)

// token is the stored OAuth credential, as JSON on disk.
//
// ObtainedAt is carried beside ExpiresAt because the two answer different
// questions: expiry decides when to renew, while when the token was last
// obtained is what an operator reads when a session is behaving oddly.
type token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	ObtainedAt   time.Time `json:"obtained_at"`
}

// store is the token file.
//
// dir is where token.json sits; empty means the operator's config dir,
// resolved on use rather than at construction. On use, because resolving it
// must never fail an operator who set LINEAR_API_KEY and has no token file
// to find. Tests set dir to a temp dir.
type store struct{ dir string }

// path names the token file. It is the only place the location is decided,
// so a test can ask for it instead of spelling out how os.UserConfigDir
// differs between macOS and Linux.
func (s store) path() (string, error) {
	dir := s.dir
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locating the user config directory: %w", err)
		}
		dir = filepath.Join(base, configSubdir)
	}
	return filepath.Join(dir, tokenFile), nil
}

// load reads the stored token. It returns an error matching fs.ErrNotExist
// when there is no token file, which is the ordinary state of a machine
// that has never run `lerp login`.
func (s store) load() (token, error) {
	var tok token
	path, err := s.path()
	if err != nil {
		return tok, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return tok, err
	}
	if err := json.Unmarshal(data, &tok); err != nil {
		return tok, fmt.Errorf("decoding %s: %w", path, err)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		return token{}, fmt.Errorf("decoding %s: missing tokens", path)
	}
	return tok, nil
}

// save writes the token, replacing whatever was there.
//
// The staged-rename shape is internal/evidence's writeJSON, copied rather
// than shared: that package is //go:build unix and rooted at a clone, and
// this file is neither. 0600 on the file and 0700 on the directory are the
// boring floor — no keychain, three platforms of complexity avoided.
func (s store) save(tok token) error {
	path, err := s.path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating the token directory: %w", err)
	}
	data, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".token-*")
	if err != nil {
		return fmt.Errorf("creating temporary token file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	// Chmod before the write, so the secret is never on disk world-readable
	// — even though CreateTemp already makes it 0600, since the mode is the
	// promise being kept here.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing the token file: %w", err)
	}
	return nil
}
