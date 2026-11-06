// Package postgres provides FrameService type "postgres": a Zalando
// postgres-operator cluster, created by Frame or adopted from whoever rendered
// it before.
//
// The operator runs the database; this provider only writes its CR. The rules
// come from a CR that was once pruned by accident and took the database with
// it:
//
//   - A cluster is never recreated. Creation happens once, for a FrameService
//     that has never had an instance. An adopted or formerly Ready instance
//     whose CR is gone degrades and waits for a person: a new CR under that
//     name would boot an empty database under the consumers' address.
//   - A cluster that exists is never claimed by accident. It takes
//     adoptUID, the UID of the very object, read by whoever decided.
//   - Nothing about an existing cluster is rewritten but what can safely move:
//     instances, volume size, placement. The other settings are compared, and
//     a difference writes nothing at all (SpecDrift), so an adoption whose
//     parameters do not describe the live cluster changes nothing.
//   - The CR and the bucket claim are data, so they carry no owner reference
//     and are not listed in status.provisioned: deletionPolicy never reaches
//     them, and the manager holds no delete verb on either. Deleting a database
//     is a deliberate kubectl delete, which the operator's own delete
//     annotations then gate.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	servicesv1beta1 "github.com/Plume-Labs/frame/api/services/v1beta1"
	"github.com/Plume-Labs/frame/internal/scheduling"
	"github.com/Plume-Labs/frame/internal/services/provider"
)

// The operator's CR is written as unstructured: this reads a handful of fields
// and a vendored API would tie Frame's go.mod to the operator's release.
// Unstructured reads also bypass the manager's cache, so a named Get costs no
// cluster-wide informer and the grant stays get, create, patch.
//
// +kubebuilder:rbac:groups=acid.zalan.do,resources=postgresqls,verbs=get;create;patch
// +kubebuilder:rbac:groups=objectbucket.io,resources=objectbucketclaims,verbs=get;create;patch

var (
	clusterGVK = schema.GroupVersionKind{Group: "acid.zalan.do", Version: "v1", Kind: "postgresql"}
	bucketGVK  = schema.GroupVersionKind{Group: "objectbucket.io", Version: "v1alpha1", Kind: "ObjectBucketClaim"}
)

const (
	// OwnerLabel marks a cluster as this FrameService's. It is set on creation
	// or adoption, and only there.
	OwnerLabel = "services.plume-labs.io/frameservice"
	// keepAnnotation keeps the object through a Helm prune or uninstall, for a
	// CR some chart rendered before Frame took it over.
	keepAnnotation = "helm.sh/resource-policy"
	keepValue      = "keep"

	defaultVersion       = "18"
	defaultRetentionDays = "14"
	// region is required by the S3 SDK and ignored by Ceph RGW.
	walRegion        = "us-east-1"
	bucketClass      = "ceph-bucket"
	clusterNameLabel = "cluster-name"
	hostnameLabel    = "kubernetes.io/hostname"
	port             = 5432
	runningStatus    = "Running"
)

// Degrade reasons, all of which wait for a person.
const (
	ReasonOperatorMissing = "OperatorMissing"
	ReasonInvalidName     = "InvalidName"
	ReasonAdoptionMissing = "AdoptionTargetMissing"
	ReasonInstanceLost    = "InstanceLost"
	ReasonNotOwned        = "ClusterNotOwned"
	ReasonSpecDrift       = "SpecDrift"
	ReasonVolumeShrink    = "VolumeShrinkRefused"
	ReasonProvisioning    = "Provisioning"
	ReasonNotRunning      = "ClusterNotRunning"
	ReasonRunning         = "Running"
)

// Provider implements provider.Provisioner.
type Provider struct {
	client client.Client
}

// New builds the provider.
func New(c client.Client) *Provider { return &Provider{client: c} }

// Type is the spec.type value.
func (p *Provider) Type() string { return "postgres" }

