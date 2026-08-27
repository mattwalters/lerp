package credentials

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/mattwalters/lerp/internal/browser"
	"github.com/mattwalters/lerp/internal/childenv"
	"github.com/mattwalters/lerp/internal/linear"
)

// defaultAuthorizeURL is Linear's OAuth authorize page — a browser goes
// here, never lerp's own HTTP (that stays on the token and revoke endpoints,
// both on api.linear.app; see internal/browser's linearHost comment).
const defaultAuthorizeURL = "https://linear.app/oauth/authorize"

// loginTimeout is how long Login waits for the callback before giving up.
// Long enough for an operator to actually look at the consent screen, short
// enough that an abandoned login does not hold the loopback port forever.
const loginTimeout = 2 * time.Minute

// viewerReader is the one method Login needs of a Linear client: confirming
// who it just signed in as. A one-method interface, the way initcmd holds
// Board, so a test can fake it without a second httptest server for
// GraphQL.
type viewerReader interface {
	ViewerIdentity(ctx context.Context) (linear.Identity, error)
}

// loginFlow carries everything the loopback PKCE flow needs, injected the
// same way oauthSource injects endpoint and clientID: so a test can drive
// the whole thing against httptest with no browser and no network.
type loginFlow struct {
	authorizeURL  string
	tokenEndpoint string
	clientID      string
	store         store
	hc            *http.Client
	open          func(rawURL string) error
	newViewer     func(hc *http.Client, accessToken string) viewerReader
	timeout       time.Duration
}

// Login runs the loopback PKCE flow end to end (RFC 8252): bind an ephemeral
// port, send the operator to Linear's own authorize page, catch the
// redirect, exchange the code, store the result, and confirm by reading the
// identity behind the token it just obtained.
func Login(ctx context.Context, out io.Writer) error {
	return login(ctx, out, loginFlow{
		authorizeURL:  defaultAuthorizeURL,
		tokenEndpoint: defaultTokenEndpoint,
		clientID:      clientID,
		store:         store{},
		hc:            &http.Client{Timeout: 30 * time.Second},
		open:          browser.Open,
		newViewer: func(hc *http.Client, accessToken string) viewerReader {
			auth := func(context.Context) (string, error) { return "Bearer " + accessToken, nil }
			return linear.New(auth, hc)
		},
		timeout: loginTimeout,
	})
}

// closeTabPage is what every response from the callback carries, success or
// refusal alike: no assets, no request values echoed back into it, and no
// claim of success before login has checked state and error — a refused or
// mismatched callback must not render a page telling the operator they are
// signed in. What actually happened is reported on out and in the returned
// error, never in the page a browser renders.
const closeTabPage = `<!DOCTYPE html>
<html><head><title>lerp</title></head>
<body>lerp has received Linear's response. You can close this tab and return to your terminal.</body>
</html>
`

// callbackResult is what the /callback handler hands back to login: either
// the authorization code, or the reason the flow is refused.
type callbackResult struct {
	code string
	err  error
}

