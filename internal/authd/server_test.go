package authd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// sessionCookieFrom extracts the frame_session cookie a response set, failing
// the test if there isn't one. Shared by every test below that needs to
// carry a session from one request into the next.
func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("response did not set a session cookie")
	return nil
}

// doWithCookie is like do, but attaches a cookie to the request first — for
// tests driving an endpoint that requires an existing session. Every caller
// exercises a POST endpoint, so the method is fixed rather than threaded
// through as a parameter that never varies.
func doWithCookie(t *testing.T, srv *Server, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func testServer(t *testing.T, users ...*framev1beta1.FrameUser) *Server {
	t.Helper()
	store := storeWith(t, users...)
	auth, err := NewAuthenticator("example.com", "https://example.com", store, testCodec())
	if err != nil {
		t.Fatalf("authenticator: %v", err)
	}
	srv, err := NewServer(ServerConfig{
		Store:           store,
		Auth:            auth,
		Issuer:          testIssuer(t),
		Codec:           testCodec(),
		BootstrapSecret: "s3cret-bootstrap",
		Namespace:       "cluster-control",
		TokenTTL:        15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	return srv
}

func do(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// bootstrapServerToken and bootstrapServerSecretName are the bootstrap token
// and Secret name every bootstrapServer caller configures; they're fixed here
// rather than threaded through as parameters that never vary.
const (
	bootstrapServerToken      = "s3cret-bootstrap"
	bootstrapServerSecretName = "frame-auth-bootstrap"
	// testConsoleOrigin is what an invitation link is built on. Fixed here
	// rather than threaded through as a parameter that never varies.
	testConsoleOrigin = "https://frame.example"
)

// bootstrapServer builds a Server whose Store and ServerConfig.Client share the
// same fake client, so a test can both drive /auth/bootstrap and read back
// what it did to the FrameUser and the bootstrap Secret. testServer (above)
// cannot be reused for this: it doesn't expose the client underneath its
// Store, and the bootstrap-completion tests need to inspect that same client
// directly (via NewStore, and via a raw Get on the Secret).
func bootstrapServer(t *testing.T, seedSecret bool, users ...*framev1beta1.FrameUser) (*Server, client.Client) {
	t.Helper()
	s := scheme.Scheme
	if err := framev1beta1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&framev1beta1.FrameUser{})
	for _, u := range users {
		b = b.WithObjects(u)
	}
	if seedSecret {
		b = b.WithObjects(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: bootstrapServerSecretName, Namespace: "cluster-control"},
			Data:       map[string][]byte{"token": []byte(bootstrapServerToken)},
		})
	}
	c := b.Build()
	store := NewStore(c, "cluster-control")
	auth, err := NewAuthenticator("example.com", "https://example.com", store, testCodec())
	if err != nil {
		t.Fatalf("authenticator: %v", err)
	}
	srv, err := NewServer(ServerConfig{
		Store:               store,
		Auth:                auth,
		Issuer:              testIssuer(t),
		Codec:               testCodec(),
		BootstrapSecret:     bootstrapServerToken,
		BootstrapSecretName: bootstrapServerSecretName,
		Client:              c,
		Namespace:           "cluster-control",
		TokenTTL:            15 * time.Minute,
		ConsoleOrigin:       testConsoleOrigin,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	return srv, c
}

func TestDiscoveryAndKeysArePublic(t *testing.T) {
	srv := testServer(t)
	for _, path := range []string{"/.well-known/openid-configuration", "/keys"} {
		rec := do(t, srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, rec.Code)
		}
	}
}

func TestBootstrapClosesOnceAUserExists(t *testing.T) {
	srv := testServer(t, fixture("alice", "alice@example.com", framev1beta1.RoleAdmin))
	rec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"eve@example.com"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bootstrap with an existing user = %d, want 404", rec.Code)
	}
}

func TestBootstrapRejectsWrongToken(t *testing.T) {
	srv := testServer(t)
	rec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"guess","email":"eve@example.com"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bootstrap with a wrong token = %d, want 401", rec.Code)
	}
}

