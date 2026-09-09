package authd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func key(id, label string) framev1beta1.WebAuthnCredential {
	return framev1beta1.WebAuthnCredential{
		ID: id, PublicKey: "cHVibGljLWtleS1tYXRlcmlhbA", SignCount: 3,
		AddedAt: metav1.Now(), Label: label,
	}
}

// doWithCookieMethod is doWithCookie for a method other than POST — DELETE,
// here — since the credential routes are the first in this package that are
// not all POSTs.
func doWithCookieMethod(t *testing.T, srv *Server, method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func decodeCredentials(t *testing.T, rec *httptest.ResponseRecorder) []credentialView {
	t.Helper()
	var body struct {
		Credentials []credentialView `json:"credentials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return body.Credentials
}

func TestListCredentialsReturnsTheCallersOwnKeysWithoutPublicKeyMaterial(t *testing.T) {
	alice := fixture("alice", "alice@example.com", framev1beta1.RoleViewer,
		key("a2V5LW9uZQ", "YubiKey 5C"), key("a2V5LXR3bw", "Pixel 8"))
	srv := testServer(t, alice)

	rec := doWithCookieMethod(t, srv, http.MethodGet, "/auth/credentials", sessionFor(t, srv, alice))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := decodeCredentials(t, rec)
	if len(got) != 2 || got[0].Label != "YubiKey 5C" || got[1].ID != "a2V5LXR3bw" {
		t.Fatalf("credentials = %+v", got)
	}
	// The public key is not the UI's business, and the less of a credential
	// record that travels, the better. The ID does travel — DELETE needs
	// something to address it.
	if strings.Contains(rec.Body.String(), "publicKey") ||
		strings.Contains(rec.Body.String(), "cHVibGljLWtleS1tYXRlcmlhbA") {
		t.Fatalf("public key material was returned: %s", rec.Body.String())
	}
	// A list of someone's enrolled authenticators must not sit in a shared
	// cache or the browser's back-forward cache, matching the precedent set
	// by /auth/invite's response (server_invite_test.go).
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}
}

func TestListCredentialsNeedsASession(t *testing.T) {
	srv := testServer(t, fixture("alice", "alice@example.com", framev1beta1.RoleViewer, key("a2V5", "k")))
	req := httptest.NewRequest(http.MethodGet, "/auth/credentials", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("list with no session = %d, want 401", rec.Code)
	}
}

func TestAdminCanReadAnotherAccountsCredentialsAndAViewerCannot(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin, key("cm9vdC1rZXk", "root key"))
	bob := fixture("bob", "bob@example.com", framev1beta1.RoleViewer, key("Ym9iLWtleQ", "bob key"))
	viewer := fixture("eve", "eve@example.com", framev1beta1.RoleViewer, key("ZXZlLWtleQ", "eve key"))
	srv := testServer(t, admin, bob, viewer)

	rec := doWithCookieMethod(t, srv, http.MethodGet,
		"/auth/credentials?user=bob%40example.com", sessionFor(t, srv, admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin reading bob = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := decodeCredentials(t, rec); len(got) != 1 || got[0].Label != "bob key" {
		t.Fatalf("admin got %+v, want bob's key", got)
	}

	rec = doWithCookieMethod(t, srv, http.MethodGet,
		"/auth/credentials?user=bob%40example.com", sessionFor(t, srv, viewer))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer reading bob = %d, want 403", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "bob key") {
		t.Fatalf("a refused read still leaked the answer: %s", rec.Body.String())
	}
}

func TestRevokeRemovesTheCallersOwnKey(t *testing.T) {
	alice := fixture("alice", "alice@example.com", framev1beta1.RoleViewer,
		key("a2V5LW9uZQ", "YubiKey 5C"), key("a2V5LXR3bw", "Pixel 8"))
	srv := testServer(t, alice)
	session := sessionFor(t, srv, alice)

	rec := doWithCookieMethod(t, srv, http.MethodDelete, "/auth/credentials/a2V5LW9uZQ", session)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	after := decodeCredentials(t, doWithCookieMethod(t, srv, http.MethodGet, "/auth/credentials", session))
	if len(after) != 1 || after[0].ID != "a2V5LXR3bw" {
		t.Fatalf("after revoking one key: %+v", after)
	}
}

func TestRevokeAnUnknownKeyIs404(t *testing.T) {
	alice := fixture("alice", "alice@example.com", framev1beta1.RoleViewer, key("a2V5LW9uZQ", "k"))
	srv := testServer(t, alice)
	rec := doWithCookieMethod(t, srv, http.MethodDelete,
		"/auth/credentials/bm90LWEta2V5", sessionFor(t, srv, alice))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoking an unknown key = %d, want 404", rec.Code)
	}
}

// The last admin is the case the design names; the guard in
// Store.RemoveCredential is broader than that, and this proves it through the
// route rather than restating it in the handler.
func TestRevokeRefusesToStrandTheLastAdmin(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin, key("cm9vdC1rZXk", "only key"))
	srv := testServer(t, admin)
	session := sessionFor(t, srv, admin)

	rec := doWithCookieMethod(t, srv, http.MethodDelete, "/auth/credentials/cm9vdC1rZXk", session)
	if rec.Code != http.StatusConflict {
		t.Fatalf("revoking the last admin's only key = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	after := decodeCredentials(t, doWithCookieMethod(t, srv, http.MethodGet, "/auth/credentials", session))
	if len(after) != 1 {
		t.Fatalf("a refused revocation removed the key anyway: %+v", after)
	}
	// The 409 body is the handler's own message, not Store.RemoveCredential's
	// err.Error(). Pinning it here means a future reword of the store's
	// wording (which also names the account) cannot silently change what the
	// API sends over the wire without a test noticing.
	const wantBody = "refusing to remove this account's last credential: " +
		"it has no password sign-in configured, so it would become unreachable\n"
	if got := rec.Body.String(); got != wantBody {
		t.Fatalf("409 body = %q, want %q", got, wantBody)
	}
}

func TestRevokeRefusesAViewerActingOnSomeoneElse(t *testing.T) {
	bob := fixture("bob", "bob@example.com", framev1beta1.RoleViewer,
		key("Ym9iLW9uZQ", "one"), key("Ym9iLXR3bw", "two"))
	eve := fixture("eve", "eve@example.com", framev1beta1.RoleViewer, key("ZXZlLWtleQ", "eve"))
	srv := testServer(t, bob, eve)

	rec := doWithCookieMethod(t, srv, http.MethodDelete,
		"/auth/credentials/Ym9iLW9uZQ?user=bob%40example.com", sessionFor(t, srv, eve))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer revoking bob's key = %d, want 403", rec.Code)
	}
	after := decodeCredentials(t, doWithCookieMethod(t, srv, http.MethodGet,
		"/auth/credentials", sessionFor(t, srv, bob)))
	if len(after) != 2 {
		t.Fatalf("a refused revocation removed a key anyway: %+v", after)
	}
}

func TestAdminCanRevokeAnotherAccountsKey(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin, key("cm9vdC1rZXk", "root"))
	bob := fixture("bob", "bob@example.com", framev1beta1.RoleViewer,
		key("Ym9iLW9uZQ", "one"), key("Ym9iLXR3bw", "two"))
	srv := testServer(t, admin, bob)

	rec := doWithCookieMethod(t, srv, http.MethodDelete,
		"/auth/credentials/Ym9iLW9uZQ?user=bob%40example.com", sessionFor(t, srv, admin))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("admin revoking bob's key = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	after := decodeCredentials(t, doWithCookieMethod(t, srv, http.MethodGet,
		"/auth/credentials", sessionFor(t, srv, bob)))
	if len(after) != 1 || after[0].ID != "Ym9iLXR3bw" {
		t.Fatalf("after the admin revoked one of bob's keys: %+v", after)
	}
}

func TestCredentialsForAnUnknownUserIs404(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin, key("cm9vdC1rZXk", "root"))
	srv := testServer(t, admin)
	rec := doWithCookieMethod(t, srv, http.MethodGet,
		"/auth/credentials?user=ghost%40example.com", sessionFor(t, srv, admin))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("admin reading a missing account = %d, want 404", rec.Code)
	}
}

func TestErrLastCredentialIsTypedNotTextual(t *testing.T) {
	// The route distinguishes "refused on purpose" (409) from "the apiserver
	// failed" (500). Matching on message text would make that distinction a
	// string comparison one reword away from turning every refusal into a 500.
	s := storeWith(t, fixture("alice", "alice@example.com", framev1beta1.RoleViewer, key("b25seQ", "only")))
	u, err := s.ByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("ByEmail: %v", err)
	}
	if err := s.RemoveCredential(context.Background(), u, "b25seQ"); !errors.Is(err, ErrLastCredential) {
		t.Fatalf("RemoveCredential = %v, want ErrLastCredential", err)
	}
}

// TestRevokeIsScopedToTheCallersOwnAccountNotAGlobalCredentialLookup pins the
// invariant the route depends on but that no other test exercises: the {id}
// path parameter is resolved within the session-derived subject's own
// credential list, never by a global lookup such as Store.ByCredentialID. A
// global lookup would make every other revoke test pass too — none of them
// address an ID that exists, but belongs to someone other than the resolved
// subject, with no ?user= present to explain the mismatch.
//
// It also pins the no-oracle property: revoking a real credential ID that is
// merely out of the caller's scope must be byte-identical to revoking an ID
// that exists nowhere at all. If the two ever diverge, the response starts
// telling an attacker which credential IDs are real.
func TestRevokeIsScopedToTheCallersOwnAccountNotAGlobalCredentialLookup(t *testing.T) {
	bob := fixture("bob", "bob@example.com", framev1beta1.RoleViewer,
		key("Ym9iLW9uZQ", "one"), key("Ym9iLXR3bw", "two"))
	eve := fixture("eve", "eve@example.com", framev1beta1.RoleViewer, key("ZXZlLWtleQ", "eve"))
	srv := testServer(t, bob, eve)

	// eve addresses one of bob's real credential IDs directly, with no
	// ?user= — so subjectOf resolves the subject as eve, the caller, not
	// bob. Correct code looks for that ID in eve's own list, does not find
	// it, and answers 404. A global lookup would find it on bob's account
	// and remove it.
	outOfScope := doWithCookieMethod(t, srv, http.MethodDelete, "/auth/credentials/Ym9iLW9uZQ", sessionFor(t, srv, eve))
	if outOfScope.Code != http.StatusNotFound {
		t.Fatalf("revoking bob's key by ID alone, no ?user= = %d, want 404: %s", outOfScope.Code, outOfScope.Body.String())
	}
	after := decodeCredentials(t, doWithCookieMethod(t, srv, http.MethodGet, "/auth/credentials", sessionFor(t, srv, bob)))
	if len(after) != 2 {
		t.Fatalf("bob lost a key to an out-of-scope lookup: %+v", after)
	}

	unknown := doWithCookieMethod(t, srv, http.MethodDelete, "/auth/credentials/bm90LWEta2V5", sessionFor(t, srv, eve))
	if unknown.Code != outOfScope.Code || unknown.Body.String() != outOfScope.Body.String() {
		t.Fatalf("a real-but-out-of-scope ID answered differently from an unknown one: got %d %q, unknown %d %q",
			outOfScope.Code, outOfScope.Body.String(), unknown.Code, unknown.Body.String())
	}
}
