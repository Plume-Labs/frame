# Relais d'alertes Alertmanager → Frame → tenants — plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Frame reçoit les alertes d'Alertmanager, les garde en `FrameAlert`, et les relaie aux `FrameAlertSubscription` (Neura d'abord) sans rien perdre.

**Architecture:** Tout vit dans le manager Frame. Un `Runnable` HTTP (toutes répliques) valide et écrit les `FrameAlert` ; un contrôleur (leader seul) relaie chaque alerte aux abonnements qui la filtrent, avec retentatives, puis purge ; un second contrôleur calcule le statut des abonnements. Le tenant reçoit le format Alertmanager v4 à une alerte par requête.

**Tech Stack:** Go 1.26, controller-runtime v0.23.3, kubebuilder markers + controller-gen, Ginkgo/Gomega + envtest (schéma), `controller-runtime/pkg/client/fake` + `net/http/httptest` (logique), Prometheus client_golang ; console React + vitest.

**Spec:** `docs/superpowers/specs/2026-09-15-alert-relay-design.md` (commit `a33471c`). Lire la spec avant chaque tâche.

## Global Constraints

- Groupe `frame.plume-labs.io`, version `v1beta1` seule, **aucun** webhook de conversion pour ces deux types.
- Les deux types sont **namespacés** et vivent dans `frame-system`.
- Nom d'une alerte : `fa-<fingerprint>` ; empreinte acceptée : `^[0-9a-f]{1,64}$`.
- Récepteur : port `8445`, route unique `POST /alertmanager`, jeton Secret `frame-system/frame-alert-receiver-token` clé `token`, relu à chaque requête, comparé en temps constant.
- Corps ≤ 1 Mio (`413`), version `"4"`, ≤ 100 alertes, status ∈ {firing, resolved} (`400`), jeton absent/faux/Secret absent (`401`), écriture en échec (`503`), autre chemin ou méthode (`404`).
- Bornes des maps : 64 entrées, clé ≤ 256 octets, valeur ≤ 4096 octets, troncature (pas de rejet).
- `lastReceivedAt` n'est réécrit que si la valeur précédente a plus de **15 min**.
- Envoi : timeout 10 s, redirections refusées, `Authorization: Bearer <jeton>`, une alerte par requête, `receiver` = nom de l'abonnement.
- Classement de réponse : `2xx` livré ; `5xx`, erreur réseau, `408`, `429` retenté ; tout autre code (y compris `3xx`) définitif.
- Attente entre essais : `min(5s × 2^(attempts-1), 10min)`.
- `excludeAlertNames` par défaut : `[Watchdog, InfoInhibitor]`.
- Rétention : flag `--alert-retention-days`, défaut `14`.
- Statut d'abonnement recalculé au plus une fois par minute.
- Lecture des Secrets de jeton par `mgr.GetAPIReader()` (non caché), jamais par le client caché.
- Métriques : `frame_alert_receiver_requests_total{code}`, `frame_alerts_received_total{state}`, `frame_alert_deliveries_total{subscription,result}`, `frame_alert_pending_deliveries{subscription}`.
- Commits en anglais, style conventional (`feat(alerting): …`), sans trailer `Co-Authored-By`.
- Tests : `make test` (génère CRD, deepcopy, rendu CRD pour envtest). Pour une boucle rapide sur la logique pure : `go test ./internal/alerting/...`.

**Précision de la spec (§3.1 vs §4.3).** La spec dit à la fois « `status` écrit par le contrôleur seul » et « le récepteur met à jour `status.state` ». Ce plan tranche : le récepteur écrit `spec` **et** `status.state` + `status.lastReceivedAt` ; le contrôleur de relais n'écrit que `status.deliveries`. Chacun passe par un **merge patch** qui ne contient que ses propres champs, pour ne jamais écraser ceux de l'autre.

---

## File Structure

| Fichier | Responsabilité |
|---|---|
| `api/frame/v1beta1/framealert_types.go` | type `FrameAlert`, `AlertDelivery` |
| `api/frame/v1beta1/framealertsubscription_types.go` | type `FrameAlertSubscription`, `AlertFilter` |
| `internal/controller/frame/framealert_v1beta1_schema_test.go` | schéma envtest des deux types |
| `internal/alerting/payload.go` | format Alertmanager v4, validation, bornes |
| `internal/alerting/receiver.go` | `Runnable` HTTP, auth, écriture idempotente |
| `internal/alerting/filter.go` | `Matches(filter, alert)` |
| `internal/alerting/sender.go` | client HTTP, classement des réponses, backoff |
| `internal/alerting/relay_controller.go` | livraisons, cas limites, purge |
| `internal/alerting/subscription_status_controller.go` | `lastSuccessAt`, `lastError`, `pendingDeliveries` |
| `internal/alerting/metrics.go` | compteurs et jauge |
| `internal/alerting/*_test.go` | tests unitaires (fake client + httptest) |
| `config/rbac/framealert*_role.yaml` | rôles d'étage |
| `test/manifests/rbac_tiers_test.go` | invariants d'étage |
| `config/manager/manager.yaml`, `config/manager/alert_receiver_service.yaml` | port 8445 + Service |
| `deploy/kubernetes/containment/networkpolicy-frame-alert-receiver.yaml` | entrée 8445 depuis Alertmanager |
| `cmd/main.go` | flags + enregistrement |
| `src/lib/alert-registry.ts` (+ `.test.ts`) | projection console des deux types |
| `src/lib/frame-sdk.ts` | deux lectures |
| `src/components/AlertsView.tsx` | vues Actives/Historique + carte Abonnements |
| `deploy/samples/test-cluster/frame-alert-subscription-neura.yaml`, `kps-values.yaml`, `check-alert-routes.sh` | déploiement |

---

### Task 1: Types `FrameAlert` et `FrameAlertSubscription` + schéma

**Files:**
- Create: `api/frame/v1beta1/framealert_types.go`
- Create: `api/frame/v1beta1/framealertsubscription_types.go`
- Modify: `config/crd/kustomization.yaml` (ajouter deux `bases/`)
- Create: `config/samples/frame_v1beta1_framealertsubscription.yaml`
- Test: `internal/controller/frame/framealert_v1beta1_schema_test.go`

**Interfaces:**
- Produces: `framev1beta1.FrameAlert{Spec FrameAlertSpec; Status FrameAlertStatus}`, `FrameAlertList`, `AlertDelivery`, constantes `AlertStateFiring = "Firing"`, `AlertStateResolved = "Resolved"` ; `framev1beta1.FrameAlertSubscription{Spec FrameAlertSubscriptionSpec; Status FrameAlertSubscriptionStatus}`, `FrameAlertSubscriptionList`, `AlertFilter`, `AlertTokenRef`.

- [ ] **Step 1: Écrire le test de schéma (échoue : types absents)**

```go
package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// Schema only: both kinds are post-freeze, v1beta1-only, without conversion.
// Table form for the same reason as frametask_v1beta1_schema_test.go — a
// top-level Test would run before BeforeSuite populates k8sClient.
var _ = Describe("FrameAlert v1beta1 schema", func() {
	now := metav1.Now()

	DescribeTable("accepts only a well-formed fingerprint and state",
		func(fp, state string, wantErr bool) {
			obj := &framev1beta1.FrameAlert{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "fa-", Namespace: "default"},
				Spec:       framev1beta1.FrameAlertSpec{Fingerprint: fp, AlertName: "X", StartsAt: now},
			}
			err := k8sClient.Create(ctx, obj)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
				if state != "" {
					obj.Status.State = state
					err = k8sClient.Status().Update(ctx, obj)
				}
			}
			if wantErr {
				Expect(err).To(HaveOccurred())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("hex fingerprint", "a1b2c3d4e5f60718", "Firing", false),
		Entry("uppercase fingerprint", "A1B2", "", true),
		Entry("empty fingerprint", "", "", true),
		Entry("unknown state", "a1b2", "Pending", true),
	)
})

var _ = Describe("FrameAlertSubscription v1beta1 schema", func() {
	It("defaults excludeAlertNames to the two meta alerts when filter is omitted", func() {
		obj := &framev1beta1.FrameAlertSubscription{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "sub-", Namespace: "default"},
			Spec: framev1beta1.FrameAlertSubscriptionSpec{
				URL:            "http://tenant.example/webhook",
				TokenSecretRef: framev1beta1.AlertTokenRef{Name: "tok", Key: "token"},
			},
		}
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
		Expect(obj.Spec.Filter.ExcludeAlertNames).To(Equal([]string{"Watchdog", "InfoInhibitor"}))
	})

	DescribeTable("rejects a URL that is not http(s)",
		func(url string, wantErr bool) {
			obj := &framev1beta1.FrameAlertSubscription{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "sub-", Namespace: "default"},
				Spec: framev1beta1.FrameAlertSubscriptionSpec{
					URL:            url,
					TokenSecretRef: framev1beta1.AlertTokenRef{Name: "tok", Key: "token"},
				},
			}
			err := k8sClient.Create(ctx, obj)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
			}
			if wantErr {
				Expect(err).To(HaveOccurred())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("http", "http://neura.svc:3000/api/it/alerts/webhook", false),
		Entry("https", "https://tenant.example/hook", false),
		Entry("file scheme", "file:///etc/passwd", true),
		Entry("empty", "", true),
	)
})
```

- [ ] **Step 2: Lancer, constater l'échec de compilation**

Run: `go vet ./internal/controller/frame/`
Expected: FAIL — `undefined: framev1beta1.FrameAlert`.

- [ ] **Step 3: Écrire `framealert_types.go`**

```go
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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	AlertStateFiring   = "Firing"
	AlertStateResolved = "Resolved"
)

// FrameAlertSpec is what Alertmanager said about one alert, written by the
// receiver alone. The object is named fa-<fingerprint>: Alertmanager's
// fingerprint is a stable hash of the labels, so one object carries the alert
// from firing to resolution without an index.
type FrameAlertSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{1,64}$`
	Fingerprint string `json:"fingerprint"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	AlertName string `json:"alertName,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Severity string `json:"severity,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace,omitempty"`
	// Bounded by the receiver (64 entries, 256-byte keys, 4096-byte values)
	// before it ever reaches the apiserver.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	Annotations map[string]string `json:"annotations,omitempty"`
	// +kubebuilder:validation:Required
	StartsAt metav1.Time `json:"startsAt"`
	// Empty while the alert fires.
	// +optional
	EndsAt *metav1.Time `json:"endsAt,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	GeneratorURL string `json:"generatorURL,omitempty"`
}

// AlertDelivery is the relay's record of one subscription for one alert.
type AlertDelivery struct {
	// +kubebuilder:validation:Required
	Subscription string `json:"subscription"`
	// The last state delivered successfully; empty until the first success.
	// +optional
	// +kubebuilder:validation:Enum=Firing;Resolved
	DeliveredState string `json:"deliveredState,omitempty"`
	// +optional
	Attempts int32 `json:"attempts,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	LastError string `json:"lastError,omitempty"`
	// +optional
	LastAttemptAt *metav1.Time `json:"lastAttemptAt,omitempty"`
	// +optional
	LastDeliveredAt *metav1.Time `json:"lastDeliveredAt,omitempty"`
	// A non-retryable answer (401, 400, a redirect). Lifted when the alert
	// changes state or the subscription's generation changes.
	// +optional
	PermanentFailure bool `json:"permanentFailure,omitempty"`
	// The alert state the permanent failure was recorded against.
	// +optional
	FailedState string `json:"failedState,omitempty"`
	// The subscription generation this entry was last evaluated against.
	// +optional
	SubscriptionGeneration int64 `json:"subscriptionGeneration,omitempty"`
}

// FrameAlertStatus: state and lastReceivedAt belong to the receiver,
// deliveries to the relay. Each writes with a merge patch holding only its
// own fields.
type FrameAlertStatus struct {
	// +optional
	// +kubebuilder:validation:Enum=Firing;Resolved
	State string `json:"state,omitempty"`
	// Rewritten at most every 15 minutes: Alertmanager resends unchanged
	// groups every few minutes, and kine on the test cluster cannot absorb
	// a write per resend.
	// +optional
	LastReceivedAt *metav1.Time `json:"lastReceivedAt,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=subscription
	Deliveries []AlertDelivery `json:"deliveries,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Alert",type=string,JSONPath=`.spec.alertName`
// +kubebuilder:printcolumn:name="Severity",type=string,JSONPath=`.spec.severity`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FrameAlert is one cluster alert as Frame received it, with its relay record.
type FrameAlert struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec   FrameAlertSpec   `json:"spec"`
	Status FrameAlertStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type FrameAlertList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameAlert `json:"items"`
}

func init() { SchemeBuilder.Register(&FrameAlert{}, &FrameAlertList{}) }
```

- [ ] **Step 4: Écrire `framealertsubscription_types.go`**

```go
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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AlertTokenRef names a Secret key in the subscription's own namespace.
type AlertTokenRef struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
}

// AlertFilter decides which alerts a subscription receives. Empty lists
// mean "no restriction", except excludeAlertNames which defaults to the two
// meta alerts that never resolve.
type AlertFilter struct {
	// +optional
	// +kubebuilder:default={"Watchdog","InfoInhibitor"}
	ExcludeAlertNames []string `json:"excludeAlertNames,omitempty"`
	// +optional
	Severities []string `json:"severities,omitempty"`
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`
}

type FrameAlertSubscriptionSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://.+`
	URL string `json:"url"`
	// +kubebuilder:validation:Required
	TokenSecretRef AlertTokenRef `json:"tokenSecretRef"`
	// +optional
	// +kubebuilder:default={}
	Filter AlertFilter `json:"filter,omitempty"`
	// +optional
	Paused bool `json:"paused,omitempty"`
}

type FrameAlertSubscriptionStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	LastSuccessAt *metav1.Time `json:"lastSuccessAt,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	LastError string `json:"lastError,omitempty"`
	// +optional
	PendingDeliveries int32 `json:"pendingDeliveries,omitempty"`
	// When the status was last computed; bounds recomputation to once a minute.
	// +optional
	ComputedAt *metav1.Time `json:"computedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.url`
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=`.status.pendingDeliveries`
// +kubebuilder:printcolumn:name="LastSuccess",type=date,JSONPath=`.status.lastSuccessAt`

