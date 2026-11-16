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

package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// Starting a ZFS scrub: the one storage command of the agent that writes.
//
// It is kept apart from storageCommands (localstorage.go), whose list is
// read-only by contract and checked as such by its tests. Its boundary:
//
//   - the argv is exactly `zpool scrub <pool>`, nothing else — no -s (stop),
//     no -p (pause), no -e, no option at all;
//   - the pool comes from the agent's own `zpool list` read of the node
//     (status.storage.local.zfs), never from the cluster: the controller's
//     request annotation is a timestamp and nothing more;
//   - the name must match ZFS's own grammar for a pool (a letter, then
//     letters, digits, '_', '-', '.', ':'), so it cannot read as an option;
//   - it runs through nsenter into PID 1's namespaces, under the host's
//     `timeout`, like the reads: the host's zpool, matched to its module.
//
// `zpool scrub` returns once the scrub is started; the scrub itself runs in
// the kernel. A pool that is not ONLINE or DEGRADED is skipped: a scrub on a
// suspended or faulted pool proves nothing and can hang the command.

// zpoolName is a pool name as zpool accepts one (zpool create(8)): a letter,
// then letters, digits, '_', '-', '.', ':' or a space, up to ZFS's 255.
// The argv reaches no shell, so a space is one argument; the leading letter
// keeps it from reading as an option.
var zpoolName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.: -]{0,254}$`)

// scrubRequestMaxAge is how old a request the agent still takes. The
// controller gives one up after ten minutes; past this one, it is a leftover
// (the policy deleted, the agent down for hours) or not the controller's,
// and starting it would scrub outside the window and beside another node.
// Wider than the controller's timeout for clock skew between the two.
const scrubRequestMaxAge = 20 * time.Minute

// scrubArgvPrefix is the fixed part of the one command this file runs.
var scrubArgvPrefix = []string{"zpool", "scrub"}

// scrubbableHealth is the pool health under which a scrub is started.
var scrubbableHealth = []string{"ONLINE", "DEGRADED"}

// errNoScrubbablePool: the node reports ZFS but no pool a scrub can run on.
var errNoScrubbablePool = errors.New("no ONLINE or DEGRADED pool to scrub")

// scrubArgv builds `zpool scrub <pool>` for a name that passes zpoolName.
func scrubArgv(pool string) ([]string, error) {
	if !zpoolName.MatchString(pool) {
		return nil, fmt.Errorf("refusing pool %q: not a ZFS pool name", pool)
	}
	return append(slices.Clone(scrubArgvPrefix), pool), nil
}

// execScrub runs argv, which must be exactly what scrubArgv builds, under the
// host's timeout. The only way anything here reaches the executor.
// It returns stderr, which says why a start failed; stdout is empty.
func execScrub(x StorageExecutor, argv []string) (string, error) {
	if len(argv) != len(scrubArgvPrefix)+1 ||
		!slices.Equal(argv[:len(scrubArgvPrefix)], scrubArgvPrefix) ||
		!zpoolName.MatchString(argv[len(argv)-1]) {
		return "", fmt.Errorf("refusing %q: not a ZFS scrub start", strings.Join(argv, " "))
	}
	_, stderr, err := x.Exec(append(slices.Clone(timeoutPrefix), argv...))
	return stderr, err
}

// ScrubPools starts a scrub on every pool of zfs that can take one, and
// returns the pools it started (or found already scrubbing). It fails when
// ZFS is not read as present, when no pool can take a scrub, or when any
// start fails: the controller then gives the request up rather than recording
// a scrub that did not run.
func ScrubPools(x StorageExecutor, zfs *framev1beta1.ZFSStatus) ([]string, error) {
	if zfs == nil || zfs.State != framev1beta1.StackPresent {
		state := "unread"
		if zfs != nil {
			state = string(zfs.State)
		}
		return nil, fmt.Errorf("ZFS is %s on this node", state)
	}
	if zfs.PoolsOmitted > 0 {
		// The read is capped; pools past the cap would go unscrubbed while
		// the node is recorded as scrubbed. The controller does not pick
		// such a node; this is the agent's own refusal.
		return nil, fmt.Errorf("the ZFS read left %d pool(s) out; refusing a partial scrub", zfs.PoolsOmitted)
	}
	var started []string
	var errs []error
	for _, p := range zfs.Pools {
		if !slices.Contains(scrubbableHealth, p.Health) {
			continue
		}
		argv, err := scrubArgv(p.Name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		stderr, err := execScrub(x, argv)
		switch {
		case err == nil:
		case strings.Contains(stderr, "currently scrubbing"):
			// Already running — a retry after a failed acknowledgement, or
			// a scrub someone else started. Either way the pool scrubs.
		case errors.Is(err, errBusy):
			errs = append(errs, fmt.Errorf("zpool scrub %s: %w", p.Name, err))
			continue
		default:
			errs = append(errs, fmt.Errorf("zpool scrub %s: %w: %s", p.Name, err, firstLine(stderr)))
			continue
		}
		started = append(started, p.Name)
	}
	if len(errs) > 0 {
		return started, errors.Join(errs...)
	}
	if len(started) == 0 {
		return nil, errNoScrubbablePool
	}
	return started, nil
}

// ServiceScrubRequest acts on the node's scrub request, once per request
// value: it acknowledges by writing the request value back with its own
// clock, THEN starts the scrub of the pools in zfs (the agent's own read).
//
// The acknowledgement comes first because the controller gives a request up
// ten minutes after issuing it unless it is acknowledged, and records the last
// read it saw so that a later read showing no scan lifts the lock. A failure
// landing between the agent's read and its `zpool scrub`, with the
// acknowledgement written only afterwards, could record a read older than the
// scan about to start, and the next read (taken before the scrub began) would
// lift the lock on a node that scrubs. Acknowledged first, a start the agent
// never makes is concluded by the controller as a scrub that ended without
// completing, which it already knows how to record.
//
// A start that fails with no pool started retracts the acknowledgement, so
// the next tick tries again until the controller gives the request up, as a
// request never acknowledged always did. Nothing scrubs then, so nothing is
// left for the lock to protect. If any pool did start, the acknowledgement
// stays: a scrub is running and the controller waits for it, and for the pool
// that did not start, it ends as a failure.
//
// It compares the request's VALUE with the acknowledgement, never its
// presence: a request the controller re-issues carries a new timestamp. A
// start that succeeded but whose acknowledgement was retracted, or any retry
// after a start already made, finds `zpool scrub` on a pool already scrubbing
// and takes it as started.
//
// zfs nil (no read yet) defers to the next tick rather than failing.
func ServiceScrubRequest(
	ctx context.Context, c client.Client, nodeName string,
	zfs *framev1beta1.ZFSStatus, x StorageExecutor, now func() time.Time,
) error {
	var node corev1.Node
	if err := c.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		return fmt.Errorf("reading node %s: %w", nodeName, err)
	}
	requested := node.Annotations[framev1beta1.ZFSScrubRequestedAnnotation]
	if requested == "" || node.Annotations[framev1beta1.ZFSScrubAcknowledgedAnnotation] == requested {
		return nil
	}
	if zfs == nil {
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, requested)
	if err != nil {
		return fmt.Errorf("scrub request %q is not a timestamp; ignored", requested)
	}
	if age := now().Sub(at); age > scrubRequestMaxAge {
		return fmt.Errorf("scrub request %s is more than %s old (%s); ignored (a leftover, or not the controller's)", requested, scrubRequestMaxAge, age.Truncate(time.Hour))
	}
	startedAt := now()
	base := node.DeepCopy()
	node.Annotations[framev1beta1.ZFSScrubAcknowledgedAnnotation] = requested
	node.Annotations[framev1beta1.ZFSScrubStartedAtAnnotation] = startedAt.UTC().Format(time.RFC3339Nano)
	// Optimistic lock: a request withdrawn or re-issued between the read
	// and this write must not be acknowledged under the old value, and no
	// scrub starts for it.
	if err := c.Patch(ctx, &node, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("acknowledging the scrub on %s: %w", nodeName, err)
	}
	started, err := ScrubPools(x, zfs)
	if err == nil {
		return nil
	}
	err = fmt.Errorf("scrub requested at %s: %w", requested, err)
	if len(started) > 0 {
		return err
	}
	acked := node.DeepCopy()
	delete(node.Annotations, framev1beta1.ZFSScrubAcknowledgedAnnotation)
	delete(node.Annotations, framev1beta1.ZFSScrubStartedAtAnnotation)
	if perr := c.Patch(ctx, &node, client.MergeFromWithOptions(acked, client.MergeFromWithOptimisticLock{})); perr != nil {
		// Left acknowledged: the controller concludes the request as a
		// failure, which is the safe direction.
		return errors.Join(err, fmt.Errorf("retracting the acknowledgement on %s: %w", nodeName, perr))
	}
	return err
}