// ParameterSchema is what the webhook enforces.
func (p *Provider) ParameterSchema() *provider.Schema {
	return &apiextensionsv1.JSONSchemaProps{
		Type:     "object",
		Required: []string{"instances", "volumeSize"},
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"instances": {
				Type:        "string",
				Description: "Postgres pods, primary included. Above one, Patroni fails over between them.",
				Pattern:     `^[1-9]$`,
			},
			"volumeSize": {
				Type:        "string",
				Description: "Size of each instance's volume. Can grow, never shrink.",
				Pattern:     `^[1-9][0-9]*(Mi|Gi|Ti)$`,
			},
			"storageClass": {
				Type:        "string",
				Description: "StorageClass of the volumes. Fixed once the cluster exists; empty is the cluster default.",
				Pattern:     `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`,
			},
			"postgresVersion": {
				Type:        "string",
				Description: "Postgres major version. Fixed once the cluster exists. Defaults to " + defaultVersion + ".",
				Pattern:     `^[0-9]{2}$`,
			},
			"database": {
				Type:        "string",
				Description: "Database to create. Fixed once the cluster exists. Defaults to the team, the name before the first dash.",
				Pattern:     `^[a-z_][a-z0-9_]*$`,
			},
			"owner": {
				Type:        "string",
				Description: "Role owning the database, and the one the binding carries. Fixed once the cluster exists. Defaults to the team.",
				Pattern:     `^[a-z_][a-z0-9_]*$`,
			},
			"nodes": {
				Type: "string",
				Description: "Comma-separated node names the instances may run on, replacing any node affinity. " +
					"Empty leaves placement alone. Moving it restarts the pods.",
				Pattern: `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?(,[a-z0-9]([-a-z0-9.]*[a-z0-9])?)*$`,
			},
			"walBucketClaim": {
				Type: "string",
				Description: "ObjectBucketClaim holding WAL-G's archive, created if it does not exist. " +
					"Empty means no archiving. Fixed once the cluster exists.",
				Pattern: `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`,
			},
			"walEndpoint": {
				Type:        "string",
				Description: "S3 endpoint of that bucket. Required with walBucketClaim. Fixed once the cluster exists.",
				Pattern:     `^https?://[^ ]+$`,
			},
			"walRetentionDays": {
				Type:        "string",
				Description: "Base backups and days kept by WAL-G. Fixed once the cluster exists. Defaults to " + defaultRetentionDays + ".",
				Pattern:     `^[1-9][0-9]*$`,
			},
			"adoptUID": {
				Type:        "string",
				Description: "UID of an existing cluster to take over. With it set, this provider never creates a cluster.",
				Pattern:     `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
			},
		},
	}
}

// Size checks what the schema's patterns cannot. A database's footprint is the
// operator's defaults or the CR's own, so none is reported.
func (p *Provider) Size(params map[string]string) (provider.Sizing, error) {
	if params["walBucketClaim"] != "" && params["walEndpoint"] == "" {
		return provider.Sizing{}, errors.New("walBucketClaim needs walEndpoint")
	}
	if n, nodes := params["instances"], params["nodes"]; nodes != "" {
		if want, _ := strconv.Atoi(n); want > len(strings.Split(nodes, ",")) {
			return provider.Sizing{}, fmt.Errorf(
				"%s instances spread one per node cannot fit on the %d listed", n, len(strings.Split(nodes, ",")))
		}
	}
	return provider.Sizing{}, nil
}

// settings is the parameters with their defaults resolved.
type settings struct {
	team, version, database, owner, storageClass string
	instances                                    int64
	volumeSize                                   string
	nodes                                        []string
	walClaim, walEndpoint, walRetention          string
	adoptUID                                     string
}

func settingsOf(svc *servicesv1beta1.FrameService) (settings, bool) {
	params := provider.Params(svc.Spec.Parameters)
	team, _, ok := strings.Cut(svc.Name, "-")
	if !ok {
		return settings{}, false
	}
	or := func(key, fallback string) string {
		if v := params[key]; v != "" {
			return v
		}
		return fallback
	}
	s := settings{
		team:         team,
		version:      or("postgresVersion", defaultVersion),
		database:     or("database", team),
		owner:        or("owner", team),
		storageClass: params["storageClass"],
		volumeSize:   params["volumeSize"],
		walClaim:     params["walBucketClaim"],
		walEndpoint:  params["walEndpoint"],
		walRetention: or("walRetentionDays", defaultRetentionDays),
		adoptUID:     params["adoptUID"],
	}
	s.instances, _ = strconv.ParseInt(params["instances"], 10, 64)
	if params["nodes"] != "" {
		s.nodes = strings.Split(params["nodes"], ",")
	}
	return s, true
}

func degraded(reason, format string, args ...any) provider.Result {
	return provider.Result{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// Reconcile converges the cluster, or says why it will not.
func (p *Provider) Reconcile(ctx context.Context, svc *servicesv1beta1.FrameService) (provider.Result, error) {
	s, ok := settingsOf(svc)
	if !ok {
		return degraded(ReasonInvalidName,
			"the operator needs a name of the form <team>-<cluster>, and %q has no dash", svc.Name), nil
	}

	cr := newObject(clusterGVK, svc)
	err := p.client.Get(ctx, client.ObjectKeyFromObject(cr), cr)
	switch {
	case meta.IsNoMatchError(err):
		return degraded(ReasonOperatorMissing, "the postgresql CRD is not installed: no operator to run this cluster"), nil
	case apierrors.IsNotFound(err):
		return p.reconcileAbsent(ctx, svc, s)
	case err != nil:
		return provider.Result{}, fmt.Errorf("reading the cluster: %w", err)
	}

	if cr.GetLabels()[OwnerLabel] != svc.Name {
		if s.adoptUID != string(cr.GetUID()) {
			return degraded(ReasonNotOwned,
				"a cluster %s already exists (UID %s) and this FrameService does not claim it: set adoptUID to take it over",
				cr.GetName(), cr.GetUID()), nil
		}
	}
	if drift := driftOf(cr, s); len(drift) > 0 {
		return degraded(ReasonSpecDrift, "nothing written: the parameters do not describe the cluster: %s",
			strings.Join(drift, "; ")), nil
	}
	if cr.GetLabels()[OwnerLabel] != svc.Name {
		if err := p.claim(ctx, cr, svc); err != nil {
			return provider.Result{}, err
		}
	}
	if s.walClaim != "" {
		if err := p.keepBucket(ctx, svc, s.walClaim); err != nil {
			return provider.Result{}, err
		}
	}

	if res, stop, err := p.converge(ctx, cr, s); stop || err != nil {
		return res, err
	}

	status, _, _ := unstructured.NestedString(cr.Object, "status", "PostgresClusterStatus")
	if status != runningStatus {
		return degraded(ReasonNotRunning, "the operator reports cluster %s as %q", cr.GetName(), status), nil
	}
	return provider.Result{Ready: true, Reason: ReasonRunning, Message: fmt.Sprintf("cluster %s running", cr.GetName())}, nil
}

// reconcileAbsent creates the cluster if this FrameService never had one.
func (p *Provider) reconcileAbsent(ctx context.Context, svc *servicesv1beta1.FrameService, s settings) (provider.Result, error) {
	if s.adoptUID != "" {
		return degraded(ReasonAdoptionMissing, "cluster %s (UID %s) does not exist: nothing created", svc.Name, s.adoptUID), nil
	}
	if svc.Status.Binding.Endpoint != "" {
		return degraded(ReasonInstanceLost,
			"cluster %s was serving and has disappeared: not recreated, a new one would be empty; restore it", svc.Name), nil
	}
	if s.walClaim != "" {
		if err := p.ensureBucket(ctx, svc, s.walClaim); err != nil {
			return provider.Result{}, err
		}
	}
	cr := newObject(clusterGVK, svc)
	cr.SetLabels(map[string]string{OwnerLabel: svc.Name})
	cr.SetAnnotations(map[string]string{keepAnnotation: keepValue})
	cr.Object["spec"] = newSpec(svc, s)
	if err := p.client.Create(ctx, cr); err != nil {
		return provider.Result{}, fmt.Errorf("creating the cluster: %w", err)
	}
	return degraded(ReasonProvisioning, "cluster %s created, waiting for the operator", cr.GetName()), nil
}

func newObject(gvk schema.GroupVersionKind, svc *servicesv1beta1.FrameService) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetName(svc.Name)
	u.SetNamespace(svc.Namespace)
	return u
}

func newSpec(svc *servicesv1beta1.FrameService, s settings) map[string]any {
	volume := map[string]any{"size": s.volumeSize}
	if s.storageClass != "" {
		volume["storageClass"] = s.storageClass
	}
	spec := map[string]any{
		"teamId":            s.team,
		"numberOfInstances": s.instances,
		"postgresql":        map[string]any{"version": s.version},
		"volume":            volume,
		"users":             map[string]any{s.owner: []any{"createdb"}},
		"databases":         map[string]any{s.database: s.owner},
	}
	if class := scheduling.PriorityClassForServiceClass(string(svc.Spec.ServiceClass)); class != "" {
		spec["podPriorityClassName"] = class
	}
	maps.Copy(spec, placement(svc.Name, s))
	if s.walClaim != "" {
		spec["env"] = walEnv(s)
	}
	return spec
}

// placement is the part of the spec the nodes and instances parameters own.
//
// The spread's labelSelector looks redundant and is not: the operator sets it
// on a loop copy (generateTopologySpreadConstraints, v2.0.0), so one left out
// selects no pod and spreads nothing.
func placement(cluster string, s settings) map[string]any {
	out := map[string]any{}
	if len(s.nodes) > 0 {
		values := make([]any, 0, len(s.nodes))
		for _, n := range s.nodes {
			values = append(values, n)
		}
		out["nodeAffinity"] = map[string]any{
			"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
				"nodeSelectorTerms": []any{map[string]any{
					"matchExpressions": []any{map[string]any{
						"key": hostnameLabel, "operator": "In", "values": values,
					}},
				}},
			},
		}
	}
	if s.instances > 1 {
		out["topologySpreadConstraints"] = []any{map[string]any{
			"maxSkew":           int64(1),
			"topologyKey":       hostnameLabel,
			"whenUnsatisfiable": "DoNotSchedule",
			"labelSelector":     map[string]any{"matchLabels": map[string]any{clusterNameLabel: cluster}},
		}}
	}
	return out
}

// walEnv is Spilo's own WAL-G switch: WAL_S3_BUCKET alone makes it compute
// archive_command. WALG_DISABLE_S3_SSE is required against Ceph RGW, which
// refuses the encryption header wal-g sends by default on a custom endpoint,
// and Spilo's automatic default for it is a no-op in spilo-18:4.1-p2.
func walEnv(s settings) []any {
	fromSecret := func(key string) map[string]any {
		return map[string]any{"name": key, "valueFrom": map[string]any{
			"secretKeyRef": map[string]any{"name": s.walClaim, "key": key},
		}}
	}
	plain := func(name, value string) map[string]any { return map[string]any{"name": name, "value": value} }
	return []any{
		map[string]any{"name": "WAL_S3_BUCKET", "valueFrom": map[string]any{
			"configMapKeyRef": map[string]any{"name": s.walClaim, "key": "BUCKET_NAME"},
		}},
		fromSecret("AWS_ACCESS_KEY_ID"),
		fromSecret("AWS_SECRET_ACCESS_KEY"),
		plain("AWS_ENDPOINT", s.walEndpoint),
		plain("AWS_REGION", walRegion),
		plain("WALG_DISABLE_S3_SSE", "true"),
		plain("BACKUP_NUM_TO_RETAIN", s.walRetention),
	}
}

// driftOf lists every fixed setting the parameters state differently from the
// cluster.
func driftOf(cr *unstructured.Unstructured, s settings) []string {
	str := func(fields ...string) string {
		v, _, _ := unstructured.NestedString(cr.Object, append([]string{"spec"}, fields...)...)
		return v
	}
	var out []string
	check := func(name, want, live string) {
		if want != live {
			out = append(out, fmt.Sprintf("%s is %q, the cluster has %q", name, want, live))
		}
	}
	check("team", s.team, str("teamId"))
	check("postgresVersion", s.version, str("postgresql", "version"))
	check("storageClass", s.storageClass, str("volume", "storageClass"))
	check("database owner", s.owner, str("databases", s.database))
	if users, _, _ := unstructured.NestedMap(cr.Object, "spec", "users"); users[s.owner] == nil {
		out = append(out, fmt.Sprintf("owner %q is not a user of the cluster", s.owner))
	}

	claim, endpoint, retention := liveWAL(cr)
	check("walBucketClaim", s.walClaim, claim)
	if s.walClaim != "" {
		check("walEndpoint", s.walEndpoint, endpoint)
		check("walRetentionDays", s.walRetention, retention)
	}
	return out
}

func liveWAL(cr *unstructured.Unstructured) (claim, endpoint, retention string) {
	env, _, _ := unstructured.NestedSlice(cr.Object, "spec", "env")
	for _, e := range env {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		value, _ := m["value"].(string)
		switch m["name"] {
		case "WAL_S3_BUCKET":
			claim, _, _ = unstructured.NestedString(m, "valueFrom", "configMapKeyRef", "name")
		case "AWS_ENDPOINT":
			endpoint = value
		case "BACKUP_NUM_TO_RETAIN":
			retention = value
		}
	}
	return claim, endpoint, retention
}

// converge writes what may move, and only what differs: an unchanged cluster
// sees no write, which is what lets an adoption be checked by its generation.
func (p *Provider) converge(ctx context.Context, cr *unstructured.Unstructured, s settings) (provider.Result, bool, error) {
	liveSize, _, _ := unstructured.NestedString(cr.Object, "spec", "volume", "size")
	live, err := resource.ParseQuantity(liveSize)
	if err != nil {
		return provider.Result{}, false, fmt.Errorf("the cluster's volume size %q: %w", liveSize, err)
	}
	want := resource.MustParse(s.volumeSize)
	if want.Cmp(live) < 0 {
		return degraded(ReasonVolumeShrink, "volumeSize %s is under the cluster's %s: a volume cannot shrink", s.volumeSize, liveSize), true, nil
	}

	spec := map[string]any{}
	if want.Cmp(live) > 0 {
		spec["volume"] = map[string]any{"size": s.volumeSize}
	}
	if n, _, _ := unstructured.NestedInt64(cr.Object, "spec", "numberOfInstances"); n != s.instances {
		spec["numberOfInstances"] = s.instances
	}
	for k, v := range placement(cr.GetName(), s) {
		if live, _, _ := unstructured.NestedFieldNoCopy(cr.Object, "spec", k); !reflect.DeepEqual(live, v) {
			spec[k] = v
		}
	}
	if len(spec) == 0 {
		return provider.Result{}, false, nil
	}
	if err := p.merge(ctx, cr, map[string]any{"spec": spec}); err != nil {
		return provider.Result{}, false, fmt.Errorf("updating the cluster: %w", err)
	}
	names := make([]string, 0, len(spec))
	for k := range spec {
		names = append(names, k)
	}
	slices.Sort(names)
	return degraded(ReasonProvisioning, "cluster %s updated (%s), waiting for the operator", cr.GetName(), strings.Join(names, ", ")), true, nil
}

// claim marks the cluster as this FrameService's. Metadata only: the operator
// does not react to it and the generation does not move.
func (p *Provider) claim(ctx context.Context, cr *unstructured.Unstructured, svc *servicesv1beta1.FrameService) error {
	err := p.merge(ctx, cr, map[string]any{"metadata": map[string]any{
		"labels":      map[string]any{OwnerLabel: svc.Name},
		"annotations": map[string]any{keepAnnotation: keepValue},
	}})
	if err != nil {
		return fmt.Errorf("claiming the cluster: %w", err)
	}
	return nil
}

func (p *Provider) merge(ctx context.Context, obj *unstructured.Unstructured, body map[string]any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return p.client.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw))
}

// ensureBucket creates the claim the archive needs, if it is not there.
func (p *Provider) ensureBucket(ctx context.Context, svc *servicesv1beta1.FrameService, name string) error {
	obc := &unstructured.Unstructured{}
	obc.SetGroupVersionKind(bucketGVK)
	obc.SetName(name)
	obc.SetNamespace(svc.Namespace)
	err := p.client.Get(ctx, client.ObjectKeyFromObject(obc), obc)
	if err == nil || !apierrors.IsNotFound(err) {
		return err
	}
	obc.SetLabels(map[string]string{OwnerLabel: svc.Name})
	obc.SetAnnotations(map[string]string{keepAnnotation: keepValue})
	obc.Object["spec"] = map[string]any{"generateBucketName": name, "storageClassName": bucketClass}
	return p.client.Create(ctx, obc)
}

// keepBucket makes sure a claim that survives its cluster's chart stays: its
// storage class deletes the bucket with the claim.
func (p *Provider) keepBucket(ctx context.Context, svc *servicesv1beta1.FrameService, name string) error {
	obc := &unstructured.Unstructured{}
	obc.SetGroupVersionKind(bucketGVK)
	obc.SetName(name)
	obc.SetNamespace(svc.Namespace)
	if err := p.client.Get(ctx, client.ObjectKeyFromObject(obc), obc); err != nil {
		return fmt.Errorf("reading the WAL bucket claim: %w", err)
	}
	if obc.GetAnnotations()[keepAnnotation] == keepValue && obc.GetLabels()[OwnerLabel] == svc.Name {
		return nil
	}
	return p.merge(ctx, obc, map[string]any{"metadata": map[string]any{
		"labels":      map[string]any{OwnerLabel: svc.Name},
		"annotations": map[string]any{keepAnnotation: keepValue},
	}})
}

// Bind hands consumers the address and the owner's credentials, read from the
// Secret the operator maintains.
func (p *Provider) Bind(ctx context.Context, svc *servicesv1beta1.FrameService) (provider.Binding, error) {
	s, ok := settingsOf(svc)
	if !ok {
		return provider.Binding{}, fmt.Errorf("invalid name %q", svc.Name)
	}
	var secret corev1.Secret
	key := client.ObjectKey{Namespace: svc.Namespace, Name: fmt.Sprintf("%s.%s.credentials.postgresql.acid.zalan.do", s.owner, svc.Name)}
	if err := p.client.Get(ctx, key, &secret); err != nil {
		if apierrors.IsForbidden(err) {
			return provider.Binding{}, &provider.SecretAccessForbidden{Namespace: svc.Namespace, Action: "read"}
		}
		return provider.Binding{}, fmt.Errorf("reading the operator's credentials: %w", err)
	}
	host := fmt.Sprintf("%s.%s.svc", svc.Name, svc.Namespace)
	endpoint := fmt.Sprintf("%s:%d", host, port)
	return provider.Binding{
		Endpoint: endpoint,
		Data: map[string][]byte{
			"host":     []byte(host),
			"port":     []byte(strconv.Itoa(port)),
			"database": []byte(s.database),
			"username": secret.Data["username"],
			"password": secret.Data["password"],
		},
	}, nil
}

var _ provider.Provisioner = (*Provider)(nil)
