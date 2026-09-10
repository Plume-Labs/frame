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

	"github.com/rmocq/frame/internal/redfish"
)

// fakeRedfish and fakeTimeoutError are split out of framemachine_controller_
// test.go, which they were pushed out of by task 6b's additions, to keep
// both files under this repo's line ceiling. They are used from Describe
// blocks in that file and any sibling FrameMachine controller test file in
// this package.

// fakeRedfish is the seam the whole controller is tested through: no network,
// no fixtures, just the two outcomes the controller has to tell apart.
type fakeRedfish struct {
	snapshot *redfish.Snapshot
	probeErr error

	// resetErr, when set, is returned by the next Reset call and then
	// cleared — a single-shot failure, so a spec can drive one attempt into
	// error and the next into success without a second fake.
	resetErr error

	resets   []string
	clearLog int
	ledCalls []bool
}

func (f *fakeRedfish) Probe(context.Context) (*redfish.Snapshot, error) {
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	return f.snapshot, nil
}
func (f *fakeRedfish) Reset(_ context.Context, t string) error {
	f.resets = append(f.resets, t)
	if f.resetErr != nil {
		err := f.resetErr
		f.resetErr = nil
		return err
	}
	return nil
}
func (f *fakeRedfish) ClearLog(context.Context) error { f.clearLog++; return nil }
func (f *fakeRedfish) SetIndicatorLED(_ context.Context, on bool) error {
	f.ledCalls = append(f.ledCalls, on)
	return nil
}

// fakeTimeoutError satisfies net.Error without pulling in a real network
// timeout, so probeFailureReason's net.Error branch can be driven
// deterministically. net.Error still requires the deprecated Temporary()
// method as of this Go version — omitting it makes errors.As silently
// return false rather than fail to compile, since the assertion happens by
// reflection against the interface, not at compile time.
type fakeTimeoutError struct{}

func (fakeTimeoutError) Error() string   { return "i/o timeout" }
func (fakeTimeoutError) Timeout() bool   { return true }
func (fakeTimeoutError) Temporary() bool { return true }
