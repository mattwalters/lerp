package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattwalters/lerp/internal/childenv"
)

// tempStore returns a store in a temp dir, holding tok when one is given.
func tempStore(t *testing.T, tok *token) store {
	t.Helper()
	s := store{dir: t.TempDir()}
	if tok != nil {
		if err := s.save(*tok); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	return s
}

// testClientID stands in for the public client a human still has to
// register in Linear; the shipped constant is empty until then.
const testClientID = "lerp-test-client"

func liveToken() token {
	now := time.Now()
	return token{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    now.Add(time.Hour),
		ObtainedAt:   now,
	}
}

// expiredSource is a source whose stored access token has already expired,
// with its token endpoint pointed at a server running h. The returned
// counter holds how many exchanges h has answered.
func expiredSource(t *testing.T, h http.HandlerFunc) (*oauthSource, store, *int) {
	t.Helper()
	expired := liveToken()
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	s := tempStore(t, &expired)

	exchanges := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*exchanges++
		h(w, r)
	}))
	t.Cleanup(srv.Close)

	source, err := storedSource(s, srv.Client())
	if err != nil {
		t.Fatalf("storedSource: %v", err)
	}
	source.endpoint = srv.URL
	source.clientID = testClientID
	return source, s, exchanges
}

func readToken(t *testing.T, s store) token {
	t.Helper()
	tok, err := s.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return tok
}

// The Done-when, read literally: an operator with LINEAR_API_KEY set sees
// zero change. The token file beside it is poisoned — decoding it would
// fail — so the key coming back verbatim is proof it was never opened.
//
// Poisoned by content rather than by mode: a test running as root reads a
// 0000 file happily, and CI containers do run as root.
func TestResolveEnvKeyWins(t *testing.T) {
	s := store{dir: t.TempDir()}
	path, err := s.path()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write poison: %v", err)
	}
	t.Setenv(childenv.LinearAPIKeyEnv, "lin_api_key")

	src, err := resolve(s, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got, err := src(context.Background())
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	// Verbatim: a personal API key carries no "Bearer " prefix.
	if got != "lin_api_key" {
		t.Errorf("header = %q, want %q", got, "lin_api_key")
	}
	// And the read consumed it: whoever reads the credential drops it, so a
	// child spawned with a nil Env has nothing to inherit. The closure still
	// answers with the key it captured before that.
	if v, ok := os.LookupEnv(childenv.LinearAPIKeyEnv); ok {
		t.Errorf("%s still set to %q after Resolve; it should be dropped from lerp's own environment", childenv.LinearAPIKeyEnv, v)
	}
}

// The consequence of that drop, said out loud because it is surprising: a
// second Resolve in the same process no longer sees the key. Both of lerp's
// startup paths resolve exactly once, so this is a documented property
// rather than a bug — but it should fail loudly if it ever stops being true
// of the doc comment.
func TestResolveConsumesTheEnvKey(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "lin_api_key")
	s := tempStore(t, nil)

	if _, err := resolve(s, nil); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	// No token file to fall back on, so the second resolve refuses.
	if _, err := resolve(s, nil); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("second resolve error = %v, want ErrNoCredentials", err)
	}
}

