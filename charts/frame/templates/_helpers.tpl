{{/*
Resource names in this chart are a fixed "frame-" prefix, not derived from
.Release.Name. That is deliberate, not an oversight — see the "Name
compatibility" section of charts/frame/README.md: the operator is already
installed on a live cluster by kustomize (which hardcodes the same prefix via
its namePrefix), and the Helm migration path is `helm install --take-ownership`
over those exact objects. Do not change this to `include "frame.fullname"`
patterns that fold in the release name.
*/}}
{{- define "frame.name" -}}
frame
{{- end -}}

{{- define "frame.chartLabels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: frame
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels. These are load-bearing: the metrics/webhook Services, the
NetworkPolicies and the ServiceMonitor all select on exactly these two keys,
matching config/manager/manager.yaml and config/rbac/*. Do not add
app.kubernetes.io/instance or other Helm-standard labels here — that would
change the Deployment's immutable selector out from under an existing
kustomize-managed rollout.
*/}}
{{- define "frame.selectorLabels" -}}
app.kubernetes.io/name: frame
control-plane: controller-manager
{{- end -}}

{{- define "frame.imageTag" -}}
{{- .Values.image.tag | default .Chart.AppVersion -}}
{{- end -}}

{{/*
frame-provisiond's image tag. Falls back to a literal "latest", not
.Chart.AppVersion like frame.imageTag: config/provisiond/deployment.yaml
hardcodes "frame-provisiond:latest" outright (no kustomize `images:`
transform parameterizes it, unlike the manager's "controller" placeholder),
so matching that exactly is what keeps `make helm-parity`'s default content
diff green with no redact exception -- the manager's image/imagePullPolicy
mismatch is a documented, permanent exception in hack/helm-parity.sh
precisely because the two placeholders never were meant to agree; there is
no equivalent reason for provisiond's default to disagree.
*/}}
{{- define "frame.provisiondImageTag" -}}
{{- .Values.provisiond.image.tag | default "latest" -}}
{{- end -}}

{{/*
provisiond's own selector labels -- distinct from frame.selectorLabels
(app.kubernetes.io/name: frame, control-plane: controller-manager), which
identifies the manager's pod, not this one. Matches
config/provisiond/deployment.yaml and service.yaml's selector/template
labels exactly. This one is load-bearing for `make helm-parity`, not just
documentation: it lands under .spec (spec.selector.matchLabels,
spec.template.metadata.labels), which the body diff does NOT strip the way
it strips top-level .metadata, so a divergence here fails the script.
*/}}
{{- define "frame.provisiondSelectorLabels" -}}
app.kubernetes.io/name: frame-provisiond
{{- end -}}

{{/*
Metrics port/scheme, derived from metrics.secure so the container's
--metrics-bind-address, the metrics Service, the NetworkPolicy ingress rule
and the ServiceMonitor endpoint can never point at four different ports (I-2:
they used to be independently hardcoded to 8443/https and silently broke
whenever metrics.secure was set to false).
*/}}
{{- define "frame.metricsPort" -}}
{{- if .Values.metrics.secure -}}8443{{- else -}}8080{{- end -}}
{{- end -}}

{{- define "frame.metricsPortName" -}}
{{- if .Values.metrics.secure -}}https{{- else -}}http{{- end -}}
{{- end -}}

{{/*
The eleven CRD-tier RBAC sets (viewer/editor/admin per CRD). Kept as a fixed
list because it mirrors the actual CRDs in api/ — not meant to be
user-editable; the toggle is rbac.tierRoles.install, not this.

Eight of the ten were scaffolded by kubebuilder. `frameuser` was not, and
had no tier at all until the API freeze — the one kind holding credential
material was the one kind nobody could be scoped to. It is also the one entry
that renders a different shape; see rbac-tier-roles.yaml. `frametask` was
added later still, alongside per-user identity — it has no controller, so its
tier is the plain shape every kind but frameuser gets.

`aggregate` says which of the three roles carry the
`rbac.frame.plume-labs.io/tier` label, i.e. which ones the three `frame-*`
aggregated ClusterRoles pick up. Default is all three; three kinds are not.

  frameuser — admin only. `get frameusers` returns status.passwordHash, so
    the viewer tier is every account's argon2id hash (docs/deployment.md says
    so in as many words), and the editor tier is a one-PATCH promotion to
    admin. Both were labelled for one commit; the whole-branch review's C4 is
    what took them back out. The webhook's requireAdminRequester is the other
    half.
  talosmachineconfig / talosupgrade — no editor. Rewriting a machine config
    or scheduling a reboot into a new OS image is an admin action;
    cluster-control-operator excludes Talos writes by name.
  framemachine — no editor (lot 1, hardware/Redfish, task 6). spec.powerRequest
    is the only console-writable field, reached only by `patch`, and it is
    admin-only: restarting a Deployment is bounded by an update strategy,
    powering off a chassis is bounded by nothing, and the chassis may be
    carrying the cluster that hosts the console making the request. The
    editor role still exists (create/update/patch/delete are legitimate
    `kubectl` operations for someone who already holds them by other means)
    but must never be labelled, or `frame-editor` aggregates
    `patch framemachines` and hands operators power control. See
    config/rbac/framemachine_editor_role.yaml's header and
    test/manifests/framemachine_rbac_test.go.
  frameinstall — no editor (2026-09-11, provisioning/Debian, task 7). Creating
    a FrameInstall wipes a machine's disks, the same class of action as
    framemachine's powerRequest, so it gets the same exclusion: admin and
    viewer only. See config/rbac/frameinstall_editor_role.yaml's header.
  framediskclaim — no editor (2026-09-13, storage, task 10). Creating a
    FrameDiskClaim destroys data, the same class of action as frameinstall's
    disk wipe and framemachine's powerRequest, so it gets the same exclusion:
    admin and viewer only. See config/rbac/framediskclaim_editor_role.yaml's
    header.

This list and config/rbac/*_role.yaml are two hand-maintained copies of one
thing. `make helm-parity` compares them, including this label.
*/}}
{{- define "frame.tierRoleCRDs" -}}
- roleBase: framediskclaim
  apiGroup: frame.plume-labs.io
  resource: framediskclaims
  aggregate: [admin, viewer]
- roleBase: frameinstall
  apiGroup: frame.plume-labs.io
  resource: frameinstalls
  aggregate: [admin, viewer]
- roleBase: framejob
  apiGroup: frame.plume-labs.io
  resource: framejobs
- roleBase: framemachine
  apiGroup: frame.plume-labs.io
  resource: framemachines
  aggregate: [admin, viewer]
- roleBase: framenode
  apiGroup: frame.plume-labs.io
  resource: framenodes
- roleBase: frameresourcequota
  apiGroup: frame.plume-labs.io
  resource: frameresourcequotas
- roleBase: framestorage
  apiGroup: frame.plume-labs.io
  resource: framestorages
- roleBase: frametask
  apiGroup: frame.plume-labs.io
  resource: frametasks
- roleBase: frameuser
  apiGroup: frame.plume-labs.io
  resource: frameusers
  aggregate: [admin]
- roleBase: schedulingpolicy
  apiGroup: frame.plume-labs.io
  resource: schedulingpolicies
- roleBase: talosmachineconfig
  apiGroup: frame.plume-labs.io
  resource: talosmachineconfigs
  aggregate: [admin, viewer]
- roleBase: talosupgrade
  apiGroup: frame.plume-labs.io
  resource: talosupgrades
  aggregate: [admin, viewer]
- roleBase: services-frameservice
  apiGroup: services.plume-labs.io
  resource: frameservices
{{- end -}}
