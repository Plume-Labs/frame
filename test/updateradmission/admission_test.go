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

// Package updateradmission runs deploy/kubernetes/updater/admission-policy.yaml
// against a real API server (envtest) as the updater's identity: the policy is
// CEL, and CEL that does not type-check or does not say what it was meant to
// is only found by an API server evaluating it.
package updateradmission

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/Plume-Labs/frame/test/manifests"
)

const (
	ns      = "frame-system"
	updater = "system:serviceaccount:frame-system:frame-updater"
	oldImg  = "registry.invalid/frame-controller@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newImg  = "registry.invalid/frame-controller@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

var (
	admin client.Client
	asUpd client.Client // the updater's identity, impersonated
	root  string
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
		imp.Impersonate = rest.ImpersonationConfig{UserName: updater}
		if asUpd, err = client.New(imp, client.Options{Scheme: scheme.Scheme}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return m.Run()
	}()
	_ = env.Stop()
	os.Exit(code)
}

func policyPath(t *testing.T) string {
	t.Helper()
	if root == "" {
		root = manifests.Root(t)
	}
	return filepath.Join(root, "deploy", "kubernetes", "updater", "admission-policy.yaml")
}

func installPolicy(t *testing.T) {
	t.Helper()
	f, err := os.Open(policyPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	dec := k8syaml.NewYAMLOrJSONDecoder(f, 4096)
	for {
		u := &unstructured.Unstructured{}
		if err := dec.Decode(&u.Object); err != nil {
			break
		}
		if len(u.Object) == 0 {
			continue
		}
		if err := admin.Create(context.Background(), u); err != nil {
			t.Fatalf("creating %s %s: %v", u.GetKind(), u.GetName(), err)
		}
	}
}

func container(name, image string) corev1.Container {
	return corev1.Container{
		Name:  name,
		Image: image,
		Args:  []string{"--leader-elect"},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: ptr(true),
		},
	}
}

func ptr[T any](v T) *T { return &v }

func labels() map[string]string { return map[string]string{"app": "t"} }

func deployment(name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels()},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels()},
				Spec: corev1.PodSpec{
					ServiceAccountName: "frame-controller-manager",
					InitContainers:     []corev1.Container{container("init", oldImg)},
					Containers:         []corev1.Container{container("manager", oldImg), container("sidecar", oldImg)},
				},
			},
		},
	}
}

func daemonSet(name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels()},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels()},
				Spec: corev1.PodSpec{
					HostPID:    true,
					Containers: []corev1.Container{container("agent", oldImg)},
				},
			},
		},
	}
}

// grantPatch gives the updater the RBAC it has in production, widened to every
// name: this test is about what the policy refuses of an allowed patch, and
// rbac.yaml is reviewed on its own.
func grantPatch(t *testing.T) {
	t.Helper()
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "test-updater-patch"},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{"apps"},
			Resources: []string{"deployments", "daemonsets"},
			Verbs:     []string{"get", "patch"},
		}},
	}
	bind := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-updater-patch"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: updater}},
	}
	for _, o := range []client.Object{role, bind} {
		if err := admin.Create(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
}

// deniedByPolicy is true only when the policy refused the request: a refusal
// by RBAC or by anything else must not pass for it.
func deniedByPolicy(err error) bool {
	return err != nil && strings.Contains(err.Error(), "frame-updater-image-only")
}

// patchDeployment applies a strategic merge patch, the way kube.go does.
func patch(ctx context.Context, c client.Client, obj client.Object, body string) error {
	return c.Patch(ctx, obj, client.RawPatch(types.StrategicMergePatchType, []byte(body)))
}

// waitEnforced blocks until the API server enforces the policy: the binding
// takes a moment to be picked up, and a test that races it proves nothing.
func waitEnforced(t *testing.T) {
	t.Helper()
	d := deployment("probe")
	if err := admin.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := patch(context.Background(), asUpd, d, `{"spec":{"template":{"spec":{"hostNetwork":true}}}}`)
		if deniedByPolicy(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the policy is not enforced after 60s")
		}
		// It was let through, so undo it as the admin and try again.
		_ = patch(context.Background(), admin, d, `{"spec":{"template":{"spec":{"hostNetwork":null}}}}`)
		time.Sleep(time.Second)
	}
}

