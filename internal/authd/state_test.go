package authd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// hasSessionCookie reports whether a response handed out a session. Every
// refusal below asserts on this as well as on the status code: a handler that
// returns 401 after already calling setSession would still have set a usable
// cookie, and the status alone would not show it.
func hasSessionCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return true
		}
	}
	return false
}

func TestRequireIssuableTreatsAnUnsetStateAsEnabled(t *testing.T) {
	// The CRD defaults spec.state to enabled and its enum admits no third
	// word, so an account read back from the apiserver is never "". Anything
	// that does not come through the apiserver — the fake client in these
	// tests, an account stored before the field existed — must still be able
	// to sign in, so the empty string is enabled and only the literal
	// "disabled" refuses.
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	if err := requireIssuable(u); err != nil {
		t.Fatalf("requireIssuable on an unset state = %v, want nil", err)
	}
	u.Spec.State = framev1beta1.StateEnabled
	if err := requireIssuable(u); err != nil {
		t.Fatalf("requireIssuable on an explicitly enabled account = %v, want nil", err)
	}
	u.Spec.State = framev1beta1.StateDisabled
	if err := requireIssuable(u); err == nil {
		t.Fatal("requireIssuable admitted a disabled account")
	}
	if requireIssuable(nil) == nil {
		t.Fatal("requireIssuable admitted a nil account")
	}
}

// TestTokenPathRefusesADisabledAccount covers path 1 of 4: POST /auth/token.
//
// This is the load-bearing one. The UI calls /auth/token every fifteen
// minutes, so the session is deliberately minted here while the account is
// still enabled and the account is disabled afterwards, through the same
// client the store reads: disabling must cut a session that is already open,
// within one token lifetime, without anyone touching the cookie.
func TestTokenPathRefusesADisabledAccount(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, u)

	sessRec := httptest.NewRecorder()
	if !srv.setSession(sessRec, u) {
		t.Fatal("setSession failed")
	}
	session := sessionCookieFrom(t, sessRec)

	// Non-vacuity: the same cookie must work before the account is disabled,
	// or the 401 below would prove nothing about spec.state.
	if rec := doWithCookie(t, srv, "/auth/token", "", session); rec.Code != http.StatusOK {
		t.Fatalf("/auth/token while enabled = %d, want 200", rec.Code)
	}

	var fresh framev1beta1.FrameUser
	if err := c.Get(context.Background(),
		client.ObjectKey{Name: "alice", Namespace: "cluster-control"}, &fresh); err != nil {
		t.Fatalf("get: %v", err)
	}
	fresh.Spec.State = framev1beta1.StateDisabled
	if err := c.Update(context.Background(), &fresh); err != nil {
		t.Fatalf("disable alice: %v", err)
	}

	rec := doWithCookie(t, srv, "/auth/token", "", session)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/auth/token for a disabled account = %d, want 401 — "+
			"disabling must cut a session already open", rec.Code)
	}
}

// TestPasswordLoginPathRefusesADisabledAccount covers path 2 of 4:
// POST /auth/login/password. The fixture is exactly
// TestPasswordLoginSucceedsWhenEnabled's, plus spec.state — so the only thing
// that can turn that test's 204 into this test's 401 is the state check.
func TestPasswordLoginPathRefusesADisabledAccount(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	u.Spec.PasswordAuth = framev1beta1.PasswordEnabled
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	u.Status.PasswordHash = hash
	u.Spec.State = framev1beta1.StateDisabled
	srv := testServer(t, u)

	rec := do(t, srv, http.MethodPost, "/auth/login/password",
		`{"email":"alice@example.com","password":"hunter2"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("password login for a disabled account = %d, want 401", rec.Code)
	}
	if hasSessionCookie(rec) {
		t.Fatal("a disabled account was handed a session cookie")
	}
}

// stubFinishLogin replaces the WebAuthn half of handleLoginFinish for the
// duration of one test, so the handler's post-ceremony half can be driven
// without signing a real assertion. Same idiom, and same reason, as
// verifyPassword in server_session.go: the alternative is a wall-clock or
// crypto-heavy test of code that is not what is under test.
func stubFinishLogin(t *testing.T, u *framev1beta1.FrameUser) {
	t.Helper()
	restore := finishLogin
	t.Cleanup(func() { finishLogin = restore })
	finishLogin = func(*Authenticator, context.Context, string, []byte) (*framev1beta1.FrameUser, error) {
		return u, nil
	}
}

func postLoginFinish(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/login/finish", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: challengeCookie, Value: "opened-by-the-stub"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestPasskeyLoginPathRefusesADisabledAccount covers path 3 of 4:
// POST /auth/login/finish.
func TestPasskeyLoginPathRefusesADisabledAccount(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	u.Spec.State = framev1beta1.StateDisabled
	srv := testServer(t, u)
	stubFinishLogin(t, u)

	rec := postLoginFinish(t, srv)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("passkey login for a disabled account = %d, want 401", rec.Code)
	}
	if hasSessionCookie(rec) {
		t.Fatal("a disabled account was handed a session cookie")
	}
}

// TestPasskeyLoginPathAdmitsAnEnabledAccount is what keeps the test above
// honest: with the same stub and the same request, an enabled account reaches
// setSession and gets its 204. Without this, a handler that rejected every
// assertion outright would look correct.
func TestPasskeyLoginPathAdmitsAnEnabledAccount(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	srv := testServer(t, u)
	stubFinishLogin(t, u)

	rec := postLoginFinish(t, srv)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("passkey login for an enabled account = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if !hasSessionCookie(rec) {
		t.Fatal("an enabled account got no session cookie")
	}
}
