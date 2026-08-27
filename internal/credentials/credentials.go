package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mattwalters/lerp/internal/childenv"
)

// defaultTokenEndpoint is Linear's OAuth token endpoint. Still Linear's own
// API, so invariant 8 is intact — and it is this package that speaks to it,
// which is what keeps internal/linear purely GraphQL.
const defaultTokenEndpoint = "https://api.linear.app/oauth/token"

// clientID is the single public OAuth client lerp ships: no secret, one
// application in the operator's authorized apps (SCOPE invariant 4).
//
// Empty until Matt registers the "Lerp" OAuth application in Linear — a
// one-time step only he can do, from inside Linear's own settings. Login and
// refresh both refuse cleanly while it is empty rather than sending Linear a
// request it can only refuse as an unknown client.
const clientID = ""

// refreshSkew is how long before its stated expiry an access token counts
// as expired. Five minutes rather than seconds because a pass that starts
// with a fresh token must not have it expire inside itself.
const refreshSkew = 5 * time.Minute

// Sentinel errors, matchable with errors.Is.
var (
	// ErrNoCredentials means neither credential is present: no
	// LINEAR_API_KEY, and no token file to fall back on.
	ErrNoCredentials = errors.New(`no Linear credentials: run "lerp login" or set ` + childenv.LinearAPIKeyEnv)
	// ErrLoginRequired means Linear rejected the stored refresh token —
	// revoked, or rotated past its replay grace. Only a new login fixes
	// it, so a source that sees this returns it forever after rather than
	// asking again.
	ErrLoginRequired = errors.New(`Linear session expired; run "lerp login"`)
)

// Resolve returns the source of the Authorization header lerp signs Linear
// requests with: LINEAR_API_KEY if it is set, otherwise the stored OAuth
// token, otherwise ErrNoCredentials.
//
// The result is asked once per request rather than read once at startup. An
// API key returns itself forever; a stored token renews itself when it is
// close to expiry. A nil hc gets a default client with a 30s timeout, the
// same as linear.New.
//
// Resolve consumes the environment variable: the key is captured and then
// removed from lerp's own environment, so a child spawned with a nil Env
// has nothing to inherit (childenv covers the children lerp builds an
// environment for; this covers the rest). It follows that a second Resolve
// in the same process no longer sees the key and falls through to the
// token file — both of lerp's startup paths resolve exactly once, which is
// what makes that acceptable.
func Resolve(hc *http.Client) (func(context.Context) (string, error), error) {
	return resolve(store{}, hc)
}

// resolve is Resolve with the token file's directory injected, so tests can
// point a store at a temp dir rather than depending on how os.UserConfigDir
// differs between macOS and Linux.
func resolve(s store, hc *http.Client) (func(context.Context) (string, error), error) {
	if key := os.Getenv(childenv.LinearAPIKeyEnv); key != "" {
		// Captured, then dropped from lerp's own environment. childenv keeps
		// the key out of the children lerp builds an environment for, but a
		// child spawned with a nil Env inherits this process wholesale — the
		// ordinary way to write exec.Command, and the way the next spawn
		// site will probably be written. Dropping it where it is read means
		// there is nothing left to inherit either way, and it stays with the
		// read rather than with one caller who has to remember. Not
		// containment: the original block is still in /proc/<pid>/environ on
		// Linux, and the operator's shell profile still exports it.
		os.Unsetenv(childenv.LinearAPIKeyEnv)
		// Verbatim, forever, and the token file is not even opened: a
		// personal API key goes in the header as-is and never expires.
		return func(context.Context) (string, error) { return key, nil }, nil
	}
	src, err := storedSource(s, hc)
	if err != nil {
		return nil, err
	}
	return src.header, nil
}

// envKeyIsSet reports whether LINEAR_API_KEY is set, dropping it from lerp's
// own environment in the same breath — the same rule Resolve follows, for
// the same reason: a child spawned with a nil Env inherits whatever this
// process still holds. Login and logout call this only to decide whether to
// mention that the env key still wins over what they just did; dropping it
// costs nothing since both commands are done with the process right after.
func envKeyIsSet() bool {
	if os.Getenv(childenv.LinearAPIKeyEnv) == "" {
		return false
	}
	os.Unsetenv(childenv.LinearAPIKeyEnv)
	return true
}

// storedSource loads the token file into a source that renews it. Tests use
// it directly, since a bound method value cannot be steered back to the
// source behind it and the token endpoint has to be pointed at httptest.
func storedSource(s store, hc *http.Client) (*oauthSource, error) {
	tok, err := s.load()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, ErrNoCredentials
	case err != nil:
		// A file that is there but will not be read is the same dead end,
		// and `lerp login` is still the remedy — but say what was wrong
		// with it, or an operator re-runs login at a permission problem
		// forever.
		return nil, fmt.Errorf("%w (%v)", ErrNoCredentials, err)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &oauthSource{
		store:    s,
		hc:       hc,
		endpoint: defaultTokenEndpoint,
		clientID: clientID,
		tok:      tok,
	}, nil
}

