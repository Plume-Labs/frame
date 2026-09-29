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

package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// nodeIdentity is what the existing Node reported before the reinstall.
// UID is kept for the record printed at preflight; BootID and MachineID are
// what the Ready check compares (kubeconfigNodeChecker.rejoined).
type nodeIdentity struct {
	UID       string
	BootID    string
	MachineID string
}

// preflightJoin checks the existing cluster against the config before the
// machine is touched, and records the existing Node's identity for the
// Ready check. It is the only thing frame bootstrap does against a cluster
// before the disk is wiped; LoadConfig itself never leaves this laptop.
//
// Each refusal is one a join would otherwise meet only after the wipe:
//   - no Node by that name: the config names a node this cluster does not
//     have, so "rejoin" would create a new one -- a typo in hostname, or the
//     wrong cluster;
//   - its InternalIP differs from network.address: the config would give the
//     machine an address other than the one the cluster knows it by;
//   - the node-password Secret still exists: k3s refuses an agent that
//     registers under a known name with a new password, which a reinstall
//     always has, and the node never comes back (checked again just before
//     MediaAttached, see checkNoNodePassword);
//   - k3sVersion newer than the server: an agent newer than its server is
//     outside k3s's supported skew.
func preflightJoin(ctx context.Context, stdout io.Writer, cfg Config) (nodeIdentity, error) {
	cs, err := clientsetFor(cfg.existingKubeconfig)
	if err != nil {
		return nodeIdentity{}, err
	}
	name := cfg.Hostname

	node, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nodeIdentity{}, fmt.Errorf(
			"the existing cluster has no Node %q: a rejoin keeps the node's name, so hostname must name a node "+
				"this cluster already has (kubectl get nodes)", name)
	}
	if err != nil {
		return nodeIdentity{}, fmt.Errorf("reading Node %s from the existing cluster: %w", name, err)
	}

	want := addressWithoutCIDR(cfg.Network.Address)
	got := internalIP(node)
	if got != want {
		return nodeIdentity{}, fmt.Errorf(
			"node %s's InternalIP is %q but network.address gives the machine %s: the cluster knows this node by "+
				"another address", name, got, want)
	}

	if err := checkNoNodePassword(ctx, cs, name); err != nil {
		return nodeIdentity{}, err
	}

	sv, err := cs.Discovery().ServerVersion()
	if err != nil {
		return nodeIdentity{}, fmt.Errorf("reading the existing cluster's version: %w", err)
	}
	c, err := compareK3sVersions(cfg.Cluster.K3sVersion, sv.GitVersion)
	if err != nil {
		return nodeIdentity{}, err
	}
	switch {
	case c > 0:
		return nodeIdentity{}, fmt.Errorf(
			"cluster.k3sVersion %s is newer than the server's %s: an agent must not run ahead of its server; "+
				"set k3sVersion to %s", cfg.Cluster.K3sVersion, sv.GitVersion, sv.GitVersion)
	case c < 0:
		_, _ = fmt.Fprintf(stdout,
			"frame bootstrap: warning: cluster.k3sVersion %s is older than the server's %s; allowed, but the "+
				"same version is what this path expects\n", cfg.Cluster.K3sVersion, sv.GitVersion)
	}

	id := nodeIdentity{
		UID:       string(node.UID),
		BootID:    node.Status.NodeInfo.BootID,
		MachineID: node.Status.NodeInfo.MachineID,
	}
	_, _ = fmt.Fprintf(stdout,
		"frame bootstrap: preflight: Node %s (uid %s) exists at %s, no stale node password, server %s; "+
			"its boot ID and machine ID must both change before Ready counts\n",
		name, id.UID, want, sv.GitVersion)
	return id, nil
}

// checkNoNodePassword refuses while kube-system/<name>.node-password.k3s
// exists. It runs twice on a join: in the preflight, and again just before
// MediaAttached (execute wires it into Deps.BeforeMediaAttached) -- the
// last point before the BMC is driven. In between, the machine's old
// k3s-agent, if it still runs, re-creates the Secret the operator deleted;
// the reinstalled agent would then be refused only after the wipe. Stop
// the old agent (systemctl stop k3s-agent) or the machine before deleting.
func checkNoNodePassword(ctx context.Context, cs kubernetes.Interface, name string) error {
	secret := name + ".node-password.k3s"
	_, err := cs.CoreV1().Secrets("kube-system").Get(ctx, secret, metav1.GetOptions{})
	switch {
	case err == nil:
		return fmt.Errorf(
			"kube-system/%s still exists: k3s refuses an agent that re-registers as %s with a new node password, "+
				"which a reinstalled machine always has. Stop the old agent (systemctl stop k3s-agent) or the "+
				"machine, then delete it: kubectl -n kube-system delete secret %s",
			secret, name, secret)
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("checking kube-system/%s: %w", secret, err)
	}
	return nil
}

// recheckNodePassword is checkNoNodePassword against the cluster a join
// config names, for Deps.BeforeMediaAttached.
func recheckNodePassword(cfg Config) func(context.Context) error {
	return func(ctx context.Context) error {
		cs, err := clientsetFor(cfg.existingKubeconfig)
		if err != nil {
			return err
		}
		return checkNoNodePassword(ctx, cs, cfg.Hostname)
	}
}

func internalIP(node *corev1.Node) string {
	for _, a := range node.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

var k3sVersionParts = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)\+k3s([0-9]+)$`)

// compareK3sVersions returns -1, 0 or 1 as want is older than, equal to or
// newer than server. Both must be k3s versions: a server that does not
// report one is not a cluster this path knows how to rejoin.
func compareK3sVersions(want, server string) (int, error) {
	w := k3sVersionParts.FindStringSubmatch(want)
	if w == nil {
		return 0, fmt.Errorf("cluster.k3sVersion %q is not a k3s version", want)
	}
	s := k3sVersionParts.FindStringSubmatch(server)
	if s == nil {
		return 0, fmt.Errorf("the existing cluster reports version %q, which is not a k3s version", server)
	}
	for i := 1; i <= 4; i++ {
		a, _ := strconv.Atoi(w[i])
		b, _ := strconv.Atoi(s[i])
		if a != b {
			if a < b {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}
