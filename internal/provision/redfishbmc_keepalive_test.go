package provision

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type connRequestsKey struct{}

// TestRedfishBMCSurvivesABMCThatDropsKeptAliveConnections reproduces the
// iLO 4 failure seen on an ML350 Gen9: "Patch .../VirtualMedia/2/: EOF".
// The iLO closes a connection after answering, without a Connection: close
// header, so Go keeps it in its idle pool and writes the next request on it.
// A GET on a dead reused connection is retried by net/http; a PATCH is not
// (not idempotent), so it fails with EOF.
//
// The server here answers exactly one request per TCP connection. Any
// further request arriving on the same connection is met by a hijack and a
// bare close -- no response, no Connection: close. A plain "close right
// after the response" would race Go's background read loop, which usually
// sees the FIN first and discards the connection; closing when the next
// request lands is the deterministic form of the same iLO behaviour.
func TestRedfishBMCSurvivesABMCThatDropsKeptAliveConnections(t *testing.T) {
	var patches int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := r.Context().Value(connRequestsKey{}).(*int32)
		if atomic.AddInt32(n, 1) > 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			atomic.AddInt32(&patches, 1)
		}
		if r.Method == http.MethodGet {
			if r.URL.Path == "/redfish/v1/Systems/1/" {
				// An active override, so ClearBootOverride goes on to PATCH:
				// the write is what net/http does not retry.
				_, _ = w.Write([]byte(`{"Boot": {"BootSourceOverrideEnabled": "Once", "BootSourceOverrideTarget": "Cd"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"Inserted": true}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, connRequestsKey{}, new(int32))
	}
	srv.Start()
	t.Cleanup(srv.Close)

	b := NewRedfishBMC(srv.URL, "admin", "s3cret", nil)
	ctx := context.Background()

	inserted, err := b.MediaInserted(ctx)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if !inserted {
		t.Fatalf("GET: Inserted = false, want true")
	}
	if err := b.ClearBootOverride(ctx); err != nil {
		t.Fatalf("PATCH after GET: %v", err)
	}
	if n := atomic.LoadInt32(&patches); n != 1 {
		t.Fatalf("got %d PATCHes, want 1: without a write this test proves nothing", n)
	}
}