// spentError marks a failure that happened after Linear had already honoured
// the exchange. The refresh token that bought it is rotated and gone, so the
// failure is not one a retry can get past.
type spentError struct{ err error }

func (e *spentError) Error() string { return e.err.Error() }
func (e *spentError) Unwrap() error { return e.err }

func spent(err error) error { return &spentError{err: err} }

// snippet reads the head of an error response, for a message that says what
// the endpoint actually complained about.
func snippet(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return strings.TrimSpace(string(body))
}

// oauthSource is a stored token that renews itself.
//
// Everything is done under mu, refresh included: concurrent callers block on
// the one exchange rather than each starting their own. That is the single
// flight. It is cheap to hold a lock across an HTTP call here because the
// call is bounded by the client's timeout and a pass issues a handful of
// requests — a waiter mechanism would be more machinery than the contention
// it saves.
type oauthSource struct {
	store store
	hc    *http.Client
	// endpoint is the token endpoint, and clientID the public client the
	// grant names. Fields rather than the constants directly, so tests can
	// point one at an httptest.Server and give it a client to be.
	endpoint string
	clientID string

	mu  sync.Mutex
	tok token
	// dead latches a renewal that cannot come back: a refusal, or a failure
	// after Linear had already spent the refresh token on it. Every later
	// request returns the same error without touching the network — a dead
	// credential must not hammer the token endpoint once per GraphQL call.
	// Only a token file the operator has since replaced clears it (see
	// header). Collapsing these into one surfaced failure, and stretching
	// the loop's next tick, is LERP-110's job.
	dead error
}

// header returns the Authorization header value for one request, renewing
// the access token first if it is at or near expiry.
func (s *oauthSource) header(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead != nil {
		// One thing gets past the latch: the operator re-authorizing in
		// another terminal. The file is the record and memory is a cache of
		// it, and that has to hold for the latch as well, or a fresh
		// token.json sits on disk while this lerp goes on reporting an
		// expired session until it is restarted. A small file read, only
		// while dead; the token endpoint is still never touched.
		stored, err := s.store.load()
		if err != nil || stored.AccessToken == s.tok.AccessToken {
			return "", s.dead
		}
		s.tok, s.dead = stored, nil
	}
	if s.fresh() {
		return "Bearer " + s.tok.AccessToken, nil
	}
	// Expired as far as this process knows — but the operator's other lerp,
	// in another clone, shares this file and may have renewed since. Read it
	// before spending a rotation: memory is a cache of the file, and a
	// refresh token this process still holds may be one Linear has already
	// retired, which would come back as a refusal and latch a credential
	// that is in fact live. Only on the renewal path, so an ordinary request
	// still touches no disk.
	if stored, err := s.store.load(); err == nil && stored.AccessToken != s.tok.AccessToken {
		s.tok = stored
	}
	if s.fresh() {
		return "Bearer " + s.tok.AccessToken, nil
	}
	if err := s.refresh(ctx); err != nil {
		return "", err
	}
	return "Bearer " + s.tok.AccessToken, nil
}

// fresh reports whether the access token is still usable — outside the
// renewal window, not merely unexpired. Called with mu held.
func (s *oauthSource) fresh() bool {
	return time.Now().Before(s.tok.ExpiresAt.Add(-refreshSkew))
}

