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

package controller

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/Plume-Labs/frame/internal/redfish"
)

// A BMC whose Redfish port refuses the connection gets its own reason, read
// through the real client's error chain rather than a hand-built error: the
// test proves transportError keeps ECONNREFUSED reachable by errors.Is.
func TestProbeFailureReasonNamesARefusedConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	// Closing the listener leaves a port nothing listens on: the kernel
	// answers the next dial with a RST, exactly what a BMC with its web
	// service down does.
	_ = ln.Close()

	c := redfish.New("https://"+addr, "u", "p", &tls.Config{MinVersion: tls.VersionTLS12})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c.Probe(ctx)
	if err == nil {
		t.Fatal("probe of a closed port succeeded")
	}
	if got := probeFailureReason(err); got != "ConnectionRefused" {
		t.Fatalf("reason = %q, want ConnectionRefused (err: %v)", got, err)
	}
}

// The other reasons keep their meaning: a timeout stays Timeout, an error
// none of the sentinels explain stays ProbeFailed.
func TestProbeFailureReasonKeepsTheOtherReasons(t *testing.T) {
	cases := map[string]error{
		"Timeout":     fmt.Errorf("redfish: request /redfish/v1/: %w", context.DeadlineExceeded),
		"ProbeFailed": errors.New("redfish: decode /redfish/v1/Systems/1: unexpected EOF"),
		"TLSError":    redfish.ErrTLS,
		"AuthFailed":  redfish.ErrAuth,
		"Unsupported": redfish.ErrUnsupported,
	}
	if got := probeFailureReason(fakeTimeoutError{}); got != "Timeout" {
		t.Errorf("a net.Error timeout = %q, want Timeout", got)
	}
	for want, err := range cases {
		if got := probeFailureReason(err); got != want {
			t.Errorf("probeFailureReason(%v) = %q, want %q", err, got, want)
		}
	}
}