// TestBootstrapRefusesWhenNoTokenIsConfigured covers the authentication
// bypass found in review: subtle.ConstantTimeCompare([]byte(""), []byte(""))
// returns 1, so a server whose BootstrapSecret is empty — the designed
// steady state once bootstrap has already consumed and deleted the Secret,
// per cmd/authd/main.go's loadBootstrapToken — must refuse every bootstrap
// attempt outright, not fall through to a compare that a caller can satisfy
// by simply omitting the token field.
func TestBootstrapRefusesWhenNoTokenIsConfigured(t *testing.T) {
	store := storeWith(t)
	auth, err := NewAuthenticator("example.com", "https://example.com", store, testCodec())
	if err != nil {
		t.Fatalf("authenticator: %v", err)
	}
	srv, err := NewServer(ServerConfig{
		Store:           store,
		Auth:            auth,
		Issuer:          testIssuer(t),
		Codec:           testCodec(),
		BootstrapSecret: "", // never configured, or already consumed
		Namespace:       "cluster-control",
		TokenTTL:        15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}

	for _, body := range []string{`{"token":""}`, `{}`} {
		rec := do(t, srv, http.MethodPost, "/auth/bootstrap", body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("bootstrap with body %q against an empty configured token = %d, want 404 "+
				"(an empty submitted token must never match an empty configured one)", body, rec.Code)
		}
	}
}

// TestBootstrapCreatesTheFirstAdminAndDeletesTheSecret is the test the brief's
// stub could never fail: it reads the FrameUser back and asserts on its role
// and password mode, and confirms the bootstrap Secret is actually gone
// afterwards, not just that the HTTP call returned 204.
func TestBootstrapCreatesTheFirstAdminAndDeletesTheSecret(t *testing.T) {
	srv, c := bootstrapServer(t, true)

	rec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"Eve@Example.com"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bootstrap = %d, want 204: %s", rec.Code, rec.Body.String())
	}

	store := NewStore(c, "cluster-control")
	u, err := store.ByEmail(context.Background(), "Eve@Example.com")
	if err != nil {
		t.Fatalf("ByEmail after bootstrap: %v", err)
	}
	if u.Spec.Role != framev1beta1.RoleAdmin {
		t.Fatalf("role = %q, want %q", u.Spec.Role, framev1beta1.RoleAdmin)
	}
	if u.Spec.PasswordAuth == framev1beta1.PasswordEnabled {
		t.Fatal("bootstrap admin was created with password auth enabled; it must enrol a passkey first")
	}

	var secret corev1.Secret
	err = c.Get(context.Background(), client.ObjectKey{Name: "frame-auth-bootstrap", Namespace: "cluster-control"}, &secret)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("bootstrap secret still readable after use (err=%v) — the token could be replayed", err)
	}
}

// TestSecondBootstrapCallIs404AfterFirstSucceeds exercises the full
// close-the-door path end to end, through the real AdminCount guard rather
// than a pre-seeded admin fixture.
func TestSecondBootstrapCallIs404AfterFirstSucceeds(t *testing.T) {
	srv, _ := bootstrapServer(t, true)

	first := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"eve@example.com"}`)
	if first.Code != http.StatusNoContent {
		t.Fatalf("first bootstrap = %d, want 204: %s", first.Code, first.Body.String())
	}

	second := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"mallory@example.com"}`)
	if second.Code != http.StatusNotFound {
		t.Fatalf("second bootstrap = %d, want 404", second.Code)
	}
}

func TestBootstrapRejectsMalformedEmail(t *testing.T) {
	srv, _ := bootstrapServer(t, true)
	rec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"not-an-email"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap with a malformed email = %d, want 400", rec.Code)
	}
}

// handleInvite checks maxEmailLength; handleBootstrap checked only
// emailPattern, which matches a local part of any length. So the same
// address got a clean, actionable 400 from one route and, from the other,
// an opaque admission failure surfacing as a 500 — after the bootstrap
// Secret had already been consulted. Same rule, same answer, both doors.
func TestBootstrapRejectsAnOverLongEmail(t *testing.T) {
	srv, c := bootstrapServer(t, true)
	tooLong := strings.Repeat("a", 250) + "@x.io" // 255 bytes, one past maxEmailLength
	rec := do(t, srv, http.MethodPost, "/auth/bootstrap",
		`{"token":"s3cret-bootstrap","email":"`+tooLong+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap with a %d-byte email = %d, want 400", len(tooLong), rec.Code)
	}
	if n := countUsers(t, c); n != 0 {
		t.Fatalf("a refused bootstrap created an account: %d users, want 0", n)
	}
}

