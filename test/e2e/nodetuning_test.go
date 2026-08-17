//go:build e2e
// +build e2e

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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/test/utils"
)

// agentImage is the node agent image built and loaded for these specs. Its own
// image, not the manager's: the agent is a separate binary in a separate,
// deliberately larger runtime image (it carries tuned).
const agentImage = "example.com/frame-agent:v0.0.1"

const (
	agentNamespace = "kube-system"
	agentDaemonSet = "frame-node-tuning-agent"
	nodeTuningName = "e2e"
)

// e2eUnit is the systemd unit these specs drive. Kind nodes run kubelet
// directly and have no k3s, so BeforeAll installs a harmless stand-in named
// k3s.service — harmless because it only sleeps, and named k3s because the
// agent's unit detection and its restart allowlist are both compile-time
// closed sets, which is the property under test everywhere else. Restarting
// it exercises the real code path (systemd-run, on the node's real systemd)
// without bouncing the kubelet out from under the test cluster.
const e2eUnit = "k3s"

// e2eUnitFile is written into the node's systemd tree so DetectKSMUnit finds
// a unit to work with.
const e2eUnitFile = `[Unit]
Description=Frame e2e stand-in for k3s.service
[Service]
ExecStart=/usr/bin/sleep infinity
Restart=always
`

