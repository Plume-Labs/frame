package authd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// sessionFor mints a session cookie for u against srv, which is how every
// test below authenticates as a caller without driving a login ceremony.
func sessionFor(t *testing.T, srv *Server, u *framev1beta1.FrameUser) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if !srv.setSession(rec, u) {
		t.Fatal("setSession failed")
	}
	return sessionCookieFrom(t, rec)
}

// inviteURLToken pulls the sealed token back out of the link the handler
// returned, so a test can assert on what the link actually carries rather
// than on the string's shape alone.
func inviteURLToken(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("invite url %q does not parse: %v", rawURL, err)
	}
	return parsed.Query().Get("token")
}

func countUsers(t *testing.T, c client.Client) int {
	t.Helper()
	var list framev1beta1.FrameUserList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("list: %v", err)
	}
	return len(list.Items)
}

func TestInviteRequiresAnAdminSession(t *testing.T) {
	viewer := fixture("bob", "bob@example.com", framev1beta1.RoleViewer)
	srv, c := bootstrapServer(t, false, viewer)

	if rec := do(t, srv, http.MethodPost, "/auth/invite",
		`{"email":"new@example.com","role":"viewer"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("invite with no session = %d, want 401", rec.Code)
	}
	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"new@example.com","role":"viewer"}`, sessionFor(t, srv, viewer))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("invite from a viewer = %d, want 403", rec.Code)
	}
	if n := countUsers(t, c); n != 1 {
		t.Fatalf("a refused invite created an account: %d users, want 1", n)
	}
}

func TestInviteCreatesAPasskeylessAccountAndReturnsALink(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)

	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"Bob@Example.com","role":"viewer"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("invite = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(body.URL, testConsoleOrigin+"/invite?token=") {
		t.Fatalf("invite url = %q, want %s/invite?token=…", body.URL, testConsoleOrigin)
	}

	// The link carries the invitee's address, sealed under its own purpose.
	payload, err := testCodec().Open(PurposeInvite, inviteURLToken(t, body.URL))
	if err != nil {
		t.Fatalf("the invitation token does not open under PurposeInvite: %v", err)
	}
	if string(payload) != "Bob@Example.com" {
		t.Fatalf("token carries %q, want the invited address", payload)
	}

	created, err := NewStore(c, "cluster-control").ByEmail(context.Background(), "Bob@Example.com")
	if err != nil {
		t.Fatalf("the invited account was not created: %v", err)
	}
	if created.Spec.Role != framev1beta1.RoleViewer {
		t.Fatalf("role = %q, want viewer", created.Spec.Role)
	}
	if created.Spec.State != framev1beta1.StateEnabled {
		t.Fatalf("state = %q, want enabled", created.Spec.State)
	}
	if created.Spec.PasswordAuth != framev1beta1.PasswordDisabled {
		t.Fatalf("passwordAuth = %q, want disabled — an invited account is passkey-only", created.Spec.PasswordAuth)
	}
	if len(created.Status.Credentials) != 0 {
		t.Fatalf("an invited account arrived holding credentials: %v", created.Status.Credentials)
	}
	if created.Name != "bob-at-example.com" {
		t.Fatalf("object name = %q, want the lowercased derivation", created.Name)
	}
}

// authd creates the FrameUser under its own ServiceAccount, and the admission
// webhook refuses a `spec.role: admin` create from anyone who is not already
// an admin. So an admin invite would fail at admission with an opaque 500;
// this refuses it here, with a message naming the way through.
func TestInviteRefusesAnAdminRole(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)

	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"new@example.com","role":"admin"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invite with role admin = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Accounts screen") {
		t.Fatalf("the refusal does not say how to get an admin: %q", rec.Body.String())
	}
	if n := countUsers(t, c); n != 1 {
		t.Fatalf("a refused invite created an account: %d users, want 1", n)
	}
}

func TestInviteRefusesAMalformedEmailAndAnUnknownRole(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)
	session := sessionFor(t, srv, admin)

	for _, body := range []string{
		`{"email":"not-an-email","role":"viewer"}`,
		`{"email":"","role":"viewer"}`,
		`{"email":"new@example.com","role":"editor"}`,
		`{"email":"new@example.com","role":""}`,
	} {
		if rec := doWithCookie(t, srv, "/auth/invite", body, session); rec.Code != http.StatusBadRequest {
			t.Fatalf("invite %s = %d, want 400", body, rec.Code)
		}
	}
	if n := countUsers(t, c); n != 1 {
		t.Fatalf("a refused invite created an account: %d users, want 1", n)
	}
}

func TestInviteRefusesADuplicate(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin,
		fixture("bob-at-example.com", "bob@example.com", framev1beta1.RoleViewer))

	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"bob@example.com","role":"viewer"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusConflict {
		t.Fatalf("inviting an existing account = %d, want 409", rec.Code)
	}
}

// The invitation token and the session cookie share one HMAC key, so the
// purpose is the only thing keeping a 24-hour invitation from being presented
// as a session — and a session from being spent as an invitation.
func TestAnInvitationTokenIsNotASession(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin)

	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"bob@example.com","role":"viewer"}`, sessionFor(t, srv, admin))
	var body struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	token := inviteURLToken(t, body.URL)

	if _, err := testCodec().Open(PurposeSession, token); err == nil {
		t.Fatal("an invitation token opened as a session")
	}
	sealed, err := testCodec().Seal(PurposeSession, []byte("bob@example.com"), time.Hour)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := testCodec().Open(PurposeInvite, sealed); err == nil {
		t.Fatal("a session cookie opened as an invitation")
	}
}