// FrameAlertSubscription declares a tenant endpoint that receives cluster alerts.
type FrameAlertSubscription struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec   FrameAlertSubscriptionSpec   `json:"spec"`
	Status FrameAlertSubscriptionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type FrameAlertSubscriptionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FrameAlertSubscription `json:"items"`
}

func init() { SchemeBuilder.Register(&FrameAlertSubscription{}, &FrameAlertSubscriptionList{}) }
```

- [ ] **Step 5: Déclarer les CRD et l'exemple**

Dans `config/crd/kustomization.yaml`, après `- bases/frame.plume-labs.io_framediskclaims.yaml` :

```yaml
- bases/frame.plume-labs.io_framealerts.yaml
- bases/frame.plume-labs.io_framealertsubscriptions.yaml
```

(Pas de patch `webhook_in_…` : ces types n'ont pas de conversion.)

`config/samples/frame_v1beta1_framealertsubscription.yaml` :

```yaml
apiVersion: frame.plume-labs.io/v1beta1
kind: FrameAlertSubscription
metadata:
  name: example-tenant
  namespace: frame-system
spec:
  url: http://tenant.tenant-ns.svc.cluster.local:3000/alerts
  tokenSecretRef: { name: example-tenant-alert-token, key: token }
```

- [ ] **Step 6: Générer et tester**

Run: `make test`
Expected: PASS, dont les 2 `Describe` ci-dessus. Vérifier aussi : `grep -c '^kind: CustomResourceDefinition' bin/crd-render/crds.yaml` augmente de 2.

- [ ] **Step 7: Commit**

```bash
git add api/frame/v1beta1/framealert_types.go api/frame/v1beta1/framealertsubscription_types.go \
  api/frame/v1beta1/zz_generated.deepcopy.go config/crd config/samples/frame_v1beta1_framealertsubscription.yaml \
  internal/controller/frame/framealert_v1beta1_schema_test.go
git commit -m "feat(alerting): add FrameAlert and FrameAlertSubscription v1beta1 types"
```

---

### Task 2: Rôles d'étage et invariants RBAC

**Files:**
- Create: `config/rbac/framealert_viewer_role.yaml`, `config/rbac/framealert_admin_role.yaml`
- Create: `config/rbac/framealertsubscription_viewer_role.yaml`, `config/rbac/framealertsubscription_admin_role.yaml`
- Modify: `config/rbac/kustomization.yaml`
- Test: `test/manifests/rbac_tiers_test.go`

**Interfaces:**
- Consumes: noms de ressource `framealerts`, `framealertsubscriptions` (Task 1).
- Produces: rien pour le code Go.

Pas de fichier `*_editor_role.yaml` pour ces deux types : l'étage editor (operators) doit **lire sans écrire**, et il hérite déjà du viewer par agrégation (`frame-editor` sélectionne aussi `tier: viewer`, cf. commit `092720e`).

- [ ] **Step 1: Écrire les tests (échouent : rôles absents)**

Ajouter à la fin de `test/manifests/rbac_tiers_test.go` :

```go
// Alert relay, spec §7. Reading the registry is what the Alerts screen does
// for everyone; deciding who receives the cluster's alerts is an admin call;
// and no human writes a FrameAlert — only the manager's ServiceAccount does.
func TestAlertRegistryTiers(t *testing.T) {
	roles := ClusterRoles(t, tierRoleFiles(t)...)
	viewer := AggregatedRules(roles, "viewer")
	editor := AggregatedRules(roles, "editor")
	admin := AggregatedRules(roles, "admin")

	for _, res := range []string{"framealerts", "framealertsubscriptions"} {
		for _, verb := range []string{"get", "list", "watch"} {
			a := Access{Group: "frame.plume-labs.io", Resource: res, Verb: verb}
			if !Grants(viewer, a) {
				t.Errorf("frame-viewer does not aggregate %s — the Alerts screen cannot load", a)
			}
		}
	}

	for _, tier := range []struct {
		name  string
		rules []rbacv1.PolicyRule
	}{{"viewer", viewer}, {"editor", editor}, {"admin", admin}} {
		for _, verb := range []string{"create", "update", "patch", "delete"} {
			a := Access{Group: "frame.plume-labs.io", Resource: "framealerts", Verb: verb}
			if Grants(tier.rules, a) {
				t.Errorf("frame-%s aggregates %s: a human could forge or erase an alert", tier.name, a)
			}
		}
	}

	for _, verb := range []string{"create", "update", "patch", "delete"} {
		a := Access{Group: "frame.plume-labs.io", Resource: "framealertsubscriptions", Verb: verb}
		if Grants(editor, a) {
			t.Errorf("frame-editor aggregates %s: an operator could redirect the cluster's alerts", a)
		}
		if !Grants(admin, a) {
			t.Errorf("frame-admin does not aggregate %s — nobody can manage subscriptions", a)
		}
	}
}
```

- [ ] **Step 2: Lancer, constater l'échec**

Run: `go test ./test/manifests/ -run TestAlertRegistryTiers -v`
Expected: FAIL — `frame-viewer does not aggregate get frame.plume-labs.io/framealerts`.

- [ ] **Step 3: Écrire les quatre rôles**

`config/rbac/framealert_viewer_role.yaml` :

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  labels:
    app.kubernetes.io/name: frame
    app.kubernetes.io/managed-by: kustomize
    rbac.frame.plume-labs.io/tier: viewer
  name: framealert-viewer-role
rules:
- apiGroups: [frame.plume-labs.io]
  resources: [framealerts]
  verbs: [get, list, watch]
- apiGroups: [frame.plume-labs.io]
  resources: [framealerts/status]
  verbs: [get]
```

`config/rbac/framealert_admin_role.yaml` — lecture seule aussi, l'étage admin ne doit pas pouvoir écrire une alerte :

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  labels:
    app.kubernetes.io/name: frame
    app.kubernetes.io/managed-by: kustomize
    rbac.frame.plume-labs.io/tier: admin
  name: framealert-admin-role
rules:
- apiGroups: [frame.plume-labs.io]
  resources: [framealerts]
  verbs: [get, list, watch]
- apiGroups: [frame.plume-labs.io]
  resources: [framealerts/status]
  verbs: [get]
```

`config/rbac/framealertsubscription_viewer_role.yaml` :

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  labels:
    app.kubernetes.io/name: frame
    app.kubernetes.io/managed-by: kustomize
    rbac.frame.plume-labs.io/tier: viewer
  name: framealertsubscription-viewer-role
rules:
- apiGroups: [frame.plume-labs.io]
  resources: [framealertsubscriptions]
  verbs: [get, list, watch]
- apiGroups: [frame.plume-labs.io]
  resources: [framealertsubscriptions/status]
  verbs: [get]
```

`config/rbac/framealertsubscription_admin_role.yaml` :

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  labels:
    app.kubernetes.io/name: frame
    app.kubernetes.io/managed-by: kustomize
    rbac.frame.plume-labs.io/tier: admin
  name: framealertsubscription-admin-role
rules:
- apiGroups: [frame.plume-labs.io]
  resources: [framealertsubscriptions]
  verbs: [create, delete, get, list, patch, update, watch]
- apiGroups: [frame.plume-labs.io]
  resources: [framealertsubscriptions/status]
  verbs: [get]
```

Dans `config/rbac/kustomization.yaml`, à côté des lignes `frametask_*` :

```yaml
- framealert_admin_role.yaml
- framealert_viewer_role.yaml
- framealertsubscription_admin_role.yaml
- framealertsubscription_viewer_role.yaml
```

- [ ] **Step 4: Tests**

Run: `go test ./test/manifests/ -v`
Expected: PASS, y compris les tests d'étage existants.

- [ ] **Step 5: Contrôle discriminant**

Ajouter temporairement `create` aux verbes de `framealert_admin_role.yaml`, relancer : `TestAlertRegistryTiers` doit échouer sur `frame-admin aggregates create …framealerts`. Retirer `create`, relancer : PASS.

- [ ] **Step 6: Commit**

```bash
git add config/rbac test/manifests/rbac_tiers_test.go
git commit -m "feat(alerting): tier roles for the alert registry and subscriptions"
```

---

### Task 3: Format Alertmanager, validation et bornes

**Files:**
- Create: `internal/alerting/payload.go`
- Test: `internal/alerting/payload_test.go`

**Interfaces:**
- Produces:
  - `type Payload struct { Version, Status, Receiver string; Alerts []Alert }` (tags JSON `version,status,receiver,alerts`)
  - `type Alert struct { Status string; Labels, Annotations map[string]string; StartsAt, EndsAt time.Time; GeneratorURL, Fingerprint string }`
  - `const MaxBodyBytes = 1 << 20`, `const MaxAlertsPerRequest = 100`
  - `func Validate(p Payload) error`
  - `func BoundMap(m map[string]string) map[string]string`
  - `func AlertObjectName(fingerprint string) string` → `"fa-" + fingerprint`

- [ ] **Step 1: Tests (échouent : package vide)**

```go
package alerting

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func validPayload() Payload {
	return Payload{Version: "4", Alerts: []Alert{{Status: "firing", Fingerprint: "a1b2c3"}}}
}

func TestValidateAcceptsAMinimalPayload(t *testing.T) {
	if err := Validate(validPayload()); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*Payload){
		"version 3":         func(p *Payload) { p.Version = "3" },
		"no alerts":         func(p *Payload) { p.Alerts = nil },
		"101 alerts":        func(p *Payload) { p.Alerts = make([]Alert, 101); for i := range p.Alerts { p.Alerts[i] = Alert{Status: "firing", Fingerprint: "ab"} } },
		"empty fingerprint": func(p *Payload) { p.Alerts[0].Fingerprint = "" },
		"uppercase fingerprint": func(p *Payload) { p.Alerts[0].Fingerprint = "AB" },
		"65-char fingerprint":   func(p *Payload) { p.Alerts[0].Fingerprint = strings.Repeat("a", 65) },
		"unknown status":        func(p *Payload) { p.Alerts[0].Status = "pending" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := validPayload()
			mutate(&p)
			if Validate(p) == nil {
				t.Fatal("invalid payload accepted")
			}
		})
	}
}

func TestBoundMapKeepsAtMost64EntriesDeterministically(t *testing.T) {
	m := map[string]string{}
	for i := 0; i < 100; i++ {
		m[strings.Repeat("k", 3)+string(rune('A'+i%26))+strings.Repeat("x", i)] = "v"
	}
	a, b := BoundMap(m), BoundMap(m)
	if len(a) != 64 {
		t.Fatalf("want 64 entries, got %d", len(a))
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			t.Fatalf("two calls kept different keys: %q", k)
		}
	}
}

func TestBoundMapTruncatesOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("é", 3000) // 6000 bytes
	got := BoundMap(map[string]string{strings.Repeat("ü", 200): long})
	for k, v := range got {
		if len(k) > 256 || len(v) > 4096 {
			t.Fatalf("not bounded: key %d bytes, value %d bytes", len(k), len(v))
		}
		if !utf8.ValidString(k) || !utf8.ValidString(v) {
			t.Fatal("truncation split a rune")
		}
	}
}

func TestBoundMapOfNilIsNil(t *testing.T) {
	if BoundMap(nil) != nil {
		t.Fatal("nil map became non-nil")
	}
}
```

- [ ] **Step 2: Lancer, constater l'échec**

Run: `go test ./internal/alerting/ -run 'TestValidate|TestBoundMap' -v`
Expected: FAIL — `undefined: Payload`.

- [ ] **Step 3: Implémenter `payload.go`**

```go
// Package alerting receives Alertmanager webhooks into FrameAlert objects and
// relays them to FrameAlertSubscriptions. See
// docs/superpowers/specs/2026-09-15-alert-relay-design.md.
package alerting

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
	"unicode/utf8"
)

const (
	MaxBodyBytes        = 1 << 20
	MaxAlertsPerRequest = 100

	maxMapEntries = 64
	maxKeyBytes   = 256
	maxValueBytes = 4096
)

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

// Payload is Alertmanager's webhook body, version 4. Frame both reads it and
// writes it (one alert per request) to tenants.
type Payload struct {
	Version  string  `json:"version"`
	Status   string  `json:"status,omitempty"`
	Receiver string  `json:"receiver,omitempty"`
	Alerts   []Alert `json:"alerts"`
}

type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

func Validate(p Payload) error {
	if p.Version != "4" {
		return fmt.Errorf("unsupported payload version %q", p.Version)
	}
	if len(p.Alerts) == 0 {
		return errors.New("no alerts")
	}
	if len(p.Alerts) > MaxAlertsPerRequest {
		return fmt.Errorf("%d alerts, at most %d", len(p.Alerts), MaxAlertsPerRequest)
	}
	for i, a := range p.Alerts {
		if !fingerprintPattern.MatchString(a.Fingerprint) {
			return fmt.Errorf("alert %d: fingerprint %q", i, a.Fingerprint)
		}
		if a.Status != "firing" && a.Status != "resolved" {
			return fmt.Errorf("alert %d: status %q", i, a.Status)
		}
	}
	return nil
}

func AlertObjectName(fingerprint string) string { return "fa-" + fingerprint }

