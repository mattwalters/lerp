package credentials

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mattwalters/lerp/internal/childenv"
)

// defaultRevokeEndpoint is Linear's OAuth revocation endpoint.
const defaultRevokeEndpoint = "https://api.linear.app/oauth/revoke"

// logoutFlow carries what Logout needs, injected the same way loginFlow is:
// so a test can point it at httptest and a temp dir.
type logoutFlow struct {
	revokeEndpoint string
	store          store
	hc             *http.Client
}

// Logout undoes lerp login: best-effort revocation at Linear, then deletes
// the token file either way. Linear's confirmation is not load-bearing to
// the local half of this — an operator must never be left thinking they are
// still signed in because the network was down or the endpoint refused. No
// flags.
func Logout(ctx context.Context, out io.Writer) error {
	return logout(ctx, out, logoutFlow{
		revokeEndpoint: defaultRevokeEndpoint,
		store:          store{},
		hc:             &http.Client{Timeout: 30 * time.Second},
	})
}

func logout(ctx context.Context, out io.Writer, flow logoutFlow) error {
	tok, err := flow.store.load()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintln(out, "nothing stored; lerp was not signed in")
		mentionEnvKey(out)
		return nil
	case err != nil:
		return fmt.Errorf("credentials: logout: load token: %w", err)
	}

	revokeErr := revoke(ctx, flow.hc, flow.revokeEndpoint, tok.AccessToken)

	path, err := flow.store.path()
	if err != nil {
		return fmt.Errorf("credentials: logout: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("credentials: logout: remove token file: %w", err)
	}

	if revokeErr != nil {
		fmt.Fprintf(out, "removed the local token; linear did not confirm the revocation (%v)\n", revokeErr)
	} else {
		fmt.Fprintln(out, "revoked and removed")
	}
	mentionEnvKey(out)
	return nil
}

// mentionEnvKey says when LINEAR_API_KEY is still set, so an operator does
// not read "revoked and removed" and expect lerp to stop working — the env
// key keeps signing requests regardless of what logout just did.
func mentionEnvKey(out io.Writer) {
	if envKeyIsSet() {
		fmt.Fprintf(out, "%s is still set, so lerp keeps working with it\n", childenv.LinearAPIKeyEnv)
	}
}

// revoke posts a best-effort revocation of the access token. Its result is
// never load-bearing to the caller: see Logout.
func revoke(ctx context.Context, hc *http.Client, endpoint, accessToken string) error {
	form := url.Values{"token": {accessToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", resp.Status, snippet(resp))
	}
	return nil
}
