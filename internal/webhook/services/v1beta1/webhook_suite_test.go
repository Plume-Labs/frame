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

package v1beta1

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	servicesv1beta1 "github.com/rmocq/frame/api/services/v1beta1"
	"github.com/rmocq/frame/internal/services/provider"
	"github.com/rmocq/frame/internal/services/provider/inference"
	// +kubebuilder:scaffold:imports
)

// crdRenderRelativeRoot is the path from this package back to the repo root.
var crdRenderRelativeRoot = filepath.Join("..", "..", "..", "..")

// renderedCRDPath is bin/crd-render, where `make crd-render` writes the CRDs
// as kustomize builds them — conversion stanza included.
//
// The suites used to read config/crd/bases directly. Those are only
// controller-gen's half: controller-gen has no marker that emits
// spec.conversion, so the stanza exists solely as a kustomize patch, and the
// bases are a shape no install ever applies. Reading the rendered output is
// what keeps this suite pointed at the schema that ships — including any
// future patch that changes one.
//
// It is NOT what makes conversion work here, and the earlier wording of this
// comment said the inverse of the truth. envtest does not need a CRD to
// declare strategy: Webhook. Environment.Start generates a serving CA
// unconditionally (PrepWithoutInstalling, before InstallCRDs) and
// modifyConversionWebhooks then *overwrites* spec.conversion on every CRD
// whose GroupKind is convertible in the scheme, creating the stanza where the
// manifest has none. It reads the Go types, not the manifests.
//
// The consequence is worth stating plainly, because it is a permanent hole in
// `make test`: strip all eight conversion stanzas out of bin/crd-render and
// both envtest suites stay 100% green. No envtest can notice a missing
// shipped stanza, whichever directory it reads, because the field it would
// assert on is the field envtest has already rewritten. hack/helm-parity.sh
// (manifest-level, in CI) and test/e2e (a real apiserver, not in CI) are the
// only guards on the shipped side.
func renderedCRDPath() string {
	// The number of ".." segments differs per suite; see crdRenderRelativeRoot above.
	return filepath.Join(crdRenderRelativeRoot, "bin", "crd-render")
}

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctx       context.Context
	cancel    context.CancelFunc
	k8sClient client.Client
	cfg       *rest.Config
	testEnv   *envtest.Environment
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Webhook Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	err = servicesv1beta1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{renderedCRDPath()},
		ErrorIfCRDPathMissing: false,

		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("..", "..", "..", "..", "config", "webhook")},
		},
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	if _, err := os.Stat(renderedCRDPath()); err != nil {
		Fail(fmt.Sprintf("%s is missing — run `make crd-render` (or `make test`, which does): %v",
			renderedCRDPath(), err))
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// start webhook server using Manager.
	webhookInstallOptions := &testEnv.WebhookInstallOptions
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    webhookInstallOptions.LocalServingHost,
			Port:    webhookInstallOptions.LocalServingPort,
			CertDir: webhookInstallOptions.LocalServingCertDir,
		}),
		LeaderElection: false,
		Metrics:        metricsserver.Options{BindAddress: "0"},
	})
	Expect(err).NotTo(HaveOccurred())

	// nil client, nil apiReader: the webhook only ever calls Size and
	// ParameterSchema through this registry, neither of which dereferences
	// either — Reconcile and Bind are the only methods that do, and
	// admission never reaches them.
	err = SetupFrameServiceWebhookWithManager(mgr, provider.NewRegistry(inference.New(7680, nil, nil)))
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:webhook

	go func() {
		defer GinkgoRecover()
		err = mgr.Start(ctx)
		Expect(err).NotTo(HaveOccurred())
	}()

	// wait for the webhook server to get ready.
	dialer := &net.Dialer{Timeout: time.Second}
	addrPort := fmt.Sprintf("%s:%d", webhookInstallOptions.LocalServingHost, webhookInstallOptions.LocalServingPort)
	Eventually(func() error {
		conn, err := tls.DialWithDialer(dialer, "tcp", addrPort, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			return err
		}

		return conn.Close()
	}).Should(Succeed())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	Eventually(func() error {
		return testEnv.Stop()
	}, time.Minute, time.Second).Should(Succeed())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
