package uiproxy

import (
	"encoding/base64"
	"strings"
)

// bearerProtocolPrefix carries a bearer token as a WebSocket subprotocol.
//
// It exists because `new WebSocket(url, protocols)` is the only WebSocket a
// browser can open, and it accepts no request headers — so the Authorization
// header every other request in the console carries has nowhere to go. This is
// Kubernetes' own convention for the problem (the same constant lives in
// k8s.io/apiserver/pkg/authentication/request/websocket), which is why the
// console's client needs no special case: it offers the entry, and whoever
// authenticates the request consumes it.
//
// Here that is this proxy, not the apiserver: the token is authd's, and the
// apiserver would neither accept it nor recognise the subprotocol. So the
// entry is removed on the way through — see tokenFromProtocols.
const bearerProtocolPrefix = "base64url.bearer.authorization.k8s.io."

// tokenFromProtocols pulls the bearer entry out of a Sec-WebSocket-Protocol
// header and returns the decoded token, every other protocol the client
// offered (in order), and whether a bearer-prefixed entry was present at
// all.
//
// `values` is Header.Values("Sec-WebSocket-Protocol"): a client may send one
// header with a comma-separated list (what a browser does) or several headers
// (what Header.Add does), and both are one list.
//
// The encoding is unpadded base64url — RFC 6455 forbids "=" in a subprotocol
// token, so the padded form never reaches here from a real client. An entry
// that does not decode yields no token rather than its raw text: the value is
// caller-controlled, and a malformed credential must fail closed.
//
// sawBearer is true whenever an entry carried the prefix, decodable or not.
// The apiserver's own WebSocket auth handler recognises the prefix on sight,
// before it would ever try to decode what follows it — so a caller-chosen
// string that merely carries the prefix must be stripped from what gets
// forwarded just the same as a real token, or the handshake reaches the
// apiserver offering a subprotocol it never offered and never asked for.
func tokenFromProtocols(values []string) (token string, remaining []string, sawBearer bool) {
	for _, v := range values {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if !strings.HasPrefix(p, bearerProtocolPrefix) {
				remaining = append(remaining, p)
				continue
			}
			sawBearer = true
			if raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(p, bearerProtocolPrefix)); err == nil {
				token = string(raw)
			}
		}
	}
	return token, remaining, sawBearer
}
