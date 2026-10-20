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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/Plume-Labs/frame/internal/redfish"
)

// Probe cadence. Every probe is a full Redfish walk of the machine — a few
// dozen requests against a BMC that answers slowly — so the interval has a
// floor: a value under MinProbeInterval is raised to it, whatever the flag
// says.
const (
	DefaultProbeInterval = 60 * time.Second
	MinProbeInterval     = 60 * time.Second

	// A failed probe waits probeBackoffBase, then twice as long on each
	// failure in a row, up to probeBackoffMax. A BMC that does not answer is
	// not asked a full walk every minute; one that recovers is probed again
	// within half an hour at worst, and on the interval as soon as one probe
	// succeeds.
	probeBackoffBase = 5 * time.Minute
	probeBackoffMax  = 30 * time.Minute

	// logoutTimeout bounds the session DELETEs sent when the manager stops:
	// shutdown must not hang on a BMC that stopped answering.
	logoutTimeout = 10 * time.Second
)

// machineClients holds, per FrameMachine, the Redfish client whose session
// is reused from one probe to the next, and the count of failed probes in a
// row. Its zero value is ready to use.
type machineClients struct {
	mu       sync.Mutex
	clients  map[types.NamespacedName]redfish.Client
	failures map[types.NamespacedName]int
}

// probeInterval is the delay between two successful probes.
func (r *FrameMachineReconciler) probeInterval() time.Duration {
	if r.ProbeInterval < MinProbeInterval {
		if r.ProbeInterval == 0 {
			return DefaultProbeInterval
		}
		return MinProbeInterval
	}
	return r.ProbeInterval
}

// probeBackoff is the delay after the n-th failed probe in a row (n >= 1).
func probeBackoff(n int) time.Duration {
	d := probeBackoffBase
	for i := 1; i < n; i++ {
		d *= 2
		if d >= probeBackoffMax {
			return probeBackoffMax
		}
	}
	return d
}

// clientFor returns the client to probe key's machine with. fresh was just
// built from the machine's spec and Secret. When the client already held
// for key talks to the same BMC with the same credentials and TLS trust, it
// is kept — and with it its session, which is the whole point: a new client
// would log in again. Otherwise fresh replaces it, and the replaced one's
// session is ended.
func (r *FrameMachineReconciler) clientFor(ctx context.Context, key types.NamespacedName, fresh redfish.Client) redfish.Client {
	m := &r.machines
	m.mu.Lock()
	held, ok := m.clients[key]
	if ok && redfish.SameTarget(held, fresh) {
		m.mu.Unlock()
		return held
	}
	if m.clients == nil {
		m.clients = map[types.NamespacedName]redfish.Client{}
	}
	m.clients[key] = fresh
	m.mu.Unlock()

	if ok && held != fresh {
		logout(ctx, key, held)
	}
	return fresh
}

// forget drops key's client and failure count and ends its session: the
// machine is gone, or its credentials are.
func (r *FrameMachineReconciler) forget(ctx context.Context, key types.NamespacedName) {
	m := &r.machines
	m.mu.Lock()
	held, ok := m.clients[key]
	delete(m.clients, key)
	delete(m.failures, key)
	m.mu.Unlock()
	if ok {
		logout(ctx, key, held)
	}
}

// probeFailed records one more failed probe for key and returns the delay
// before the next one.
func (r *FrameMachineReconciler) probeFailed(key types.NamespacedName) time.Duration {
	m := &r.machines
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failures == nil {
		m.failures = map[types.NamespacedName]int{}
	}
	m.failures[key]++
	return probeBackoff(m.failures[key])
}

// probeSucceeded clears key's failure count and returns the delay before
// the next probe.
func (r *FrameMachineReconciler) probeSucceeded(key types.NamespacedName) time.Duration {
	m := &r.machines
	m.mu.Lock()
	delete(m.failures, key)
	m.mu.Unlock()
	return r.probeInterval()
}

// logoutAll ends every held session. It runs when the manager stops.
func (r *FrameMachineReconciler) logoutAll(ctx context.Context) {
	m := &r.machines
	m.mu.Lock()
	held := m.clients
	m.clients = nil
	m.failures = nil
	m.mu.Unlock()
	for key, rc := range held {
		logout(ctx, key, rc)
	}
}

func logout(ctx context.Context, key types.NamespacedName, rc redfish.Client) {
	if err := rc.Logout(ctx); err != nil {
		logf.FromContext(ctx).Info("Could not end the Redfish session; it will expire on the BMC's idle timeout",
			"machine", key, "error", err.Error())
	}
}

// sessionLogoutOnStop is the manager runnable that ends every held session
// when the manager stops, so a controller restart does not leave one
// session per machine open on the BMCs until their idle timeout.
func (r *FrameMachineReconciler) sessionLogoutOnStop() manager.Runnable {
	return manager.RunnableFunc(func(ctx context.Context) error {
		<-ctx.Done()
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), logoutTimeout)
		defer cancel()
		r.logoutAll(ctrl.LoggerInto(lctx, logf.FromContext(ctx)))
		return nil
	})
}
