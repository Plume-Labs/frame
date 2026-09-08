//go:build envtest

package uiproxy_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/uiproxy"
)

var (
	testEnv   *envtest.Environment
	restCfg   *rest.Config
	k8sClient client.Client
)

// apierrorsIsAlreadyExists wraps apierrors.IsAlreadyExists so the fixtures
// below read as "create, tolerating that it's already there" without a
// second import alias per call site.
func apierrorsIsAlreadyExists(err error) bool {
	return apierrors.IsAlreadyExists(err)
}

// stubVerifier stands in for the JWKS verifier: this spec proves what the
// apiserver does with an impersonated identity, not how the token was read.
type stubVerifier struct{ id uiproxy.Identity }

func (s stubVerifier) Verify(context.Context, string) (uiproxy.Identity, error) { return s.id, nil }

func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "bin", "crd-render")},
		ErrorIfCRDPathMissing: true,
	}
	if dir := os.Getenv("KUBEBUILDER_ASSETS"); dir == "" {
		// Same fallback the controller suites use: bin/k8s/<version>.
		matches, _ := filepath.Glob(filepath.Join("..", "..", "bin", "k8s", "*"))
		if len(matches) > 0 {
			testEnv.BinaryAssetsDirectory = matches[0]
		}
	}
	var err error
	restCfg, err = testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "envtest start failed: %v\nRun `make crd-render setup-envtest` (or `make test`, which does both) before this suite.\n", err)
		os.Exit(1)
	}
	if err := framev1beta1.AddToScheme(scheme.Scheme); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	k8sClient, err = client.New(restCfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = testEnv.Stop()
	os.Exit(code)
}

// bindGroups gives frame:operators the right to patch nodes and frame:viewers
// only the right to read them — the shape the real tier bindings have.
func bindGroups(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	roles := []struct {
		name  string
		verbs []string
		group string
	}{
		{"test-node-patcher", []string{"get", "list", "patch"}, "frame:operators"},
		{"test-node-reader", []string{"get", "list"}, "frame:viewers"},
	}
	for _, r := range roles {
		cr := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: r.name},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: r.verbs},
				{APIGroups: []string{"frame.plume-labs.io"}, Resources: []string{"frametasks"}, Verbs: []string{"get", "list"}},
			},
		}
		if err := k8sClient.Create(ctx, cr); err != nil && !apierrorsIsAlreadyExists(err) {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = k8sClient.Delete(context.Background(), cr)
		})
		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: r.name},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: r.name},
			Subjects:   []rbacv1.Subject{{Kind: "Group", Name: r.group, APIGroup: rbacv1.GroupName}},
		}
		if err := k8sClient.Create(ctx, crb); err != nil && !apierrorsIsAlreadyExists(err) {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = k8sClient.Delete(context.Background(), crb)
		})
	}
}

func proxyFor(t *testing.T, id uiproxy.Identity) *uiproxy.Proxy {
	t.Helper()
	u, err := url.Parse(restCfg.Host)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := rest.TransportFor(restCfg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := uiproxy.New(uiproxy.Options{
		Verifier:    stubVerifier{id: id},
		Recorder:    uiproxy.NewRecorder(k8sClient, "default", nil),
		Upstream:    u,
		Transport:   tr,
		GroupPrefix: "frame:",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The control that discriminates. A proxy that forwarded without
// impersonating would answer 200 to both callers — envtest's config is an
// admin — so asserting only the operator's 200 would pass against a proxy
// that does nothing at all.
func TestOperatorMayPatchWhereViewerMayNot(t *testing.T) {
	bindGroups(t)
	// A node to act on: envtest runs no kubelet, so create one.
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w2"}}
	if err := k8sClient.Create(context.Background(), node); err != nil && !apierrorsIsAlreadyExists(err) {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(context.Background(), node)
	})

	cases := []struct {
		name     string
		id       uiproxy.Identity
		wantCode int
	}{
		{"operator", uiproxy.Identity{User: "alice@example.com", Groups: []string{"operators"}}, http.StatusOK},
		{"viewer", uiproxy.Identity{User: "bob@example.com", Groups: []string{"viewers"}}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := proxyFor(t, tc.id)
			body := []byte(`{"spec":{"unschedulable":true}}`)
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/nodes/w2", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer stub")
			req.Header.Set("Content-Type", "application/merge-patch+json")
			req.Header.Set("X-Frame-Action", "cordon node w2")
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("got %d, want %d — body: %s", rec.Code, tc.wantCode, rec.Body.String())
			}

			var tasks framev1beta1.FrameTaskList
			if err := k8sClient.List(context.Background(), &tasks, client.InNamespace("default")); err != nil {
				t.Fatal(err)
			}
			// Exactly one, not "at least one": a scan that kept the last
			// match would silently pick the wrong object the moment a
			// second envtest spec ever wrote a FrameTask for the same user
			// into this namespace — precisely the failure mode this file
			// exists to rule out.
			var matches []framev1beta1.FrameTask
			for i := range tasks.Items {
				if tasks.Items[i].Spec.User == tc.id.User {
					matches = append(matches, tasks.Items[i])
				}
			}
			if len(matches) != 1 {
				t.Fatalf("found %d FrameTasks for %s, want exactly 1", len(matches), tc.id.User)
			}
			found := &matches[0]
			if int(found.Status.HTTPCode) != tc.wantCode {
				t.Fatalf("task recorded code %d, want %d", found.Status.HTTPCode, tc.wantCode)
			}
			if found.Spec.Action != "cordon node w2" {
				t.Fatalf("task action = %q", found.Spec.Action)
			}
		})
	}
}
