package credentials

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mattwalters/lerp/internal/linear"
)

// fakeViewer is the viewerReader a test hands Login instead of a real
// linear.HTTP, so identity confirmation costs no second httptest server.
type fakeViewer struct {
	identity linear.Identity
	err      error
}

func (f fakeViewer) ViewerIdentity(context.Context) (linear.Identity, error) {
	return f.identity, f.err
}

// baseLoginFlow returns a loginFlow pointed at srv and a temp-dir store,
// with everything but open and the token handler already wired the way
// every test below wants them.
func baseLoginFlow(t *testing.T, srv *httptest.Server, open func(string) error) loginFlow {
	t.Helper()
	return loginFlow{
		authorizeURL:  "https://linear.app/oauth/authorize",
		tokenEndpoint: srv.URL,
		clientID:      testClientID,
		store:         store{dir: t.TempDir()},
		hc:            srv.Client(),
		open:          open,
		newViewer: func(hc *http.Client, accessToken string) viewerReader {
			return fakeViewer{identity: linear.Identity{ID: "user-1", Name: "Ada Lovelace", Email: "ada@example.com"}}
		},
		timeout: 2 * time.Second,
	}
}

// authorizeParts pulls the redirect_uri, state and code_challenge a fake
// opener needs out of the authorize URL Login builds.
func authorizeParts(t *testing.T, rawURL string) (redirectURI, state, challenge string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	q := u.Query()
	return q.Get("redirect_uri"), q.Get("state"), q.Get("code_challenge")
}

// getCallback issues one GET against redirectURI with the given query
// values layered on top of it.
func getCallback(t *testing.T, redirectURI string, extra url.Values) *http.Response {
	t.Helper()
	u, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("parse redirect_uri: %v", err)
	}
	u.RawQuery = extra.Encode()
	resp, err := http.Get(u.String())
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	return resp
}

// A build with no client id refuses before opening a listener or a browser
// at all — Linear could only refuse it as an unknown client.
func TestLoginRefusesWithNoClientID(t *testing.T) {
	var out strings.Builder
	var opened bool
	flow := loginFlow{
		authorizeURL:  "https://linear.app/oauth/authorize",
		tokenEndpoint: "https://example.invalid/token",
		clientID:      "",
		store:         store{dir: t.TempDir()},
		hc:            http.DefaultClient,
		open:          func(string) error { opened = true; return nil },
		newViewer: func(hc *http.Client, accessToken string) viewerReader {
			return fakeViewer{}
		},
		timeout: time.Second,
	}
	err := login(context.Background(), &out, flow)
	if err == nil || !strings.Contains(err.Error(), "client id") {
		t.Fatalf("err = %v, want the missing client id named", err)
	}
	if opened {
		t.Error("a browser was opened despite no client id being configured")
	}
}

// The happy path: a fake opener that performs the callback GET itself,
// recomputing S256(code_verifier) at the token endpoint the way Linear
// would. Writes a loadable token file and prints the identity.
func TestLoginHappyPath(t *testing.T) {
	challengeCh := make(chan string, 1)
	var out strings.Builder

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
			return
		}
		if got := r.Form.Get("grant_type"); got != "authorization_code" {
			t.Errorf("grant_type = %q, want authorization_code", got)
		}
		if got := r.Form.Get("code"); got != "auth-code-1" {
			t.Errorf("code = %q", got)
		}
		if got := r.Form.Get("client_id"); got != testClientID {
			t.Errorf("client_id = %q", got)
		}
		verifier := r.Form.Get("code_verifier")
		want := <-challengeCh
		if got := pkceChallenge(verifier); got != want {
			t.Errorf("S256(code_verifier) = %q, want the challenge shown on the authorize leg %q", got, want)
		}
		_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)

	flow := baseLoginFlow(t, srv, func(rawURL string) error {
		redirectURI, state, challenge := authorizeParts(t, rawURL)
		challengeCh <- challenge
		resp := getCallback(t, redirectURI, url.Values{"state": {state}, "code": {"auth-code-1"}})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("callback status = %d", resp.StatusCode)
		}
		return nil
	})

	if err := login(context.Background(), &out, flow); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(out.String(), "signed in to Linear as Ada Lovelace <ada@example.com>") {
		t.Errorf("output %q does not print the identity", out.String())
	}
	tok := readToken(t, flow.store)
	if tok.AccessToken != "access-1" || tok.RefreshToken != "refresh-1" {
		t.Errorf("stored token = %+v", tok)
	}
}

// A mismatched state is a hard error: no retry, nothing stored.
func TestLoginStateMismatchFailsWithNothingStored(t *testing.T) {
	var out strings.Builder
	var tokenCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)

	flow := baseLoginFlow(t, srv, func(rawURL string) error {
		redirectURI, _, _ := authorizeParts(t, rawURL)
		resp := getCallback(t, redirectURI, url.Values{"state": {"wrong-state"}, "code": {"auth-code-1"}})
		resp.Body.Close()
		return nil
	})

	err := login(context.Background(), &out, flow)
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("err = %v, want the state mismatch named", err)
	}
	if tokenCalls != 0 {
		t.Errorf("tokenCalls = %d, want 0 — a mismatched state must never reach the token endpoint", tokenCalls)
	}
	if _, loadErr := flow.store.load(); loadErr == nil {
		t.Error("a token was stored despite the state mismatch")
	}
}

