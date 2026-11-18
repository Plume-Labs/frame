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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBoundedTransportGivesUpOnAServerThatNeverAnswers(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)

	client := &http.Client{Transport: boundedTransport(100 * time.Millisecond)}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	start := time.Now()
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected a header timeout")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s to give up", time.Since(start))
	}
}

func TestBuildAndBaseClientsAreBounded(t *testing.T) {
	if defaultBuildClient.Transport.(*http.Transport).ResponseHeaderTimeout == 0 {
		t.Fatal("the build client has no response-header timeout")
	}
	if baseHTTPClient.Transport.(*http.Transport).ResponseHeaderTimeout == 0 {
		t.Fatal("the base-image client has no response-header timeout")
	}
}