// BoundMap keeps the 64 smallest keys (sorted, so two replicas bound the
// same alert identically) and truncates keys and values on rune boundaries.
func BoundMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > maxMapEntries {
		keys = keys[:maxMapEntries]
	}
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[truncate(k, maxKeyBytes)] = truncate(m[k], maxValueBytes)
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
```

- [ ] **Step 4: Tests**

Run: `go test ./internal/alerting/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/alerting/payload.go internal/alerting/payload_test.go
git commit -m "feat(alerting): validate and bound Alertmanager v4 payloads"
```

---

### Task 4: Récepteur HTTP

**Files:**
- Create: `internal/alerting/receiver.go`
- Create: `internal/alerting/metrics.go`
- Test: `internal/alerting/receiver_test.go`

**Interfaces:**
- Consumes: `Payload`, `Validate`, `BoundMap`, `AlertObjectName`, `MaxBodyBytes` (Task 3) ; `framev1beta1.FrameAlert`, `AlertStateFiring/Resolved` (Task 1).
- Produces:
  - `type Receiver struct { Client client.Client; TokenReader client.Reader; Namespace, TokenSecret, Addr string; Now func() time.Time; Log logr.Logger }`
  - `func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request)`
  - `func (r *Receiver) Start(ctx context.Context) error`, `func (r *Receiver) NeedLeaderElection() bool` (→ `false`)
  - `const lastReceivedRefresh = 15 * time.Minute`
  - métriques `receiverRequests *prometheus.CounterVec` (`code`), `alertsReceived *prometheus.CounterVec` (`state`) enregistrées dans `metrics.Registry` ; `deliveries *prometheus.CounterVec` (`subscription`,`result`), `pendingDeliveries *prometheus.GaugeVec` (`subscription`) déclarées ici pour les tâches 6-7.

- [ ] **Step 1: Tests (échouent)**

```go
package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"github.com/go-logr/logr"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

const ns = "frame-system"

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := framev1beta1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func tokenSecret(value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "frame-alert-receiver-token", Namespace: ns},
		Data:       map[string][]byte{"token": []byte(value)},
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newReceiver(t *testing.T, funcs *interceptor.Funcs, objs ...client.Object) (*Receiver, client.Client, *clock) {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlert{}).WithObjects(objs...)
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	c := b.Build()
	ck := &clock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	return &Receiver{Client: c, TokenReader: c, Namespace: ns, TokenSecret: "frame-alert-receiver-token",
		Now: ck.now, Log: logr.Discard()}, c, ck
}

func post(r http.Handler, token string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	switch b := body.(type) {
	case []byte:
		buf.Write(b)
	default:
		_ = json.NewEncoder(&buf).Encode(b)
	}
	req := httptest.NewRequest(http.MethodPost, "/alertmanager", &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func firing(fp string) Payload {
	return Payload{Version: "4", Alerts: []Alert{{
		Status: "firing", Fingerprint: fp,
		Labels:   map[string]string{"alertname": "KubeCPUOvercommit", "severity": "warning", "namespace": "kube-system"},
		StartsAt: time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC),
	}}}
}

func TestReceiverRejectsMissingWrongOrUnconfiguredToken(t *testing.T) {
	r, _, _ := newReceiver(t, nil, tokenSecret("s3cret"))
	if code := post(r, "", firing("ab")).Code; code != http.StatusUnauthorized {
		t.Errorf("no token: got %d", code)
	}
	if code := post(r, "wrong", firing("ab")).Code; code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d", code)
	}
	unconfigured, _, _ := newReceiver(t, nil)
	if code := post(unconfigured, "anything", firing("ab")).Code; code != http.StatusUnauthorized {
		t.Errorf("secret absent: got %d", code)
	}
	empty, _, _ := newReceiver(t, nil, tokenSecret(""))
	if code := post(empty, "", firing("ab")).Code; code != http.StatusUnauthorized {
		t.Errorf("empty secret must not match an empty bearer: got %d", code)
	}
}

func TestReceiverRejectsOtherPathsAndMethods(t *testing.T) {
	r, _, _ := newReceiver(t, nil, tokenSecret("s3cret"))
	for _, tc := range []struct{ method, path string }{{http.MethodGet, "/alertmanager"}, {http.MethodPost, "/"}} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer s3cret")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: got %d", tc.method, tc.path, rec.Code)
		}
	}
}

func TestReceiverRejectsOversizedAndInvalidBodies(t *testing.T) {
	r, _, _ := newReceiver(t, nil, tokenSecret("s3cret"))
	if code := post(r, "s3cret", bytes.Repeat([]byte("a"), MaxBodyBytes+1)).Code; code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized: got %d", code)
	}
	if code := post(r, "s3cret", []byte("{not json")).Code; code != http.StatusBadRequest {
		t.Errorf("bad json: got %d", code)
	}
	bad := firing("AB")
	if code := post(r, "s3cret", bad).Code; code != http.StatusBadRequest {
		t.Errorf("bad fingerprint: got %d", code)
	}
}

func TestReceiverCreatesAFiringAlert(t *testing.T) {
	r, c, ck := newReceiver(t, nil, tokenSecret("s3cret"))
	if code := post(r, "s3cret", firing("ab12")).Code; code != http.StatusOK {
		t.Fatalf("got %d", code)
	}
	var fa framev1beta1.FrameAlert
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "fa-ab12"}, &fa); err != nil {
		t.Fatal(err)
	}
	if fa.Spec.AlertName != "KubeCPUOvercommit" || fa.Spec.Severity != "warning" || fa.Spec.Namespace != "kube-system" {
		t.Errorf("spec not projected from labels: %+v", fa.Spec)
	}
	if fa.Status.State != framev1beta1.AlertStateFiring || !fa.Status.LastReceivedAt.Time.Equal(ck.t) {
		t.Errorf("status: %+v", fa.Status)
	}
	if fa.Spec.EndsAt != nil {
		t.Error("a firing alert carries endsAt")
	}
}

func TestReceiverResolvesThenReopens(t *testing.T) {
	r, c, _ := newReceiver(t, nil, tokenSecret("s3cret"))
	post(r, "s3cret", firing("ab12"))
	res := firing("ab12")
	res.Alerts[0].Status = "resolved"
	res.Alerts[0].EndsAt = time.Date(2026, 9, 15, 12, 5, 0, 0, time.UTC)
	post(r, "s3cret", res)

	var fa framev1beta1.FrameAlert
	key := types.NamespacedName{Namespace: ns, Name: "fa-ab12"}
	_ = c.Get(context.Background(), key, &fa)
	if fa.Status.State != framev1beta1.AlertStateResolved || fa.Spec.EndsAt == nil {
		t.Fatalf("not resolved: state=%s endsAt=%v", fa.Status.State, fa.Spec.EndsAt)
	}

	post(r, "s3cret", firing("ab12"))
	_ = c.Get(context.Background(), key, &fa)
	if fa.Status.State != framev1beta1.AlertStateFiring || fa.Spec.EndsAt != nil {
		t.Fatalf("not reopened: state=%s endsAt=%v", fa.Status.State, fa.Spec.EndsAt)
	}
}

func TestReceiverDoesNotRewriteAnIdenticalResendWithin15Minutes(t *testing.T) {
	writes := 0
	funcs := &interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, o ...client.PatchOption) error {
			writes++
			return c.Patch(ctx, obj, p, o...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, o ...client.SubResourcePatchOption) error {
			writes++
			return c.SubResource(sub).Patch(ctx, obj, p, o...)
		},
	}
	r, _, ck := newReceiver(t, funcs, tokenSecret("s3cret"))
	post(r, "s3cret", firing("ab12"))
	before := writes

	ck.t = ck.t.Add(5 * time.Minute)
	post(r, "s3cret", firing("ab12"))
	if writes != before {
		t.Fatalf("identical resend after 5 min wrote %d times", writes-before)
	}

	ck.t = ck.t.Add(11 * time.Minute)
	post(r, "s3cret", firing("ab12"))
	if writes == before {
		t.Fatal("resend after 16 min did not refresh lastReceivedAt")
	}
}

func TestReceiverBoundsLabelsBeforeWriting(t *testing.T) {
	r, c, _ := newReceiver(t, nil, tokenSecret("s3cret"))
	p := firing("ab12")
	p.Alerts[0].Annotations = map[string]string{"description": strings.Repeat("x", 10000)}
	post(r, "s3cret", p)
	var fa framev1beta1.FrameAlert
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "fa-ab12"}, &fa)
	if len(fa.Spec.Annotations["description"]) != 4096 {
		t.Fatalf("annotation not truncated: %d bytes", len(fa.Spec.Annotations["description"]))
	}
}

func TestReceiverAnswers503WhenAWriteFails(t *testing.T) {
	funcs := &interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, o ...client.CreateOption) error {
			return errors.New("etcdserver: request timed out")
		},
	}
	r, _, _ := newReceiver(t, funcs, tokenSecret("s3cret"))
	if code := post(r, "s3cret", firing("ab12")).Code; code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 so Alertmanager retries", code)
	}
}

func TestReceiverDoesNotNeedLeaderElection(t *testing.T) {
	if (&Receiver{}).NeedLeaderElection() {
		t.Fatal("receiver would only run on the leader; Alertmanager hits every replica")
	}
}
```

- [ ] **Step 2: Lancer, constater l'échec**

Run: `go test ./internal/alerting/ -run TestReceiver -v`
Expected: FAIL — `undefined: Receiver`.

- [ ] **Step 3: `metrics.go`**

```go
package alerting

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	receiverRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "frame_alert_receiver_requests_total",
		Help: "Alertmanager webhook requests, by HTTP answer.",
	}, []string{"code"})
	alertsReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "frame_alerts_received_total",
		Help: "Alerts received from Alertmanager, by state.",
	}, []string{"state"})
	deliveries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "frame_alert_deliveries_total",
		Help: "Relay attempts to subscriptions, by result (delivered, retry, permanent).",
	}, []string{"subscription", "result"})
	pendingDeliveries = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "frame_alert_pending_deliveries",
		Help: "Alerts whose current state is not yet delivered to the subscription.",
	}, []string{"subscription"})
)

func init() {
	metrics.Registry.MustRegister(receiverRequests, alertsReceived, deliveries, pendingDeliveries)
}
```

- [ ] **Step 4: `receiver.go`**

```go
package alerting

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

const lastReceivedRefresh = 15 * time.Minute

// Receiver is the Alertmanager webhook endpoint. It only records: the relay
// to tenants happens in RelayReconciler, never inside this request, so a
// tenant outage cannot fail Alertmanager's delivery.
type Receiver struct {
	Client      client.Client
	TokenReader client.Reader // mgr.GetAPIReader(): no cluster-wide Secret cache
	Namespace   string
	TokenSecret string
	Addr        string
	Now         func() time.Time
	Log         logr.Logger
}

// NeedLeaderElection is false: Alertmanager reaches whichever replica the
// Service picks, and writes are idempotent by object name.
func (r *Receiver) NeedLeaderElection() bool { return false }

func (r *Receiver) Start(ctx context.Context) error {
	srv := &http.Server{Addr: r.Addr, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-errc:
		return err
	}
}

func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	code := r.serve(req)
	receiverRequests.WithLabelValues(strconv.Itoa(code)).Inc()
	w.WriteHeader(code)
}

func (r *Receiver) serve(req *http.Request) int {
	if req.URL.Path != "/alertmanager" || req.Method != http.MethodPost {
		return http.StatusNotFound
	}
	if !r.authorized(req) {
		return http.StatusUnauthorized
	}
	var p Payload
	dec := json.NewDecoder(http.MaxBytesReader(nil, req.Body, MaxBodyBytes))
	if err := dec.Decode(&p); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return http.StatusRequestEntityTooLarge
		}
		return http.StatusBadRequest
	}
	if err := Validate(p); err != nil {
		return http.StatusBadRequest
	}
	for _, a := range p.Alerts {
		if err := r.record(req.Context(), a); err != nil {
			r.Log.Error(err, "recording alert", "fingerprint", a.Fingerprint)
			return http.StatusServiceUnavailable
		}
	}
	return http.StatusOK
}

func (r *Receiver) authorized(req *http.Request) bool {
	got, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok || got == "" {
		return false
	}
	var s corev1.Secret
	key := types.NamespacedName{Namespace: r.Namespace, Name: r.TokenSecret}
	if err := r.TokenReader.Get(req.Context(), key, &s); err != nil {
		return false
	}
	want := s.Data["token"]
	return len(want) > 0 && subtle.ConstantTimeCompare([]byte(got), want) == 1
}

func (r *Receiver) record(ctx context.Context, a Alert) error {
	now := metav1.NewTime(r.Now())
	state := framev1beta1.AlertStateFiring
	var endsAt *metav1.Time
	if a.Status == "resolved" {
		state = framev1beta1.AlertStateResolved
		t := metav1.NewTime(a.EndsAt)
		endsAt = &t
	}
	alertsReceived.WithLabelValues(state).Inc()

	spec := framev1beta1.FrameAlertSpec{
		Fingerprint:  a.Fingerprint,
		AlertName:    truncate(a.Labels["alertname"], 256),
		Severity:     truncate(a.Labels["severity"], 64),
		Namespace:    truncate(a.Labels["namespace"], 63),
		Labels:       BoundMap(a.Labels),
		Annotations:  BoundMap(a.Annotations),
		StartsAt:     metav1.NewTime(a.StartsAt),
		EndsAt:       endsAt,
		GeneratorURL: truncate(a.GeneratorURL, 2048),
	}

	var fa framev1beta1.FrameAlert
	key := types.NamespacedName{Namespace: r.Namespace, Name: AlertObjectName(a.Fingerprint)}
	err := r.Client.Get(ctx, key, &fa)
	if apierrors.IsNotFound(err) {
		fa = framev1beta1.FrameAlert{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}, Spec: spec}
		if err := r.Client.Create(ctx, &fa); err != nil {
			return err
		}
		return r.patchStatus(ctx, &fa, state, now)
	}
	if err != nil {
		return err
	}

	if !equality.Semantic.DeepEqual(fa.Spec, spec) {
		orig := fa.DeepCopy()
		fa.Spec = spec
		if err := r.Client.Patch(ctx, &fa, client.MergeFrom(orig)); err != nil {
			return err
		}
	}
	stale := fa.Status.LastReceivedAt == nil || now.Sub(fa.Status.LastReceivedAt.Time) >= lastReceivedRefresh
	if fa.Status.State != state || stale {
		return r.patchStatus(ctx, &fa, state, now)
	}
	return nil
}