var _ = Describe("NodeTuning", Ordered, func() {
	var nodeName string

	BeforeAll(func() {
		By("building the node agent image")
		cmd := exec.Command("make", "docker-build-agent", fmt.Sprintf("IMG_AGENT=%s", agentImage))
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to build the node agent image")

		By("loading the node agent image on Kind")
		Expect(utils.LoadImageToKindClusterWithName(agentImage)).To(Succeed())

		By("creating the manager namespace")
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", namespace))

		By("installing CRDs")
		_, err = utils.Run(exec.Command("make", "install"))
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		_, err = utils.Run(exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage)))
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("resolving the node to tune")
		out, err := utils.Run(exec.Command("kubectl", "get", "nodes",
			"-o", "jsonpath={.items[0].metadata.name}"))
		Expect(err).NotTo(HaveOccurred())
		nodeName = strings.TrimSpace(stripWarnings(out))
		Expect(nodeName).NotTo(BeEmpty(), "no node to run the agent on")

		By("giving the node a k3s.service for the agent to detect")
		execOnNode(nodeName, fmt.Sprintf(
			"cat > /etc/systemd/system/%s.service <<'EOF'\n%sEOF\n"+
				"systemctl daemon-reload && systemctl restart %s.service",
			e2eUnit, e2eUnitFile, e2eUnit))

		By("deploying the node-tuning agent")
		_, err = utils.Run(exec.Command("kubectl", "apply", "-k",
			"deploy/kubernetes/base/node-tuning-agent"))
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the agent manifests")
		_, err = utils.Run(exec.Command("kubectl", "set", "image",
			"-n", agentNamespace, "daemonset/"+agentDaemonSet, "agent="+agentImage))
		Expect(err).NotTo(HaveOccurred(), "Failed to point the agent at the test image")
		_, err = utils.Run(exec.Command("kubectl", "rollout", "status",
			"-n", agentNamespace, "daemonset/"+agentDaemonSet, "--timeout=180s"))
		Expect(err).NotTo(HaveOccurred(), "The node agent never became ready")
	})

	AfterAll(func() {
		By("removing the NodeTuning and the agent")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "nodetuning", nodeTuningName,
			"--ignore-not-found", "--timeout=120s"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "-k",
			"deploy/kubernetes/base/node-tuning-agent", "--ignore-not-found"))

		if nodeName != "" {
			By("removing the stand-in unit and everything written under it")
			execOnNode(nodeName, fmt.Sprintf(
				"systemctl stop %s.service || true; "+
					"rm -rf /etc/systemd/system/%s.service /etc/systemd/system/%s.service.d "+
					"/run/frame-agent; systemctl daemon-reload || true",
				e2eUnit, e2eUnit, e2eUnit))

			By("dropping the annotations and labels the agent published")
			for _, key := range []string{
				framev1beta1.TuningUnitAnnotation,
				framev1beta1.TuningUnitActiveEnterAnnotation,
				framev1beta1.TuningRestartRequestedAnnotation,
			} {
				_, _ = utils.Run(exec.Command("kubectl", "annotate", "node", nodeName, key+"-"))
			}
		}

		By("undeploying the controller-manager")
		_, _ = utils.Run(exec.Command("make", "undeploy"))
		By("uninstalling CRDs")
		_, _ = utils.Run(exec.Command("make", "uninstall"))
		By("removing the manager namespace")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", namespace,
			"--ignore-not-found", "--timeout=120s"))
	})

	// The founding bug, end to end: a drop-in on disk while systemd still
	// reports the old value must surface as needing a restart, never as
	// InSync.
	//
	// Unlike the plan's sketch, the drop-in is not written behind the agent's
	// back — the agent writes it itself, from the spec, which is strictly
	// stronger: it proves both halves at once (the file *is* written, and the
	// file is *not* what gets reported). An agent that reported the drop-in it
	// had just written would report memoryKSM=true here and fail; one that
	// wrote nothing at all fails the file assertion.
	It("must not read a drop-in on disk as applied", func() {
		applyCR(fmt.Sprintf(`
apiVersion: frame.plume-labs.io/v1beta1
kind: NodeTuning
metadata:
  name: %s
spec:
  nodeSelector:
    matchLabels:
      kubernetes.io/os: linux
  ksm:
    enabled: true
`, nodeTuningName))

		By("waiting for the agent to apply the drop-in")
		Eventually(func(g Gomega) string {
			return execOnNodeOutput(g, nodeName,
				fmt.Sprintf("cat /etc/systemd/system/%s.service.d/10-ksm.conf 2>/dev/null", e2eUnit))
		}, 3*time.Minute, 5*time.Second).Should(ContainSubstring("MemoryKSM=yes"))

		By("checking what it reported while systemd has not reloaded")
		Eventually(func(g Gomega) *framev1beta1.ObservedKSM {
			return nodeStatus(g, nodeName).Observed.KSM
		}, 2*time.Minute, 5*time.Second).ShouldNot(BeNil(), "the agent never reported this node")

		obs := nodeStatus(Default, nodeName)
		Expect(obs.Observed.KSM.MemoryKSM).To(BeFalse(),
			"the agent must report what systemd says, not the file it just wrote")
		Expect(obs.Phase).To(BeElementOf(
			framev1beta1.PhaseRebootPending,
			framev1beta1.PhaseDrifted,
		), "a file on disk must not read as applied")
	})

	// The agent's half of the restart protocol. Without both annotations the
	// controller refuses to cordon anything at all — it will not take a node
	// apart for a restart it could not afterwards verify — so this is the
	// difference between the feature working and every approved node draining
	// and then timing out into Failed.
	It("publishes the unit it detected and that unit's ActiveEnterTimestamp", func() {
		Eventually(func(g Gomega) string {
			return nodeAnnotation(g, nodeName, framev1beta1.TuningUnitAnnotation)
		}, 2*time.Minute, 5*time.Second).Should(Equal(e2eUnit))

		raw := nodeAnnotation(Default, nodeName, framev1beta1.TuningUnitActiveEnterAnnotation)
		// Parsed exactly the way the controller parses it (annotationTime in
		// nodetuning_rollout.go). A value in any other format reads to the
		// controller as "no timestamp yet", forever and silently.
		_, err := time.Parse(time.RFC3339Nano, raw)
		Expect(err).NotTo(HaveOccurred(), "controller-unreadable timestamp %q", raw)
	})

	// The other half: a restart request must produce a real restart of a real
	// unit on a real systemd — scheduled detached, so that the process issuing
	// it is not what runs it — and must then be acted on exactly once, because
	// the request annotation stays on the node until the controller has
	// verified the restart.
	It("services a restart request once, detached, and republishes the new timestamp", func() {
		before := nodeAnnotation(Default, nodeName, framev1beta1.TuningUnitActiveEnterAnnotation)
		Expect(before).NotTo(BeEmpty())

		By("asking for a restart the way the controller does")
		_, err := utils.Run(exec.Command("kubectl", "annotate", "node", nodeName, "--overwrite",
			fmt.Sprintf("%s=%s", framev1beta1.TuningRestartRequestedAnnotation,
				time.Now().UTC().Format(time.RFC3339Nano))))
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the unit to actually come back")
		Eventually(func(g Gomega) string {
			return nodeAnnotation(g, nodeName, framev1beta1.TuningUnitActiveEnterAnnotation)
		}, 3*time.Minute, 5*time.Second).ShouldNot(Equal(before),
			"the unit's ActiveEnterTimestamp never moved: nothing restarted it")

		after := nodeAnnotation(Default, nodeName, framev1beta1.TuningUnitActiveEnterAnnotation)

		// The request annotation is still on the node — the controller only
		// clears it once it has verified and uncordoned — so an agent keyed on
		// the annotation's presence rather than its value restarts the unit
		// again on every tick from here on. Two ticks' worth of silence is the
		// discriminating observation.
		By("checking it is not replayed while the request is unchanged")
		Consistently(func(g Gomega) string {
			return nodeAnnotation(g, nodeName, framev1beta1.TuningUnitActiveEnterAnnotation)
		}, 90*time.Second, 10*time.Second).Should(Equal(after),
			"the unit restarted again on an unchanged request")
	})
})

