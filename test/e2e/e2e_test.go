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
	"slices"
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
	"talosmachineconfigs", "talosupgrades", "frameusers", "frameservices",
}

// frameCRDs is every Frame CRD by its full resource name, for the checks that
// operate on the CustomResourceDefinition object rather than on CRs.
var frameCRDs = []string{
	"framejobs.frame.plume-labs.io",
	"framenodes.frame.plume-labs.io",
	"frameresourcequotas.frame.plume-labs.io",
	"schedulingpolicies.frame.plume-labs.io",
	"talosmachineconfigs.frame.plume-labs.io",
	"talosupgrades.frame.plume-labs.io",
	"frameusers.frame.plume-labs.io",
	"frameservices.services.plume-labs.io",
}

// storageVersion is the version every Frame CRD stores at. Kept beside
// frameCRDs because the migration spec and hack/migrate-storage-version.sh
// both have to agree with it.
const storageVersion = "v1beta1"

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

		It("should have CA injection for the CRD conversion webhooks", func() {
			// A different object and a different replacement block from the two
			// above: the conversion CA lands in the CRD's own
			// spec.conversion.webhook.clientConfig.caBundle, wired by the blocks
			// under config/default's crdkustomizecainjection markers. Nothing in
			// Go exercises this — envtest rewrites clientConfig itself and injects
			// its own CA, so the shipped manifests are never the thing under test
			// there. It is pure manifest plumbing, and it is the part of the
			// conversion webhook that fails silently when it is wrong (F14 layer 3).
			//
			// This replaces the eight copy-pasted per-CRD specs kubebuilder
			// scaffolded: same caBundle assertion on the same eight objects, plus
			// the strategy check none of them made. A CRD with a caBundle and
			// strategy None would have passed all eight.
			for _, crd := range frameCRDs {
				By("checking " + crd)
				verify := func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "crd", crd,
						"-o", "jsonpath={.spec.conversion.strategy}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(out).To(Equal("Webhook"), crd+" is not served by the conversion webhook")

					out, err = utils.Run(exec.Command("kubectl", "get", "crd", crd,
						"-o", "jsonpath={.spec.conversion.webhook.clientConfig.caBundle}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(len(out)).To(BeNumerically(">", 10),
						crd+" has no caBundle — cert-manager did not inject it")
				}
				Eventually(verify).Should(Succeed())
			}
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

		It("converts a FrameJob written as v1alpha1 and read as v1beta1", func() {
			// Deliberately written at the *old* version, the way an existing
			// client or a stored object arrives. The apiserver runs ConvertTo
			// on the way into etcd, because v1beta1 is storage — so this
			// exercises the deployed webhook, its cert-manager CA, its service
			// coordinate and its conversionReviewVersions, none of which any
			// Go test can reach: envtest generates its own serving CA and
			// rewrites every convertible CRD in the scheme, so the unit and
			// envtest suites are green whatever the shipped manifests say
			// (F14 layer 3), and they start from objects they just created at
			// the storage version rather than at v1alpha1 (F14 layer 4).
			//
			// It depends on the Workflow CRD the preceding spec installs, which
			// is why this context is Ordered.
			applyCR(fmt.Sprintf(`
apiVersion: frame.plume-labs.io/v1alpha1
kind: FrameJob
metadata:
  name: conv-e2e-job
  namespace: %s
spec:
  pipeline: training
  namespace: some-other-namespace
  serviceClass: HIGH
  priority: high
  gpuCount: 1
`, crNamespace))

			By("reading it at v1beta1 and finding no spec.namespace")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get",
					"framejobs.v1beta1.frame.plume-labs.io", "conv-e2e-job",
					"-n", crNamespace, "-o", "jsonpath={.spec}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring(`"pipeline":"training"`))
				g.Expect(out).NotTo(ContainSubstring("some-other-namespace"),
					"spec.namespace must not survive into the storage version")
			}).Should(Succeed())

			By("reading it back at v1alpha1 and finding the normalised namespace")
			Eventually(func(g Gomega) {
				out, err := kubectlGet(g, "framejobs.v1alpha1.frame.plume-labs.io", "conv-e2e-job",
					crNamespace, "{.spec.namespace}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal(crNamespace),
					"a v1alpha1 client sees the namespace the operator acts in")
			}).Should(Succeed())

			By("finding the Workflow beside the FrameJob, not in the namespace it asked for")
			Eventually(func(g Gomega) {
				_, err := kubectlGet(g, "workflow.argoproj.io", "conv-e2e-job", crNamespace,
					"{.metadata.name}")
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())
			out, err := utils.Run(exec.Command("kubectl", "get", "workflow.argoproj.io",
				"-A", "-o", "jsonpath={range .items[*]}{.metadata.namespace}{\"\\n\"}{end}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(out).NotTo(ContainSubstring("some-other-namespace"))

			By("seeing a phase projected out of conditions at v1alpha1")
			Eventually(func(g Gomega) {
				phase, err := kubectlGet(g, "framejobs.v1alpha1.frame.plume-labs.io", "conv-e2e-job",
					crNamespace, "{.status.phase}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(phase).To(BeElementOf("Submitted", "Running", "Completed", "Failed", "Suspended"),
					"status.phase is computed from conditions, never stored")

				stored, err := kubectlGet(g, "framejobs.v1beta1.frame.plume-labs.io", "conv-e2e-job",
					crNamespace, "{.status.phase}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(stored).To(BeEmpty(), "v1beta1 has no status.phase at all")
			}).Should(Succeed())

			By("emitting the deprecation warning that is the migration policy's only enforcement")
			// utils.Run uses CombinedOutput, so the apiserver's Warning header
			// — which kubectl prints to stderr — lands in out. err is nil on a
			// successful read, so it must not be dereferenced here.
			out, err = utils.Run(exec.Command("kubectl", "get",
				"framejobs.v1alpha1.frame.plume-labs.io", "conv-e2e-job", "-n", crNamespace))
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("deprecated"),
				"reading at v1alpha1 must warn — the deprecation policy has no other enforcement")
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

			By("checking the node carries the frame-prefixed rack label and no reserved-prefix one")
			Eventually(func(g Gomega) {
				out, err := kubectlGet(g, "node", nodeName, "",
					`{.metadata.labels.frame\.plume-labs\.io/rack}`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("rack-e2e"))

				out, err = kubectlGet(g, "node", nodeName, "",
					`{.metadata.labels.topology\.kubernetes\.io/rack}`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(BeEmpty(), "topology.kubernetes.io/ is reserved for upstream keys")
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

		It("provisions a FrameService through its inference provider", func() {
			// The inference provider requires a PersistentVolumeClaim for model
			// weights, named by parameters.modelCache and defaulting to
			// model-cache-pvc, in the FrameService's own namespace. Absent that,
			// the provider degrades with ModelCacheMissing and creates no
			// Deployment at all — so it has to exist before the FrameService is
			// created, not be waited for afterwards.
			By("creating the model cache PVC the inference provider requires")
			applyCR(fmt.Sprintf(`
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: model-cache-pvc
  namespace: %s
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
`, crNamespace))

			applyCR(fmt.Sprintf(`
apiVersion: services.plume-labs.io/v1alpha1
kind: FrameService
metadata:
  name: e2e-inference
  namespace: %s
spec:
  type: inference
  serviceClass: HIGH
  parameters:
    model: llama-3.1-8b-instruct
    contextLength: "4096"
`, crNamespace))

			By("checking the Deployment was created and carries the GPU request")
			Eventually(func(g Gomega) {
				out, err := kubectlGet(g, "deployment", "e2e-inference", crNamespace,
					`{.spec.template.spec.containers[0].resources.limits.nvidia\.com/gpu}`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("1"))
			}).Should(Succeed())

			By("checking the Deployment is placed for the HIGH service class, not a named node")
			Eventually(func(g Gomega) {
				out, err := kubectlGet(g, "deployment", "e2e-inference", crNamespace,
					`{.spec.template.spec.nodeSelector.frame\.plume-labs\.io/service-class}`)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("HIGH"))
			}).Should(Succeed())

			By("checking the provider's own API-key Secret exists")
			// This is the Secret the inference provider creates for itself,
			// separate from the controller's binding Secret (which is never
			// written here — see below).
			Eventually(func(g Gomega) {
				_, err := kubectlGet(g, "secret", "e2e-inference-inference-key", crNamespace,
					"{.metadata.name}")
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

			By("checking the status reports the sizing Frame computed")
			Eventually(func(g Gomega) {
				out, err := kubectlGet(g, "frameservice", "e2e-inference", crNamespace,
					"{.status.sizing.gpu}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("1"))
			}).Should(Succeed())

			// The pod never becomes Ready in Kind: there is no GPU to satisfy
			// the nvidia.com/gpu request and the llama.cpp image is never
			// pulled, so the Deployment sits at 0/1 ready replicas forever and
			// the FrameService reports Degraded/RolloutInProgress rather than
			// Ready. That is asserted here as the expected, honest outcome —
			// not worked around — because it is what Reconcile is documented to
			// report from a rollout that has not finished. Because Ready is
			// never reached, the controller never calls Bind, so
			// status.binding.secretRef is deliberately never asserted: it stays
			// empty for the same reason.
			By("checking the status reports the rollout in progress, not a silent Pending")
			Eventually(readyReason("frameservice", "e2e-inference")).Should(Equal("RolloutInProgress"))
		})

		It("refuses a FrameService that cannot fit the card", func() {
			// The refusal is the feature: it happens at admission, so there is
			// no object to inspect afterwards and no pod to crash-loop.
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(fmt.Sprintf(`
apiVersion: services.plume-labs.io/v1alpha1
kind: FrameService
metadata:
  name: e2e-too-big
  namespace: %s
spec:
  type: inference
  parameters:
    model: llama-3.1-8b-instruct
    contextLength: "32768"
`, crNamespace))
			out, err := utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "A service that cannot fit must be refused")
			Expect(out).To(ContainSubstring("7680Mi"))
		})

		It("completes the storage migration so v1alpha1 could be removed", func() {
			// storedVersions only grows. The apiserver appends the new storage
			// version the moment an apply makes it one, and never removes an
			// entry — not even once the last object stored at the old version
			// has been rewritten. A version cannot be dropped from
			// spec.versions while it appears there, so without a migration
			// there is no point at which anyone could say v1alpha1 is
			// removable, and the deprecation policy is unenforceable
			// (F14 layer 5).
			//
			// A fresh Kind cluster installs the CRDs already at
			// storedVersions: ["v1beta1"], so asserting that straight away
			// would prove nothing. This spec first reproduces the shape a
			// cluster upgraded from the pre-freeze build is actually in — an
			// object stored at v1alpha1, and both versions listed — and then
			// runs hack/migrate-storage-version.sh, the same script the
			// upgrade path documents, rather than a reimplementation of it.

			By("putting FrameJob's storage back on v1alpha1, the pre-upgrade shape")
			const jobCRD = "framejobs.frame.plume-labs.io"
			setStorageVersion(jobCRD, "v1alpha1")
			DeferCleanup(func() {
				// If anything below fails, the CRD must not be left storing
				// the deprecated version. Best-effort on purpose: Ginkgo runs
				// this after the enclosing container's AfterAll, which has
				// already run `make uninstall`, so the CRD is usually gone by
				// now — and failing the suite for tidying up something that no
				// longer exists would turn a green run red.
				restoreStorageVersion(jobCRD, storageVersion)
			})

			By("writing a FrameJob so the apiserver records v1alpha1 as stored")
			_, err := utils.Run(exec.Command("kubectl", "patch", "framejob", "conv-e2e-job",
				"-n", crNamespace, "--type=merge",
				"-p", `{"metadata":{"annotations":{"frame.plume-labs.io/e2e-storage-rehearsal":"true"}}}`))
			Expect(err).NotTo(HaveOccurred())

			By("moving storage forward again, which is all an operator upgrade does")
			setStorageVersion(jobCRD, storageVersion)

			By("confirming the cliff is real: both versions are now stored")
			Eventually(func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "crd", jobCRD,
					"-o", "jsonpath={.status.storedVersions}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("v1alpha1"),
					"the rehearsal did not put v1alpha1 into storedVersions, so the migration below proves nothing")
				g.Expect(out).To(ContainSubstring(storageVersion))
			}).Should(Succeed())

			By("running hack/migrate-storage-version.sh as a dry run first")
			out, err := utils.Run(exec.Command("./hack/migrate-storage-version.sh"))
			Expect(err).NotTo(HaveOccurred(), "dry run failed:\n%s", out)
			Expect(out).To(ContainSubstring("Dry run"))
			Expect(out).To(ContainSubstring("rewrite " + crNamespace + "/conv-e2e-job"))

			By("confirming the dry run changed nothing")
			out, err = utils.Run(exec.Command("kubectl", "get", "crd", jobCRD,
				"-o", "jsonpath={.status.storedVersions}"))
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring("v1alpha1"))

			By("running it for real")
			out, err = utils.Run(exec.Command("./hack/migrate-storage-version.sh", "--apply"))
			Expect(err).NotTo(HaveOccurred(), "migration failed:\n%s", out)

			By("finding every CRD storing only the storage version")
			for _, crd := range frameCRDs {
				Eventually(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "crd", crd,
						"-o", "jsonpath={.status.storedVersions}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(out).To(Equal(`["`+storageVersion+`"]`),
						crd+" still lists a version other than "+storageVersion+", so v1alpha1 cannot be removed")
				}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
			}

			By("finding the rewritten objects still readable through both versions")
			// The migration is only worth anything if what it rewrote survived
			// the round trip it forced them through.
			for _, v := range []string{"v1alpha1", storageVersion} {
				out, err := kubectlGet(nil, "framejobs."+v+".frame.plume-labs.io", "conv-e2e-job",
					crNamespace, "{.spec.pipeline}")
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(Equal("training"), "conv-e2e-job is unreadable at "+v+" after the migration")
			}
		})
	})
})

// setStorageVersion moves a CRD's storage flag onto one of its served
// versions. The index of each version in spec.versions is read back rather
// than assumed, because it is manifest ordering and nothing enforces it. Both
// flags move in one JSON patch: the apiserver requires exactly one storage
// version, and only an atomic patch never passes through zero or two.
func setStorageVersion(crd, version string) {
	GinkgoHelper()
	names, patch, err := storageVersionPatch(crd, version)
	Expect(err).NotTo(HaveOccurred())
	Expect(names).To(ContainElement(version), crd+" does not serve "+version)

	_, err = utils.Run(exec.Command("kubectl", "patch", "crd", crd, "--type=json", "-p", patch))
	Expect(err).NotTo(HaveOccurred(), "failed to move %s's storage version to %s", crd, version)
}

// restoreStorageVersion is setStorageVersion's best-effort twin, for
// DeferCleanup. It exists only to stop a mid-spec failure leaving a CRD
// storing the deprecated version; by the time it runs the CRD may legitimately
// be gone, and that must not fail the suite.
func restoreStorageVersion(crd, version string) {
	names, patch, err := storageVersionPatch(crd, version)
	if err != nil || !slices.Contains(names, version) {
		return
	}
	_, _ = utils.Run(exec.Command("kubectl", "patch", "crd", crd, "--type=json", "-p", patch))
}

// storageVersionPatch reads a CRD's served versions and returns them alongside
// the JSON patch that makes exactly `version` the storage one.
func storageVersionPatch(crd, version string) (names []string, patch string, err error) {
	out, err := utils.Run(exec.Command("kubectl", "get", "crd", crd,
		"-o", `jsonpath={range .spec.versions[*]}{.name}{"\n"}{end}`))
	if err != nil {
		return nil, "", err
	}
	names = utils.GetNonEmptyLines(out)
	ops := make([]string, 0, len(names))
	for i, name := range names {
		ops = append(ops, fmt.Sprintf(
			`{"op":"replace","path":"/spec/versions/%d/storage","value":%t}`, i, name == version))
	}
	return names, "[" + strings.Join(ops, ",") + "]", nil
}

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
	out, err := utils.Run(exec.Command("kubectl", args...))
	return stripWarnings(out), err
}

// stripWarnings drops the apiserver's deprecation warnings from kubectl
// output. utils.Run reads CombinedOutput, so every read at a version carrying
// `deprecated: true` arrives as "Warning: …\n" ahead of the value — which is
// not part of the value, and turns every exact-match assertion on a v1alpha1
// read into a false failure. The specs that assert the warning *is* emitted
// call utils.Run directly and so still see it.
func stripWarnings(out string) string {
	lines := strings.Split(out, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "Warning:") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
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