// patchStatus touches only state and lastReceivedAt; deliveries belong to
// the relay and must not be sent back in this patch.
func (r *Receiver) patchStatus(ctx context.Context, fa *framev1beta1.FrameAlert, state string, now metav1.Time) error {
	orig := fa.DeepCopy()
	fa.Status.State = state
	fa.Status.LastReceivedAt = &now
	return r.Client.Status().Patch(ctx, fa, client.MergeFrom(orig))
}
```

Note : `EndsAt` d'une alerte `firing` vaut `0001-01-01` chez Alertmanager ; le code ne le lit que pour `resolved`.

- [ ] **Step 5: Tests**

Run: `go test ./internal/alerting/ -v`
Expected: PASS.

- [ ] **Step 6: Contrôle discriminant**

Remplacer temporairement `return len(want) > 0 && subtle.ConstantTimeCompare(...) == 1` par `return true` : `TestReceiverRejectsMissingWrongOrUnconfiguredToken` doit échouer. Remettre ; puis supprimer temporairement `|| stale` et la condition sur `fa.Status.State` (patcher toujours) : `TestReceiverDoesNotRewriteAnIdenticalResendWithin15Minutes` doit échouer. Remettre, relancer : PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/alerting/receiver.go internal/alerting/metrics.go internal/alerting/receiver_test.go
git commit -m "feat(alerting): Alertmanager webhook receiver writing FrameAlerts"
```

---

### Task 5: Filtre et envoi HTTP

**Files:**
- Create: `internal/alerting/filter.go`, `internal/alerting/sender.go`
- Test: `internal/alerting/filter_test.go`, `internal/alerting/sender_test.go`

**Interfaces:**
- Consumes: `Payload`, `Alert` (Task 3) ; `framev1beta1.AlertFilter`, `FrameAlert` (Task 1).
- Produces:
  - `func Matches(f framev1beta1.AlertFilter, a *framev1beta1.FrameAlert) bool`
  - `type Outcome int` avec `Delivered`, `Retry`, `Permanent`
  - `type Sender struct { HTTP *http.Client }`, `func NewSender() *Sender`
  - `func (s *Sender) Send(ctx context.Context, url, token, receiver string, a *framev1beta1.FrameAlert, state string) (Outcome, error)`
  - `func Backoff(attempts int32) time.Duration`

- [ ] **Step 1: Tests du filtre (échouent)**

```go
package alerting

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func alertNamed(name, sev, namespace string) *framev1beta1.FrameAlert {
	return &framev1beta1.FrameAlert{
		ObjectMeta: metav1.ObjectMeta{Name: "fa-ab"},
		Spec:       framev1beta1.FrameAlertSpec{Fingerprint: "ab", AlertName: name, Severity: sev, Namespace: namespace},
	}
}

func TestMatches(t *testing.T) {
	meta := framev1beta1.AlertFilter{ExcludeAlertNames: []string{"Watchdog", "InfoInhibitor"}}
	cases := []struct {
		name string
		f    framev1beta1.AlertFilter
		a    *framev1beta1.FrameAlert
		want bool
	}{
		{"empty filter takes everything", framev1beta1.AlertFilter{}, alertNamed("Watchdog", "none", ""), true},
		{"excluded name", meta, alertNamed("Watchdog", "none", ""), false},
		{"other name passes", meta, alertNamed("KubeCPUOvercommit", "warning", ""), true},
		{"severity kept", framev1beta1.AlertFilter{Severities: []string{"critical"}}, alertNamed("X", "critical", ""), true},
		{"severity dropped", framev1beta1.AlertFilter{Severities: []string{"critical"}}, alertNamed("X", "warning", ""), false},
		{"namespace kept", framev1beta1.AlertFilter{Namespaces: []string{"neura"}}, alertNamed("X", "warning", "neura"), true},
		{"namespace dropped", framev1beta1.AlertFilter{Namespaces: []string{"neura"}}, alertNamed("X", "warning", "rook-ceph"), false},
		{"namespace filter drops cluster-wide alerts", framev1beta1.AlertFilter{Namespaces: []string{"neura"}}, alertNamed("X", "warning", ""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(tc.f, tc.a); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Tests de l'envoi (échouent)**

```go
package alerting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func sampleAlert() *framev1beta1.FrameAlert {
	end := metav1.NewTime(time.Date(2026, 9, 15, 12, 5, 0, 0, time.UTC))
	return &framev1beta1.FrameAlert{Spec: framev1beta1.FrameAlertSpec{
		Fingerprint: "ab12", AlertName: "X",
		Labels:   map[string]string{"alertname": "X"},
		StartsAt: metav1.NewTime(time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC)),
		EndsAt:   &end,
	}}
}

func TestSendDeliversAnAlertmanagerV4BodyWithOneAlert(t *testing.T) {
	var got Payload
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()

	out, err := NewSender().Send(context.Background(), srv.URL, "tok", "neura", sampleAlert(), framev1beta1.AlertStateResolved)
	if out != Delivered || err != nil {
		t.Fatalf("got %v %v", out, err)
	}
	if auth != "Bearer tok" {
		t.Errorf("Authorization = %q", auth)
	}
	if got.Version != "4" || got.Receiver != "neura" || got.Status != "resolved" || len(got.Alerts) != 1 {
		t.Fatalf("payload: %+v", got)
	}
	if got.Alerts[0].Fingerprint != "ab12" || got.Alerts[0].Status != "resolved" || got.Alerts[0].EndsAt.IsZero() {
		t.Fatalf("alert: %+v", got.Alerts[0])
	}
}

func TestSendFiringOmitsEndsAtEvenIfTheObjectHasOne(t *testing.T) {
	var got Payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	_, _ = NewSender().Send(context.Background(), srv.URL, "tok", "neura", sampleAlert(), framev1beta1.AlertStateFiring)
	if got.Alerts[0].Status != "firing" || !got.Alerts[0].EndsAt.IsZero() {
		t.Fatalf("firing replay of a resolved alert: %+v", got.Alerts[0])
	}
}

func TestSendClassifiesAnswers(t *testing.T) {
	for _, tc := range []struct {
		code int
		want Outcome
	}{
		{200, Delivered}, {204, Delivered},
		{500, Retry}, {503, Retry}, {408, Retry}, {429, Retry},
		{400, Permanent}, {401, Permanent}, {404, Permanent},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code) }))
		out, _ := NewSender().Send(context.Background(), srv.URL, "tok", "neura", sampleAlert(), framev1beta1.AlertStateFiring)
		srv.Close()
		if out != tc.want {
			t.Errorf("HTTP %d: got %v, want %v", tc.code, out, tc.want)
		}
	}
}

func TestSendRefusesRedirects(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked = true
		}
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer redirector.Close()

	out, _ := NewSender().Send(context.Background(), redirector.URL, "tok", "neura", sampleAlert(), framev1beta1.AlertStateFiring)
	if out != Permanent || leaked {
		t.Fatalf("outcome %v, token leaked to redirect target: %v", out, leaked)
	}
}

func TestSendUnreachableIsRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if out, err := NewSender().Send(context.Background(), url, "tok", "neura", sampleAlert(), framev1beta1.AlertStateFiring); out != Retry || err == nil {
		t.Fatalf("got %v %v", out, err)
	}
}

func TestBackoff(t *testing.T) {
	for attempts, want := range map[int32]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 4: 40 * time.Second, 8: 10 * time.Minute, 60: 10 * time.Minute} {
		if got := Backoff(attempts); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", attempts, got, want)
		}
	}
}
```

- [ ] **Step 3: Lancer, constater l'échec**

Run: `go test ./internal/alerting/ -run 'TestMatches|TestSend|TestBackoff' -v`
Expected: FAIL — `undefined: Matches`.

- [ ] **Step 4: `filter.go`**

```go
package alerting