// refresh exchanges the refresh token for a new pair. It is called with mu
// held.
func (s *oauthSource) refresh(ctx context.Context) error {
	if s.clientID == "" {
		// Latched, because no request can change it: a build with no client
		// id cannot renew a token at all, and Linear would refuse the grant
		// as a bad client — which reads as ErrLoginRequired and sends the
		// operator after a `lerp login` that would refuse the same way.
		// Reachable only from a hand-written token file: Login refuses
		// before it ever writes one while clientID is empty.
		s.dead = errors.New("credentials: this lerp has no Linear OAuth client id, so a stored token cannot be renewed; set " + childenv.LinearAPIKeyEnv)
		return s.dead
	}
	next, err := exchange(ctx, s.hc, s.endpoint, s.clientID, s.tok.RefreshToken)
	if err != nil {
		// Latched on two grounds: a refusal, which asking again cannot get
		// past, and a failure after Linear honoured the exchange, where the
		// refresh token in memory has already been rotated away. Both would
		// otherwise re-exchange once per GraphQL call, and the second would
		// burn the file's replay grace doing it — leaving a live session
		// looking expired half an hour later.
		var afterTheFact *spentError
		if errors.Is(err, ErrLoginRequired) || errors.As(err, &afterTheFact) {
			s.dead = err
		}
		return err
	}
	// Linear rotates the refresh token, so the new one reaches disk before
	// it is adopted in memory, and a write that fails fails the refresh:
	// the in-memory token would otherwise be one nobody could recover. The
	// retry then re-exchanges with the file's older refresh token, which
	// Linear's 30-minute replay grace still honours — the same grace that
	// covers a crash between the exchange and the write.
	if err := s.store.save(next); err != nil {
		// Latched, like a refusal, and for the same reason. The exchange
		// Linear just honoured has already rotated the refresh token, so
		// retrying spends another rotation against a disk that is still
		// unwritable — and once the file's older token falls out of its
		// grace, the operator is told their session expired when what
		// actually failed was a write. The recovery the grace exists for is
		// the next lerp re-exchanging from the file, not this one asking
		// again once per GraphQL call.
		s.dead = fmt.Errorf("credentials: the renewed Linear token could not be stored: %w — fix that and run lerp again", err)
		return s.dead
	}
	s.tok = next
	return nil
}

// exchange posts one refresh_token grant. The client is public: a client id,
// no secret (SCOPE invariant 4).
func exchange(ctx context.Context, hc *http.Client, endpoint, clientID, refreshToken string) (token, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}
	next, refused, err := postGrant(ctx, hc, endpoint, form)
	switch {
	case refused:
		// The grant was refused, not fumbled: RFC 6749 §5.2 puts a revoked
		// or replayed refresh token at 400, and a client Linear will not
		// accept at 401. Asking again cannot help, so the caller latches
		// this — which is why the status and the body come along. A
		// disabled client id and an expired session read identically
		// otherwise, and only one of them is fixed by logging in again.
		return token{}, fmt.Errorf("%w (%s)", ErrLoginRequired, err)
	case err != nil:
		return token{}, fmt.Errorf("credentials: refresh: %w", err)
	}
	if next.RefreshToken == "" {
		// A server that rotates nothing sends no refresh token back. Keep
		// the one that worked rather than writing a file that cannot renew.
		next.RefreshToken = refreshToken
	}
	return next, nil
}

// postGrant posts one token-endpoint request and reports whether the
// endpoint refused the grant outright — RFC 6749 §5.2 puts a revoked,
// replayed, or otherwise bad grant at 400 or 401. Refresh and login's own
// authorization-code exchange each decide what a refusal means to their own
// caller (ErrLoginRequired for one, a plain login failure for the other), so
// this reports refused rather than choosing for them. Everything else — a
// 429 behind a shared egress IP, a 403 from a WAF rule, a 404 if the path
// moves, any 5xx — is the endpoint declining to answer rather than refusing
// the grant, and is returned as a plain error worth retrying. Errors here
// are unwrapped, for the caller's own "credentials: <verb>:" prefix.
func postGrant(ctx context.Context, hc *http.Client, endpoint string, form url.Values) (tok token, refused bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return token{}, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := hc.Do(req)
	if err != nil {
		return token{}, false, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusUnauthorized:
		return token{}, true, fmt.Errorf("%s: %s", resp.Status, snippet(resp))
	default:
		return token{}, false, fmt.Errorf("%s: %s", resp.Status, snippet(resp))
	}

	tok, err = decodeGrant(resp)
	return tok, false, err
}

// decodeGrant decodes a token endpoint's 200 response. Reached only once
// Linear has honoured the request — refusal is classified by postGrant
// before this runs — so every failure from here on is spent: the grant this
// call sent is already consumed, and asking again with it can only fail.
// Both grants decode through this one path.
func decodeGrant(resp *http.Response) (token, error) {
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return token{}, spent(fmt.Errorf("decode token response: %w", err))
	}
	if body.AccessToken == "" {
		return token{}, spent(errors.New("token endpoint returned no access token"))
	}
	if body.ExpiresIn <= int64(refreshSkew/time.Second) {
		// A lifetime shorter than the renewal window is as unusable as no
		// lifetime at all: the token arrives already inside the skew, so it
		// would be treated as expired on the very next request and on every
		// request after it — an exchange per GraphQL call, which is the one
		// thing a renewing source must never do.
		return token{}, spent(fmt.Errorf("token endpoint returned a %ds lifetime, shorter than the %s renewal window", body.ExpiresIn, refreshSkew))
	}
	now := time.Now()
	return token{
		AccessToken:  body.AccessToken,
		RefreshToken: body.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(body.ExpiresIn) * time.Second),
		ObtainedAt:   now,
	}, nil
}
