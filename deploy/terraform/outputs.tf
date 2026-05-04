output "namespace" {
  description = "Kubernetes namespace where resources are deployed"
  value       = kubernetes_namespace.cluster_control.metadata[0].name
}

output "deployment_name" {
  description = "Kubernetes Deployment name"
  value       = kubernetes_deployment.ui.metadata[0].name
}

output "service_name" {
  description = "Kubernetes Service name"
  value       = kubernetes_service.ui.metadata[0].name
}

output "port_forward_command" {
  description = "kubectl command to access the UI locally"
  value       = "kubectl port-forward -n ${kubernetes_namespace.cluster_control.metadata[0].name} svc/${kubernetes_service.ui.metadata[0].name} 8080:80"
}
