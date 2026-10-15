// Package managerrbac proves, against a real apiserver, what the manager's
// shipped RBAC lets it do with Secrets — and what it no longer can.
//
// The roles and bindings are rendered by kustomize from
// config/overlays/production — the overlay this cluster is deployed from —
// never written here: a fixture that builds its own RBAC proves the code,
// not the manifest (an earlier version of this test bound the namespaces
// itself, and could not see that the deployed render bound none). Every
// call is made as the manager's service account (impersonation), not as
// the envtest admin.
package managerrbac

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
	"github.com/gothshoot/frame/internal/cephmgr"
	framectrl "github.com/gothshoot/frame/internal/controller/frame"
	"github.com/gothshoot/frame/test/manifests"
)

const (
	mgrNS   = "frame-system" // kustomize's namespace for everything under config/
	mgrSA   = "frame-controller-manager"
	readNS  = "default"   // overlay: frame-manager-secret-reader (FrameMachine default/ml350-g9)
	writeNS = "apps"      // not bound by the overlay: a FrameService namespace, bound by the test as the doc says
	otherNS = "elsewhere" // bound to nothing
	overlay = "config/overlays/production"
)

var (
	admin client.Client
	mgr   client.Client // the manager's service account, impersonated
)

func TestMain(m *testing.M) {
	env := &envtest.Environment{}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if matches, _ := filepath.Glob(filepath.Join("..", "..", "bin", "k8s", "*")); len(matches) > 0 {
			env.BinaryAssetsDirectory = matches[0]
		}
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "envtest start failed: %v (run `make setup-envtest` or `make test`)\n", err)
		os.Exit(1)
	}
	code := func() int {
		if admin, err = client.New(cfg, client.Options{Scheme: scheme.Scheme}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		imp := rest.CopyConfig(cfg)
		imp.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + mgrNS + ":" + mgrSA}
		if mgr, err = client.New(imp, client.Options{Scheme: scheme.Scheme}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return m.Run()
	}()
	_ = env.Stop()
	os.Exit(code)
}

// testIMG stands in for the registry image deploy-production is given.
const testIMG = "registry.invalid/frame-controller:test"

