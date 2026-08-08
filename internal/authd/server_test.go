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

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
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
// tests driving an endpoint that requires an existing session.
func doWithCookie(t *testing.T, srv *Server, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func testServer(t *testing.T, users ...*framev1alpha1.FrameUser) *Server {
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

// bootstrapServer builds a Server whose Store and ServerConfig.Client share the
// same fake client, so a test can both drive /auth/bootstrap and read back
// what it did to the FrameUser and the bootstrap Secret. testServer (above)
// cannot be reused for this: it doesn't expose the client underneath its
// Store, and the bootstrap-completion tests need to inspect that same client
// directly (via NewStore, and via a raw Get on the Secret).
func bootstrapServer(t *testing.T, token, secretName string, seedSecret bool, users ...*framev1alpha1.FrameUser) (*Server, client.Client) {
	t.Helper()
	s := scheme.Scheme
	if err := framev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&framev1alpha1.FrameUser{})
	for _, u := range users {
		b = b.WithObjects(u)
	}
	if seedSecret {
		b = b.WithObjects(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "cluster-control"},
			Data:       map[string][]byte{"token": []byte(token)},
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
		BootstrapSecret:     token,
		BootstrapSecretName: secretName,
		Client:              c,
		Namespace:           "cluster-control",
		TokenTTL:            15 * time.Minute,
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
	srv := testServer(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin))
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
	srv, c := bootstrapServer(t, "s3cret-bootstrap", "frame-auth-bootstrap", true)

	rec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"Eve@Example.com"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bootstrap = %d, want 204: %s", rec.Code, rec.Body.String())
	}

	store := NewStore(c, "cluster-control")
	u, err := store.ByEmail(context.Background(), "Eve@Example.com")
	if err != nil {
		t.Fatalf("ByEmail after bootstrap: %v", err)
	}
	if u.Spec.Role != framev1alpha1.RoleAdmin {
		t.Fatalf("role = %q, want %q", u.Spec.Role, framev1alpha1.RoleAdmin)
	}
	if u.Spec.PasswordAuth == framev1alpha1.PasswordEnabled {
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
	srv, _ := bootstrapServer(t, "s3cret-bootstrap", "frame-auth-bootstrap", true)

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
	srv, _ := bootstrapServer(t, "s3cret-bootstrap", "frame-auth-bootstrap", true)
	rec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"not-an-email"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap with a malformed email = %d, want 400", rec.Code)
	}
}

func TestPasswordLoginRefusedWhenDisabled(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin)
	u.Spec.PasswordAuth = framev1alpha1.PasswordDisabled
	hash, _ := HashPassword("hunter2")
	u.Spec.PasswordHash = hash
	srv := testServer(t, u)

	rec := do(t, srv, http.MethodPost, "/auth/login/password", `{"email":"alice@example.com","password":"hunter2"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("password login on a passkey-only account = %d, want 401", rec.Code)
	}
}

func TestPasswordLoginSucceedsWhenEnabled(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin)
	u.Spec.PasswordAuth = framev1alpha1.PasswordEnabled
	hash, _ := HashPassword("hunter2")
	u.Spec.PasswordHash = hash
	srv := testServer(t, u)

	rec := do(t, srv, http.MethodPost, "/auth/login/password", `{"email":"alice@example.com","password":"hunter2"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("password login = %d, want 204", rec.Code)
	}
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "Secure") {
		t.Fatalf("session cookie is not HttpOnly+Secure: %q", cookie)
	}
	if !strings.Contains(cookie, "SameSite=Strict") {
		t.Fatalf("session cookie is not SameSite=Strict: %q", cookie)
	}
}

func TestTokenRequiresASession(t *testing.T) {
	srv := testServer(t, fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin))
	rec := do(t, srv, http.MethodPost, "/auth/token", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("token without session = %d, want 401", rec.Code)
	}
}

// TestDummyPasswordHashIsValidAndVerifiable covers the timing-oracle fix
// found in review: handlePasswordLogin must call VerifyPassword exactly once
// on every failure path, including the ones where there is no real account
// or no real hash (unknown email, password disabled), using
// dummyPasswordHash so the argon2id cost — and so the wall-clock time — is
// identical to a genuine attempt. That guarantee only holds if
// dummyPasswordHash is itself a real, correctly-parsing argon2id hash rather
// than an empty or malformed placeholder that VerifyPassword would reject in
// microseconds via its early parse-failure return (see VerifyPassword's own
// "malformed input" fast path in password.go) — which would silently
// reintroduce the timing gap this fix closes.
func TestDummyPasswordHashIsValidAndVerifiable(t *testing.T) {
	if dummyPasswordHash == "" {
		t.Fatal("dummyPasswordHash was not initialized")
	}
	if !VerifyPassword(dummyPasswordHash, "authd-dummy-password-for-timing-safety") {
		t.Fatal("dummyPasswordHash does not parse as a genuine argon2id hash — VerifyPassword would " +
			"take its malformed-input fast path instead of paying the real argon2 cost, reopening the timing oracle")
	}
}

