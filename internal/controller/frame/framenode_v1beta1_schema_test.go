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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// These specs are about the v1beta1 *schema*, not about the FrameNode
// controller: the enum, the bounds and the CEL rules introduced in the API
// freeze are enforced by the apiserver from the CRD, so nothing but a real
// apiserver can show them working. They run against the CRDs as kustomize
// renders them (renderedCRDPath), which is the artefact the cluster installs.
//
// The bounds are the point. A ceiling can be raised after the freeze but never
// introduced, so each one is asserted by writing an object the apiserver must
// *reject* — an assertion that the field merely exists would pass just as well
// against v1alpha1's unbounded string. Each rejection is paired with a value
// sitting exactly on the bound, so a rule that rejects everything cannot pass
// for a rule that rejects the right things.
var _ = Describe("FrameNode v1beta1 schema", func() {
	// liveHostname is the hostname of the stored FrameNode these fixtures
	// mirror.
	const liveHostname = "node-cp"

	// liveShapedNode is modelled field-for-field on node-cp, one of the
	// three FrameNodes stored on the test cluster (rack-01/local, HIGH, a /24
	// network address, no spec.disk), minus status.phase which v1beta1 drops.
	// If v1beta1 cannot hold this, conversion has nowhere to put the objects
	// that exist.
	liveShapedNode := func(name string) *framev1beta1.FrameNode {
		return &framev1beta1.FrameNode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: framev1beta1.FrameNodeSpec{
				IP:       "192.0.2.201",
				Role:     "controlplane",
				Hostname: liveHostname,
				Rack:     "rack-01",
				Zone:     "local",
				Network: framev1beta1.NetworkSpec{
					Address: "192.0.2.201/24",
					Gateway: "192.0.2.254",
					DNS:     []string{"192.0.2.254"},
				},
				ServiceClass: framev1beta1.ServiceClassHigh,
			},
		}
	}

	It("holds a stored object's shape without loss", func() {
		node := liveShapedNode("live-shaped-node")
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		key := types.NamespacedName{Name: "live-shaped-node", Namespace: "default"}
		Expect(k8sClient.Get(ctx, key, node)).To(Succeed())

		// network.address is a CIDR on all three stored objects, not a bare
		// address. It deliberately carries no isIP rule; if one is ever added
		// this spec is what fails.
		Expect(node.Spec.Network.Address).To(Equal("192.0.2.201/24"))

		node.Status.ObservedGeneration = 1
		node.Status.NodeName = liveHostname
		node.Status.Conditions = []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "Online",
			Message:            "Synced from k8s Node",
			ObservedGeneration: 1,
			LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())

		back := &framev1beta1.FrameNode{}
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		Expect(back.Spec.ServiceClass).To(Equal(framev1beta1.ServiceClassHigh))
		Expect(back.Spec.Rack).To(Equal("rack-01"))
		Expect(back.Spec.Zone).To(Equal("local"))
		Expect(back.Status.ObservedGeneration).To(BeNumerically("==", 1))

		// The reason is the state machine's value, not a label: Task 20
		// repoints framenode_controller.go off Status.Phase and onto exactly
		// this string. If it stops round tripping, that move breaks silently.
		Expect(back.Status.Conditions).To(HaveLen(1))
		Expect(back.Status.Conditions[0].Reason).To(Equal("Online"))
	})

	// The Talos provisioning half of FrameNode is gone: spec.disk gated it,
	// and status.discovered* reported what the maintenance API answered. None
	// of the stored FrameNodes ever carried any of them. Pruning is silent, so
	// as for status.phase the fields are written through an unstructured
	// client and must read back gone, beside a kept field that proves the
	// writes happened.
	It("no longer stores spec.disk or the Talos discovery status", func() {
		node := liveShapedNode("no-talos-fields")
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		key := types.NamespacedName{Name: "no-talos-fields", Namespace: "default"}
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameNode"))
		Expect(k8sClient.Get(ctx, key, raw)).To(Succeed())
		Expect(unstructured.SetNestedField(raw.Object, "/dev/nvme0n1", "spec", "disk")).To(Succeed())
		Expect(unstructured.SetNestedField(raw.Object, "rack-02", "spec", "rack")).To(Succeed())
		Expect(k8sClient.Update(ctx, raw)).To(Succeed())

		Expect(k8sClient.Get(ctx, key, raw)).To(Succeed())
		Expect(unstructured.SetNestedField(raw.Object, "v1.8.0", "status", "discoveredTalosVersion")).To(Succeed())
		Expect(unstructured.SetNestedField(raw.Object, "cp", "status", "discoveredHostname")).To(Succeed())
		Expect(unstructured.SetNestedSlice(raw.Object, []any{map[string]any{"name": "/dev/sda", "size": "1TB", "type": "ssd"}},
			"status", "discoveredDisks")).To(Succeed())
		Expect(unstructured.SetNestedField(raw.Object, liveHostname, "status", "nodeName")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, raw)).To(Succeed())

		back := &unstructured.Unstructured{}
		back.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameNode"))
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		for _, path := range [][]string{
			{"spec", "disk"},
			{"status", "discoveredTalosVersion"},
			{"status", "discoveredHostname"},
			{"status", "discoveredDisks"},
		} {
			_, found, err := unstructured.NestedFieldNoCopy(back.Object, path...)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "v1beta1 must not store %v", path)
		}
		rack, _, _ := unstructured.NestedString(back.Object, "spec", "rack")
		Expect(rack).To(Equal("rack-02"), "the spec write happened")
		nodeName, _, _ := unstructured.NestedString(back.Object, "status", "nodeName")
		Expect(nodeName).To(Equal(liveHostname), "the status write happened")
	})

	It("does not store status.phase (F2)", func() {
		// Pruning is silent, so the only way to see it is to write the field
		// through an unstructured client and read it back gone. The v1alpha1
		// schema still has phase; this asserts the v1beta1 schema does not.
		node := liveShapedNode("no-phase")
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameNode"))
		key := types.NamespacedName{Name: "no-phase", Namespace: "default"}
		Expect(k8sClient.Get(ctx, key, raw)).To(Succeed())
		Expect(unstructured.SetNestedField(raw.Object, "Online", "status", "phase")).To(Succeed())
		Expect(unstructured.SetNestedField(raw.Object, liveHostname, "status", "nodeName")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, raw)).To(Succeed())

		back := &unstructured.Unstructured{}
		back.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameNode"))
		Expect(k8sClient.Get(ctx, key, back)).To(Succeed())
		_, found, err := unstructured.NestedString(back.Object, "status", "phase")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "v1beta1 must not store status.phase")

		// The counterpart, so the assertion above cannot pass because the
		// status update silently did nothing at all.
		nodeName, found, err := unstructured.NestedString(back.Object, "status", "nodeName")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(nodeName).To(Equal(liveHostname))
	})

	DescribeTable("rejects a spec the freeze bounds forbid",
		func(name string, mutate func(*framev1beta1.FrameNodeSpec)) {
			node := liveShapedNode(name)
			mutate(&node.Spec)
			err := k8sClient.Create(ctx, node)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })
			}
			Expect(err).To(HaveOccurred(), "the apiserver accepted a spec the schema must reject")
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
		},
		// T1: rack is written straight onto the Kubernetes Node as a label
		// value. v1alpha1 admitted every one of these.
		Entry("a rack longer than 63 characters (T1)", "reject-rack-length",
			func(s *framev1beta1.FrameNodeSpec) { s.Rack = strings.Repeat("a", 64) }),
		Entry("a rack containing a slash and spaces (T1)", "reject-rack-chars",
			func(s *framev1beta1.FrameNodeSpec) { s.Rack = "rack/one with spaces" }),
		Entry("a rack starting with a dash, which no label value may (T1)", "reject-rack-leading-dash",
			func(s *framev1beta1.FrameNodeSpec) { s.Rack = "-rack-01" }),
		Entry("a rack ending in a dot, which no label value may (T1)", "reject-rack-trailing-dot",
			func(s *framev1beta1.FrameNodeSpec) { s.Rack = "rack-01." }),
		Entry("a zone longer than 63 characters (T1)", "reject-zone-length",
			func(s *framev1beta1.FrameNodeSpec) { s.Zone = strings.Repeat("z", 64) }),
		Entry("a zone containing a colon (T1)", "reject-zone-chars",
			func(s *framev1beta1.FrameNodeSpec) { s.Zone = "eu:west" }),
		// T2: Format=ip was a no-op, so this is the first schema that catches it.
		Entry("an ip that is not an IP address (T2)", "reject-ip-shape",
			func(s *framev1beta1.FrameNodeSpec) { s.IP = "192.0.2.999" }),
		Entry("an ip that is a hostname (T2)", "reject-ip-hostname",
			func(s *framev1beta1.FrameNodeSpec) { s.IP = liveHostname }),
		Entry("an ip longer than 45 characters (T2)", "reject-ip-length",
			func(s *framev1beta1.FrameNodeSpec) { s.IP = strings.Repeat("1", 46) }),
		Entry("a missing ip, which is required", "reject-ip-missing",
			func(s *framev1beta1.FrameNodeSpec) { s.IP = "" }),
		Entry("a serviceClass outside the three tiers (F4)", "reject-unknown-class",
			func(s *framev1beta1.FrameNodeSpec) { s.ServiceClass = "URGENT" }),
		Entry("a role outside the enum", "reject-role",
			func(s *framev1beta1.FrameNodeSpec) { s.Role = "etcd" }),
		Entry("an uppercase hostname", "reject-hostname-case",
			func(s *framev1beta1.FrameNodeSpec) { s.Hostname = "Node-CP" }),
		Entry("a dns entry that is not an IP", "reject-dns",
			func(s *framev1beta1.FrameNodeSpec) { s.Network.DNS = []string{"resolver.local"} }),
		// Fix Round 1: length is the only thing bounding network.address.
		Entry("a network.address longer than 45 characters", "reject-address-length",
			func(s *framev1beta1.FrameNodeSpec) { s.Network.Address = strings.Repeat("a", 46) }),
	)

	It("rejects an explicitly empty serviceClass on the wire, and does not default an absent one", func() {
		// The empty string is deliberately not an enum member (F4), and it was
		// FrameNode's alone. Proving its removal needs an unstructured object:
		// the typed FrameNodeSpec tags serviceClass omitempty, so a Go client
		// setting it to "" sends no key at all. A hand-written YAML, a kubectl
		// patch or the TypeScript SDK can all still put "" on the wire.
		raw := &unstructured.Unstructured{}
		raw.SetGroupVersionKind(framev1beta1.GroupVersion.WithKind("FrameNode"))
		raw.SetNamespace("default")
		raw.SetName("reject-empty-class")
		Expect(unstructured.SetNestedMap(raw.Object, map[string]any{
			"ip":           "192.0.2.201",
			"serviceClass": "",
		}, "spec")).To(Succeed())

		err := k8sClient.Create(ctx, raw)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, raw) })
		}
		Expect(err).To(HaveOccurred(), `the apiserver accepted serviceClass: ""`)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)

		// The counterpart, so the spec above cannot pass by rejecting
		// everything — and the assertion that matters for FrameNode
		// specifically: unlike FrameJob, an absent serviceClass stays absent.
		// A node is discovered before it is classified, so a default here
		// would classify hardware nobody has looked at.
		typed := &framev1beta1.FrameNode{
			ObjectMeta: metav1.ObjectMeta{Name: "reject-empty-class", Namespace: "default"},
			Spec:       framev1beta1.FrameNodeSpec{IP: "192.0.2.201", ServiceClass: ""},
		}
		Expect(k8sClient.Create(ctx, typed)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, typed) })
		Expect(typed.Spec.ServiceClass).To(BeEmpty(), "FrameNode.spec.serviceClass must stay undefaulted")
	})

	It("accepts a spec sitting exactly on each bound", func() {
		// The complement of the table above: a bound that rejects the legal
		// maximum is a different bug from one that accepts everything, and
		// only writing both tells them apart.
		node := liveShapedNode("on-the-bound-node")
		node.Spec.Rack = strings.Repeat("a", 63)
		node.Spec.Zone = strings.Repeat("z", 63)
		node.Spec.Hostname = strings.Repeat("h", 63)
		// 39 characters: a fully expanded IPv6, which is the longest string
		// isIP() accepts. maxLength=45 is therefore unreachable — see the
		// following spec for why it is 45 anyway.
		node.Spec.IP = "2001:0db8:85a3:0000:0000:8a2e:0370:7334"
		Expect(node.Spec.IP).To(HaveLen(39))
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })
	})

	It("bounds network.address by length only, deliberately not by format", func() {
		// Fix Round 1. The bound exists because an unbounded string had to
		// not survive the freeze. The *absence* of a pattern is equally deliberate: the field
		// is named "address" while every stored value is a prefix, and
		// guessing at freeze time is worse than documenting the format and
		// leaving it unenforced. Both halves are asserted, so a later reader
		// who adds a pattern breaks a spec that says why it was declined.
		longestRealPrefix := "2001:0db8:85a3:0000:0000:8a2e:0370:7334/128"
		Expect(longestRealPrefix).To(HaveLen(43), "the longest expanded IPv6 prefix")

		node := liveShapedNode("address-longest-prefix")
		node.Spec.Network.Address = longestRealPrefix
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

		// Exactly on the bound, and not a CIDR at all: no pattern is enforced
		// here, and that is the documented decision rather than an oversight.
		unformatted := liveShapedNode("address-unformatted")
		unformatted.Spec.Network.Address = strings.Repeat("a", 45)
		Expect(k8sClient.Create(ctx, unformatted)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, unformatted) })
	})

	It("rejects the IP forms isIP() does not accept but net.ParseIP does", func() {
		// T2 replaced a no-op Format=ip marker with a CEL rule, and the CEL
		// rule is stricter than the Go webhook it supersedes: the pinned
		// k8s.io/apiserver IP library rejects IPv4-mapped IPv6 and zoned
		// addresses, both of which net.ParseIP accepts. That is a genuine
		// behaviour change at the API boundary, so it is pinned rather than
		// discovered later by a client that sends one. None of the three
		// stored FrameNodes uses either form.
		for name, ip := range map[string]string{
			"reject-ip-v4mapped-short": "::ffff:192.168.100.228",
			"reject-ip-v4mapped-long":  "0000:0000:0000:0000:0000:ffff:192.168.100.228",
			"reject-ip-zoned":          "fe80::1%eth0",
		} {
			node := liveShapedNode(name)
			node.Spec.IP = ip
			err := k8sClient.Create(ctx, node)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })
			}
			Expect(err).To(HaveOccurred(), "the apiserver accepted ip: %q", ip)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error for %q, got: %v", ip, err)
		}
	})

})