// TestPasswordLoginIsNotServed pins "no sign-in by password". authd used to
// register POST /auth/login/password; it was closed because a password alone
// is a single factor, on a console whose every other way in is a passkey.
//
// Reopening it is not a matter of deleting this test: a password route — this
// one, or the future "set a password" one — ships only together with a second
// factor (TOTP) in the same change, and this test is then rewritten to require
// that factor rather than removed.
//
// The account below is the one the old route would have let in: password
// sign-in enabled, a hash present on status, the right password in the body.
// The answer must be the mux's own 404, identical in status and body to a
// path that never existed, so the route cannot be told apart from absence; no
// session cookie may come back.
func TestPasswordLoginIsNotServed(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	u.Spec.PasswordAuth = framev1beta1.PasswordEnabled
	u.Status.PasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"
	srv := testServer(t, u)

	absent := do(t, srv, http.MethodPost, "/auth/login/no-such-route", `{}`)
	if absent.Code != http.StatusNotFound {
		t.Fatalf("control: an absent route = %d, want 404", absent.Code)
	}
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		rec := do(t, srv, method, "/auth/login/password", `{"email":"alice@example.com","password":"hunter2"}`)
		if rec.Code != absent.Code || rec.Body.String() != absent.Body.String() {
			t.Fatalf("%s /auth/login/password = %d %q, want what an absent route answers: %d %q",
				method, rec.Code, rec.Body.String(), absent.Code, absent.Body.String())
		}
		if hasSessionCookie(rec) {
			t.Fatalf("%s /auth/login/password handed out a session cookie", method)
		}
	}
}

// assertSessionCookie checks every attribute setSessionFor puts on the
// frame_session cookie, on a real response. HttpOnly keeps it out of reach of
// script (an XSS cannot lift the session), Secure keeps it off plain HTTP,
// SameSite=Strict keeps cross-site requests from carrying it, Path "/" scopes
// it to the console, and MaxAge bounds it to the session lifetime.
func assertSessionCookie(t *testing.T, rec *httptest.ResponseRecorder, wantMaxAge time.Duration) {
	t.Helper()
	c := sessionCookieFrom(t, rec)
	if c.Value == "" {
		t.Fatal("session cookie is empty")
	}
	if !c.HttpOnly {
		t.Fatal("session cookie is not HttpOnly")
	}
	if !c.Secure {
		t.Fatal("session cookie is not Secure")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie SameSite = %v, want Strict", c.SameSite)
	}
	if c.Path != "/" {
		t.Fatalf("session cookie Path = %q, want /", c.Path)
	}
	if c.MaxAge != int(wantMaxAge.Seconds()) {
		t.Fatalf("session cookie MaxAge = %d, want %d", c.MaxAge, int(wantMaxAge.Seconds()))
	}
}

// The two routes that hand out a full session: passkey sign-in and bootstrap.
// (The invitation's enrolment cookie goes through the same setSessionFor; its
// purpose and TTL are covered in server_invite_test.go.)
func TestPasskeySignInSetsAHardenedSessionCookie(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	srv := testServer(t, u)
	stubFinishLogin(t, u)
	rec := postLoginFinish(t, srv)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("passkey sign-in = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	assertSessionCookie(t, rec, 12*time.Hour)
}

func TestBootstrapSetsAHardenedSessionCookie(t *testing.T) {
	srv, _ := bootstrapServer(t, true)
	rec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"eve@example.com"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bootstrap = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	assertSessionCookie(t, rec, 12*time.Hour)
}

func TestTokenRequiresASession(t *testing.T) {
	srv := testServer(t, fixture("alice", "alice@example.com", framev1beta1.RoleAdmin))
	rec := do(t, srv, http.MethodPost, "/auth/token", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("token without session = %d, want 401", rec.Code)
	}
}

// TestBootstrapSessionAllowsImmediateRegisterBegin drives the real sequence
// review flagged as broken: bootstrap with a valid token, then use the
// cookie that came back against /auth/register/begin. Before the fix,
// handleBootstrap returned 204 without ever calling setSession, so the
// caller had a freshly-created admin account and nothing to authenticate
// the very next call that enrols its passkey — sessionUser would 401, and
// with AdminCount() > 0 and the bootstrap Secret already deleted, that admin
// could never sign in again. This test fails if setSession is removed from
// handleBootstrap: sessionCookieFrom would fail the test outright because
// there would be no Set-Cookie header to find.
func TestBootstrapSessionAllowsImmediateRegisterBegin(t *testing.T) {
	srv, _ := bootstrapServer(t, true)

	bootstrapRec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"eve@example.com"}`)
	if bootstrapRec.Code != http.StatusNoContent {
		t.Fatalf("bootstrap = %d, want 204: %s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	session := sessionCookieFrom(t, bootstrapRec)

	rec := doWithCookie(t, srv, "/auth/register/begin", "", session)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("register/begin with the bootstrap session cookie = 401, want anything else "+
			"(the freshly-bootstrapped admin has no other way to enrol a passkey): %s", rec.Body.String())
	}
}

// TestTokenIsBodyOnlyNeverCookie guards the design document's claim that the
// ID token is "held in memory only — never localStorage": /auth/token must
// return it exclusively in the JSON body, and the response must not also
// set it (or anything derived from it) as a cookie, which would give an XSS
// a way to read it that surviving only in memory is meant to prevent.
func TestTokenIsBodyOnlyNeverCookie(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	srv := testServer(t, u)
	session := sessionFor(t, srv, u)

	rec := doWithCookie(t, srv, "/auth/token", "", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("token = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		IDToken   string `json:"id_token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.IDToken == "" {
		t.Fatal("response body carries no id_token")
	}
	if body.ExpiresIn <= 0 {
		t.Fatalf("expires_in = %d, want > 0", body.ExpiresIn)
	}

	if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("token response set Set-Cookie header(s), want none: %v", got)
	}
}

