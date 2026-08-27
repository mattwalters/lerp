package browser

import "testing"

// The URL is Linear's own, but `open`/`xdg-open` launch whatever scheme the
// machine has a handler for. So an endpoint that is not Linear cannot reach
// the operator's file:// paths, or their editor's URL handler, through one
// keystroke.
func TestOnlyALinearHTTPSURLIsOpenable(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://linear.app/acme/issue/LERP-1/first", true},
		{"https://linear.app/l/LERP-1", true},
		{"HTTPS://LINEAR.APP/acme/issue/LERP-1", true}, // the host is case-insensitive
		{"", false},
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"vscode://file/etc/passwd", false},
		{"http://linear.app/acme/issue/LERP-1", false},     // plaintext is not the door
		{"https://linear.app.evil.test/acme", false},       // a longer host that starts with it
		{"https://evil.linear.app/acme", false},            // a subdomain is not the host either
		{"https://evil.test/linear.app/acme", false},       // a path that reads like the host
		{"https://linear.app@evil.test/acme", false},       // userinfo, and the host is theirs
		{"https://attacker:secret@linear.app/acme", false}, // userinfo, and the host is Linear's
		{"https://linear.app:8443/acme", false},            // Linear serves one port
		{"https://linear.app/x ;id", false},                // a space is a command line to xdg-open's $BROWSER
		{"//linear.app/acme", false},                       // right host, no scheme at all
		{"-froot@evil.test", false},                        // parses fine; the scheme check is what stops it
		{"https://linear.app\x00/acme", false},             // a control byte Parse refuses outright
	} {
		if got := Openable(tc.url); got != tc.want {
			t.Errorf("Openable(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

// An empty or otherwise refused URL does not silently succeed.
func TestOpenRefusesWhatOpenableRefuses(t *testing.T) {
	if err := Open("file:///etc/passwd"); err == nil {
		t.Fatal("Open of a non-Linear URL returned no error")
	}
}

// When LERP_OPEN=desktop is set, non-Linear URLs are still refused.
func TestOpenRefusesWhatOpenableRefusesUnderDesktop(t *testing.T) {
	t.Setenv(OpenEnv, "desktop")
	if err := Open("file:///etc/passwd"); err == nil {
		t.Fatal("Open of a non-Linear URL returned no error under LERP_OPEN=desktop")
	}
}

// When LERP_OPEN=desktop is set, rewrite swaps the scheme for linear://.
// Any other value or unset leaves the URL untouched.
func TestRewriteSwapsSchemeForDesktop(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		mode string
		want string
	}{
		{
			name: "desktop mode rewrites https to linear",
			url:  "https://linear.app/acme/issue/LERP-1/first",
			mode: "desktop",
			want: "linear://linear.app/acme/issue/LERP-1/first",
		},
		{
			name: "desktop mode preserves query and fragment",
			url:  "https://linear.app/acme/issue/LERP-1?filter=true#anchor",
			mode: "desktop",
			want: "linear://linear.app/acme/issue/LERP-1?filter=true#anchor",
		},
		{
			name: "unset mode leaves url untouched",
			url:  "https://linear.app/acme/issue/LERP-1/first",
			mode: "",
			want: "https://linear.app/acme/issue/LERP-1/first",
		},
		{
			name: "other mode leaves url untouched",
			url:  "https://linear.app/acme/issue/LERP-1/first",
			mode: "browser",
			want: "https://linear.app/acme/issue/LERP-1/first",
		},
		{
			name: "case-sensitive mode check does not rewrite for uppercase",
			url:  "https://linear.app/acme/issue/LERP-1/first",
			mode: "DESKTOP",
			want: "https://linear.app/acme/issue/LERP-1/first",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rewrite(tc.url, tc.mode); got != tc.want {
				t.Errorf("rewrite(%q, %q) = %q, want %q", tc.url, tc.mode, got, tc.want)
			}
		})
	}
}