func TestPolicy(t *testing.T) {
	ctx := context.Background()
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	grantPatch(t)
	installPolicy(t)
	waitEnforced(t)

	img := func(c, image string) string {
		return fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"name":%q,"image":%q}]}}}}`, c, image)
	}
	cases := []struct {
		name  string
		body  string
		allow bool
	}{
		{"image to a digest", img("manager", newImg), true},
		{"image of the other container to a digest", img("sidecar", newImg), true},
		{"image to a tag", img("manager", "registry.invalid/frame-controller:latest"), false},
		{"image to a tag with a digest-looking suffix", img("manager", "registry.invalid/x:sha256:"+strings.Repeat("a", 64)), false},
		{"image to a short digest", img("manager", "registry.invalid/x@sha256:abc"), false},
		{"a new argument", `{"spec":{"template":{"spec":{"containers":[{"name":"manager","args":["--leader-elect","--x"]}]}}}}`, false},
		{"a privileged security context", `{"spec":{"template":{"spec":{"containers":[{"name":"manager","securityContext":{"privileged":true}}]}}}}`, false},
		{"an environment variable", `{"spec":{"template":{"spec":{"containers":[{"name":"manager","env":[{"name":"A","value":"b"}]}]}}}}`, false},
		{"a command", `{"spec":{"template":{"spec":{"containers":[{"name":"manager","command":["sh"]}]}}}}`, false},
		{"another container", `{"spec":{"template":{"spec":{"containers":[{"name":"extra","image":"` + newImg + `"}]}}}}`, false},
		{"another service account", `{"spec":{"template":{"spec":{"serviceAccountName":"cluster-admin"}}}}`, false},
		{"host PID", `{"spec":{"template":{"spec":{"hostPID":true}}}}`, false},
		{"host network", `{"spec":{"template":{"spec":{"hostNetwork":true}}}}`, false},
		{"a volume", `{"spec":{"template":{"spec":{"volumes":[{"name":"v","hostPath":{"path":"/"}}]}}}}`, false},
		{"an init container image", `{"spec":{"template":{"spec":{"initContainers":[{"name":"init","image":"` + newImg + `"}]}}}}`, false},
		{"replicas", `{"spec":{"replicas":0}}`, false},
		{"pod labels", `{"spec":{"template":{"metadata":{"labels":{"x":"y"}}}}}`, false},
		{"an owner that does not exist", `{"metadata":{"ownerReferences":[{"apiVersion":"v1","kind":"ConfigMap","name":"gone","uid":"00000000-0000-0000-0000-000000000001"}]}}`, false},
		{"a finalizer", `{"metadata":{"finalizers":["example.com/x"]}}`, false},
		{"a label on the workload", `{"metadata":{"labels":{"x":"y"}}}`, false},
		{"an annotation on the workload", `{"metadata":{"annotations":{"x":"y"}}}`, false},
		{"pausing the rollout", `{"spec":{"paused":true}}`, false},
		{"a rollout strategy", `{"spec":{"strategy":{"type":"Recreate","rollingUpdate":null}}}`, false},
		{"a progress deadline", `{"spec":{"progressDeadlineSeconds":3600}}`, false},
		{"a revision history limit", `{"spec":{"revisionHistoryLimit":0}}`, false},
		{"a node name", `{"spec":{"template":{"spec":{"nodeName":"n1"}}}}`, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := deployment(fmt.Sprintf("d%d", i))
			if err := admin.Create(ctx, d); err != nil {
				t.Fatal(err)
			}
			err := patch(ctx, asUpd, d, tc.body)
			if tc.allow && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !tc.allow && !deniedByPolicy(err) {
				t.Fatalf("not refused by the policy: %v", err)
			}
			// The same patch by anyone else is none of the policy's business.
			o := deployment(fmt.Sprintf("o%d", i))
			if err := admin.Create(ctx, o); err != nil {
				t.Fatal(err)
			}
			if err := patch(ctx, admin, o, tc.body); err != nil {
				t.Fatalf("the policy must only bind the updater, but the admin was refused: %v", err)
			}
		})
	}

	t.Run("daemonset image to a digest", func(t *testing.T) {
		ds := daemonSet("agent")
		if err := admin.Create(ctx, ds); err != nil {
			t.Fatal(err)
		}
		if err := patch(ctx, asUpd, ds, `{"spec":{"template":{"spec":{"containers":[{"name":"agent","image":"`+newImg+`"}]}}}}`); err != nil {
			t.Fatalf("refused: %v", err)
		}
	})
	for name, body := range map[string]string{
		"daemonset update strategy":   `{"spec":{"updateStrategy":{"type":"OnDelete"}}}`,
		"daemonset min ready seconds": `{"spec":{"minReadySeconds":3600}}`,
		"daemonset owner":             `{"metadata":{"ownerReferences":[{"apiVersion":"v1","kind":"ConfigMap","name":"gone","uid":"00000000-0000-0000-0000-000000000001"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			ds := daemonSet("ds-" + strings.ReplaceAll(name, " ", "-"))
			if err := admin.Create(ctx, ds); err != nil {
				t.Fatal(err)
			}
			if err := patch(ctx, asUpd, ds, body); !deniedByPolicy(err) {
				t.Fatalf("not refused by the policy: %v", err)
			}
		})
	}
	t.Run("daemonset without host PID cannot be given it", func(t *testing.T) {
		ds := daemonSet("agent2")
		ds.Spec.Template.Spec.HostPID = false
		if err := admin.Create(ctx, ds); err != nil {
			t.Fatal(err)
		}
		if err := patch(ctx, asUpd, ds, `{"spec":{"template":{"spec":{"hostPID":true}}}}`); !deniedByPolicy(err) {
			t.Fatalf("not refused by the policy: %v", err)
		}
	})
}