// A hand-written token file authenticates, and the header it produces is
// what a linear client actually puts on the wire.
func TestResolveStoredToken(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	s := tempStore(t, ptr(liveToken()))

	src, err := resolve(s, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got, err := src(context.Background())
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	if got != "Bearer access-1" {
		t.Errorf("header = %q, want %q", got, "Bearer access-1")
	}
}

// An expired access token with a live refresh token renews transparently,
// and concurrent callers share the one exchange rather than each starting
// their own.
func TestRefreshUnderConcurrency(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	expired := liveToken()
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	s := tempStore(t, &expired)

	var mu sync.Mutex
	var exchanges int
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		mu.Lock()
		exchanges++
		gotForm = r.Form
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-2","refresh_token":"refresh-2","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)

	// storedSource rather than resolve, so the token endpoint can be pointed
	// at the test server; resolve builds the same source with Linear's own.
	source, err := storedSource(s, srv.Client())
	if err != nil {
		t.Fatalf("storedSource: %v", err)
	}
	source.endpoint = srv.URL
	source.clientID = testClientID
	src := source.header

	const callers = 8
	headers := make([]string, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			headers[i], errs[i] = src(context.Background())
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		if headers[i] != "Bearer access-2" {
			t.Errorf("caller %d header = %q, want %q", i, headers[i], "Bearer access-2")
		}
	}
	if exchanges != 1 {
		t.Errorf("exchanges = %d, want 1 — the refresh is not single-flight", exchanges)
	}
	if got := gotForm.Get("grant_type"); got != "refresh_token" {
		t.Errorf("grant_type = %q", got)
	}
	if got := gotForm.Get("refresh_token"); got != "refresh-1" {
		t.Errorf("refresh_token = %q, want refresh-1", got)
	}
	// A public client: the id, and no secret anywhere in the grant.
	if got := gotForm.Get("client_id"); got != testClientID {
		t.Errorf("client_id = %q, want %q", got, testClientID)
	}
	if got := gotForm.Get("client_secret"); got != "" {
		t.Errorf("client_secret = %q, want it absent", got)
	}

	// The rotated refresh token is on disk, or the next process is holding
	// one Linear will not honour.
	stored := readToken(t, s)
	if stored.RefreshToken != "refresh-2" || stored.AccessToken != "access-2" {
		t.Errorf("stored token = %+v, want the rotated pair", stored)
	}
	if stored.ObtainedAt.IsZero() || !stored.ExpiresAt.After(time.Now()) {
		t.Errorf("stored token times = %v / %v", stored.ObtainedAt, stored.ExpiresAt)
	}

	path, err := s.path()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file mode = %v, want 0600", perm)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != tokenFile {
			t.Errorf("leftover file beside the token: %s", e.Name())
		}
	}
}

// A refresh Linear refuses is one actionable error, latched: the endpoint is
// asked once and never again.
func TestRejectedRefreshIsLatched(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	expired := liveToken()
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	s := tempStore(t, &expired)

	var exchanges int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	source, err := storedSource(s, srv.Client())
	if err != nil {
		t.Fatalf("storedSource: %v", err)
	}
	source.endpoint = srv.URL
	source.clientID = testClientID
	src := source.header

	for i := range 3 {
		if _, err := src(context.Background()); !errors.Is(err, ErrLoginRequired) {
			t.Fatalf("call %d: err = %v, want ErrLoginRequired", i, err)
		}
	}
	if exchanges != 1 {
		t.Errorf("exchanges = %d, want 1 — a dead credential is hammering the token endpoint", exchanges)
	}
	// The refused refresh token is still on disk: nothing overwrote it, so a
	// rotation Linear did honour and lerp did not see is still recoverable.
	if stored := readToken(t, s); stored.RefreshToken != "refresh-1" {
		t.Errorf("stored refresh token = %q, want it untouched", stored.RefreshToken)
	}
}

