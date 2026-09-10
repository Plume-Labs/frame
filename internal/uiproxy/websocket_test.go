package uiproxy

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestTokenFromProtocols(t *testing.T) {
	const tok = "header.payload.signature"
	// Unpadded base64url of the token above. A subprotocol token cannot
	// contain "=", so the padded form is not merely untidy — the browser
	// refuses to send it and the handshake never leaves the tab.
	const enc = "aGVhZGVyLnBheWxvYWQuc2lnbmF0dXJl"

	cases := []struct {
		name     string
		values   []string
		wantTok  string
		wantRest []string
		wantSaw  bool
	}{
		{
			// What a browser sends: one header, comma-separated.
			name:     "one header two protocols",
			values:   []string{"v4.channel.k8s.io, " + bearerProtocolPrefix + enc},
			wantTok:  tok,
			wantRest: []string{"v4.channel.k8s.io"},
			wantSaw:  true,
		},
		{
			// What a Go client sends: Header.Add twice.
			name:     "two headers",
			values:   []string{"v4.channel.k8s.io", bearerProtocolPrefix + enc},
			wantTok:  tok,
			wantRest: []string{"v4.channel.k8s.io"},
			wantSaw:  true,
		},
		{
			name:     "no bearer entry",
			values:   []string{"v4.channel.k8s.io"},
			wantTok:  "",
			wantRest: []string{"v4.channel.k8s.io"},
			wantSaw:  false,
		},
		{
			// Undecodable is not a token, but it was still seen: the apiserver
			// recognises the prefix on sight, so this must be reported as
			// present even though nothing decoded — see the sawBearer doc on
			// tokenFromProtocols.
			name:     "undecodable bearer entry",
			values:   []string{"v4.channel.k8s.io, " + bearerProtocolPrefix + "not!base64url"},
			wantTok:  "",
			wantRest: []string{"v4.channel.k8s.io"},
			wantSaw:  true,
		},
		{
			name:     "no header at all",
			values:   nil,
			wantTok:  "",
			wantRest: nil,
			wantSaw:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTok, gotRest, gotSaw := tokenFromProtocols(tc.values)
			if gotTok != tc.wantTok {
				t.Fatalf("token = %q, want %q", gotTok, tc.wantTok)
			}
			if len(gotRest) != len(tc.wantRest) || (len(gotRest) > 0 && !reflect.DeepEqual(gotRest, tc.wantRest)) {
				t.Fatalf("remaining = %v, want %v", gotRest, tc.wantRest)
			}
			if gotSaw != tc.wantSaw {
				t.Fatalf("sawBearer = %v, want %v", gotSaw, tc.wantSaw)
			}
		})
	}
}

// A browser cannot set Authorization on a WebSocket, so without this the
// exec handshake is refused by the proxy's own 401 and the terminal never
// opens for anyone. Remove the subprotocol lookup and this test reports 401.
func TestAcceptsTheBearerTokenFromTheWebSocketSubprotocol(t *testing.T) {
	up := echoUpgrade(t)
	defer up.Close()
	p := newTestProxy(t, stubVerifier{id: Identity{User: "alice@example.com", Groups: []string{"admins"}}}, up.URL)
	front := httptest.NewServer(p)
	defer front.Close()

	enc := base64.RawURLEncoding.EncodeToString([]byte("a.b.c"))
	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	// No Authorization header — exactly what a browser can send.
	req := "GET /api/v1/namespaces/neura/pods/api-0/exec HTTP/1.1\r\n" +
		"Host: frame.test\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Protocol: v4.channel.k8s.io, " + bearerProtocolPrefix + enc + "\r\n\r\n"
	if _, err := fmt.Fprint(conn, req); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("got %d (%s), want 101", res.StatusCode, body)
	}
}

// The console's token is authd's, not a credential the apiserver accepts.
// Forwarded, the apiserver's own WebSocket handler would see a subprotocol it
// did not offer and refuse the handshake — and the token would land in the
// apiserver's audit log for every shell anyone opens.
func TestStripsTheBearerSubprotocolBeforeForwarding(t *testing.T) {
	var seen http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	p := newTestProxy(t, stubVerifier{id: Identity{User: "alice@example.com"}}, up.URL)

	enc := base64.RawURLEncoding.EncodeToString([]byte("a.b.c"))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/neura/pods/api-0/exec", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "v4.channel.k8s.io, "+bearerProtocolPrefix+enc)
	p.ServeHTTP(httptest.NewRecorder(), req)

	got := seen.Get("Sec-WebSocket-Protocol")
	if strings.Contains(got, bearerProtocolPrefix) {
		t.Fatalf("the console's bearer token reached the apiserver: %q", got)
	}
	if got != "v4.channel.k8s.io" {
		t.Fatalf("Sec-WebSocket-Protocol = %q, want v4.channel.k8s.io — the real protocol must survive", got)
	}
}

// Fix for review finding 1: an undecodable bearer-prefixed entry is still a
// bearer entry as far as the apiserver is concerned — its WebSocket auth
// handler recognises the prefix on sight, before it would ever try to decode
// what follows it. A caller can supply an independently valid Authorization
// header (so the request isn't rejected for lacking a token at all) alongside
// a garbage subprotocol that merely carries the bearer prefix.
//
// This is the discriminating assertion: gate the strip on `wsToken != ""`
// (the pre-fix behaviour) instead of on `sawBearer`, and this entry — which
// never decodes, so wsToken is always "" — is left untouched in
// Sec-WebSocket-Protocol and reaches the upstream verbatim, tripping the
// bearerProtocolPrefix Contains check below.
func TestStripsAnUndecodableBearerSubprotocolBeforeForwarding(t *testing.T) {
	var seen http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	p := newTestProxy(t, stubVerifier{id: Identity{User: "alice@example.com"}}, up.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/neura/pods/api-0/exec", nil)
	req.Header.Set("Authorization", "Bearer good")
	req.Header.Set("Sec-WebSocket-Protocol", "v4.channel.k8s.io, "+bearerProtocolPrefix+"not!base64url")
	p.ServeHTTP(httptest.NewRecorder(), req)

	got := seen.Get("Sec-WebSocket-Protocol")
	if strings.Contains(got, bearerProtocolPrefix) {
		t.Fatalf("an undecodable bearer-prefixed subprotocol reached the apiserver: %q", got)
	}
	if got != "v4.channel.k8s.io" {
		t.Fatalf("Sec-WebSocket-Protocol = %q, want v4.channel.k8s.io — the real protocol must survive", got)
	}
}