// TestPolicyNamesEveryField fails when the vendored Kubernetes types hold a
// pod spec or container field the policy does not compare: the policy is a
// list of fields, and a Kubernetes upgrade adds to it silently otherwise.
func TestPolicyNamesEveryField(t *testing.T) {
	raw, err := os.ReadFile(policyPath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	check := func(typ reflect.Type, prefixes []string, exempt map[string]bool) {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if name == "" || exempt[name] {
				continue
			}
			found := false
			for _, p := range prefixes {
				if strings.Contains(text, p+name+" ") {
					found = true
				}
			}
			if !found {
				t.Errorf("%s.%s is not compared by admission-policy.yaml", typ.Name(), name)
			}
		}
	}
	// containers is compared by its own rule, and image is the one thing the
	// policy lets through.
	check(reflect.TypeOf(corev1.PodSpec{}), []string{"variables.newSpec.?"}, map[string]bool{"containers": true})
	check(reflect.TypeOf(corev1.Container{}), []string{"o.?"}, map[string]bool{"name": true, "image": true})
	// selector and template have their own rules.
	check(reflect.TypeOf(appsv1.DeploymentSpec{}), []string{"variables.newWorkload.?"}, map[string]bool{"selector": true, "template": true})
	check(reflect.TypeOf(appsv1.DaemonSetSpec{}), []string{"variables.newWorkload.?"}, map[string]bool{"selector": true, "template": true})
	// What the API server owns, or cannot be changed by a patch, is not the
	// policy's concern; the rest of the metadata is.
	check(reflect.TypeOf(metav1.ObjectMeta{}), []string{"variables.newMeta.?"}, map[string]bool{
		"name": true, "generateName": true, "namespace": true, "selfLink": true, "uid": true,
		"resourceVersion": true, "generation": true, "creationTimestamp": true,
		"deletionTimestamp": true, "deletionGracePeriodSeconds": true, "managedFields": true,
		"annotations": true, // compared by its own rule, which lets the DaemonSet generation through
	})
	if !strings.Contains(text, "variables.newMeta.?annotations.orValue") {
		t.Error("annotations are not compared by admission-policy.yaml")
	}

	// Every comparison names the same field on both sides: a typo on one side
	// would compare "none" to "none" and always pass, and the checks above only
	// read the first side.
	for _, m := range regexp.MustCompile(`\.\?(\w+) == [\w.]+\.\?(\w+)`).FindAllStringSubmatch(text, -1) {
		if m[1] != m[2] {
			t.Errorf("a comparison reads %s on one side and %s on the other", m[1], m[2])
		}
	}
}
