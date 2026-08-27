package credentials

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/childenv"
)

func baseLogoutFlow(t *testing.T, srv *httptest.Server, s store) logoutFlow {
	t.Helper()
	return logoutFlow{
		revokeEndpoint: srv.URL,
		store:          s,
		hc:             srv.Client(),
	}
}

// A successful revoke removes the file and says both happened.
func TestLogoutRevokesAndRemoves(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("token"); got != "access-1" {
			t.Errorf("token = %q, want the access token", got)
		}
	}))
	t.Cleanup(srv.Close)

	s := tempStore(t, ptr(liveToken()))
	var out strings.Builder
	if err := logout(context.Background(), &out, baseLogoutFlow(t, srv, s)); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if gotAuth != "Bearer access-1" {
		t.Errorf("Authorization = %q, want the bearer access token", gotAuth)
	}
	if !strings.Contains(out.String(), "revoked and removed") {
		t.Errorf("output %q does not say it revoked and removed", out.String())
	}
	if _, err := s.load(); err == nil {
		t.Error("the token file is still there")
	}
}

// Revocation failing still removes the file, and says which of the two
// happened.
func TestLogoutRemovesEvenWhenRevokeFails(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not today", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	s := tempStore(t, ptr(liveToken()))
	var out strings.Builder
	if err := logout(context.Background(), &out, baseLogoutFlow(t, srv, s)); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(out.String(), "did not confirm") {
		t.Errorf("output %q does not say linear did not confirm", out.String())
	}
	if !strings.Contains(out.String(), "not today") {
		t.Errorf("output %q does not carry the endpoint's reason", out.String())
	}
	if _, err := s.load(); err == nil {
		t.Error("the token file is still there despite the failed revoke")
	}
}

// Nothing stored says so, and never calls the revoke endpoint.
func TestLogoutWithNothingStored(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "")
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	t.Cleanup(srv.Close)

	s := store{dir: t.TempDir()}
	var out strings.Builder
	if err := logout(context.Background(), &out, baseLogoutFlow(t, srv, s)); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(out.String(), "nothing stored") {
		t.Errorf("output %q does not say nothing was stored", out.String())
	}
	if calls != 0 {
		t.Errorf("calls = %d, want 0 — nothing to revoke", calls)
	}
}

// LINEAR_API_KEY set is mentioned, so an operator does not read "revoked and
// removed" and expect lerp to stop working.
func TestLogoutMentionsTheEnvKey(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "lin_api_key")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)

	s := tempStore(t, ptr(liveToken()))
	var out strings.Builder
	if err := logout(context.Background(), &out, baseLogoutFlow(t, srv, s)); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(out.String(), childenv.LinearAPIKeyEnv) {
		t.Errorf("output %q does not mention %s", out.String(), childenv.LinearAPIKeyEnv)
	}
	if v, ok := os.LookupEnv(childenv.LinearAPIKeyEnv); ok {
		t.Errorf("%s still set to %q after logout; it should be dropped like every other read", childenv.LinearAPIKeyEnv, v)
	}
}
