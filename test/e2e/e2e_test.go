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
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rmocq/frame/test/utils"
)

// namespace where the project is deployed in
const namespace = "frame-system"

// serviceAccountName created for the project
const serviceAccountName = "frame-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "frame-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "frame-metrics-binding"

// crNamespace holds the custom resources the CRD specs create.
const crNamespace = "frame-e2e"

// e2ePriorityClass is the cluster-scoped PriorityClass a SchedulingPolicy projects.
const e2ePriorityClass = "frame-e2e-high"

// frameKinds is every Frame CRD, used to release finalizers before teardown.
var frameKinds = []string{
	"framejobs", "framenodes", "frameresourcequotas", "schedulingpolicies",
	"talosmachineconfigs", "talosupgrades", "frameusers",
}

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				By("getting the name of the controller-manager pod")
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				By("validating the pod's status")
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=frame-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			By("waiting for the webhook service endpoints to be ready")
			verifyWebhookEndpointsReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpointslices.discovery.k8s.io", "-n", namespace,
					"-l", "kubernetes.io/service-name=frame-webhook-service",
					"-o", "jsonpath={range .items[*]}{range .endpoints[*]}{.addresses[*]}{end}{end}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Webhook endpoints should exist")
				g.Expect(output).ShouldNot(BeEmpty(), "Webhook endpoints not yet ready")
			}
			Eventually(verifyWebhookEndpointsReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying the mutating webhook server is ready")
			verifyMutatingWebhookReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "mutatingwebhookconfigurations.admissionregistration.k8s.io",
					"frame-mutating-webhook-configuration",
					"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "MutatingWebhookConfiguration should exist")
				g.Expect(output).ShouldNot(BeEmpty(), "Mutating webhook CA bundle not yet injected")
			}
			Eventually(verifyMutatingWebhookReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying the validating webhook server is ready")
			verifyValidatingWebhookReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "validatingwebhookconfigurations.admissionregistration.k8s.io",
					"frame-validating-webhook-configuration",
					"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "ValidatingWebhookConfiguration should exist")
				g.Expect(output).ShouldNot(BeEmpty(), "Validating webhook CA bundle not yet injected")
			}
			Eventually(verifyValidatingWebhookReady, 3*time.Minute, time.Second).Should(Succeed())

			By("waiting additional time for webhook server to stabilize")
			time.Sleep(5 * time.Second)

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		It("should provisioned cert-manager", func() {
			By("validating that cert-manager has the certificate Secret")
			verifyCertManager := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "secrets", "webhook-server-cert", "-n", namespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyCertManager).Should(Succeed())
		})

		It("should have CA injection for mutating webhooks", func() {
			By("checking CA injection for mutating webhooks")
			verifyCAInjection := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"mutatingwebhookconfigurations.admissionregistration.k8s.io",
					"frame-mutating-webhook-configuration",
					"-o", "go-template={{ range .webhooks }}{{ .clientConfig.caBundle }}{{ end }}")
				mwhOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(mwhOutput)).To(BeNumerically(">", 10))
			}
			Eventually(verifyCAInjection).Should(Succeed())
		})

		It("should have CA injection for validating webhooks", func() {
			By("checking CA injection for validating webhooks")
			verifyCAInjection := func(g Gomega) {
				cmd := exec.Command("kubectl", "get",
					"validatingwebhookconfigurations.admissionregistration.k8s.io",
					"frame-validating-webhook-configuration",
					"-o", "go-template={{ range .webhooks }}{{ .clientConfig.caBundle }}{{ end }}")
				vwhOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(vwhOutput)).To(BeNumerically(">", 10))
			}
			Eventually(verifyCAInjection).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})

	// One spec per CRD, each proving the same chain against a real apiserver:
	// a spec is admitted, the controller turns it into a cluster effect, and the
	// effect is reported back in status. Envtest already covers the reconcile
	// logic; what only a real cluster proves is that the deployed manager —
	// with its generated RBAC, its webhooks and its cert-manager TLS — can
	// actually perform those effects.
	Context("CRD reconciliation", Ordered, func() {
		BeforeAll(func() {
			By("creating a namespace for the test resources")
			cmd := exec.Command("kubectl", "create", "ns", crNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the CR namespace")

			By("labelling it for the HIGH service class")
			cmd = exec.Command("kubectl", "label", "--overwrite", "ns", crNamespace,
				"frame.plume-labs.io/service-class=HIGH")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to label the CR namespace")
		})

		AfterAll(func() {
			// Strip finalizers before the outer AfterAll undeploys the manager.
			// Once it is gone nothing answers the finalizers, and both the CRD
			// uninstall and the namespace deletion would hang on them.
			By("releasing finalizers on any leftover Frame CRs")
			for _, kind := range frameKinds {
				out, err := utils.Run(exec.Command("kubectl", "get", kind,
					"-n", crNamespace, "-o", "name", "--ignore-not-found"))
				if err != nil {
					continue
				}
				for _, ref := range utils.GetNonEmptyLines(out) {
					if !strings.Contains(ref, "/") {
						continue
					}
					_, _ = utils.Run(exec.Command("kubectl", "patch", ref, "-n", crNamespace,
						"--type=merge", "-p", `{"metadata":{"finalizers":null}}`))
				}
			}

			By("removing the test namespace and the cluster-scoped leftovers")
			_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", crNamespace,
				"--ignore-not-found", "--timeout=120s"))
			_, _ = utils.Run(exec.Command("kubectl", "delete", "priorityclass",
				e2ePriorityClass, "--ignore-not-found"))
			_, _ = utils.Run(exec.Command("kubectl", "delete", "crd",
				"workflows.argoproj.io", "--ignore-not-found"))
		})

		It("projects a SchedulingPolicy into a PriorityClass", func() {
			applyCR(fmt.Sprintf(`
apiVersion: frame.plume-labs.io/v1alpha1
kind: SchedulingPolicy
metadata:
  name: e2e-policy
  namespace: %s
spec:
  scheduler: default
  priorityClass: %s
  priorityValue: 123456
`, crNamespace, e2ePriorityClass))

			By("checking the PriorityClass carries the requested value")
			Eventually(func(g Gomega) {
				value, err := kubectlGet(g, "priorityclass", e2ePriorityClass, "",
					"{.value}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(value).To(Equal("123456"))
			}).Should(Succeed())

			By("checking the status reports it applied")
			Eventually(readyReason("schedulingpolicy", "e2e-policy")).Should(Equal("Applied"))
		})

		It("projects a FrameResourceQuota into a namespace ResourceQuota", func() {
			applyCR(fmt.Sprintf(`
apiVersion: frame.plume-labs.io/v1alpha1
kind: FrameResourceQuota
metadata:
  name: e2e-quota
  namespace: %s
spec:
  serviceClass: HIGH
  maxJobs: 7
  maxCPU: "12"
`, crNamespace))

			By("checking maxJobs became an object-count quota on FrameJobs, not on pods")
			Eventually(func(g Gomega) {
				hard, err := kubectlGet(g, "resourcequota", "frame-high", crNamespace,
					`{.spec.hard.count/framejobs\.frame\.plume-labs\.io}`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(hard).To(Equal("7"))
			}).Should(Succeed())

			By("checking the status reports the namespaces it reached")
			Eventually(readyReason("frameresourcequota", "e2e-quota")).Should(Equal("Reconciled"))
		})

		It("turns a FrameJob into an Argo Workflow", func() {
			// A stand-in for the real Argo CRD: the assertion is that Frame
			// creates the Workflow object with the right owner and parameters,
			// not that Argo executes it. Installing Argo itself would test
			// Argo, and would tie the suite to an upstream release.
			By("installing a permissive Workflow CRD")
			applyCR(`
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: workflows.argoproj.io
spec:
  group: argoproj.io
  scope: Namespaced
  names:
    kind: Workflow
    listKind: WorkflowList
    plural: workflows
    singular: workflow
  versions:
    - name: v1alpha1
      served: true
      storage: true
      subresources:
        status: {}
      schema:
        openAPIV3Schema:
          type: object
          x-kubernetes-preserve-unknown-fields: true
`)
			Eventually(func(g Gomega) {
				_, err := utils.Run(exec.Command("kubectl", "get", "crd", "workflows.argoproj.io"))
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

			applyCR(fmt.Sprintf(`
apiVersion: frame.plume-labs.io/v1alpha1
kind: FrameJob
metadata:
  name: e2e-job
  namespace: %s
spec:
  name: e2e-job
  pipeline: training
  namespace: %s
  serviceClass: HIGH
  priority: high
  gpuCount: 2
`, crNamespace, crNamespace))

			By("checking the Workflow exists and carries the GPU count as a parameter")
			Eventually(func(g Gomega) {
				out, err := kubectlGet(g, "workflow.argoproj.io", "e2e-job", crNamespace,
					"{.spec.arguments.parameters[?(@.name=='gpu-count')].value}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("2"))
			}).Should(Succeed())

			By("checking the status names the Workflow it created")
			Eventually(func(g Gomega) {
				out, err := kubectlGet(g, "framejob", "e2e-job", crNamespace,
					"{.status.argoWorkflowName}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("e2e-job"))
			}).Should(Succeed())
		})

		It("syncs a FrameNode onto the real Kubernetes Node", func() {
			By("finding a node in the cluster")
			nodeName, err := utils.Run(exec.Command("kubectl", "get", "nodes",
				"-o", "jsonpath={.items[0].metadata.name}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(nodeName).NotTo(BeEmpty())

			applyCR(fmt.Sprintf(`
apiVersion: frame.plume-labs.io/v1alpha1
kind: FrameNode
metadata:
  name: e2e-node
  namespace: %s
spec:
  ip: 127.0.0.1
  role: worker
  hostname: %s
  disk: /dev/null
  rack: rack-e2e
  zone: zone-e2e
  serviceClass: HIGH
  network:
    address: 127.0.0.1/8
    gateway: 127.0.0.1
    dns:
      - 1.1.1.1
`, crNamespace, nodeName))

			// Phase 3 (sync from the Kubernetes Node) is only reached once the
			// node is past provisioning. Provisioning talks Talos gRPC, which
			// has no counterpart in Kind, so the phase is set directly here —
			// the point of this spec is the sync, not the provisioning that
			// precedes it in production.
			By("advancing the FrameNode past provisioning")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "patch", "framenode", "e2e-node",
					"-n", crNamespace, "--subresource=status", "--type=merge",
					"-p", `{"status":{"phase":"Provisioning"}}`)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

			By("checking the controller labelled the real Node")
			Eventually(func(g Gomega) {
				out, err := kubectlGet(g, "node", nodeName, "",
					`{.metadata.labels.frame\.plume-labs\.io/service-class}`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("HIGH"))
			}).Should(Succeed())

			By("checking the status was filled from the Node")
			Eventually(func(g Gomega) {
				out, err := kubectlGet(g, "framenode", "e2e-node", crNamespace,
					"{.status.nodeName}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal(nodeName))
			}).Should(Succeed())
		})

		// The two Talos kinds drive a gRPC endpoint that does not exist in Kind.
		// What a real cluster can still prove is that an unreachable target ends
		// as a reported failure rather than a hang or a crash — a controller
		// that wedges on a missing endpoint is the failure mode worth catching.
		It("reports a TalosMachineConfig it cannot apply", func() {
			applyCR(fmt.Sprintf(`
apiVersion: frame.plume-labs.io/v1alpha1
kind: TalosMachineConfig
metadata:
  name: e2e-machineconfig
  namespace: %s
spec:
  nodeName: nowhere
  talosEndpoint: "127.0.0.1:50000"
  talosSecretRef:
    name: absent-talos-certs
    namespace: %s
  configPatch: |
    machine:
      sysctls:
        vm.max_map_count: "524288"
`, crNamespace, crNamespace))

			Eventually(readyReason("talosmachineconfig", "e2e-machineconfig")).
				Should(Equal("ClientBuildFailed"))
		})

		It("reports a TalosUpgrade it cannot request", func() {
			applyCR(fmt.Sprintf(`
apiVersion: frame.plume-labs.io/v1alpha1
kind: TalosUpgrade
metadata:
  name: e2e-upgrade
  namespace: %s
spec:
  nodeName: nowhere
  talosEndpoint: "127.0.0.1:50000"
  talosSecretRef:
    name: absent-talos-certs
    namespace: %s
  image: ghcr.io/siderolabs/talos:v1.9.0
`, crNamespace, crNamespace))

			Eventually(readyReason("talosupgrade", "e2e-upgrade")).
				Should(Equal("ClientBuildFailed"))
		})

		// FrameUser has no controller. Its cluster effect is the admission
		// decision itself, which is what this spec exercises against the
		// deployed webhook and its cert-manager-issued TLS.
		It("refuses to let the last FrameUser admin be removed", func() {
			applyCR(fmt.Sprintf(`
apiVersion: frame.plume-labs.io/v1alpha1
kind: FrameUser
metadata:
  name: e2e-admin-one
  namespace: %s
spec:
  email: one@example.test
  role: admin
---
apiVersion: frame.plume-labs.io/v1alpha1
kind: FrameUser
metadata:
  name: e2e-admin-two
  namespace: %s
spec:
  email: two@example.test
  role: admin
`, crNamespace, crNamespace))

			By("allowing one admin to go while another remains")
			_, err := utils.Run(exec.Command("kubectl", "delete", "frameuser",
				"e2e-admin-two", "-n", crNamespace))
			Expect(err).NotTo(HaveOccurred())

			By("refusing the deletion of the last one")
			out, err := utils.Run(exec.Command("kubectl", "delete", "frameuser",
				"e2e-admin-one", "-n", crNamespace))
			Expect(err).To(HaveOccurred(), "The last admin must not be deletable")
			Expect(out).To(ContainSubstring("refusing to remove the last admin"))

			By("refusing the demotion of the last one just as firmly")
			out, err = utils.Run(exec.Command("kubectl", "patch", "frameuser",
				"e2e-admin-one", "-n", crNamespace, "--type=merge",
				"-p", `{"spec":{"role":"viewer"}}`))
			Expect(err).To(HaveOccurred(), "The last admin must not be demotable")
			Expect(out).To(ContainSubstring("refusing to remove the last admin"))
		})
	})
})

// applyCR pipes a manifest through kubectl apply. Manifests are written inline
// so each spec reads as one thing: the input, and what the cluster does with it.
func applyCR(manifest string) {
	GinkgoHelper()
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to apply manifest:\n%s", manifest)
}

// kubectlGet reads one JSONPath expression off a resource, namespaced or not.
func kubectlGet(_ Gomega, kind, name, ns, jsonPath string) (string, error) {
	args := []string{"get", kind, name, "-o", "jsonpath=" + jsonPath}
	if ns != "" {
		args = append(args, "-n", ns)
	}
	return utils.Run(exec.Command("kubectl", args...))
}

// readyReason returns a Gomega-friendly getter for the Ready condition's reason,
// which is where every Frame controller records what it did or why it could not.
func readyReason(kind, name string) func(Gomega) string {
	return func(g Gomega) string {
		out, err := kubectlGet(g, kind, name, crNamespace,
			`{.status.conditions[?(@.type=="Ready")].reason}`)
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	By("creating temporary file to store the token request")
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		By("executing kubectl command to create the token")
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		By("parsing the JSON output to extract the token")
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