// render renders dir (under config/) through hack/render-overlay.sh — the
// code path `make deploy-production` uses — with testIMG, and returns the
// path of the rendered YAML. `make test` builds bin/kustomize first
// (crd-render); KUSTOMIZE overrides it.
//
// go test caches a passing result against the files the test opened;
// kustomize reads config/ in another process, invisible to it. So every
// file under config/ and the script are read here too: editing the overlay
// then re-runs this test instead of answering "ok (cached)".
func render(t *testing.T, dir string) string {
	t.Helper()
	root := manifests.Root(t)
	inputs := []string{filepath.Join(root, "hack", "render-overlay.sh")}
	_ = filepath.WalkDir(filepath.Join(root, "config"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			inputs = append(inputs, p)
		}
		return nil
	})
	for _, p := range inputs {
		if _, err := os.ReadFile(p); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(filepath.Join(root, "hack", "render-overlay.sh"), strings.TrimPrefix(dir, "config/"), testIMG)
	if os.Getenv("KUSTOMIZE") == "" {
		cmd.Env = append(os.Environ(), "KUSTOMIZE="+filepath.Join(root, "bin", "kustomize"))
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rendering %s: %v: %s — run `make kustomize`", dir, err, stderr.String())
	}
	path := filepath.Join(t.TempDir(), "render.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The overlay is applied as rendered (make deploy-production): it must
// carry the image it was given — never config/manager's placeholder or a
// :latest — and must not create frame-provisiond, which this cluster
// doesn't run (a Deployment and two Services, one a NodePort).
func TestProductionRenderImageAndNoProvisiond(t *testing.T) {
	raw, err := os.ReadFile(render(t, overlay))
	if err != nil {
		t.Fatal(err)
	}
	var managerImage string
	dec := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		var u unstructured.Unstructured
		if err := dec.Decode(&u.Object); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if u.Object == nil {
			continue
		}
		if strings.Contains(u.GetName(), "provisiond") {
			t.Errorf("%s: renders %s %s; frame-provisiond must not be deployed", overlay, u.GetKind(), u.GetName())
		}
		if u.GetKind() == "Deployment" && u.GetName() == "frame-controller-manager" {
			cs, _, _ := unstructured.NestedSlice(u.Object, "spec", "template", "spec", "containers")
			for _, c := range cs {
				if m, ok := c.(map[string]any); ok && m["name"] == "manager" {
					managerImage, _ = m["image"].(string)
				}
			}
		}
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if !strings.HasPrefix(l, "image:") {
			continue
		}
		img := strings.TrimSpace(strings.TrimPrefix(l, "image:"))
		if strings.Contains(img, "example.com") || strings.HasSuffix(img, ":latest") {
			t.Errorf("%s: renders image %q (placeholder or :latest)", overlay, img)
		}
	}
	if managerImage != testIMG {
		t.Errorf("%s: manager image %q, want the IMG given, %q", overlay, managerImage, testIMG)
	}
}

// apply installs the RBAC of the rendered overlay, as `kubectl apply -k`
// would: roles, cluster roles and every binding, namespaces first.
func apply(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	must := func(o client.Object) {
		t.Helper()
		if err := admin.Create(ctx, o); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("creating %T %s/%s: %v", o, o.GetNamespace(), o.GetName(), err)
		}
	}
	for _, ns := range []string{mgrNS, writeNS, otherNS} {
		must(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	}
	path := render(t, overlay)
	for _, cr := range manifests.ClusterRoles(t, path) {
		must(&cr)
	}
	for _, r := range manifests.Roles(t, path) {
		must(&r)
	}
	for _, b := range manifests.ClusterRoleBindings(t, path) {
		must(&b)
	}
	for _, b := range manifests.RoleBindings(t, path) {
		must(&b)
	}
}

// managerBindings maps namespace → ClusterRole for the RoleBindings that
// give the manager's service account a secret-access ClusterRole.
func managerBindings(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, b := range manifests.RoleBindings(t, path) {
		if b.RoleRef.Kind != "ClusterRole" || !strings.HasPrefix(b.RoleRef.Name, "frame-manager-secret-") {
			continue
		}
		for _, s := range b.Subjects {
			if s.Kind == "ServiceAccount" && s.Namespace == mgrNS && s.Name == mgrSA {
				out[b.Namespace] = b.RoleRef.Name
			}
		}
	}
	return out
}

// The rendered installs, not a fixture: config/default binds the manager
// to no namespace outside its own (that is the point), and the deployed
// overlay binds exactly the namespaces holding Frame objects that use
// Secrets today — default (FrameMachine), and nothing to the writer: no
// FrameService exists on the cluster.
func TestRenderedInstallsBindTheRightNamespaces(t *testing.T) {
	if got := managerBindings(t, render(t, "config/default")); len(got) != 0 {
		t.Errorf("config/default binds the manager to secret-access roles in %v; "+
			"it must bind none (sites do it in an overlay)", got)
	}
	path := render(t, overlay)
	want := map[string]string{readNS: "frame-manager-secret-reader"}
	got := managerBindings(t, path)
	for ns, role := range want {
		if got[ns] != role {
			t.Errorf("%s: namespace %s bound to %q, want %q", overlay, ns, got[ns], role)
		}
	}
	for ns, role := range got {
		if want[ns] == "" {
			t.Errorf("%s: unexpected binding %s → %s (add it to this test with the object that needs it)", overlay, ns, role)
		}
	}
	roles := manifests.ClusterRoles(t, path)
	for _, role := range want {
		if _, ok := roles[role]; !ok {
			t.Errorf("%s: ClusterRole %s is bound but not rendered", overlay, role)
		}
	}
}

func secret(ns, name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func forbidden(t *testing.T, what string, err error) {
	t.Helper()
	if !apierrors.IsForbidden(err) {
		t.Errorf("%s: err %v, want Forbidden", what, err)
	}
}

func TestManagerSecretAccessIsPerNamespace(t *testing.T) {
	apply(t)
	ctx := context.Background()
	for _, s := range []*corev1.Secret{
		secret(mgrNS, cephmgr.StateSecretName, nil),
		secret(mgrNS, cephmgr.SecretName, map[string]string{
			"url": "https://dashboard:8443", "username": "u", "password": "p",
		}),
		secret(mgrNS, "alert-token", map[string]string{"token": "t"}),
		secret(readNS, "bmc", map[string]string{"username": "u", "password": "p"}),
		secret(otherNS, "bmc", map[string]string{"username": "u", "password": "p"}),
		secret("kube-system", "anything", map[string]string{"k": "v"}),
	} {
		if err := admin.Create(ctx, s); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}

	// ── What the manager does, with its own rights ──

	// ceph-mgr account and write-ahead gate, in the manager's namespace.
	cfg, err := cephmgr.SecretLoader(mgr, mgrNS)(ctx)
	if err != nil || cfg.Username != "u" {
		t.Fatalf("reading the dashboard account: %+v, %v", cfg, err)
	}
	gate := &cephmgr.SecretLoginGate{Reader: mgr, Writer: mgr, Namespace: mgrNS}
	if err := gate.Begin(ctx, cfg); err != nil {
		t.Fatalf("gate Begin (get+update of the state Secret): %v", err)
	}
	if err := gate.Succeeded(ctx, cfg); err != nil {
		t.Fatalf("gate Succeeded: %v", err)
	}
	// Alert token and FrameInstall kubeconfig, in the manager's namespace.
	if err := mgr.Get(ctx, client.ObjectKey{Namespace: mgrNS, Name: "alert-token"}, &corev1.Secret{}); err != nil {
		t.Errorf("alert token: %v", err)
	}
	if err := mgr.Create(ctx, secret(mgrNS, "fi-kubeconfig", map[string]string{"kubeconfig": "x"})); err != nil {
		t.Errorf("FrameInstall kubeconfig: %v", err)
	}
	// FrameMachine BMC credentials, where the reader is bound.
	bmc := framev1beta1.BMCSpec{Address: "192.0.2.10", CredentialsRef: "bmc"}
	if _, err := framectrl.BuildRedfishClient(ctx, mgr, readNS, bmc); err != nil {
		t.Errorf("BMC credentials in %s: %v", readNS, err)
	}
	// FrameService: nothing in its namespace until the writer is bound
	// there — then create, update, delete. The binding is the one
	// docs/deployment.md tells the operator to add when a FrameService is
	// declared; the overlay binds none (TestRenderedInstallsBindTheRightNamespaces).
	forbidden(t, "FrameService Secret before the writer is bound", mgr.Create(ctx, secret(writeNS, "early", nil)))
	if err := admin.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "frame-manager-secret-writer", Namespace: writeNS},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "frame-manager-secret-writer"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: mgrSA, Namespace: mgrNS}},
	}); err != nil {
		t.Fatal(err)
	}
	svcSecret := secret(writeNS, "svc-binding", nil)
	if _, err := controllerutil.CreateOrUpdate(ctx, mgr, svcSecret, func() error {
		svcSecret.Data = map[string][]byte{"k": []byte("v1")}
		return nil
	}); err != nil {
		t.Errorf("FrameService create: %v", err)
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, mgr, svcSecret, func() error {
		svcSecret.Data = map[string][]byte{"k": []byte("v2")}
		return nil
	}); err != nil {
		t.Errorf("FrameService update: %v", err)
	}
	if err := mgr.Delete(ctx, svcSecret); err != nil {
		t.Errorf("FrameService delete: %v", err)
	}

	// ── What it can no longer do ──

	_, err = framectrl.BuildRedfishClient(ctx, mgr, otherNS, bmc)
	forbidden(t, "BMC credentials in an unbound namespace", err)
	forbidden(t, "get in kube-system",
		mgr.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "anything"}, &corev1.Secret{}))
	forbidden(t, "create where only the reader is bound", mgr.Create(ctx, secret(readNS, "x", nil)))
	forbidden(t, "create in an unbound namespace", mgr.Create(ctx, secret(otherNS, "x", nil)))
	forbidden(t, "list Secrets cluster-wide", mgr.List(ctx, &corev1.SecretList{}))
	forbidden(t, "list Secrets in its own namespace", mgr.List(ctx, &corev1.SecretList{}, client.InNamespace(mgrNS)))
	other := &corev1.Secret{}
	if err := admin.Get(ctx, client.ObjectKey{Namespace: mgrNS, Name: "alert-token"}, other); err != nil {
		t.Fatal(err)
	}
	other.Data["token"] = []byte("changed")
	forbidden(t, "update a Secret of its namespace other than the state Secret", mgr.Update(ctx, other))
	forbidden(t, "delete in its own namespace", mgr.Delete(ctx, secret(mgrNS, "alert-token", nil)))
}