// TestTokenReflectsRoleChangedAfterSessionIssued guards role freshness: the
// design document says minting "fresh from the current role" means a
// demotion (or promotion) takes effect within one token lifetime instead of
// lasting as long as the session. This creates a FrameUser, mints a session
// for it directly (bypassing login, since only the role-after-session-issued
// ordering matters here), promotes the account, and asserts the next
// /auth/token call reflects the new role rather than a stale one captured
// when the session was created — the session cookie carries only the email
// (see setSession), so this also incidentally proves the role is read fresh
// from the store on every call to handleToken, not cached in the cookie.
func TestTokenReflectsRoleChangedAfterSessionIssued(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1beta1.RoleViewer)
	srv, c := bootstrapServer(t, false, u)

	sessRec := httptest.NewRecorder()
	if !srv.setSession(sessRec, u) {
		t.Fatal("setSession failed")
	}
	session := sessionCookieFrom(t, sessRec)

	var fresh framev1beta1.FrameUser
	if err := c.Get(context.Background(), client.ObjectKey{Name: "alice", Namespace: "cluster-control"}, &fresh); err != nil {
		t.Fatalf("get: %v", err)
	}
	fresh.Spec.Role = framev1beta1.RoleAdmin
	if err := c.Update(context.Background(), &fresh); err != nil {
		t.Fatalf("promote alice to admin: %v", err)
	}

	rec := doWithCookie(t, srv, "/auth/token", "", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("token = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	tok, err := jwt.ParseSigned(body.IDToken, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	var claims struct {
		Groups []string `json:"groups"`
	}
	if err := tok.UnsafeClaimsWithoutVerification(&claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if len(claims.Groups) != 1 || claims.Groups[0] != "admins" {
		t.Fatalf("groups claim = %v, want [admins] reflecting the role change made after the session was issued", claims.Groups)
	}
}

// TestRegisterBeginIgnoresRequestBodyAccount pins the guarantee
// handleRegisterBegin's own comment asserts but that, before this test,
// nothing checked: the account enrolled comes from the session cookie, never
// from the request body. A session for alice plus a body naming bob must
// still enrol a credential for alice — proven here by reading
// publicKey.user.name back out of the registration options and checking it
// names the session owner, not the body's account.
func TestRegisterBeginIgnoresRequestBodyAccount(t *testing.T) {
	alice := fixture("alice", "alice@example.com", framev1beta1.RoleAdmin)
	bob := fixture("bob", "bob@example.com", framev1beta1.RoleViewer)
	srv := testServer(t, alice, bob)

	sessRec := httptest.NewRecorder()
	if !srv.setSession(sessRec, alice) {
		t.Fatal("setSession failed")
	}
	session := sessionCookieFrom(t, sessRec)

	rec := doWithCookie(t, srv, "/auth/register/begin", `{"email":"bob@example.com"}`, session)
	if rec.Code != http.StatusOK {
		t.Fatalf("register/begin = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var opts struct {
		PublicKey struct {
			User struct {
				Name string `json:"name"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &opts); err != nil {
		t.Fatalf("decode options: %v", err)
	}
	if opts.PublicKey.User.Name != "alice@example.com" {
		t.Fatalf("register/begin enrolled for %q, want the session owner alice@example.com "+
			"— a request body naming another account must not redirect enrollment", opts.PublicKey.User.Name)
	}
}