// A refusal the token endpoint did not make — a 500, a network failure — is
// not latched: it is worth trying again.
func TestServerErrorIsNotLatched(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	expired := liveToken()
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	s := tempStore(t, &expired)

	var exchanges int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		if exchanges == 1 {
			http.Error(w, "upstream is down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"access-2","refresh_token":"refresh-2","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)

	source, err := storedSource(s, srv.Client())
	if err != nil {
		t.Fatalf("storedSource: %v", err)
	}
	source.endpoint = srv.URL
	source.clientID = testClientID
	src := source.header

	_, err = src(context.Background())
	if err == nil {
		t.Fatal("first call: want an error")
	}
	if errors.Is(err, ErrLoginRequired) {
		t.Errorf("a 502 latched as ErrLoginRequired: %v", err)
	}
	got, err := src(context.Background())
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got != "Bearer access-2" {
		t.Errorf("header = %q, want the renewed token", got)
	}
}

// A token endpoint that declines to answer — rate limited — is not a refused
// grant, and must not latch: `lerp login` is unauthenticated from the same
// address and could not help.
func TestRateLimitIsNotLatched(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	expired := liveToken()
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	s := tempStore(t, &expired)

	var exchanges int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		if exchanges == 1 {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"access-2","refresh_token":"refresh-2","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)

	source, err := storedSource(s, srv.Client())
	if err != nil {
		t.Fatalf("storedSource: %v", err)
	}
	source.endpoint = srv.URL
	source.clientID = testClientID

	_, err = source.header(context.Background())
	if err == nil {
		t.Fatal("first call: want an error")
	}
	if errors.Is(err, ErrLoginRequired) {
		t.Errorf("a 429 latched as an expired session: %v", err)
	}
	if !strings.Contains(err.Error(), "slow down") {
		t.Errorf("error %q does not say what the endpoint complained about", err)
	}
	got, err := source.header(context.Background())
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got != "Bearer access-2" {
		t.Errorf("header = %q, want the renewed token", got)
	}
}

// Two lerps share one token file. When the other one has already renewed,
// this one adopts what is on disk rather than spending a rotation on a
// refresh token Linear has retired — which would come back a refusal and
// latch a credential that is live.
func TestRenewalPrefersTheFileOverStaleMemory(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	expired := liveToken()
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	s := tempStore(t, &expired)

	var exchanges int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	source, err := storedSource(s, srv.Client())
	if err != nil {
		t.Fatalf("storedSource: %v", err)
	}
	source.endpoint = srv.URL
	source.clientID = testClientID

	// The other lerp renews and writes; this one still holds access-1.
	renewed := liveToken()
	renewed.AccessToken, renewed.RefreshToken = "access-2", "refresh-2"
	if err := s.save(renewed); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := source.header(context.Background())
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	if got != "Bearer access-2" {
		t.Errorf("header = %q, want the other process's token", got)
	}
	if exchanges != 0 {
		t.Errorf("exchanges = %d, want 0 — a rotation was spent on a token already superseded", exchanges)
	}
}

// A refresh whose write fails is a failed refresh — adopting a token nobody
// can recover is worse than not renewing — and it is latched, so a pass does
// not keep spending rotations against a disk that cannot hold the result.
func TestRefreshWriteFailureIsLatched(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	expired := liveToken()
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	s := tempStore(t, &expired)

	var exchanges int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		_, _ = w.Write([]byte(`{"access_token":"access-2","refresh_token":"refresh-2","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)

	source, err := storedSource(s, srv.Client())
	if err != nil {
		t.Fatalf("storedSource: %v", err)
	}
	source.endpoint = srv.URL
	source.clientID = testClientID
	// The token loaded, then the disk went bad underneath it. Bad in a way
	// root cannot shrug off: the directory's parent is a regular file, so
	// MkdirAll fails with ENOTDIR whoever is asking.
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "wall"), nil, 0o600); err != nil {
		t.Fatalf("write wall: %v", err)
	}
	source.store = store{dir: filepath.Join(base, "wall", "lerp")}

	for i := range 3 {
		_, err := source.header(context.Background())
		if err == nil {
			t.Fatalf("call %d: want an error", i)
		}
		if !strings.Contains(err.Error(), "could not be stored") {
			t.Errorf("call %d: err = %v, want the write failure named", i, err)
		}
		if errors.Is(err, ErrLoginRequired) {
			t.Errorf("call %d: a write failure reported as an expired session: %v", i, err)
		}
	}
	if exchanges != 1 {
		t.Errorf("exchanges = %d, want 1 — a failed write is rotating the refresh token once per request", exchanges)
	}
	// Not adopted: the retry, in the next lerp, must re-exchange with the
	// refresh token still on disk, inside Linear's replay grace.
	if source.tok.AccessToken != "access-1" {
		t.Errorf("in-memory access token = %q, want the unsaved one rejected", source.tok.AccessToken)
	}
	if stored := readToken(t, s); stored.RefreshToken != "refresh-1" {
		t.Errorf("stored refresh token = %q, want it untouched", stored.RefreshToken)
	}
}

// A lifetime shorter than the renewal window is refused rather than adopted:
// adopting it would mean an exchange per GraphQL call.
func TestShortLifetimeIsRefused(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	source, s, exchanges := expiredSource(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"access-2","refresh_token":"refresh-2","expires_in":60}`))
	})
	for i := range 3 {
		_, err := source.header(context.Background())
		if err == nil || !strings.Contains(err.Error(), "renewal window") {
			t.Fatalf("call %d: err = %v, want the short lifetime refused", i, err)
		}
	}
	// Latched, and asked exactly once: Linear honoured that exchange, so the
	// refresh token this source still holds is already rotated away. Asking
	// again could only fail, and would spend the file's replay grace doing
	// it — the very "exchange per GraphQL call" this guard exists to stop.
	if *exchanges != 1 {
		t.Errorf("exchanges = %d, want 1", *exchanges)
	}
	if stored := readToken(t, s); stored.AccessToken != "access-1" {
		t.Errorf("an unusable token was stored: %+v", stored)
	}
}

// A response that arrives after Linear honoured the exchange but cannot be
// used — a proxy's HTML error page where the token pair should be — is
// latched for the same reason: the rotation has already been spent.
func TestUndecodableExchangeIsLatched(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	source, s, exchanges := expiredSource(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>gateway timeout</html>"))
	})
	for i := range 3 {
		if _, err := source.header(context.Background()); err == nil {
			t.Fatalf("call %d: want an error", i)
		}
	}
	if *exchanges != 1 {
		t.Errorf("exchanges = %d, want 1", *exchanges)
	}
	if stored := readToken(t, s); stored.AccessToken != "access-1" {
		t.Errorf("stored token = %+v, want it untouched", stored)
	}
}

