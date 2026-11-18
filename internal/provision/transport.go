/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package provision

import (
	"net"
	"net/http"
	"time"
)

// boundedTransport is http.DefaultTransport with the phases that can hang
// forever given a limit: connecting, the TLS handshake, and waiting for
// response headers. http.DefaultClient has none of these on the response
// header, so an endpoint that accepts the connection and never answers would
// block a provisioning reconcile until its context ends — and several callers
// pass a context without a deadline. The response body is deliberately not
// bounded here: an ISO download is long by nature, and the caller's context
// owns that.
func boundedTransport(responseHeader time.Duration) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ResponseHeaderTimeout = responseHeader
	return t
}

// defaultBuildClient talks to the in-cluster build API. Building an image can
// take minutes before the first response byte, hence the long header timeout.
var defaultBuildClient = &http.Client{Transport: boundedTransport(15 * time.Minute)}