import (
	"slices"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// Matches applies a subscription filter. A namespace filter drops alerts
// that carry no namespace: a tenant scoped to its namespaces must not see
// cluster-wide infrastructure alerts.
func Matches(f framev1beta1.AlertFilter, a *framev1beta1.FrameAlert) bool {
	if slices.Contains(f.ExcludeAlertNames, a.Spec.AlertName) {
		return false
	}
	if len(f.Severities) > 0 && !slices.Contains(f.Severities, a.Spec.Severity) {
		return false
	}
	if len(f.Namespaces) > 0 && !slices.Contains(f.Namespaces, a.Spec.Namespace) {
		return false
	}
	return true
}
```

- [ ] **Step 5: `sender.go`**

```go
package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

type Outcome int

const (
	Delivered Outcome = iota
	Retry
	Permanent
)

func (o Outcome) String() string { return [...]string{"delivered", "retry", "permanent"}[o] }

type Sender struct{ HTTP *http.Client }

// NewSender refuses redirects: following one would send the tenant's token
// to wherever the redirect points.
func NewSender() *Sender {
	return &Sender{HTTP: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *Sender) Send(ctx context.Context, url, token, receiver string, a *framev1beta1.FrameAlert, state string) (Outcome, error) {
	body, err := json.Marshal(buildPayload(receiver, a, state))
	if err != nil {
		return Permanent, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Permanent, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return Retry, err
	}
	_ = resp.Body.Close()
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300:
		return Delivered, nil
	case c >= 500, c == http.StatusRequestTimeout, c == http.StatusTooManyRequests:
		return Retry, fmt.Errorf("HTTP %d", c)
	default:
		return Permanent, fmt.Errorf("HTTP %d", c)
	}
}

func buildPayload(receiver string, a *framev1beta1.FrameAlert, state string) Payload {
	status := "firing"
	var endsAt time.Time
	if state == framev1beta1.AlertStateResolved {
		status = "resolved"
		if a.Spec.EndsAt != nil {
			endsAt = a.Spec.EndsAt.Time
		}
	}
	return Payload{Version: "4", Status: status, Receiver: receiver, Alerts: []Alert{{
		Status: status, Fingerprint: a.Spec.Fingerprint,
		Labels: a.Spec.Labels, Annotations: a.Spec.Annotations,
		StartsAt: a.Spec.StartsAt.Time, EndsAt: endsAt, GeneratorURL: a.Spec.GeneratorURL,
	}}}
}

func Backoff(attempts int32) time.Duration {
	if attempts < 1 {
		return 0
	}
	d := 5 * time.Second
	for i := int32(1); i < attempts && d < 10*time.Minute; i++ {
		d *= 2
	}
	return min(d, 10*time.Minute)
}
```

- [ ] **Step 6: Tests**

Run: `go test ./internal/alerting/ -v`
Expected: PASS.

- [ ] **Step 7: Contrôle discriminant**

Retirer temporairement `CheckRedirect` : `TestSendRefusesRedirects` doit échouer (fuite du jeton ou `Delivered`). Déplacer temporairement `c == http.StatusUnauthorized` dans le cas `Retry` : `TestSendClassifiesAnswers` doit échouer. Remettre, relancer : PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/alerting/filter.go internal/alerting/sender.go internal/alerting/filter_test.go internal/alerting/sender_test.go
git commit -m "feat(alerting): subscription filter and tenant sender"
```

---

### Task 6: Contrôleur de relais et purge

**Files:**
- Create: `internal/alerting/relay_controller.go`
- Test: `internal/alerting/relay_controller_test.go`

**Interfaces:**
- Consumes: `Matches`, `Sender`, `Outcome`, `Backoff` (Task 5) ; `deliveries` (Task 4) ; types Task 1.
- Produces:
  - `type RelayReconciler struct { Client client.Client; TokenReader client.Reader; Namespace string; Sender *Sender; Retention time.Duration; Now func() time.Time }`
  - `func (r *RelayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)`
  - `func (r *RelayReconciler) SetupWithManager(mgr ctrl.Manager) error`

- [ ] **Step 1: Tests (échouent)**

```go
package alerting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// tenant records what it received and answers with the next scripted code.
type tenant struct {
	mu       sync.Mutex
	codes    []int
	received []string // alert statuses, in order
	srv      *httptest.Server
}

func newTenant(codes ...int) *tenant {
	tn := &tenant{codes: codes}
	tn.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Payload
		_ = json.NewDecoder(r.Body).Decode(&p)
		tn.mu.Lock()
		defer tn.mu.Unlock()
		tn.received = append(tn.received, p.Alerts[0].Status)
		code := 200
		if len(tn.codes) > 0 {
			code, tn.codes = tn.codes[0], tn.codes[1:]
		}
		w.WriteHeader(code)
	}))
	return tn
}

func subscription(name, url string, f framev1beta1.AlertFilter) *framev1beta1.FrameAlertSubscription {
	return &framev1beta1.FrameAlertSubscription{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: framev1beta1.FrameAlertSubscriptionSpec{
			URL: url, TokenSecretRef: framev1beta1.AlertTokenRef{Name: name + "-token", Key: "token"}, Filter: f,
		},
	}
}

func subToken(name string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-token", Namespace: ns},
		Data: map[string][]byte{"token": []byte("tok")}}
}

func storedAlert(state string, endsAt *metav1.Time) *framev1beta1.FrameAlert {
	return &framev1beta1.FrameAlert{
		ObjectMeta: metav1.ObjectMeta{Name: "fa-ab12", Namespace: ns},
		Spec: framev1beta1.FrameAlertSpec{Fingerprint: "ab12", AlertName: "KubeCPUOvercommit", Severity: "warning",
			StartsAt: metav1.NewTime(time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC)), EndsAt: endsAt},
		Status: framev1beta1.FrameAlertStatus{State: state},
	}
}

func newRelay(t *testing.T, objs ...client.Object) (*RelayReconciler, client.Client, *clock) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlert{}, &framev1beta1.FrameAlertSubscription{}).
		WithObjects(objs...).Build()
	ck := &clock{t: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	return &RelayReconciler{Client: c, TokenReader: c, Namespace: ns, Sender: NewSender(),
		Retention: 14 * 24 * time.Hour, Now: ck.now}, c, ck
}

func reconcileAlert(t *testing.T, r *RelayReconciler) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "fa-ab12"}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func getAlert(t *testing.T, c client.Client) *framev1beta1.FrameAlert {
	t.Helper()
	var fa framev1beta1.FrameAlert
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "fa-ab12"}, &fa); err != nil {
		t.Fatal(err)
	}
	return &fa
}

func delivery(fa *framev1beta1.FrameAlert, sub string) *framev1beta1.AlertDelivery {
	for i := range fa.Status.Deliveries {
		if fa.Status.Deliveries[i].Subscription == sub {
			return &fa.Status.Deliveries[i]
		}
	}
	return nil
}

func TestRelayDeliversAFiringAlertOnce(t *testing.T) {
	tn := newTenant()
	defer tn.srv.Close()
	r, c, _ := newRelay(t, storedAlert("Firing", nil), subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{}), subToken("neura"))

	reconcileAlert(t, r)
	reconcileAlert(t, r)

	if len(tn.received) != 1 || tn.received[0] != "firing" {
		t.Fatalf("tenant received %v", tn.received)
	}
	d := delivery(getAlert(t, c), "neura")
	if d == nil || d.DeliveredState != "Firing" || d.Attempts != 0 || d.LastDeliveredAt == nil {
		t.Fatalf("delivery: %+v", d)
	}
}

func TestRelaySendsFiringThenResolvedForAnAlertNeverDelivered(t *testing.T) {
	tn := newTenant()
	defer tn.srv.Close()
	end := metav1.NewTime(time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC))
	r, c, _ := newRelay(t, storedAlert("Resolved", &end), subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{}), subToken("neura"))

	reconcileAlert(t, r)

	if len(tn.received) != 2 || tn.received[0] != "firing" || tn.received[1] != "resolved" {
		t.Fatalf("tenant received %v, want [firing resolved]", tn.received)
	}
	if d := delivery(getAlert(t, c), "neura"); d.DeliveredState != "Resolved" {
		t.Fatalf("delivery: %+v", d)
	}
}

func TestRelayRetriesA5xxWithBackoffThenDelivers(t *testing.T) {
	tn := newTenant(503)
	defer tn.srv.Close()
	r, c, ck := newRelay(t, storedAlert("Firing", nil), subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{}), subToken("neura"))

	res := reconcileAlert(t, r)
	d := delivery(getAlert(t, c), "neura")
	if d.Attempts != 1 || d.DeliveredState != "" || d.LastError == "" || res.RequeueAfter != 5*time.Second {
		t.Fatalf("after 503: %+v requeue=%v", d, res.RequeueAfter)
	}

	ck.t = ck.t.Add(2 * time.Second)
	reconcileAlert(t, r)
	if len(tn.received) != 1 {
		t.Fatalf("retried before backoff elapsed: %v", tn.received)
	}

	ck.t = ck.t.Add(4 * time.Second)
	reconcileAlert(t, r)
	if d := delivery(getAlert(t, c), "neura"); d.DeliveredState != "Firing" || d.Attempts != 0 {
		t.Fatalf("not delivered after backoff: %+v", d)
	}
}

func TestRelayStopsOnAPermanentFailureUntilTheSubscriptionChanges(t *testing.T) {
	tn := newTenant(401)
	defer tn.srv.Close()
	sub := subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{})
	r, c, ck := newRelay(t, storedAlert("Firing", nil), sub, subToken("neura"))

	reconcileAlert(t, r)
	ck.t = ck.t.Add(time.Hour)
	reconcileAlert(t, r)
	if len(tn.received) != 1 {
		t.Fatalf("401 retried: %v", tn.received)
	}
	if d := delivery(getAlert(t, c), "neura"); !d.PermanentFailure {
		t.Fatalf("not marked permanent: %+v", d)
	}

	var s framev1beta1.FrameAlertSubscription
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "neura"}, &s)
	s.Generation = 2 // the fake client does not bump generation; the token was fixed
	_ = c.Update(context.Background(), &s)
	reconcileAlert(t, r)
	if len(tn.received) != 2 {
		t.Fatalf("generation change did not lift the failure: %v", tn.received)
	}
}

func TestRelayHoldsDeliveriesWhilePaused(t *testing.T) {
	tn := newTenant()
	defer tn.srv.Close()
	sub := subscription("neura", tn.srv.URL, framev1beta1.AlertFilter{})
	sub.Spec.Paused = true
	r, c, _ := newRelay(t, storedAlert("Firing", nil), sub, subToken("neura"))
	reconcileAlert(t, r)
	if len(tn.received) != 0 {
		t.Fatal("paused subscription received an alert")
	}
	if d := delivery(getAlert(t, c), "neura"); d == nil || d.DeliveredState != "" {
		t.Fatalf("paused delivery must stay pending: %+v", d)
	}
}

func TestRelayDropsEntriesForDeletedOrNonMatchingSubscriptions(t *testing.T) {
	fa := storedAlert("Firing", nil)
	fa.Status.Deliveries = []framev1beta1.AlertDelivery{{Subscription: "gone", DeliveredState: "Firing"}, {Subscription: "strict"}}
	strict := subscription("strict", "http://unused.invalid", framev1beta1.AlertFilter{Severities: []string{"critical"}})
	r, c, _ := newRelay(t, fa, strict, subToken("strict"))
	reconcileAlert(t, r)
	if got := getAlert(t, c).Status.Deliveries; len(got) != 0 {
		t.Fatalf("stale entries kept: %+v", got)
	}
}

func TestRelayPurgesOnlyDeliveredAlertsPastRetention(t *testing.T) {
	end := metav1.NewTime(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	pending := storedAlert("Resolved", &end)
	pending.Status.Deliveries = []framev1beta1.AlertDelivery{{Subscription: "neura", DeliveredState: "Firing", Attempts: 3,
		LastAttemptAt: &metav1.Time{Time: time.Date(2026, 9, 15, 11, 59, 59, 0, time.UTC)}}}
	sub := subscription("neura", "http://unreachable.invalid:1", framev1beta1.AlertFilter{})
	r, c, _ := newRelay(t, pending, sub, subToken("neura"))

	reconcileAlert(t, r)
	getAlert(t, c) // still there: resolution not delivered yet

	var fa framev1beta1.FrameAlert
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "fa-ab12"}, &fa)
	fa.Status.Deliveries[0].DeliveredState = "Resolved"
	_ = c.Status().Update(context.Background(), &fa)
	reconcileAlert(t, r)
	err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "fa-ab12"}, &fa)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("delivered alert past retention not purged: %v", err)
	}
}

func TestRelayRequeuesAResolvedAlertAtItsPurgeTime(t *testing.T) {
	end := metav1.NewTime(time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC))
	fa := storedAlert("Resolved", &end)
	fa.Status.Deliveries = []framev1beta1.AlertDelivery{{Subscription: "neura", DeliveredState: "Resolved"}}
	r, _, _ := newRelay(t, fa, subscription("neura", "http://unused.invalid", framev1beta1.AlertFilter{}), subToken("neura"))
	res := reconcileAlert(t, r)
	want := end.Add(14*24*time.Hour).Sub(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	if res.RequeueAfter != want {
		t.Fatalf("requeue %v, want %v", res.RequeueAfter, want)
	}
}
```

- [ ] **Step 2: Lancer, constater l'échec**

Run: `go test ./internal/alerting/ -run TestRelay -v`
Expected: FAIL — `undefined: RelayReconciler`.

- [ ] **Step 3: `relay_controller.go`**

```go
package alerting

import (
	"context"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framealerts,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framealerts/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framealertsubscriptions,verbs=get;list;watch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framealertsubscriptions/status,verbs=get;patch;update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// RelayReconciler delivers each FrameAlert's current state to the
// subscriptions whose filter accepts it, then purges it once resolved,
// delivered everywhere and past retention. Runs on the leader only.
type RelayReconciler struct {
	Client      client.Client
	TokenReader client.Reader
	Namespace   string
	Sender      *Sender
	Retention   time.Duration
	Now         func() time.Time
}

func (r *RelayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fa framev1beta1.FrameAlert
	if err := r.Client.Get(ctx, req.NamespacedName, &fa); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var subs framev1beta1.FrameAlertSubscriptionList
	if err := r.Client.List(ctx, &subs, client.InNamespace(r.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	now := r.Now()
	state := fa.Status.State
	if state == "" {
		return ctrl.Result{}, nil // the receiver has not written the state yet
	}

	existing := map[string]framev1beta1.AlertDelivery{}
	for _, d := range fa.Status.Deliveries {
		existing[d.Subscription] = d
	}
	var next []framev1beta1.AlertDelivery
	var requeue time.Duration
	for i := range subs.Items {
		sub := &subs.Items[i]
		if !Matches(sub.Spec.Filter, &fa) {
			continue // not in the filter (any more): no entry, nothing sent
		}
		d, ok := existing[sub.Name]
		if !ok {
			d = framev1beta1.AlertDelivery{Subscription: sub.Name}
		}
		if d.SubscriptionGeneration != sub.Generation {
			d.SubscriptionGeneration = sub.Generation
			d.PermanentFailure, d.FailedState = false, ""
		}
		if d.PermanentFailure && d.FailedState != state {
			d.PermanentFailure, d.FailedState = false, ""
		}
		if wait := r.deliver(ctx, sub, &fa, &d, state, now); wait > 0 && (requeue == 0 || wait < requeue) {
			requeue = wait
		}
		next = append(next, d)
	}
	sort.Slice(next, func(i, j int) bool { return next[i].Subscription < next[j].Subscription })

	if !equality.Semantic.DeepEqual(next, fa.Status.Deliveries) {
		orig := fa.DeepCopy()
		fa.Status.Deliveries = next
		// Merge patch of deliveries only: state/lastReceivedAt belong to the receiver.
		if err := r.Client.Status().Patch(ctx, &fa, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}

	if state == framev1beta1.AlertStateResolved && fa.Spec.EndsAt != nil && allDelivered(next, state) {
		purgeAt := fa.Spec.EndsAt.Add(r.Retention)
		if !now.Before(purgeAt) {
			return ctrl.Result{}, client.IgnoreNotFound(r.Client.Delete(ctx, &fa))
		}
		if wait := purgeAt.Sub(now); requeue == 0 || wait < requeue {
			requeue = wait
		}
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// deliver updates d in place and returns how long to wait before the next
// attempt (0 when nothing is due).
func (r *RelayReconciler) deliver(ctx context.Context, sub *framev1beta1.FrameAlertSubscription,
	fa *framev1beta1.FrameAlert, d *framev1beta1.AlertDelivery, state string, now time.Time) time.Duration {
	if sub.Spec.Paused || d.PermanentFailure || d.DeliveredState == state {
		return 0
	}
	if d.Attempts > 0 && d.LastAttemptAt != nil {
		if due := d.LastAttemptAt.Add(Backoff(d.Attempts)); now.Before(due) {
			return due.Sub(now)
		}
	}
	token, err := r.token(ctx, sub)
	if err != nil {
		return r.fail(sub, d, Retry, err, state, now)
	}
	sequence := []string{state}
	if d.DeliveredState == "" && state == framev1beta1.AlertStateResolved {
		// Otherwise the tenant gets a resolution for a fingerprint it never
		// saw, ignores it, and the incident exists nowhere.
		sequence = []string{framev1beta1.AlertStateFiring, state}
	}
	for _, st := range sequence {
		if d.DeliveredState == st {
			continue
		}
		out, err := r.Sender.Send(ctx, sub.Spec.URL, token, sub.Name, fa, st)
		if out != Delivered {
			return r.fail(sub, d, out, err, state, now)
		}
		deliveries.WithLabelValues(sub.Name, Delivered.String()).Inc()
		t := metav1.NewTime(now)
		d.DeliveredState, d.Attempts, d.LastError, d.LastAttemptAt, d.LastDeliveredAt = st, 0, "", &t, &t
	}
	return 0
}

func (r *RelayReconciler) fail(sub *framev1beta1.FrameAlertSubscription, d *framev1beta1.AlertDelivery,
	out Outcome, err error, state string, now time.Time) time.Duration {
	deliveries.WithLabelValues(sub.Name, out.String()).Inc()
	t := metav1.NewTime(now)
	d.LastAttemptAt = &t
	if err != nil {
		d.LastError = truncate(err.Error(), 1024)
	}
	if out == Permanent {
		d.PermanentFailure, d.FailedState = true, state
		return 0
	}
	d.Attempts++
	return Backoff(d.Attempts)
}

func (r *RelayReconciler) token(ctx context.Context, sub *framev1beta1.FrameAlertSubscription) (string, error) {
	var s corev1.Secret
	key := types.NamespacedName{Namespace: sub.Namespace, Name: sub.Spec.TokenSecretRef.Name}
	if err := r.TokenReader.Get(ctx, key, &s); err != nil {
		return "", err
	}
	v, ok := s.Data[sub.Spec.TokenSecretRef.Key]
	if !ok || len(v) == 0 {
		return "", apierrors.NewNotFound(corev1.Resource("secrets"), key.Name+"/"+sub.Spec.TokenSecretRef.Key)
	}
	return string(v), nil
}

func allDelivered(ds []framev1beta1.AlertDelivery, state string) bool {
	for _, d := range ds {
		if d.DeliveredState != state {
			return false
		}
	}
	return true
}

func (r *RelayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// ponytail: a subscription change re-enqueues every alert in the namespace
	// (hundreds at most). Narrow to Firing + pending if that ever shows up in
	// reconcile latency.
	allAlerts := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list framev1beta1.FrameAlertList
		if err := mgr.GetClient().List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for _, a := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
		}
		return reqs
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("framealert-relay").
		For(&framev1beta1.FrameAlert{}).
		Watches(&framev1beta1.FrameAlertSubscription{}, allAlerts).
		Complete(r)
}
```

- [ ] **Step 4: Tests**

Run: `go test ./internal/alerting/ -v`
Expected: PASS.

- [ ] **Step 5: Contrôle discriminant**

Trois cassures temporaires, une à la fois, chacune doit faire échouer le test indiqué, puis être retirée :
1. `sequence := []string{state}` sans la branche firing+resolved → `TestRelaySendsFiringThenResolvedForAnAlertNeverDelivered`.
2. Retirer `&& allDelivered(next, state)` → `TestRelayPurgesOnlyDeliveredAlertsPastRetention`.
3. Dans `fail`, supprimer le bloc `if out == Permanent {…}` → `TestRelayStopsOnAPermanentFailureUntilTheSubscriptionChanges`.

Relancer après remise en état : PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/alerting/relay_controller.go internal/alerting/relay_controller_test.go
git commit -m "feat(alerting): relay FrameAlerts to subscriptions with retries and purge"
```

---

### Task 7: Statut des abonnements

**Files:**
- Create: `internal/alerting/subscription_status_controller.go`
- Test: `internal/alerting/subscription_status_controller_test.go`

**Interfaces:**
- Consumes: `Matches` (Task 5) ; `pendingDeliveries` (Task 4) ; types Task 1 ; helpers de test `newRelay`-like définis ci-dessous.
- Produces: `type SubscriptionStatusReconciler struct { Client client.Client; Namespace string; Now func() time.Time }`, `Reconcile`, `SetupWithManager`.

- [ ] **Step 1: Tests (échouent)**

```go
package alerting

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func alertWith(name, state string, ds ...framev1beta1.AlertDelivery) *framev1beta1.FrameAlert {
	return &framev1beta1.FrameAlert{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       framev1beta1.FrameAlertSpec{Fingerprint: "ab", AlertName: "X"},
		Status:     framev1beta1.FrameAlertStatus{State: state, Deliveries: ds},
	}
}

func at(min int) *metav1.Time {
	t := metav1.NewTime(time.Date(2026, 9, 15, 12, min, 0, 0, time.UTC))
	return &t
}

func TestSubscriptionStatusCountsPendingAndReportsLatestSuccessAndError(t *testing.T) {
	sub := subscription("neura", "http://x", framev1beta1.AlertFilter{})
	objs := []client.Object{
		sub,
		alertWith("fa-1", "Firing", framev1beta1.AlertDelivery{Subscription: "neura", DeliveredState: "Firing", LastDeliveredAt: at(1)}),
		alertWith("fa-2", "Resolved", framev1beta1.AlertDelivery{Subscription: "neura", DeliveredState: "Firing", LastDeliveredAt: at(3),
			LastError: "HTTP 503", LastAttemptAt: at(4)}),
		alertWith("fa-3", "Firing"), // matches, never evaluated by the relay yet: pending
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlertSubscription{}).WithObjects(objs...).Build()
	ck := &clock{t: time.Date(2026, 9, 15, 12, 10, 0, 0, time.UTC)}
	r := &SubscriptionStatusReconciler{Client: c, Namespace: ns, Now: ck.now}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "neura"}}); err != nil {
		t.Fatal(err)
	}
	var got framev1beta1.FrameAlertSubscription
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "neura"}, &got)
	if got.Status.PendingDeliveries != 2 {
		t.Errorf("pending = %d, want 2 (fa-2 resolution, fa-3 never sent)", got.Status.PendingDeliveries)
	}
	if got.Status.LastSuccessAt == nil || !got.Status.LastSuccessAt.Equal(at(3)) {
		t.Errorf("lastSuccessAt = %v, want 12:03", got.Status.LastSuccessAt)
	}
	if got.Status.LastError != "HTTP 503" {
		t.Errorf("lastError = %q", got.Status.LastError)
	}
}

