// Package browser hands a URL to the operator's OS browser opener —
// `open` on macOS, `xdg-open` elsewhere — and nothing else. Callers name
// specific URLs (a Linear ticket, an OAuth authorize page); the package's
// job is only to make sure whatever string reaches here is one of those
// and not something the opener will happily hand to a different scheme
// handler.
package browser

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/mattwalters/lerp/internal/childenv"
)

// OpenEnv names the environment variable that opts in to deep-linking into
// Linear's desktop app instead of the web browser.
const OpenEnv = "LERP_OPEN"

// linearHost is the only host Open will launch. Every caller today — the
// TUI's `o`, and login's authorize URL — points at Linear's own web app, so
// an allowlist of one covers both without either copying the check: LERP-86
// is what a security gate rotting at the site nobody remembered looks like,
// and childenv's doc makes the same argument for keeping a gate to one place.
const linearHost = "linear.app"

// Openable reports whether rawURL is one Open may be handed. The string
// usually arrives over the wire — Linear's own url field, or the redirect a
// browser follows — so this gates what the URL turned out to be, not
// anything typed by hand. `open`/`xdg-open` launch whatever scheme the
// machine has a handler for, so a compromised or impersonated endpoint would
// otherwise reach a file:// path, or some third party's URL-scheme handler.
func Openable(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	// Whitespace first, because Parse allows it in a path: Linear
	// percent-encodes it, and xdg-utils honours a $BROWSER holding a %s by
	// substituting the URL into a command line and letting the shell split
	// it again — so a space is one of the few ways a URL on the right host
	// still reaches something other than a browser.
	if strings.ContainsAny(rawURL, " \t\n\r\v\f") {
		return false
	}
	// Host, not Hostname: a port is not something Linear sends, and
	// refusing one costs nothing. Parse has already lowercased the scheme;
	// the host it leaves as it found it. Userinfo is refused rather than
	// ignored — the host is still Linear's, so it redirects nobody, but
	// https://anything-at-all@linear.app/… is a browser dialog quoting an
	// attacker's string back at the operator.
	return u.Scheme == "https" && u.User == nil && strings.EqualFold(u.Host, linearHost)
}

// rewrite swaps the https:// scheme for linear:// when mode is "desktop",
// so the OS opener routes the URL straight to Linear's desktop app instead
// of bouncing through a browser first. Any other mode leaves the URL
// untouched.
func rewrite(rawURL, mode string) string {
	if mode != "desktop" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Scheme = "linear"
	return u.String()
}

// Open hands rawURL to the OS opener. The opener is a child like any other,
// so it gets its environment from childenv: a personal `xdg-open` wrapper
// that logs what it was given must not be logging the operator's Linear key.
//
// When LERP_OPEN=desktop is set, the URL is rewritten to Linear's linear://
// deep-link scheme before being passed to the opener.
func Open(rawURL string) error {
	if !Openable(rawURL) {
		return fmt.Errorf("refusing to open %s: only a plain https://%s link opens from here", rawURL, linearHost)
	}
	target := rewrite(rawURL, os.Getenv(OpenEnv))
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", target)
	default:
		c = exec.Command("xdg-open", target)
	}
	c.Env = childenv.Inherited()
	if err := c.Start(); err != nil {
		return fmt.Errorf("open %s: %w", target, err)
	}
	go c.Wait()
	return nil
}