// A 403 is not a refused grant — a WAF rule, not a revoked session — so it
// must not latch. Same for the 404 an endpoint that moved would give.
func TestNonGrantRefusalsAreNotLatched(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Setenv(childenv.LinearAPIKeyEnv, "")
			var calls int
			source, _, exchanges := expiredSource(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					http.Error(w, "not today", status)
					return
				}
				_, _ = w.Write([]byte(`{"access_token":"access-2","refresh_token":"refresh-2","expires_in":3600}`))
			})
			_, err := source.header(context.Background())
			if err == nil {
				t.Fatal("first call: want an error")
			}
			if errors.Is(err, ErrLoginRequired) {
				t.Errorf("a %d latched as an expired session: %v", status, err)
			}
			got, err := source.header(context.Background())
			if err != nil {
				t.Fatalf("second call: %v", err)
			}
			if got != "Bearer access-2" {
				t.Errorf("header = %q, want the renewed token", got)
			}
			if *exchanges != 2 {
				t.Errorf("exchanges = %d, want 2", *exchanges)
			}
		})
	}
}

// A refusal names what came back, so a disabled client id does not read as
// an expired session an operator can log their way out of.
func TestRefusalCarriesTheEndpointsAnswer(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	source, _, _ := expiredSource(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusBadRequest)
	})
	_, err := source.header(context.Background())
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want ErrLoginRequired", err)
	}
	if !strings.Contains(err.Error(), "invalid_client") {
		t.Errorf("error %q does not say what the endpoint answered", err)
	}
}

// The latch is memory too, and the file outranks memory: an operator who
// re-authorizes in another terminal does not have to restart this lerp.
func TestReloginOnDiskClearsTheLatch(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	source, s, exchanges := expiredSource(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	})
	if _, err := source.header(context.Background()); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want ErrLoginRequired", err)
	}
	// Still latched while the file holds the token that died.
	if _, err := source.header(context.Background()); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want the latch to hold", err)
	}

	relogin := liveToken()
	relogin.AccessToken, relogin.RefreshToken = "access-9", "refresh-9"
	if err := s.save(relogin); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := source.header(context.Background())
	if err != nil {
		t.Fatalf("after re-login: %v", err)
	}
	if got != "Bearer access-9" {
		t.Errorf("header = %q, want the re-authorized token", got)
	}
	if *exchanges != 1 {
		t.Errorf("exchanges = %d, want 1 — the latch let a request through", *exchanges)
	}
}

// No credential at all names both remedies.
func TestResolveNoCredentials(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	_, err := resolve(store{dir: t.TempDir()}, nil)
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}
	for _, want := range []string{"lerp login", childenv.LinearAPIKeyEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// A token file that is there but will not decode is the same dead end, and
// says what was wrong with it.
func TestResolveUndecodableTokenFile(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	s := store{dir: t.TempDir()}
	path, err := s.path()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = resolve(s, nil)
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the file", err)
	}
}

// The store round-trips, and reports a missing file as fs.ErrNotExist so
// resolve can tell "never logged in" from "cannot read it".
func TestStoreRoundTrip(t *testing.T) {
	s := store{dir: filepath.Join(t.TempDir(), "nested")}
	if _, err := s.load(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("load of a missing file: %v, want fs.ErrNotExist", err)
	}
	want := liveToken()
	if err := s.save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := readToken(t, s)
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) || !got.ObtainedAt.Equal(want.ObtainedAt) {
		t.Errorf("times did not round trip: %+v", got)
	}
	// The directory save had to create is the operator's alone.
	info, err := os.Stat(s.dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("token dir mode = %v, want 0700", perm)
	}
	// A second save replaces rather than appends, and leaves nothing behind.
	if err := s.save(want); err != nil {
		t.Fatalf("second save: %v", err)
	}
	var decoded token
	data, err := os.ReadFile(filepath.Join(s.dir, tokenFile))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode after replace: %v", err)
	}
}

// The default store is the operator's config dir, not the clone's.
func TestDefaultStorePath(t *testing.T) {
	path, err := store{}.path()
	if err != nil {
		t.Skipf("no user config dir here: %v", err)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	if want := filepath.Join(base, configSubdir, tokenFile); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func ptr[T any](v T) *T { return &v }