func TestSubscriptionStatusIsRecomputedAtMostOncePerMinute(t *testing.T) {
	sub := subscription("neura", "http://x", framev1beta1.AlertFilter{})
	sub.Status.ComputedAt = at(10)
	sub.Status.ObservedGeneration = 1
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&framev1beta1.FrameAlertSubscription{}).WithObjects(sub, alertWith("fa-3", "Firing")).Build()
	ck := &clock{t: time.Date(2026, 9, 15, 12, 10, 20, 0, time.UTC)}
	r := &SubscriptionStatusReconciler{Client: c, Namespace: ns, Now: ck.now}

	res, _ := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "neura"}})
	var got framev1beta1.FrameAlertSubscription
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "neura"}, &got)
	if got.Status.PendingDeliveries != 0 {
		t.Fatal("recomputed within the minute")
	}
	if res.RequeueAfter != 40*time.Second {
		t.Fatalf("requeue %v, want 40s", res.RequeueAfter)
	}
}
```

- [ ] **Step 2: Lancer, constater l'échec**

Run: `go test ./internal/alerting/ -run TestSubscriptionStatus -v`
Expected: FAIL — `undefined: SubscriptionStatusReconciler`.

- [ ] **Step 3: `subscription_status_controller.go`**

```go
package alerting

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

const statusRecomputeEvery = time.Minute

// SubscriptionStatusReconciler summarises the relay records into the
// subscription's status: the health of the link, visible in the console.
type SubscriptionStatusReconciler struct {
	Client    client.Client
	Namespace string
	Now       func() time.Time
}

func (r *SubscriptionStatusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var sub framev1beta1.FrameAlertSubscription
	if err := r.Client.Get(ctx, req.NamespacedName, &sub); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	now := r.Now()
	if c := sub.Status.ComputedAt; c != nil && sub.Status.ObservedGeneration == sub.Generation {
		if wait := c.Add(statusRecomputeEvery).Sub(now); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}

	var alerts framev1beta1.FrameAlertList
	if err := r.Client.List(ctx, &alerts, client.InNamespace(r.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	var pending int32
	var lastSuccess, lastErrorAt *metav1.Time
	lastError := ""
	for i := range alerts.Items {
		a := &alerts.Items[i]
		if a.Status.State == "" || !Matches(sub.Spec.Filter, a) {
			continue
		}
		var d *framev1beta1.AlertDelivery
		for j := range a.Status.Deliveries {
			if a.Status.Deliveries[j].Subscription == sub.Name {
				d = &a.Status.Deliveries[j]
			}
		}
		if d == nil || d.DeliveredState != a.Status.State {
			pending++
		}
		if d == nil {
			continue
		}
		if d.LastDeliveredAt != nil && (lastSuccess == nil || d.LastDeliveredAt.After(lastSuccess.Time)) {
			lastSuccess = d.LastDeliveredAt
		}
		if d.LastError != "" && d.LastAttemptAt != nil && (lastErrorAt == nil || d.LastAttemptAt.After(lastErrorAt.Time)) {
			lastErrorAt, lastError = d.LastAttemptAt, d.LastError
		}
	}
	pendingDeliveries.WithLabelValues(sub.Name).Set(float64(pending))

	orig := sub.DeepCopy()
	computed := metav1.NewTime(now)
	sub.Status.ObservedGeneration = sub.Generation
	sub.Status.PendingDeliveries = pending
	sub.Status.LastSuccessAt = lastSuccess
	sub.Status.LastError = lastError
	sub.Status.ComputedAt = &computed
	if err := r.Client.Status().Patch(ctx, &sub, client.MergeFrom(orig)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: statusRecomputeEvery}, nil
}

func (r *SubscriptionStatusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	allSubs := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		var list framev1beta1.FrameAlertSubscriptionList
		if err := mgr.GetClient().List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for _, s := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: s.Namespace, Name: s.Name}})
		}
		return reqs
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("framealertsubscription-status").
		For(&framev1beta1.FrameAlertSubscription{}).
		Watches(&framev1beta1.FrameAlert{}, allSubs).
		Complete(r)
}
```

- [ ] **Step 4: Tests**

Run: `go test ./internal/alerting/ -v`
Expected: PASS.

- [ ] **Step 5: Contrôle discriminant**

Supprimer temporairement le bloc `if c := sub.Status.ComputedAt …` : `TestSubscriptionStatusIsRecomputedAtMostOncePerMinute` doit échouer. Remettre : PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/alerting/subscription_status_controller.go internal/alerting/subscription_status_controller_test.go
git commit -m "feat(alerting): summarise relay health into subscription status"
```

---

### Task 8: Câblage du manager, Service et NetworkPolicy

**Files:**
- Modify: `cmd/main.go` (flags ~l.284-310, enregistrement après le dernier `SetupWithManager` ~l.674)
- Modify: `config/manager/manager.yaml` (args + port), `config/manager/kustomization.yaml` (resources)
- Create: `config/manager/alert_receiver_service.yaml`
- Create: `deploy/kubernetes/containment/networkpolicy-frame-alert-receiver.yaml`
- Modify: `deploy/kubernetes/containment/kustomization.yaml`
- Test: `test/manifests/alert_receiver_manifests_test.go`

**Interfaces:**
- Consumes: `alerting.Receiver`, `alerting.RelayReconciler`, `alerting.SubscriptionStatusReconciler`, `alerting.NewSender` (Tasks 4-7).
- Produces: flags `--alert-receiver-bind-address` (défaut `:8445`, `0` désactive), `--alert-namespace` (défaut `frame-system`), `--alert-receiver-token-secret` (défaut `frame-alert-receiver-token`), `--alert-retention-days` (défaut `14`).

- [ ] **Step 1: Test de manifestes (échoue)**

`test/manifests/alert_receiver_manifests_test.go` :

```go
package manifests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The receiver is only reachable if three files agree on one port: the
// manager's flag, the container port the Service targets, and the
// NetworkPolicy that lets Alertmanager in. And a policy that selects the
// manager pod isolates it for every port — the conversion webhook (9443),
// metrics (8443) and probes (8081) must stay explicitly open or every CRD
// read in the cluster fails.
func TestAlertReceiverPortAgreesAcrossManifests(t *testing.T) {
	root := Root(t)
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	manager := read("config/manager/manager.yaml")
	service := read("config/manager/alert_receiver_service.yaml")
	policy := read("deploy/kubernetes/containment/networkpolicy-frame-alert-receiver.yaml")

	for name, want := range map[string]string{
		"manager flag":         "--alert-receiver-bind-address=:8445",
		"manager port":         "containerPort: 8445",
		"service target":       "targetPort: alert-receiver",
		"policy receiver port": "port: 8445",
		"policy webhook port":  "port: 9443",
		"policy metrics port":  "port: 8443",
		"policy probe port":    "port: 8081",
	} {
		haystack := map[string]string{
			"manager flag": manager, "manager port": manager, "service target": service,
			"policy receiver port": policy, "policy webhook port": policy,
			"policy metrics port": policy, "policy probe port": policy,
		}[name]
		if !strings.Contains(haystack, want) {
			t.Errorf("%s: %q missing", name, want)
		}
	}
}
```

- [ ] **Step 2: Lancer, constater l'échec**

Run: `go test ./test/manifests/ -run TestAlertReceiverPortAgreesAcrossManifests -v`
Expected: FAIL — fichier `alert_receiver_service.yaml` introuvable.

- [ ] **Step 3: Flags et enregistrement dans `cmd/main.go`**

Avec les autres déclarations de flags :

```go
	var alertReceiverAddr, alertNamespace, alertTokenSecret string
	var alertRetentionDays int
	flag.StringVar(&alertReceiverAddr, "alert-receiver-bind-address", ":8445",
		"Address of the Alertmanager webhook receiver. Set to 0 to disable it.")
	flag.StringVar(&alertNamespace, "alert-namespace", "frame-system",
		"Namespace holding FrameAlerts, FrameAlertSubscriptions and their token Secrets.")
	flag.StringVar(&alertTokenSecret, "alert-receiver-token-secret", "frame-alert-receiver-token",
		"Secret (key `token`) Alertmanager must present as a bearer token.")
	flag.IntVar(&alertRetentionDays, "alert-retention-days", 14,
		"Days a resolved, fully delivered FrameAlert is kept before purge.")
```

Import : `"github.com/rmocq/frame/internal/alerting"`. Après le dernier bloc `SetupWithManager` et avant `mgr.AddHealthzCheck` :

```go
	if err := (&alerting.RelayReconciler{
		Client: mgr.GetClient(), TokenReader: mgr.GetAPIReader(), Namespace: alertNamespace,
		Sender: alerting.NewSender(), Retention: time.Duration(alertRetentionDays) * 24 * time.Hour, Now: time.Now,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "framealert-relay")
		os.Exit(1)
	}
	if err := (&alerting.SubscriptionStatusReconciler{
		Client: mgr.GetClient(), Namespace: alertNamespace, Now: time.Now,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "framealertsubscription-status")
		os.Exit(1)
	}
	if alertReceiverAddr != "0" {
		if err := mgr.Add(&alerting.Receiver{
			Client: mgr.GetClient(), TokenReader: mgr.GetAPIReader(), Namespace: alertNamespace,
			TokenSecret: alertTokenSecret, Addr: alertReceiverAddr, Now: time.Now,
			Log: ctrl.Log.WithName("alert-receiver"),
		}); err != nil {
			setupLog.Error(err, "Failed to add the alert receiver")
			os.Exit(1)
		}
	}
```