// error= on the callback is a crisp refusal, verbatim, and nothing stored.
func TestLoginErrorParamIsACrispRefusal(t *testing.T) {
	var out strings.Builder
	var tokenCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
	}))
	t.Cleanup(srv.Close)

	flow := baseLoginFlow(t, srv, func(rawURL string) error {
		redirectURI, state, _ := authorizeParts(t, rawURL)
		resp := getCallback(t, redirectURI, url.Values{"state": {state}, "error": {"access_denied"}})
		resp.Body.Close()
		return nil
	})

	err := login(context.Background(), &out, flow)
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v, want access_denied named", err)
	}
	if tokenCalls != 0 {
		t.Errorf("tokenCalls = %d, want 0", tokenCalls)
	}
	if _, loadErr := flow.store.load(); loadErr == nil {
		t.Error("a token was stored despite the refusal")
	}
}

// A request to any path but /callback 404s and does not complete the flow —
// the real callback, hit afterwards, still succeeds.
func TestLoginStrayPathDoesNotCompleteTheFlow(t *testing.T) {
	challengeCh := make(chan string, 1)
	var out strings.Builder

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
			return
		}
		verifier := r.Form.Get("code_verifier")
		want := <-challengeCh
		if got := pkceChallenge(verifier); got != want {
			t.Errorf("S256(code_verifier) mismatch")
		}
		_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)

	flow := baseLoginFlow(t, srv, func(rawURL string) error {
		redirectURI, state, challenge := authorizeParts(t, rawURL)
		challengeCh <- challenge
		u, err := url.Parse(redirectURI)
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/favicon.ico"
		strayResp, err := http.Get(u.String())
		if err != nil {
			t.Fatal(err)
		}
		strayResp.Body.Close()
		if strayResp.StatusCode != http.StatusNotFound {
			t.Errorf("stray path status = %d, want 404", strayResp.StatusCode)
		}
		resp := getCallback(t, redirectURI, url.Values{"state": {state}, "code": {"auth-code-1"}})
		resp.Body.Close()
		return nil
	})

	if err := login(context.Background(), &out, flow); err != nil {
		t.Fatalf("login: %v", err)
	}
	tok := readToken(t, flow.store)
	if tok.AccessToken != "access-1" {
		t.Errorf("stored token = %+v", tok)
	}
}

// No callback within the injected deadline fires the timeout, nothing
// stored.
func TestLoginTimesOutWithoutACallback(t *testing.T) {
	var out strings.Builder
	var tokenCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
	}))
	t.Cleanup(srv.Close)

	flow := baseLoginFlow(t, srv, func(rawURL string) error { return nil })
	flow.timeout = 20 * time.Millisecond

	err := login(context.Background(), &out, flow)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if tokenCalls != 0 {
		t.Errorf("tokenCalls = %d, want 0", tokenCalls)
	}
}

// A 400 from the token endpoint surfaces its body, and nothing is stored.
func TestLoginSurfacesTokenEndpointRefusal(t *testing.T) {
	var out strings.Builder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	flow := baseLoginFlow(t, srv, func(rawURL string) error {
		redirectURI, state, _ := authorizeParts(t, rawURL)
		resp := getCallback(t, redirectURI, url.Values{"state": {state}, "code": {"auth-code-1"}})
		resp.Body.Close()
		return nil
	})

	err := login(context.Background(), &out, flow)
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("err = %v, want the endpoint's body named", err)
	}
	if errors.Is(err, ErrLoginRequired) {
		t.Errorf("a login exchange refusal reported as an expired session: %v", err)
	}
	if _, loadErr := flow.store.load(); loadErr == nil {
		t.Error("a token was stored despite the refusal")
	}
}

// The loopback listener is closed before login returns, not after.
func TestLoginListenerRefusesConnectionsAfterReturn(t *testing.T) {
	challengeCh := make(chan string, 1)
	var out strings.Builder
	var port string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
			return
		}
		<-challengeCh
		_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)

	flow := baseLoginFlow(t, srv, func(rawURL string) error {
		redirectURI, state, challenge := authorizeParts(t, rawURL)
		u, err := url.Parse(redirectURI)
		if err != nil {
			t.Fatal(err)
		}
		port = u.Port()
		challengeCh <- challenge
		resp := getCallback(t, redirectURI, url.Values{"state": {state}, "code": {"auth-code-1"}})
		resp.Body.Close()
		return nil
	})

	if err := login(context.Background(), &out, flow); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second); err == nil {
		t.Error("the loopback port still accepts connections after login returned")
	}
}
