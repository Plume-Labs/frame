variable "kubeconfig_path" {
  description = "Path to the kubeconfig file for the local cluster"
  type        = string
  default     = "~/.kube/config"
}

variable "kube_context" {
  description = "Kubernetes context to use (null = current context)"
  type        = string
  default     = null
}

variable "namespace" {
  description = "Kubernetes namespace for Cluster Control resources"
  type        = string
  default     = "cluster-control"
}

variable "image_repository" {
  description = "Container image repository"
  type        = string
  default     = "ghcr.io/plume-labs/frame"
}

variable "image_tag" {
  description = "Container image tag to deploy"
  type        = string
  default     = "latest"
}

variable "replicas" {
  description = "Number of UI pod replicas"
  type        = number
  default     = 1
}

variable "node_env" {
  description = "Application environment (development or production)"
  type        = string
  default     = "production"

  validation {
    condition     = contains(["development", "production"], var.node_env)
    error_message = "node_env must be 'development' or 'production'."
  }
}

variable "resources" {
  description = "CPU/memory resource requests and limits for the UI container"
  type = object({
    requests = object({
      memory = string
      cpu    = string
    })
    limits = object({
      memory = string
      cpu    = string
    })
  })
  default = {
    requests = {
      memory = "256Mi"
      cpu    = "100m"
    }
    limits = {
      memory = "512Mi"
      cpu    = "500m"
    }
  }
}
