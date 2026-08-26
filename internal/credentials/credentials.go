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
)

// apiKeyEnv holds a Linear personal API key. It is the credential lerp has
// always read, and it still wins: an operator who exports it gets exactly
// the behaviour they had before `lerp login` existed, with nothing on disk
// consulted.
const apiKeyEnv = "LINEAR_API_KEY"

// defaultTokenEndpoint is Linear's OAuth token endpoint. Still Linear's own
// API, so invariant 8 is intact — and it is this package that speaks to it,
// which is what keeps internal/linear purely GraphQL.
const defaultTokenEndpoint = "https://api.linear.app/oauth/token"

// clientID is the single public OAuth client lerp ships: no secret, one
// application in the operator's authorized apps (SCOPE invariant 4).
//
// Empty until LERP-109 registers the application. Nothing can have written
// a token file before the command that creates one exists, so no refresh
// can reach the endpoint with it empty.
const clientID = ""

// refreshSkew is how long before its stated expiry an access token counts
// as expired. Five minutes rather than seconds because a pass that starts
// with a fresh token must not have it expire inside itself.
const refreshSkew = 5 * time.Minute

// Sentinel errors, matchable with errors.Is.
var (
	// ErrNoCredentials means neither credential is present: no
	// LINEAR_API_KEY, and no token file to fall back on.
	ErrNoCredentials = errors.New(`no Linear credentials: run "lerp login" or set ` + apiKeyEnv)
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
func Resolve(hc *http.Client) (func(context.Context) (string, error), error) {
	return resolve(store{}, hc)
}

// resolve is Resolve with the token file's directory injected, so tests can
// point a store at a temp dir rather than depending on how os.UserConfigDir
// differs between macOS and Linux.
func resolve(s store, hc *http.Client) (func(context.Context) (string, error), error) {
	if key := os.Getenv(apiKeyEnv); key != "" {
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
	// dead latches a refusal. Once Linear has rejected the refresh token,
	// every later request returns the same error without touching the
	// network: a dead credential must not hammer the token endpoint once
	// per GraphQL call. Collapsing these into one surfaced failure, and
	// stretching the loop's next tick, is LERP-110's job.
	dead error
}

// header returns the Authorization header value for one request, renewing
// the access token first if it is at or near expiry.
func (s *oauthSource) header(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead != nil {
		return "", s.dead
	}
	if time.Now().Before(s.tok.ExpiresAt.Add(-refreshSkew)) {
		return "Bearer " + s.tok.AccessToken, nil
	}
	if err := s.refresh(ctx); err != nil {
		return "", err
	}
	return "Bearer " + s.tok.AccessToken, nil
}

// refresh exchanges the refresh token for a new pair. It is called with mu
// held.
func (s *oauthSource) refresh(ctx context.Context) error {
	if s.clientID == "" {
		// Latched, because no request can change it: a build with no client
		// id cannot renew a token at all, and Linear would refuse the grant
		// as a bad client — which reads as ErrLoginRequired and sends the
		// operator after a `lerp login` this build does not have. Reachable
		// only from a hand-written token file until LERP-109 fills the
		// constant in.
		s.dead = errors.New("credentials: this lerp has no Linear OAuth client id, so a stored token cannot be renewed; set " + apiKeyEnv)
		return s.dead
	}
	next, err := exchange(ctx, s.hc, s.endpoint, s.clientID, s.tok.RefreshToken)
	if err != nil {
		if errors.Is(err, ErrLoginRequired) {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return token{}, fmt.Errorf("credentials: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := hc.Do(req)
	if err != nil {
		return token{}, fmt.Errorf("credentials: refresh: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// The grant was refused, not fumbled: a revoked token, or a
		// rotated one past its grace. Asking again cannot help.
		return token{}, ErrLoginRequired
	default:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return token{}, fmt.Errorf("credentials: refresh: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return token{}, fmt.Errorf("credentials: decode refresh response: %w", err)
	}
	if body.AccessToken == "" {
		return token{}, errors.New("credentials: refresh: token endpoint returned no access token")
	}
	if body.ExpiresIn <= int64(refreshSkew/time.Second) {
		// A lifetime shorter than the renewal window is as unusable as no
		// lifetime at all: the token arrives already inside the skew, so it
		// would be treated as expired on the very next request and on every
		// request after it — an exchange per GraphQL call, which is the one
		// thing this source must never do.
		return token{}, fmt.Errorf("credentials: refresh: token endpoint returned a %ds lifetime, shorter than the %s renewal window", body.ExpiresIn, refreshSkew)
	}
	now := time.Now()
	next := token{
		AccessToken:  body.AccessToken,
		RefreshToken: body.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(body.ExpiresIn) * time.Second),
		ObtainedAt:   now,
	}
	if next.RefreshToken == "" {
		// A server that rotates nothing sends no refresh token back. Keep
		// the one that worked rather than writing a file that cannot renew.
		next.RefreshToken = refreshToken
	}
	return next, nil
}