func login(ctx context.Context, out io.Writer, flow loginFlow) error {
	if flow.clientID == "" {
		// No request can fix this: a build with no client id would only
		// reach Linear to be refused as an unknown client. Refused here,
		// before a listener or a browser is even opened.
		return errors.New("credentials: this lerp has no Linear OAuth client id built in; login cannot proceed")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("credentials: login: open loopback listener: %w", err)
	}

	verifier, err := randomURLSafe(32)
	if err != nil {
		listener.Close()
		return fmt.Errorf("credentials: login: generate PKCE verifier: %w", err)
	}
	state, err := randomURLSafe(32)
	if err != nil {
		listener.Close()
		return fmt.Errorf("credentials: login: generate state: %w", err)
	}
	challenge := pkceChallenge(verifier)

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	authorize := buildAuthorizeURL(flow.authorizeURL, flow.clientID, redirectURI, state, challenge)

	results := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, closeTabPage)
		q := r.URL.Query()
		// Constant-time, though the loopback socket is local: state is a
		// secret this flow generated, and comparing it like one costs
		// nothing.
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			sendResult(results, callbackResult{err: errors.New("credentials: login: the callback's state did not match; refusing rather than retrying")})
			return
		}
		if errParam := q.Get("error"); errParam != "" {
			sendResult(results, callbackResult{err: fmt.Errorf("credentials: login: linear refused: %s", errParam)})
			return
		}
		code := q.Get("code")
		if code == "" {
			sendResult(results, callbackResult{err: errors.New("credentials: login: callback carried no code")})
			return
		}
		sendResult(results, callbackResult{code: code})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)
	var shutdownOnce sync.Once
	shutdown := func() {
		// Idempotent, so both the call right after the callback and the
		// deferred safety net for an early return (the timeout path) can
		// call it without either blocking on the other's work.
		shutdownOnce.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		})
	}
	defer shutdown()

	if err := flow.open(authorize); err != nil {
		fmt.Fprintf(out, "could not open a browser (%v); open this URL to sign in to Linear:\n%s\n", err, authorize)
	} else {
		fmt.Fprintf(out, "opening your browser to sign in to Linear; if it does not open, visit:\n%s\n", authorize)
	}

	waitCtx, cancel := context.WithTimeout(ctx, flow.timeout)
	defer cancel()
	var res callbackResult
	select {
	case res = <-results:
	case <-waitCtx.Done():
		return errors.New("credentials: login: timed out waiting for the callback")
	}
	// Right after catching the redirect, ahead of the exchange: the socket
	// has done its one job, and closing it now — rather than leaving it open
	// through the exchange and the identity read — is what keeps the window
	// nothing else is listening in as short as the flow allows.
	shutdown()
	if res.err != nil {
		return res.err
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
		"client_id":     {flow.clientID},
	}
	tok, refused, err := postGrant(ctx, flow.hc, flow.tokenEndpoint, form)
	switch {
	case refused:
		return fmt.Errorf("credentials: login: linear refused the exchange (%s)", err)
	case err != nil:
		return fmt.Errorf("credentials: login: exchange: %w", err)
	}
	if tok.RefreshToken == "" {
		return errors.New("credentials: login: linear's response carried no refresh token")
	}
	if err := flow.store.save(tok); err != nil {
		return fmt.Errorf("credentials: login: store token: %w", err)
	}

	// The identity behind the token just obtained, not through Resolve —
	// which prefers LINEAR_API_KEY and would cheerfully confirm the wrong
	// identity when one is set. A failure here is reported but does not fail
	// the command: the token is already on disk and good, and the brief's
	// refusal list — nothing stored on a non-zero exit — only covers the
	// callback and the exchange. Failing here too would tell the operator to
	// `lerp login` again and burn a second authorization for a login that
	// already succeeded.
	identity, err := flow.newViewer(flow.hc, tok.AccessToken).ViewerIdentity(ctx)
	if err != nil {
		fmt.Fprintf(out, "signed in to Linear, but could not confirm the identity: %v\n", err)
	} else {
		fmt.Fprintf(out, "signed in to Linear as %s <%s>\n", identity.Name, identity.Email)
	}
	if envKeyIsSet() {
		fmt.Fprintf(out, "%s is still set, so lerp and lerp init will keep using it rather than this login\n", childenv.LinearAPIKeyEnv)
	}
	return nil
}

func sendResult(ch chan<- callbackResult, r callbackResult) {
	select {
	case ch <- r:
	default:
		// Already delivered — a second hit on /callback (a browser retry, a
		// prefetch) finds nothing waiting for it.
	}
}

// randomURLSafe returns n random bytes, base64url-raw encoded — the shape
// PKCE and state both want.
func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceChallenge is S256(verifier): the code_challenge sent on the authorize
// leg, checked by Linear against the code_verifier sent on the exchange.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// buildAuthorizeURL is Linear's authorize endpoint with the PKCE and actor
// parameters this flow needs. actor=user is SCOPE invariant 4: work must
// show up as the operator, never as lerp itself.
func buildAuthorizeURL(base, clientID, redirectURI, state, challenge string) string {
	v := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {"read,write"},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"actor":                 {"user"},
	}
	return base + "?" + v.Encode()
}