// TestPasswordLoginAlwaysVerifiesExactlyOnce is the structural,
// timing-independent counterpart to the timing-oracle fix: it proves
// handlePasswordLogin calls verifyPassword exactly once per request on every
// failure path (unknown email, password disabled, wrong password), not just
// that the dummy hash constant happens to be valid. A regression to the
// original `err != nil || u.Spec.PasswordAuth != Enabled || !VerifyPassword(...)`
// short-circuit would make this test fail with 0 calls for the first two
// cases, even though TestUnknownEmailAndWrongPasswordAreIndistinguishable and
// TestDummyPasswordHashIsValidAndVerifiable would both still pass — neither
// of those actually observes whether verifyPassword ran.
func TestPasswordLoginAlwaysVerifiesExactlyOnce(t *testing.T) {
	enabled := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin)
	enabled.Spec.PasswordAuth = framev1alpha1.PasswordEnabled
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	enabled.Spec.PasswordHash = hash

	disabled := fixture("bob", "bob@example.com", framev1alpha1.RoleViewer)
	disabled.Spec.PasswordAuth = framev1alpha1.PasswordDisabled

	cases := []struct {
		name string
		srv  *Server
		body string
	}{
		{"unknown email", testServer(t, enabled), `{"email":"ghost@example.com","password":"nope"}`},
		{"password disabled", testServer(t, disabled), `{"email":"bob@example.com","password":"nope"}`},
		{"wrong password", testServer(t, enabled), `{"email":"alice@example.com","password":"nope"}`},
	}

	original := verifyPassword
	defer func() { verifyPassword = original }()

	for _, tc := range cases {
		calls := 0
		verifyPassword = func(encoded, plain string) bool {
			calls++
			return original(encoded, plain)
		}
		rec := do(t, tc.srv, http.MethodPost, "/auth/login/password", tc.body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", tc.name, rec.Code)
		}
		if calls != 1 {
			t.Fatalf("%s: verifyPassword called %d times, want exactly 1", tc.name, calls)
		}
	}
}

func TestUnknownEmailAndWrongPasswordAreIndistinguishable(t *testing.T) {
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin)
	u.Spec.PasswordAuth = framev1alpha1.PasswordEnabled
	hash, _ := HashPassword("hunter2")
	u.Spec.PasswordHash = hash
	srv := testServer(t, u)

	wrong := do(t, srv, http.MethodPost, "/auth/login/password", `{"email":"alice@example.com","password":"nope"}`)
	unknown := do(t, srv, http.MethodPost, "/auth/login/password", `{"email":"ghost@example.com","password":"nope"}`)
	if wrong.Code != unknown.Code || wrong.Body.String() != unknown.Body.String() {
		t.Fatalf("responses differ: wrong=%d/%q unknown=%d/%q — this leaks which accounts exist",
			wrong.Code, wrong.Body.String(), unknown.Code, unknown.Body.String())
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
	srv, _ := bootstrapServer(t, "s3cret-bootstrap", "frame-auth-bootstrap", true)

	bootstrapRec := do(t, srv, http.MethodPost, "/auth/bootstrap", `{"token":"s3cret-bootstrap","email":"eve@example.com"}`)
	if bootstrapRec.Code != http.StatusNoContent {
		t.Fatalf("bootstrap = %d, want 204: %s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	session := sessionCookieFrom(t, bootstrapRec)

	rec := doWithCookie(t, srv, http.MethodPost, "/auth/register/begin", "", session)
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
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin)
	u.Spec.PasswordAuth = framev1alpha1.PasswordEnabled
	hash, _ := HashPassword("hunter2")
	u.Spec.PasswordHash = hash
	srv := testServer(t, u)

	loginRec := do(t, srv, http.MethodPost, "/auth/login/password", `{"email":"alice@example.com","password":"hunter2"}`)
	session := sessionCookieFrom(t, loginRec)

	rec := doWithCookie(t, srv, http.MethodPost, "/auth/token", "", session)
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
	u := fixture("alice", "alice@example.com", framev1alpha1.RoleViewer)
	srv, c := bootstrapServer(t, "s3cret-bootstrap", "frame-auth-bootstrap", false, u)

	sessRec := httptest.NewRecorder()
	if !srv.setSession(sessRec, u) {
		t.Fatal("setSession failed")
	}
	session := sessionCookieFrom(t, sessRec)

	var fresh framev1alpha1.FrameUser
	if err := c.Get(context.Background(), client.ObjectKey{Name: "alice", Namespace: "cluster-control"}, &fresh); err != nil {
		t.Fatalf("get: %v", err)
	}
	fresh.Spec.Role = framev1alpha1.RoleAdmin
	if err := c.Update(context.Background(), &fresh); err != nil {
		t.Fatalf("promote alice to admin: %v", err)
	}

	rec := doWithCookie(t, srv, http.MethodPost, "/auth/token", "", session)
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
	alice := fixture("alice", "alice@example.com", framev1alpha1.RoleAdmin)
	bob := fixture("bob", "bob@example.com", framev1alpha1.RoleViewer)
	srv := testServer(t, alice, bob)

	sessRec := httptest.NewRecorder()
	if !srv.setSession(sessRec, alice) {
		t.Fatal("setSession failed")
	}
	session := sessionCookieFrom(t, sessRec)

	rec := doWithCookie(t, srv, http.MethodPost, "/auth/register/begin", `{"email":"bob@example.com"}`, session)
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