(`time` est-il déjà importé dans `main.go` ? Vérifier avec `grep -n '"time"' cmd/main.go` ; l'ajouter sinon.)

- [ ] **Step 4: Manifestes**

`config/manager/manager.yaml` — ajouter à `args` :

```yaml
          - --alert-receiver-bind-address=:8445
```

et à `ports` :

```yaml
        - containerPort: 8445
          name: alert-receiver
          protocol: TCP
```

`config/manager/alert_receiver_service.yaml` :

```yaml
apiVersion: v1
kind: Service
metadata:
  name: frame-alert-receiver
  namespace: system
  labels:
    app.kubernetes.io/name: frame
spec:
  selector:
    control-plane: controller-manager
    app.kubernetes.io/name: frame
  ports:
    - name: http
      port: 8445
      targetPort: alert-receiver
      protocol: TCP
```

(`namespace: system` et le préfixe de nom sont réécrits par `config/default` comme pour les autres objets : vérifier au Step 5 que le rendu donne `frame-system/frame-alert-receiver`. Si `namePrefix: frame-` produit `frame-frame-alert-receiver`, nommer l'objet `alert-receiver` ici.)

`config/manager/kustomization.yaml` :

```yaml
resources:
- manager.yaml
- alert_receiver_service.yaml
```

`deploy/kubernetes/containment/networkpolicy-frame-alert-receiver.yaml` :

```yaml
# Alertmanager may reach the manager's alert receiver (8445); nothing else may.
#
# Selecting the manager pod isolates it for EVERY port, so the ports that
# already worked stay open to any source, exactly as before this policy:
# 9443 (conversion webhook — the apiserver calls it on every read of a
# converted CRD; closing it breaks the whole cluster's view of Frame objects),
# 8443 (metrics) and 8081 (kubelet probes). Only 8445 is narrowed.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: frame-alert-receiver
  namespace: frame-system
  labels:
    app.kubernetes.io/part-of: cluster-control-containment
spec:
  podSelector:
    matchLabels:
      control-plane: controller-manager
      app.kubernetes.io/name: frame
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring
          podSelector:
            matchLabels:
              app.kubernetes.io/name: alertmanager
      ports:
        - protocol: TCP
          port: 8445
    - ports:
        - protocol: TCP
          port: 9443
        - protocol: TCP
          port: 8443
        - protocol: TCP
          port: 8081
```

Dans `deploy/kubernetes/containment/kustomization.yaml`, ajouter `- networkpolicy-frame-alert-receiver.yaml` aux `resources`.

- [ ] **Step 5: Tests, build, rendu**

Run: `make test && make build && kustomize build config/default | grep -A3 'kind: Service' | grep -E 'name: .*alert' && kustomize build deploy/kubernetes/containment | grep 'name: frame-alert-receiver'`
Expected: PASS ; le Service rendu s'appelle `frame-alert-receiver` dans `frame-system` (sinon appliquer la note du Step 4 et relancer) ; la NetworkPolicy apparaît.

- [ ] **Step 6: Commit**

```bash
git add cmd/main.go config/manager deploy/kubernetes/containment test/manifests/alert_receiver_manifests_test.go config/rbac/role.yaml
git commit -m "feat(alerting): run the alert receiver and relay in the manager"
```

---

### Task 9: Console — registre et abonnements

**Files:**
- Create: `src/lib/alert-registry.ts`, `src/lib/alert-registry.test.ts`
- Modify: `src/lib/frame-sdk.ts` (deux méthodes dans le client `cluster`, à côté de `alerts()`)
- Modify: `src/components/AlertsView.tsx`

**Interfaces:**
- Consumes: CRD `framealerts`, `framealertsubscriptions` dans `config().taskNamespace` (= `frame-system`, déjà le namespace opérationnel de Frame).
- Produces:
  - `export type RegistryAlert = { name, fingerprint, alertName, severity, namespace, state: 'Firing'|'Resolved', startsAt, endsAt, deliveries: { subscription, delivered: boolean, lastError: string, permanent: boolean }[] }`
  - `export type AlertSubscriptionHealth = { name, url, paused, pending, lastSuccessAt, lastError }`
  - `export function projectAlerts(items: unknown[]): RegistryAlert[]`, `export function projectSubscriptions(items: unknown[]): AlertSubscriptionHealth[]`
  - SDK : `frame.cluster.alertRegistry(): Promise<RegistryAlert[]>`, `frame.cluster.alertSubscriptions(): Promise<AlertSubscriptionHealth[]>`, `alertRegistryPath()`, `alertSubscriptionsPath()`.

- [ ] **Step 1: Tests (échouent)**

```ts
import { describe, it, expect } from 'vitest'
import { projectAlerts, projectSubscriptions } from './alert-registry'

describe('projectAlerts', () => {
  const raw = {
    metadata: { name: 'fa-ab12' },
    spec: { fingerprint: 'ab12', alertName: 'KubeCPUOvercommit', severity: 'warning', startsAt: '2026-09-15T11:00:00Z' },
    status: {
      state: 'Firing',
      deliveries: [
        { subscription: 'neura', deliveredState: 'Firing' },
        { subscription: 'other', deliveredState: '', lastError: 'HTTP 401', permanentFailure: true },
      ],
    },
  }

  it('marks a delivery delivered only when its state matches the alert state', () => {
    const [a] = projectAlerts([raw])
    expect(a.deliveries).toEqual([
      { subscription: 'neura', delivered: true, lastError: '', permanent: false },
      { subscription: 'other', delivered: false, lastError: 'HTTP 401', permanent: true },
    ])
  })

  it('sorts firing before resolved, then newest first', () => {
    const resolved = { ...raw, metadata: { name: 'fa-old' }, status: { state: 'Resolved' } }
    const newer = { ...raw, metadata: { name: 'fa-new' }, spec: { ...raw.spec, startsAt: '2026-09-15T11:30:00Z' } }
    expect(projectAlerts([resolved, raw, newer]).map((a) => a.name)).toEqual(['fa-new', 'fa-ab12', 'fa-old'])
  })

  it('skips objects the receiver has not given a state yet', () => {
    expect(projectAlerts([{ ...raw, status: {} }])).toEqual([])
  })
})

describe('projectSubscriptions', () => {
  it('projects the health fields with safe defaults', () => {
    expect(
      projectSubscriptions([
        { metadata: { name: 'neura' }, spec: { url: 'http://n' }, status: { pendingDeliveries: 2, lastError: 'HTTP 503' } },
      ]),
    ).toEqual([{ name: 'neura', url: 'http://n', paused: false, pending: 2, lastSuccessAt: '', lastError: 'HTTP 503' }])
  })
})
```

- [ ] **Step 2: Lancer, constater l'échec**

Run: `npx vitest run src/lib/alert-registry.test.ts`
Expected: FAIL — module introuvable.

- [ ] **Step 3: `src/lib/alert-registry.ts`**

```ts
/**
 * Console projection of the alert registry (FrameAlert) and of subscription
 * health (FrameAlertSubscription). Pure, so the screen's reading of the
 * objects is testable without an apiserver.
 */

export type AlertDeliveryView = { subscription: string; delivered: boolean; lastError: string; permanent: boolean }

export type RegistryAlert = {
  name: string
  fingerprint: string
  alertName: string
  severity: string
  namespace: string
  state: 'Firing' | 'Resolved'
  startsAt: string
  endsAt: string
  deliveries: AlertDeliveryView[]
}

export type AlertSubscriptionHealth = {
  name: string
  url: string
  paused: boolean
  pending: number
  lastSuccessAt: string
  lastError: string
}

type RawAlert = {
  metadata?: { name?: string }
  spec?: { fingerprint?: string; alertName?: string; severity?: string; namespace?: string; startsAt?: string; endsAt?: string }
  status?: {
    state?: string
    deliveries?: { subscription?: string; deliveredState?: string; lastError?: string; permanentFailure?: boolean }[]
  }
}

export function projectAlerts(items: unknown[]): RegistryAlert[] {
  return (items as RawAlert[])
    .filter((i) => i.status?.state === 'Firing' || i.status?.state === 'Resolved')
    .map((i) => {
      const state = i.status!.state as 'Firing' | 'Resolved'
      return {
        name: i.metadata?.name ?? '',
        fingerprint: i.spec?.fingerprint ?? '',
        alertName: i.spec?.alertName ?? 'Unknown',
        severity: i.spec?.severity || 'none',
        namespace: i.spec?.namespace ?? '',
        state,
        startsAt: i.spec?.startsAt ?? '',
        endsAt: i.spec?.endsAt ?? '',
        deliveries: (i.status?.deliveries ?? []).map((d) => ({
          subscription: d.subscription ?? '',
          delivered: d.deliveredState === state,
          lastError: d.lastError ?? '',
          permanent: d.permanentFailure ?? false,
        })),
      }
    })
    .sort((a, b) => (a.state === b.state ? b.startsAt.localeCompare(a.startsAt) : a.state === 'Firing' ? -1 : 1))
}

type RawSubscription = {
  metadata?: { name?: string }
  spec?: { url?: string; paused?: boolean }
  status?: { pendingDeliveries?: number; lastSuccessAt?: string; lastError?: string }
}

export function projectSubscriptions(items: unknown[]): AlertSubscriptionHealth[] {
  return (items as RawSubscription[]).map((s) => ({
    name: s.metadata?.name ?? '',
    url: s.spec?.url ?? '',
    paused: s.spec?.paused ?? false,
    pending: s.status?.pendingDeliveries ?? 0,
    lastSuccessAt: s.status?.lastSuccessAt ?? '',
    lastError: s.status?.lastError ?? '',
  }))
}
```

- [ ] **Step 4: SDK (`src/lib/frame-sdk.ts`)**

Près de `taskListPath()` :

```ts
/** FrameAlerts live beside FrameTasks, in Frame's operational namespace. */
export function alertRegistryPath(): string {
  return frameListPath('framealerts', config().taskNamespace)
}

export function alertSubscriptionsPath(): string {
  return frameListPath('framealertsubscriptions', config().taskNamespace)
}
```

Dans le client `cluster`, juste après `alerts()` :

```ts
  /** The alert registry (FrameAlert), active and resolved, with relay state. */
  async alertRegistry(): Promise<RegistryAlert[]> {
    const list = await k8sFetch<ListResponse<unknown>>(alertRegistryPath())
    return projectAlerts(list.items ?? [])
  }

  /** Tenant subscriptions and the health of their relay link. */
  async alertSubscriptions(): Promise<AlertSubscriptionHealth[]> {
    const list = await k8sFetch<ListResponse<unknown>>(alertSubscriptionsPath())
    return projectSubscriptions(list.items ?? [])
  }
```

Import en tête : `import { projectAlerts, projectSubscriptions, type RegistryAlert, type AlertSubscriptionHealth } from './alert-registry'`. Réexporter les deux types avec les autres exports de types du fichier.

- [ ] **Step 5: `AlertsView.tsx`**

Ajouter, dans le composant, sous les deux `useLiveResource` existants :

```tsx
  const { state: registryState } = useLiveResource<RegistryAlert[]>(
    () => frame.cluster.alertRegistry(),
    [],
    [alertRegistryPath()],
  )
  const { state: subsState } = useLiveResource<AlertSubscriptionHealth[]>(
    () => frame.cluster.alertSubscriptions(),
    [],
    [alertSubscriptionsPath()],
  )
  const [view, setView] = useState<'Firing' | 'Resolved'>('Firing')
  const registry = registryState.phase === 'ready' ? registryState.data : []
  const subscriptions = subsState.phase === 'ready' ? subsState.data : []
```

Et rendre, après la carte des alertes actives existante, ces deux cartes :

```tsx
      <Card>
        <CardHeader className="flex flex-row items-center gap-2">
          <CardTitle className="font-mono text-base">Registry</CardTitle>
          <div className="ml-auto flex gap-1">
            {(['Firing', 'Resolved'] as const).map((v) => (
              <Button key={v} size="sm" variant={view === v ? 'default' : 'outline'} onClick={() => setView(v)}>
                {v === 'Firing' ? 'Active' : 'History'}
              </Button>
            ))}
          </div>
        </CardHeader>
        <CardContent className="space-y-2">
          {registry.filter((a) => a.state === view).length === 0 ? (
            <p className="text-sm font-mono text-muted-foreground">
              {view === 'Firing' ? 'No active alert in the registry.' : 'No resolved alert within retention.'}
            </p>
          ) : (
            registry
              .filter((a) => a.state === view)
              .map((a) => (
                <div key={a.name} className="flex items-center gap-3 p-2 rounded border border-border font-mono text-xs">
                  <span className={TONE[a.severity] ?? TONE.none}>{a.alertName}</span>
                  <span className="text-muted-foreground">{a.namespace || 'cluster'}</span>
                  <span className="ml-auto flex gap-1">
                    {a.deliveries.map((d) => (
                      <Badge
                        key={d.subscription}
                        variant="outline"
                        className={d.delivered ? 'text-accent' : d.permanent ? 'text-destructive' : 'text-warning'}
                        title={d.lastError || undefined}
                      >
                        {d.subscription} {d.delivered ? '✓' : d.permanent ? '✗' : '…'}
                      </Badge>
                    ))}
                  </span>
                </div>
              ))
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-base">Subscriptions</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          {subscriptions.length === 0 ? (
            <p className="text-sm font-mono text-muted-foreground">No tenant receives the cluster's alerts.</p>
          ) : (
            subscriptions.map((s) => (
              <div key={s.name} className="flex items-center gap-3 p-2 rounded border border-border font-mono text-xs">
                <span className="font-bold">{s.name}</span>
                <span className="text-muted-foreground truncate">{s.url}</span>
                {s.paused && <Badge variant="outline">paused</Badge>}
                <span className="ml-auto">pending {s.pending}</span>
                <span className="text-muted-foreground">{s.lastSuccessAt ? `last ok ${s.lastSuccessAt}` : 'never delivered'}</span>
                {s.lastError && <span className="text-destructive">{s.lastError}</span>}
              </div>
            ))
          )}
        </CardContent>
      </Card>
```

Imports à ajouter : `RegistryAlert`, `AlertSubscriptionHealth`, `alertRegistryPath`, `alertSubscriptionsPath` depuis `@/lib/frame-sdk`.

- [ ] **Step 6: Tests et build**

Run: `npx vitest run && npm run build`
Expected: PASS ; build sans erreur TypeScript.

- [ ] **Step 7: Commit**

```bash
git add src/lib/alert-registry.ts src/lib/alert-registry.test.ts src/lib/frame-sdk.ts src/components/AlertsView.tsx
git commit -m "feat(console): alert registry history and subscription health"
```

---

### Task 10: Échantillons de déploiement et routage Alertmanager

**Files:**
- Create: `deploy/samples/test-cluster/frame-alert-subscription-neura.yaml`
- Modify: `deploy/samples/test-cluster/kps-values.yaml`
- Create: `deploy/samples/test-cluster/check-alert-routes.sh`

**Interfaces:**
- Consumes: Service `frame-system/frame-alert-receiver:8445` (Task 8), type `FrameAlertSubscription` (Task 1).
- Produces: rien pour le code.

- [ ] **Step 1: Script de vérification du routage (échoue : config pas encore modifiée)**

`deploy/samples/test-cluster/check-alert-routes.sh` :

```bash
#!/usr/bin/env bash
# Verifies the Alertmanager routing in kps-values.yaml with amtool, offline.
# Every alert must reach Frame's receiver; critical ones go through the
# `critical` receiver, which also points at Frame.
set -euo pipefail
cd "$(dirname "$0")"
command -v amtool >/dev/null || { echo "amtool missing (github.com/prometheus/alertmanager releases)" >&2; exit 1; }
tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
python3 -c 'import sys,yaml; yaml.safe_dump(yaml.safe_load(open("kps-values.yaml"))["alertmanager"]["config"], sys.stdout)' \
  | sed 's#/etc/alertmanager/secrets/frame-alert-receiver-token/token#/etc/hostname#' > "$tmp"
amtool check-config "$tmp" >/dev/null
fail=0
check() { # expected-receivers label=value...
  local want=$1; shift
  local got; got=$(amtool config routes test --config.file="$tmp" "$@" | tail -1)
  if [[ "$got" == "$want" ]]; then echo "ok   $* -> $got"; else echo "FAIL $* -> $got (want $want)"; fail=1; fi
}
check critical severity=critical alertname=CephHealthError
check default severity=warning alertname=KubeCPUOvercommit
check default alertname=Watchdog
grep -q 'frame-alert-receiver.frame-system.svc' "$tmp" || { echo "FAIL receivers do not point at Frame"; fail=1; }
grep -q 'alert-sink' "$tmp" && { echo "FAIL alert-sink still configured"; fail=1; }
exit $fail
```

Run: `chmod +x deploy/samples/test-cluster/check-alert-routes.sh && deploy/samples/test-cluster/check-alert-routes.sh`
Expected: FAIL — `receivers do not point at Frame` et `alert-sink still configured`.

- [ ] **Step 2: Modifier `kps-values.yaml`**

Remplacer le bloc `receivers:` (les deux `alert-sink` et le commentaire Slack restent en commentaire) par :

```yaml
    receivers:
      # Frame's alert receiver records every alert (FrameAlert) and relays it
      # to tenant subscriptions — see docs/superpowers/specs/2026-09-15-alert-relay-design.md.
      # `critical` stays a separate receiver so critical routing (grouping,
      # future fan-out) can diverge without touching the default path.
      - name: default
        webhook_configs:
          - url: http://frame-alert-receiver.frame-system.svc:8445/alertmanager
            send_resolved: true
            max_alerts: 100
            http_config:
              authorization:
                credentials_file: /etc/alertmanager/secrets/frame-alert-receiver-token/token
      - name: critical
        webhook_configs:
          - url: http://frame-alert-receiver.frame-system.svc:8445/alertmanager
            send_resolved: true
            max_alerts: 100
            http_config:
              authorization:
                credentials_file: /etc/alertmanager/secrets/frame-alert-receiver-token/token
      # - name: critical
      #   slack_configs:
      #     - api_url_file: /etc/alertmanager/secrets/slack/url   # from a Secret
      #       channel: '#alerts'
      #       send_resolved: true
```

Et sous `alertmanagerSpec:` :

```yaml
    secrets: [frame-alert-receiver-token]   # mounted under /etc/alertmanager/secrets/
```

- [ ] **Step 3: Abonnement Neura**

`deploy/samples/test-cluster/frame-alert-subscription-neura.yaml` :

```yaml
# Neura receives every cluster alert except the two meta alerts (defaulted).
# The token is the one Neura already expects in neura-neura-secret
# (IT_ALERT_WEBHOOK_TOKEN); it is sealed into frame-system/neura-alert-webhook.
apiVersion: frame.plume-labs.io/v1beta1
kind: FrameAlertSubscription
metadata:
  name: neura
  namespace: frame-system
spec:
  url: http://neura-neura-api.neura.svc.cluster.local:3000/api/it/alerts/webhook
  tokenSecretRef:
    name: neura-alert-webhook
    key: token
```

- [ ] **Step 4: Vérifier**

Run: `deploy/samples/test-cluster/check-alert-routes.sh`
Expected: trois `ok`, exit 0.

- [ ] **Step 5: Commit**

```bash
git add deploy/samples/test-cluster/kps-values.yaml deploy/samples/test-cluster/frame-alert-subscription-neura.yaml deploy/samples/test-cluster/check-alert-routes.sh
git commit -m "feat(monitoring): route Alertmanager to Frame's receiver; Neura subscription sample"
```

---

### Task 11: Déploiement sur le cluster de test et preuve

Tâche d'exploitation, **exécutée par la session principale** (pas par un sous-agent) : elle écrit sur le cluster partagé. Chaque étape a sa vérification ; s'arrêter à la première qui échoue.

**Files:**
- Modify: `config/manager/kustomization.yaml` (`newTag` = sha du commit de Task 10)
- Create: `deploy/samples/test-cluster/frame-alert-receiver-token.sealed.yaml`, `deploy/samples/test-cluster/frame-alert-receiver-token.monitoring.sealed.yaml`, `deploy/samples/test-cluster/neura-alert-webhook.frame-system.sealed.yaml`

- [ ] **Step 1: Image**

```bash
export KUBECONFIG=~/Neura/.test-cluster/kubeconfig-neura-test.yaml
SHA=$(git rev-parse --short HEAD)
make docker-build docker-push IMG=192.168.2.201:30500/frame-controller:$SHA
```

Expected: push OK. Mettre `newTag: <SHA>` dans `config/manager/kustomization.yaml`.

- [ ] **Step 2: Jetons scellés (valeurs jamais affichées)**

```bash
umask 077
RT=$(openssl rand -hex 32)
kubectl create secret generic frame-alert-receiver-token -n frame-system --dry-run=client -o yaml --from-literal=token="$RT" \
  | kubeseal --controller-namespace kube-system --format yaml > deploy/samples/test-cluster/frame-alert-receiver-token.sealed.yaml
kubectl create secret generic frame-alert-receiver-token -n monitoring --dry-run=client -o yaml --from-literal=token="$RT" \
  | kubeseal --controller-namespace kube-system --format yaml > deploy/samples/test-cluster/frame-alert-receiver-token.monitoring.sealed.yaml
NT=$(kubectl -n neura get secret neura-neura-secret -o jsonpath='{.data.IT_ALERT_WEBHOOK_TOKEN}' | base64 -d)
# Labelled frame.plume-labs.io/alert-token=true: this is the Secret the relay
# reads via tokenSecretRef, and it refuses to send using any Secret without
# the label (I1 — a frame-admin has no Secret access, so an unlabelled Secret
# could otherwise be used to exfiltrate one it does not own).
# frame-alert-receiver-token above must NOT carry this label: the receiver
# reads it directly, not through a subscription.
kubectl create secret generic neura-alert-webhook -n frame-system --dry-run=client -o yaml --from-literal=token="$NT" \
  | kubectl label --local -f - frame.plume-labs.io/alert-token=true -o yaml \
  | kubeseal --controller-namespace kube-system --format yaml > deploy/samples/test-cluster/neura-alert-webhook.frame-system.sealed.yaml
unset RT NT
kubectl apply -f deploy/samples/test-cluster/frame-alert-receiver-token.sealed.yaml \
  -f deploy/samples/test-cluster/frame-alert-receiver-token.monitoring.sealed.yaml \
  -f deploy/samples/test-cluster/neura-alert-webhook.frame-system.sealed.yaml
kubectl -n monitoring delete sealedsecret neura-alert-webhook --ignore-not-found
```

Vérification (empreintes, pas valeurs) :

```bash
h() { kubectl -n "$1" get secret "$2" -o json | jq -r ".data[\"$3\"]|@base64d" | sha256sum | cut -c1-12; }
echo "receiver: $(h frame-system frame-alert-receiver-token token) = $(h monitoring frame-alert-receiver-token token)"
echo "neura:    $(h frame-system neura-alert-webhook token) = $(h neura neura-neura-secret IT_ALERT_WEBHOOK_TOKEN)"
```

Expected: deux paires identiques.

- [ ] **Step 3: CRD, RBAC, manager, NetworkPolicy**

Même méthode que le 2026-09-15 (rendu `config/default`, **sans** `frame-provisiond`) :

```bash
kustomize build config/default | python3 -c '
import sys, yaml
docs = [d for d in yaml.safe_load_all(sys.stdin) if d and "provisiond" not in d["metadata"]["name"]]
yaml.safe_dump_all(docs, sys.stdout, sort_keys=False)' > /tmp/frame-default.yaml
grep -c "^kind:" /tmp/frame-default.yaml   # non nul ; aucun objet frame-provisiond ne doit rester
kubectl diff -f /tmp/frame-default.yaml | grep '^[-+]' | grep -v -E 'generation|resourceVersion|managedFields' | head -60
```

Relire le diff : attendu = 2 CRD nouvelles, ClusterRoles (nouveaux rôles + règles `framealerts`), Deployment (image, arg, port), Service `frame-alert-receiver`. **Tout autre changement : arrêter.** Puis :

```bash
kubectl apply -f /tmp/frame-default.yaml
kubectl apply -f deploy/kubernetes/containment/networkpolicy-frame-alert-receiver.yaml
kubectl -n frame-system rollout status deploy/frame-controller-manager --timeout=300s
kubectl -n frame-system logs deploy/frame-controller-manager --since=2m | grep -i -E 'error|alert' | head
```

Expected: rollout OK, pas d'erreur. Contrôle que la NetworkPolicy n'a pas coupé la conversion : `kubectl get framejobs.v1alpha1.frame.plume-labs.io -A | head -3` répond.

- [ ] **Step 4: Contrôle discriminant du récepteur, en réel**

Trois sondes, chacune avec son résultat attendu :

```bash
probe() { # namespace [labels]
  kubectl -n "$1" run probe-$RANDOM --rm -i --restart=Never --image=curlimages/curl:8.10.1 --quiet ${2:+--labels=$2} -- \
    curl -s -m 5 -o /dev/null -w '%{http_code}\n' -X POST http://frame-alert-receiver.frame-system.svc:8445/alertmanager -d '{}' \
    || echo blocked
}
probe monitoring app.kubernetes.io/name=alertmanager   # attendu : 401 (autorisé par le réseau, refusé sans jeton)
probe monitoring                                       # attendu : blocked (mauvais pod, même namespace)
probe default app.kubernetes.io/name=alertmanager      # attendu : blocked (bon label, mauvais namespace)
```

Expected: `401`, `blocked`, `blocked`. Un `401` sur l'une des deux dernières veut dire que la NetworkPolicy ne filtre pas (kube-router absent ou policy non appliquée) : arrêter.

- [ ] **Step 5: Abonnement puis Alertmanager**

```bash
kubectl apply -f deploy/samples/test-cluster/frame-alert-subscription-neura.yaml
deploy/samples/test-cluster/check-alert-routes.sh
helm -n monitoring get values kps -o yaml > /tmp/kps-live.yaml
python3 - <<'EOF'
import yaml
live = yaml.safe_load(open('/tmp/kps-live.yaml'))
live['alertmanager'] = yaml.safe_load(open('deploy/samples/test-cluster/kps-values.yaml'))['alertmanager']
yaml.safe_dump(live, open('/tmp/kps-new.yaml', 'w'), sort_keys=False)
EOF
helm template kps prometheus-community/kube-prometheus-stack --version 87.19.1 -n monitoring -f /tmp/kps-live.yaml > /tmp/r1.yaml
helm template kps prometheus-community/kube-prometheus-stack --version 87.19.1 -n monitoring -f /tmp/kps-new.yaml > /tmp/r2.yaml
diff /tmp/r1.yaml /tmp/r2.yaml | grep '^[<>]' | grep -v -E 'admin-password|checksum/secret'
```

Expected: le diff ne porte que sur `alertmanager.yaml` (base64) et `secrets: [frame-alert-receiver-token]`. Puis :

```bash
helm upgrade kps prometheus-community/kube-prometheus-stack -n monitoring --version 87.19.1 -f /tmp/kps-new.yaml --wait --timeout 8m
```

Si le mode auto refuse `helm upgrade`, donner la commande à l'utilisateur telle quelle et attendre qu'il confirme.

- [ ] **Step 6: Preuve — en base**

Attendre ~6 min (`group_wait` 30 s + `group_interval` 5 min), puis :

```bash
kubectl -n frame-system get framealerts
kubectl -n frame-system get framealertsubscription neura -o jsonpath='{.status}{"\n"}'
kubectl -n neura exec neura-db-0 -c postgres -- psql -U postgres -d neura -Atc \
  "select severity, left(summary,60), opened_at, closed_at from it.incidents order by opened_at desc limit 10"
```

Expected :
- une `FrameAlert` par alerte active (`KubeCPUOvercommit`, `KubeMemoryOvercommit`, …, Watchdog inclus) ;
- abonnement `neura` : `lastSuccessAt` renseigné, `pendingDeliveries: 0` ;
- `it.incidents` : une ligne par alerte active hors Watchdog/InfoInhibitor.

Puis **observer une fermeture réelle** : attendre qu'une alerte se résolve (ou créer un silence n'aide pas — un silence ne résout pas). Si aucune ne se résout dans l'heure, provoquer une alerte courte et inoffensive : `kubectl -n default run crashloop-probe --image=busybox:1.36 --restart=Always -- sh -c 'exit 1'` (déclenche `KubePodCrashLooping` en ~15 min), attendre son incident côté Neura, supprimer le pod, attendre la résolution (~5-15 min), et vérifier que `closed_at` est renseigné pour cette ligne. Supprimer toute trace du pod de test.

- [ ] **Step 7: Commit et suivi**

```bash
git add config/manager/kustomization.yaml deploy/samples/test-cluster/*.sealed.yaml
git commit -m "chore(deploy): alert relay on the test cluster"
```

Mettre à jour la description du jalon Neura « Alertes Frame reçues dans Neura » (projet Neura `678364ed-b50f-4a7e-882b-96c0ce29b1fc`, jalon `30a339ed-4d08-4267-973e-2b00182e49ea`) : chemin Alertmanager → Frame (`FrameAlert`, `FrameAlertSubscription/neura`) → webhook Neura, et les quatre constats de la Step 6 avec leur date. Ne passer le jalon à terminé qu'avec la fermeture réelle observée.
