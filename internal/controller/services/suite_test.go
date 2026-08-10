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

package services

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	conversionwebhook "sigs.k8s.io/controller-runtime/pkg/webhook/conversion"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	servicesv1beta1 "github.com/rmocq/frame/api/services/v1beta1"
	// +kubebuilder:scaffold:imports
)

// crdRenderRelativeRoot is the path from this package back to the repo root.
var crdRenderRelativeRoot = filepath.Join("..", "..", "..")

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
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	err = servicesv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = servicesv1beta1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{renderedCRDPath()},
		ErrorIfCRDPathMissing: true,
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

	serveConversionWebhook(testEnv)

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

// serveConversionWebhook starts a /convert endpoint on the coordinate envtest
// has already reserved for it.
//
// This is not optional and it is not this suite opting in to something. From
// Task 18 on, the v1alpha1 kinds implement Convertible and the v1beta1 kinds
// implement Hub, and envtest reacts to that by itself: Environment.Start
// unconditionally generates a serving CA (server.go's PrepWithoutInstalling),
// hands it to CRDInstallOptions.WebhookOptions, and modifyConversionWebhooks
// then rewrites *every CRD whose GroupKind is convertible in the scheme* to
// spec.conversion.strategy: Webhook with a clientConfig pointing at a local
// https://host:port/convert. It reads the Go types, not the CRD — so the
// rendered manifests still say nothing about conversion and every create and
// get in this suite still fails with "connection refused" until something
// answers on that port.
//
// So: writing the conversion functions turns the conversion webhook on in
// envtest, whatever the manifests say. Serving it here restores the suite and
// makes every spec below exercise the real dispatch path — the apiserver
// calling ConvertTo and ConvertFrom over HTTP — rather than only the Go
// functions.
//
// What it does NOT do, and what Task 19 still owns in full: the conversion
// stanza in the shipped CRDs (a kustomize patch, config/crd), the manager
// serving /convert in production, the cert-manager CA injection and Service
// coordinate, and moving +kubebuilder:storageversion onto the eight v1beta1
// kinds. A green suite here is not evidence for any of that; `make
// helm-parity` is what guards the shipped side (Task 10).
func serveConversionWebhook(env *envtest.Environment) {
	GinkgoHelper()
	opts := env.WebhookInstallOptions

	srv := webhook.NewServer(webhook.Options{
		Host:    opts.LocalServingHost,
		Port:    opts.LocalServingPort,
		CertDir: opts.LocalServingCertDir,
	})
	srv.Register("/convert", conversionwebhook.NewWebhookHandler(scheme.Scheme, conversionwebhook.NewRegistry()))

	exited := make(chan error, 1)
	go func() {
		defer GinkgoRecover()
		exited <- srv.Start(ctx)
	}()

	// Verify against the CA envtest generated rather than skipping
	// verification: it is the same CA the apiserver was handed, so a
	// successful handshake here proves the apiserver's call will get that far
	// too. A readiness probe that skipped verification would still pass
	// against a certificate the apiserver goes on to reject.
	roots := x509.NewCertPool()
	Expect(roots.AppendCertsFromPEM(opts.LocalServingCAData)).To(BeTrue(),
		"envtest produced no usable serving CA, so conversion could not be verified by anyone")

	addr := net.JoinHostPort(opts.LocalServingHost, strconv.Itoa(opts.LocalServingPort))
	Eventually(func() error {
		select {
		case err := <-exited:
			return fmt.Errorf("the conversion webhook server exited: %w", err)
		default:
		}
		conn, err := tls.Dial("tcp", addr, &tls.Config{
			RootCAs:    roots,
			ServerName: opts.LocalServingHost,
			MinVersion: tls.VersionTLS12,
		})
		if err != nil {
			return err
		}
		return conn.Close()
	}, 15*time.Second, 100*time.Millisecond).Should(Succeed(),
		"nothing is serving a verifiable /convert at %s, so every convertible kind in this suite would fail to read", addr)
}

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
	basePath := filepath.Join("..", "..", "..", "bin", "k8s")
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
