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
The seven CRD-tier RBAC sets kubebuilder scaffolds (viewer/editor/admin per
CRD). Kept as a fixed list because it mirrors the actual CRDs in api/ — not
meant to be user-editable; the toggle is rbac.tierRoles.install, not this.
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
- roleBase: schedulingpolicy
  apiGroup: frame.plume-labs.io
  resource: schedulingpolicies
- roleBase: talosmachineconfig
  apiGroup: frame.plume-labs.io
  resource: talosmachineconfigs
- roleBase: talosupgrade
  apiGroup: frame.plume-labs.io
  resource: talosupgrades
- roleBase: services-frameservice
  apiGroup: services.plume-labs.io
  resource: frameservices
{{- end -}}
