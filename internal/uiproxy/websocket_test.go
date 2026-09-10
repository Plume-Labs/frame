package uiproxy

import (
	"reflect"
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
	}{
		{
			// What a browser sends: one header, comma-separated.
			name:     "one header two protocols",
			values:   []string{"v4.channel.k8s.io, " + bearerProtocolPrefix + enc},
			wantTok:  tok,
			wantRest: []string{"v4.channel.k8s.io"},
		},
		{
			// What a Go client sends: Header.Add twice.
			name:     "two headers",
			values:   []string{"v4.channel.k8s.io", bearerProtocolPrefix + enc},
			wantTok:  tok,
			wantRest: []string{"v4.channel.k8s.io"},
		},
		{
			name:     "no bearer entry",
			values:   []string{"v4.channel.k8s.io"},
			wantTok:  "",
			wantRest: []string{"v4.channel.k8s.io"},
		},
		{
			// Undecodable is not a token. Returning the raw text would put a
			// caller-controlled string into Verify, and a padded or otherwise
			// malformed value must fail closed.
			name:     "undecodable bearer entry",
			values:   []string{"v4.channel.k8s.io, " + bearerProtocolPrefix + "not!base64url"},
			wantTok:  "",
			wantRest: []string{"v4.channel.k8s.io"},
		},
		{
			name:     "no header at all",
			values:   nil,
			wantTok:  "",
			wantRest: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTok, gotRest := tokenFromProtocols(tc.values)
			if gotTok != tc.wantTok {
				t.Fatalf("token = %q, want %q", gotTok, tc.wantTok)
			}
			if len(gotRest) != len(tc.wantRest) || (len(gotRest) > 0 && !reflect.DeepEqual(gotRest, tc.wantRest)) {
				t.Fatalf("remaining = %v, want %v", gotRest, tc.wantRest)
			}
		})
	}
}
