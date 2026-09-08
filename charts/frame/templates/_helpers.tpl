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
The nine CRD-tier RBAC sets (viewer/editor/admin per CRD). Kept as a fixed
list because it mirrors the actual CRDs in api/ — not meant to be
user-editable; the toggle is rbac.tierRoles.install, not this.

Seven of the nine were scaffolded by kubebuilder. `frameuser` was not, and
had no tier at all until the API freeze — the one kind holding credential
material was the one kind nobody could be scoped to. It is also the one entry
that renders a different shape; see rbac-tier-roles.yaml. `frametask` was
added later still, alongside per-user identity — it has no controller, so its
tier is the plain shape every kind but frameuser gets.

`aggregate` says which of the three roles carry the
`rbac.frame.plume-labs.io/tier` label, i.e. which ones the three `frame-*`
aggregated ClusterRoles pick up. Default is all three; two kinds are not.

  frameuser — admin only. `get frameusers` returns status.passwordHash, so
    the viewer tier is every account's argon2id hash (docs/deployment.md says
    so in as many words), and the editor tier is a one-PATCH promotion to
    admin. Both were labelled for one commit; the whole-branch review's C4 is
    what took them back out. The webhook's requireAdminRequester is the other
    half.
  talosmachineconfig / talosupgrade — no editor. Rewriting a machine config
    or scheduling a reboot into a new OS image is an admin action;
    cluster-control-operator excludes Talos writes by name.

This list and config/rbac/*_role.yaml are two hand-maintained copies of one
thing. `make helm-parity` compares them, including this label.
*/}}
{{- define "frame.tierRoleCRDs" -}}
- roleBase: framejob
  apiGroup: frame.plume-labs.io
  resource: framejobs
- roleBase: framenode
  apiGroup: frame.plume-labs.io
  resource: framenodes
- roleBase: frameresourcequota
  apiGroup: frame.plume-labs.io
  resource: frameresourcequotas
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