// nodeStatus returns this node's entry in the NodeTuning's status. Read as
// JSON and unmarshalled rather than picked apart with a jsonpath filter, so
// the spec asserts against the same typed shape the controller writes.
func nodeStatus(g Gomega, nodeName string) framev1beta1.NodeTuningNodeStatus {
	out, err := kubectlGet(g, "nodetuning", nodeTuningName, "", "{.status.nodes}")
	g.Expect(err).NotTo(HaveOccurred())

	raw := strings.TrimSpace(out)
	if raw == "" {
		return framev1beta1.NodeTuningNodeStatus{}
	}
	var nodes []framev1beta1.NodeTuningNodeStatus
	g.Expect(json.Unmarshal([]byte(raw), &nodes)).To(Succeed(), "status.nodes was %q", raw)
	for _, n := range nodes {
		if n.Name == nodeName {
			return n
		}
	}
	return framev1beta1.NodeTuningNodeStatus{}
}

// nodeAnnotation reads one annotation off a Node.
func nodeAnnotation(g Gomega, nodeName, key string) string {
	// The key contains dots, which jsonpath would read as path separators.
	out, err := kubectlGet(g, "node", nodeName, "",
		fmt.Sprintf(`{.metadata.annotations.%s}`, strings.ReplaceAll(key, ".", `\.`)))
	g.Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

// execOnNode runs a shell command inside the Kind node's container — the only
// way to see and touch what the agent writes on the "node", since the node is
// a container on this machine.
func execOnNode(nodeName, script string) {
	GinkgoHelper()
	out, err := utils.Run(exec.Command(containerTool(), "exec", nodeName, "sh", "-c", script))
	Expect(err).NotTo(HaveOccurred(), "on-node command failed: %s\n%s", script, out)
}

// execOnNodeOutput is execOnNode for a command whose output is the assertion,
// and whose failure is just "not yet" — every caller polls it.
func execOnNodeOutput(g Gomega, nodeName, script string) string {
	out, _ := utils.Run(exec.Command(containerTool(), "exec", nodeName, "sh", "-c", script))
	return out
}

// containerTool is whatever runs the Kind nodes, matching the Makefile's
// CONTAINER_TOOL.
func containerTool() string {
	if v := os.Getenv("CONTAINER_TOOL"); v != "" {
		return v
	}
	return "docker"
}
