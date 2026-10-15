package redfish

import (
	"net/http"
	"testing"
)

// The poller's client opens one connection per request, like the
// provisioning one (internal/provision/redfishbmc_keepalive_test.go shows
// the EOF an iLO 4 gives otherwise): its writes -- Reset, ClearLog, the
// session login -- are POSTs net/http does not retry on a dead reused
// connection.
func TestNewDisablesKeepAlives(t *testing.T) {
	c, ok := New("https://bmc.invalid", "u", "p", nil).(*client)
	if !ok {
		t.Fatal("New did not return *client")
	}
	tr, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.httpClient.Transport)
	}
	if !tr.DisableKeepAlives {
		t.Error("DisableKeepAlives = false, want true")
	}
}
