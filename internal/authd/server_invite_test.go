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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
//
// Reads url.Fragment, not Query: the token rides in the fragment so it is
// never sent to a server. url.Parse has already percent-decoded Fragment
// into RawFragment's decoded form, which is the counterpart of the
// url.QueryEscape the handler applies — except for '+', which QueryEscape
// writes for a space and Fragment does not decode back. The sealed tokens
// this codec produces are base64url, so no space can arise; parsing the
// pair the same way the browser's URLSearchParams does is what keeps this
// helper honest about what the console will actually receive.
func inviteURLToken(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("invite url %q does not parse: %v", rawURL, err)
	}
	values, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatalf("invite url fragment %q does not parse: %v", parsed.Fragment, err)
	}
	return values.Get("token")
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

	operator := fixture("carol", "carol@example.com", framev1beta1.RoleOperator)
	if err := c.Create(context.Background(), operator); err != nil {
		t.Fatalf("seed operator: %v", err)
	}
	rec = doWithCookie(t, srv, "/auth/invite",
		`{"email":"new@example.com","role":"viewer"}`, sessionFor(t, srv, operator))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("invite from an operator = %d, want 403", rec.Code)
	}

	if n := countUsers(t, c); n != 2 {
		t.Fatalf("a refused invite created an account: %d users, want 2 (viewer + operator fixtures)", n)
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
	// The response body is a bearer credential (the sealed invite token), like
	// every session cookie this package hands out — it must not sit in a
	// shared cache or the browser's back-forward cache.
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}

	var body struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The fragment, not the query string: a fragment is never sent to a
	// server, so the token stays out of access logs and out of the
	// same-origin Referer of every asset the /invite page loads. A '?' here
	// would put a live bearer credential back into all of them.
	if strings.Contains(body.URL, "?") {
		t.Fatalf("invite url = %q, want the token in the fragment and nothing in the query string", body.URL)
	}
	if !strings.HasPrefix(body.URL, testConsoleOrigin+"/invite#token=") {
		t.Fatalf("invite url = %q, want %s/invite#token=…", body.URL, testConsoleOrigin)
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
	// The object name is the lowercased, "-at-"-spelled derivation of the
	// email, plus a hash suffix that keeps two distinct addresses from ever
	// deriving the same name (frameUserNameForEmail). The suffix itself is
	// not pinned here — only that the readable prefix is right and a
	// disambiguating suffix is actually present.
	const wantPrefix = "bob-at-example.com-"
	if !strings.HasPrefix(created.Name, wantPrefix) || len(created.Name) == len(wantPrefix) {
		t.Fatalf("object name = %q, want %q plus a disambiguating suffix", created.Name, wantPrefix)
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

	// A 255-byte local part pushes the address one byte past maxEmailLength
	// (254, mirroring the CRD's own MaxLength on spec.email). Nothing else
	// in this handler rejects it — emailPattern alone matches a local part
	// of any length — so without the length check this row is the one that
	// would turn green and hide it.
	tooLongEmail := strings.Repeat("a", 250) + "@x.io"
	for _, body := range []string{
		`{"email":"not-an-email","role":"viewer"}`,
		`{"email":"","role":"viewer"}`,
		`{"email":"new@example.com","role":"editor"}`,
		`{"email":"new@example.com","role":""}`,
		`{"email":"` + tooLongEmail + `","role":"viewer"}`,
	} {
		if rec := doWithCookie(t, srv, "/auth/invite", body, session); rec.Code != http.StatusBadRequest {
			t.Fatalf("invite %s = %d, want 400", body, rec.Code)
		}
	}
	if n := countUsers(t, c); n != 1 {
		t.Fatalf("a refused invite created an account: %d users, want 1", n)
	}

	// An unknown role is not a request to become an admin, so it must not
	// be answered with advice about promoting to admin — that advice is
	// gated on body.Role == RoleAdmin specifically, not on "any refused
	// role".
	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"new@example.com","role":"editor"}`, session)
	if strings.Contains(rec.Body.String(), "Accounts screen") {
		t.Fatalf("a typo'd role got admin-promotion advice: %q", rec.Body.String())
	}
}

// The fixture's object name — "bob-at-example.com" — is deliberately the
// pre-disambiguation derivation, not what frameUserNameForEmail produces
// today (which appends a hash suffix). This is the live cluster's bootstrap
// admin's shape: an account created before invite existed, under whatever
// name derivation was in force then. Identity here is Spec.Email, checked by
// Store.ByEmail before Create ever runs — not the derived object name, which
// this fixture deliberately does not match. A version that disambiguated the
// name (so two distinct addresses can never collide) without also adding
// that email pre-check would compute a fresh, different name for this same
// address, see no name collision at Create, and silently create a second
// FrameUser with the same Spec.Email — this test would then observe 200, not
// 409, and fail.
func TestInviteRefusesADuplicate(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin,
		fixture("bob-at-example.com", "bob@example.com", framev1beta1.RoleViewer))

	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"bob@example.com","role":"viewer"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusConflict {
		t.Fatalf("inviting an existing account = %d, want 409", rec.Code)
	}
	if n := countUsers(t, c); n != 2 {
		t.Fatalf("a refused duplicate invite created another account: %d users, want 2 (admin + bob)", n)
	}

	// Case must not be a way around the same check.
	rec = doWithCookie(t, srv, "/auth/invite",
		`{"email":"Bob@Example.com","role":"viewer"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusConflict {
		t.Fatalf("inviting an existing account under a different case = %d, want 409", rec.Code)
	}
	if n := countUsers(t, c); n != 2 {
		t.Fatalf("a case-varied duplicate invite created another account: %d users, want 2", n)
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

// inviteFor drives a real /auth/invite as an admin and returns the token from
// the link. Going through the route rather than sealing a token by hand is
// what makes the tests below cover the pair rather than one half of it.
func inviteFor(t *testing.T, srv *Server, admin *framev1beta1.FrameUser, email, role string) string {
	t.Helper()
	rec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"`+email+`","role":"`+role+`"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("invite = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return inviteURLToken(t, body.URL)
}

func TestInviteAcceptGrantsAShortSessionThatCanEnrol(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin)
	token := inviteFor(t, srv, admin, "bob@example.com", "viewer")

	rec := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("accept = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	session := sessionCookieFrom(t, rec)
	if session.MaxAge != int(enrolSessionTTL.Seconds()) {
		t.Fatalf("accepted session Max-Age = %d, want %d — an accepted invitation is a "+
			"window to enrol, not a working day", session.MaxAge, int(enrolSessionTTL.Seconds()))
	}

	// The session is not merely present; it is the one thing the invitee
	// needs. /auth/register/begin is the only route to a first credential.
	begin := doWithCookie(t, srv, "/auth/register/begin", "", session)
	if begin.Code != http.StatusOK {
		t.Fatalf("register/begin on an accepted invitation = %d, want 200: %s", begin.Code, begin.Body.String())
	}
}

// TestSecondInviteAcceptIsRefusedOnceAKeyIsEnrolled is the single-use proof.
// The first acceptance succeeds; a credential is then added through the store,
// exactly as a completed enrolment would; the same link is presented again and
// must be refused. Delete the credential check in handleInviteAccept and this
// returns 204 — the link would be a standing key to the account.
func TestSecondInviteAcceptIsRefusedOnceAKeyIsEnrolled(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)
	token := inviteFor(t, srv, admin, "bob@example.com", "viewer")

	if rec := do(t, srv, http.MethodPost, "/auth/invite/accept",
		`{"token":"`+token+`"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("first accept = %d, want 204", rec.Code)
	}

	store := NewStore(c, "cluster-control")
	bob, err := store.ByEmail(context.Background(), "bob@example.com")
	if err != nil {
		t.Fatalf("ByEmail: %v", err)
	}
	if err := store.AddCredential(context.Background(), bob, framev1beta1.WebAuthnCredential{
		ID: "ZW5yb2xsZWQ", PublicKey: "cGs", AddedAt: metav1.Now(), Label: "YubiKey 5C",
	}); err != nil {
		t.Fatalf("AddCredential: %v", err)
	}

	rec := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
	if rec.Code != http.StatusGone {
		t.Fatalf("second accept = %d, want 410", rec.Code)
	}
	if hasSessionCookie(rec) {
		t.Fatal("a spent invitation still handed out a session")
	}
}

// The password branch of the same guard. No route sets a password today, so
// this state is unreachable — which is exactly why it is pinned: "any
// credential" must keep meaning any credential when one arrives.
func TestInviteAcceptIsRefusedOnceAPasswordExists(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)
	token := inviteFor(t, srv, admin, "bob@example.com", "viewer")

	var bob framev1beta1.FrameUser
	// Derive the name rather than spelling it: frameUserNameForEmail appends a
	// hash suffix, so a literal "bob-at-example.com" is NotFound here.
	if err := c.Get(context.Background(),
		client.ObjectKey{Name: frameUserNameForEmail("bob@example.com"), Namespace: "cluster-control"}, &bob); err != nil {
		t.Fatalf("get: %v", err)
	}
	bob.Status.PasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"
	if err := c.Status().Update(context.Background(), &bob); err != nil {
		t.Fatalf("status update: %v", err)
	}

	rec := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
	if rec.Code != http.StatusGone {
		t.Fatalf("accept for an account holding a password = %d, want 410", rec.Code)
	}
	// Unlike the credential branch's own test, this one previously stopped at
	// the status code: a regression that minted the cookie before checking
	// PasswordHash would still return 410 here and pass unnoticed.
	if hasSessionCookie(rec) {
		t.Fatal("a spent invitation still handed out a session")
	}
}

// TestInviteAcceptPathRefusesADisabledAccount covers path 4 of 4:
// POST /auth/invite/accept. Same shape as the three in state_test.go, and
// named the same way, because the defect guarded against is "the check exists
// in three places out of four".
func TestInviteAcceptPathRefusesADisabledAccount(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)
	token := inviteFor(t, srv, admin, "bob@example.com", "viewer")

	var bob framev1beta1.FrameUser
	// Derive the name rather than spelling it: frameUserNameForEmail appends a
	// hash suffix, so a literal "bob-at-example.com" is NotFound here.
	if err := c.Get(context.Background(),
		client.ObjectKey{Name: frameUserNameForEmail("bob@example.com"), Namespace: "cluster-control"}, &bob); err != nil {
		t.Fatalf("get: %v", err)
	}
	bob.Spec.State = framev1beta1.StateDisabled
	if err := c.Update(context.Background(), &bob); err != nil {
		t.Fatalf("disable bob: %v", err)
	}

	rec := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("accept for a disabled account = %d, want 403", rec.Code)
	}
	if hasSessionCookie(rec) {
		t.Fatal("a disabled account was handed a session cookie")
	}
}

func TestInviteAcceptRefusesForgedExpiredAndUnknown(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin)

	expired, err := testCodec().Seal(PurposeInvite, []byte("bob@example.com"), -time.Second)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// A well-formed, unexpired token for an account that does not exist —
	// deleted between invitation and acceptance.
	unknown, err := testCodec().Seal(PurposeInvite, []byte("ghost@example.com"), time.Hour)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	sessionShaped, err := testCodec().Seal(PurposeSession, []byte("root@example.com"), time.Hour)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	for name, token := range map[string]string{
		"forged":        "forged.token",
		"expired":       expired,
		"unknown":       unknown,
		"session-shape": sessionShaped,
	} {
		// t.Run so a failure in one case is reported against that case, and
		// map iteration order (randomised by Go) does not hide the other
		// cases behind a single Fatalf that stops the loop.
		t.Run(name, func(t *testing.T) {
			rec := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("accept with a %s token = %d, want 401", name, rec.Code)
			}
			if hasSessionCookie(rec) {
				t.Fatalf("a %s token produced a session cookie", name)
			}
		})
	}
}

// TestAcceptingAnUnspentLinkTwiceIsNotItselfSpending pins today's behaviour:
// accepting the same link twice, with no credential enrolled in between, is
// not refused — both calls return 204. This is deliberate, not an oversight:
// handleInviteAccept's guard is "does the account hold a credential", not
// "has this link been presented before", so re-loading the invite page or
// retrying a flaky network call does not burn the invitation. It is only
// safe because each acceptance grants its own PurposeEnrol cookie that can
// do nothing but enrol a credential (see TestAnAcceptedInvitationCannotMintATokenOrInvite);
// two such cookies live at once is not the standing access a second full
// session would be. If this test's expectation ever needs to become 410 on
// the second call, that is a deliberate design change, not a silent one.
func TestAcceptingAnUnspentLinkTwiceIsNotItselfSpending(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin)
	token := inviteFor(t, srv, admin, "bob@example.com", "viewer")

	first := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
	if first.Code != http.StatusNoContent {
		t.Fatalf("first accept = %d, want 204: %s", first.Code, first.Body.String())
	}
	second := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
	if second.Code != http.StatusNoContent {
		t.Fatalf("second accept of an unspent link = %d, want 204: %s", second.Code, second.Body.String())
	}
}

// TestAnAcceptedInvitationCannotMintATokenOrInvite is the fix for the defect
// the review found: setSessionFor originally sealed the accept-minted cookie
// under PurposeSession, the same purpose as a full sign-in, so it worked on
// every session-consuming route — including /auth/token, which would hand
// the holder a minted id_token, and /auth/invite, which would let a
// sufficiently-privileged invitee invite further accounts. Neither the
// account's role nor the short cookie TTL stopped that: the TTL only bounded
// how long a single enrolment window lasted, and it was trivially renewable
// by accepting again (see TestAcceptingAnUnspentLinkTwiceIsNotItselfSpending),
// making the link a renewable day-long console credential rather than the
// enrolment-only credential the design intended. Sealing under PurposeEnrol
// and restricting sessionUser to PurposeSession closes both paths: this test
// is the one that would have caught it, since /auth/token is exactly the
// route a full session — but not an enrolment cookie — must be able to reach.
func TestAnAcceptedInvitationCannotMintATokenOrInvite(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin)
	token := inviteFor(t, srv, admin, "bob@example.com", "viewer")

	rec := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("accept = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	enrolSession := sessionCookieFrom(t, rec)

	if tokenRec := doWithCookie(t, srv, "/auth/token", "", enrolSession); tokenRec.Code != http.StatusUnauthorized {
		t.Fatalf("/auth/token with an enrolment-only cookie = %d, want 401: %s",
			tokenRec.Code, tokenRec.Body.String())
	}
	if inviteRec := doWithCookie(t, srv, "/auth/invite",
		`{"email":"carol@example.com","role":"viewer"}`, enrolSession); inviteRec.Code != http.StatusUnauthorized {
		t.Fatalf("/auth/invite with an enrolment-only cookie = %d, want 401: %s",
			inviteRec.Code, inviteRec.Body.String())
	}
}

// TestOrdinarySessionStillWorksOnRegisterRoutes guards the permissive
// reader against the opposite mistake: sessionUserFor must still accept a
// normal PurposeSession cookie on the register routes, not just the new
// PurposeEnrol one, or ordinary passkey management (adding a second key from
// an already-signed-in session) would break.
func TestOrdinarySessionStillWorksOnRegisterRoutes(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin)
	session := sessionFor(t, srv, admin)

	rec := doWithCookie(t, srv, "/auth/register/begin", "", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("register/begin with an ordinary session = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// TestEnrolCookieCanReachRegisterFinish pins the permissive reader on the
// *last* step of enrolment, not just the first. Every other test in this
// package that exercises the enrol cookie stops at /auth/register/begin —
// nothing before this drove /auth/register/finish with it. That is a real
// gap, not a redundant belt-and-braces test: handleRegisterFinish reads its
// own sessionUserFor call independently of handleRegisterBegin's, so
// reverting line 109 of server_webauthn.go from sessionUserFor back to the
// strict sessionUser leaves every other test in the suite green — begin
// would still work, and nothing else calls finish — while the invitation
// flow silently dead-ends at the last step for every real invitee.
//
// A garbage WebAuthn attestation body is enough to pin the reader: it drives
// FinishRegistration to fail the ceremony (400), which is a different,
// later failure than sessionUserFor rejecting the cookie before the ceremony
// is even reached (401). This test cannot fabricate a real signed
// attestation — that needs a live authenticator — so 400 is the strongest
// assertion available from here, and it is exactly the one that
// distinguishes "the cookie was admitted" from "the cookie was refused."
func TestEnrolCookieCanReachRegisterFinish(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin)
	token := inviteFor(t, srv, admin, "bob@example.com", "viewer")

	acceptRec := do(t, srv, http.MethodPost, "/auth/invite/accept", `{"token":"`+token+`"}`)
	if acceptRec.Code != http.StatusNoContent {
		t.Fatalf("accept = %d, want 204: %s", acceptRec.Code, acceptRec.Body.String())
	}
	enrol := sessionCookieFrom(t, acceptRec)

	beginRec := doWithCookie(t, srv, "/auth/register/begin", "", enrol)
	if beginRec.Code != http.StatusOK {
		t.Fatalf("register/begin with the enrol cookie = %d, want 200: %s", beginRec.Code, beginRec.Body.String())
	}
	var challenge *http.Cookie
	for _, c := range beginRec.Result().Cookies() {
		if c.Name == challengeCookie {
			challenge = c
		}
	}
	if challenge == nil {
		t.Fatal("register/begin did not set a challenge cookie")
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/register/finish", strings.NewReader("not-a-real-attestation"))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(enrol)
	req.AddCookie(challenge)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("register/finish with the enrol cookie and a garbage body = %d, want 400 — "+
			"400 means the cookie was admitted and only the ceremony itself failed; "+
			"401 would mean handleRegisterFinish rejected the enrol cookie before ever "+
			"reaching the ceremony: %s", rec.Code, rec.Body.String())
	}
}

// TestInviteLinkRequiresAnAdminSession is the /auth/invite/link counterpart
// to TestInviteRequiresAnAdminSession above: an anonymous caller gets 401,
// and a caller with a valid session that is not an admin — viewer or
// operator — gets 403. This is the mutant "the admin gate was dropped (or
// only checked for an anonymous caller)": a version that checked only
// sessionUser's ok and skipped the role comparison would let the viewer and
// operator cases below through as 200.
func TestInviteLinkRequiresAnAdminSession(t *testing.T) {
	viewer := fixture("bob", "bob@example.com", framev1beta1.RoleViewer)
	target := fixture("dana-at-example.com", "dana@example.com", framev1beta1.RoleViewer)
	srv, c := bootstrapServer(t, false, viewer, target)

	if rec := do(t, srv, http.MethodPost, "/auth/invite/link",
		`{"email":"dana@example.com"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("invite/link with no session = %d, want 401", rec.Code)
	}
	rec := doWithCookie(t, srv, "/auth/invite/link",
		`{"email":"dana@example.com"}`, sessionFor(t, srv, viewer))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("invite/link from a viewer = %d, want 403", rec.Code)
	}

	operator := fixture("carol", "carol@example.com", framev1beta1.RoleOperator)
	if err := c.Create(context.Background(), operator); err != nil {
		t.Fatalf("seed operator: %v", err)
	}
	rec = doWithCookie(t, srv, "/auth/invite/link",
		`{"email":"dana@example.com"}`, sessionFor(t, srv, operator))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("invite/link from an operator = %d, want 403", rec.Code)
	}
}

// TestInviteLinkReturnsAFreshLinkForAnExistingAccount is the 200 path: an
// admin re-requesting a link for an account that already exists and holds no
// credential gets a link shaped exactly like handleInvite's — Cache-Control:
// no-store, the token in the fragment (never the query string), sealed under
// PurposeInvite, and opening to the target's own address.
func TestInviteLinkReturnsAFreshLinkForAnExistingAccount(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	target := fixture("bob-at-example.com", "bob@example.com", framev1beta1.RoleViewer)
	srv, _ := bootstrapServer(t, false, admin, target)

	rec := doWithCookie(t, srv, "/auth/invite/link", `{"email":"bob@example.com"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("invite/link = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q — the body carries a bearer credential", got, "no-store")
	}

	var body struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The mutant "the token rides in the query string, not the fragment":
	// a '?' anywhere in the URL means the token would be sent to a server on
	// every request the /invite page makes and logged along the way.
	if strings.Contains(body.URL, "?") {
		t.Fatalf("invite/link url = %q, want the token in the fragment and nothing in the query string", body.URL)
	}
	if !strings.HasPrefix(body.URL, testConsoleOrigin+"/invite#token=") {
		t.Fatalf("invite/link url = %q, want %s/invite#token=…", body.URL, testConsoleOrigin)
	}

	// The mutant "sealed under the wrong purpose": a token sealed under
	// PurposeSession (or any purpose but PurposeInvite) fails to open here,
	// and — the other half of the same mutant — must not open as a session
	// either.
	payload, err := testCodec().Open(PurposeInvite, inviteURLToken(t, body.URL))
	if err != nil {
		t.Fatalf("the link's token does not open under PurposeInvite: %v", err)
	}
	if string(payload) != "bob@example.com" {
		t.Fatalf("token carries %q, want the target account's address", payload)
	}
	if _, err := testCodec().Open(PurposeSession, inviteURLToken(t, body.URL)); err == nil {
		t.Fatal("the link's token opened as a session")
	}
}

// TestInviteLinkReturns404ForAnUnknownEmail covers the case the gap
// description calls out first: no account holds the address at all.
func TestInviteLinkReturns404ForAnUnknownEmail(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, _ := bootstrapServer(t, false, admin)

	rec := doWithCookie(t, srv, "/auth/invite/link", `{"email":"ghost@example.com"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invite/link for an unknown email = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// TestInviteLinkResolvesByEmailExactlyNotCaseInsensitively pins the
// constraint that ByEmail must be used unmodified: a lookup by a
// differently-cased address must miss, even though the account exists under
// another casing. Store.ByEmail's own history records folding case there as
// a Critical defect (see TestByEmailDoesNotConflateAccountsThatDifferOnlyByCase
// in store_test.go); a version of this handler that pre-normalized the
// input, or swapped in a case-insensitive lookup, would turn this into a
// 200 instead of the 404 that ByEmail's exact-match contract requires.
func TestInviteLinkResolvesByEmailExactlyNotCaseInsensitively(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	target := fixture("bob-at-example.com", "bob@example.com", framev1beta1.RoleViewer)
	srv, _ := bootstrapServer(t, false, admin, target)

	rec := doWithCookie(t, srv, "/auth/invite/link", `{"email":"Bob@Example.com"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invite/link for a differently-cased address = %d, want 404 (ByEmail is exact-match only)", rec.Code)
	}
}

// TestInviteLinkRefusesAnAccountThatAlreadyHasACredential is the 410 guard:
// an account that has already enrolled a passkey must not be handed another
// invitation link, using the exact condition and wording
// handleInviteAccept uses for the same state. Deleting this guard would
// turn this into a 200 that mints a link acceptance would refuse anyway.
func TestInviteLinkRefusesAnAccountThatAlreadyHasACredential(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	target := fixture("bob-at-example.com", "bob@example.com", framev1beta1.RoleViewer,
		framev1beta1.WebAuthnCredential{ID: "ZW5yb2xsZWQ", PublicKey: "cGs", AddedAt: metav1.Now(), Label: "YubiKey 5C"})
	srv, _ := bootstrapServer(t, false, admin, target)

	rec := doWithCookie(t, srv, "/auth/invite/link", `{"email":"bob@example.com"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusGone {
		t.Fatalf("invite/link for an enrolled account = %d, want 410: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "this account already has a credential") {
		t.Fatalf("body = %q, want the same wording handleInviteAccept uses", rec.Body.String())
	}
}

// TestInviteLinkRefusesAnAccountThatAlreadyHasAPassword is the password
// branch of the same 410 guard.
func TestInviteLinkRefusesAnAccountThatAlreadyHasAPassword(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	srv, c := bootstrapServer(t, false, admin)

	var bob framev1beta1.FrameUser
	target := fixture("bob-at-example.com", "bob@example.com", framev1beta1.RoleViewer)
	if err := c.Create(context.Background(), target); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := c.Get(context.Background(),
		client.ObjectKey{Name: "bob-at-example.com", Namespace: "cluster-control"}, &bob); err != nil {
		t.Fatalf("get: %v", err)
	}
	bob.Status.PasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"
	if err := c.Status().Update(context.Background(), &bob); err != nil {
		t.Fatalf("status update: %v", err)
	}

	rec := doWithCookie(t, srv, "/auth/invite/link", `{"email":"bob@example.com"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusGone {
		t.Fatalf("invite/link for an account holding a password = %d, want 410: %s", rec.Code, rec.Body.String())
	}
}

// TestInviteLinkRefusesADisabledAccount is the 403 guard: a disabled account
// cannot be issued an identity, so a link for it is useless. Deleting the
// requireIssuable check would turn this into a 200.
func TestInviteLinkRefusesADisabledAccount(t *testing.T) {
	admin := fixture("root", "root@example.com", framev1beta1.RoleAdmin)
	target := fixture("bob-at-example.com", "bob@example.com", framev1beta1.RoleViewer)
	target.Spec.State = framev1beta1.StateDisabled
	srv, _ := bootstrapServer(t, false, admin, target)

	rec := doWithCookie(t, srv, "/auth/invite/link", `{"email":"bob@example.com"}`, sessionFor(t, srv, admin))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("invite/link for a disabled account = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "this account is disabled") {
		t.Fatalf("body = %q, want the same wording handleInviteAccept uses", rec.Body.String())
	}
}
