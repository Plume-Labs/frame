terraform {
  required_version = ">= 1.6"

  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.35"
    }
  }
}

provider "kubernetes" {
  config_path    = var.kubeconfig_path
  config_context = var.kube_context
}

# ── Namespace ─────────────────────────────────────────────────────────────────

resource "kubernetes_namespace" "cluster_control" {
  metadata {
    name = var.namespace
    labels = {
      "app.kubernetes.io/name"    = "cluster-control"
      "app.kubernetes.io/part-of" = "monitoring-platform"
    }
  }
}

# ── RBAC ──────────────────────────────────────────────────────────────────────

resource "kubernetes_service_account" "ui" {
  metadata {
    name      = "cluster-control-ui"
    namespace = kubernetes_namespace.cluster_control.metadata[0].name
    labels = {
      "app.kubernetes.io/name"      = "cluster-control"
      "app.kubernetes.io/component" = "ui"
    }
  }
}

resource "kubernetes_cluster_role" "viewer" {
  metadata {
    name = "cluster-control-viewer"
    labels = {
      "app.kubernetes.io/name"      = "cluster-control"
      "app.kubernetes.io/component" = "ui"
    }
  }

  rule {
    api_groups = [""]
    resources  = ["nodes", "pods", "services", "namespaces"]
    verbs      = ["get", "list", "watch"]
  }

  rule {
    api_groups = ["apps"]
    resources  = ["deployments", "daemonsets", "statefulsets"]
    verbs      = ["get", "list", "watch"]
  }

  rule {
    api_groups = ["metrics.k8s.io"]
    resources  = ["nodes", "pods"]
    verbs      = ["get", "list"]
  }
}

resource "kubernetes_cluster_role_binding" "viewer" {
  metadata {
    name = "cluster-control-viewer"
    labels = {
      "app.kubernetes.io/name"      = "cluster-control"
      "app.kubernetes.io/component" = "ui"
    }
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role.viewer.metadata[0].name
  }

  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.ui.metadata[0].name
    namespace = kubernetes_namespace.cluster_control.metadata[0].name
  }
}

# ── Deployment ────────────────────────────────────────────────────────────────

resource "kubernetes_deployment" "ui" {
  metadata {
    name      = "cluster-control-ui"
    namespace = kubernetes_namespace.cluster_control.metadata[0].name
    labels = {
      app                           = "cluster-control"
      component                     = "ui"
      "app.kubernetes.io/name"      = "cluster-control"
      "app.kubernetes.io/part-of"   = "monitoring-platform"
      "app.kubernetes.io/component" = "ui"
    }
  }

  spec {
    replicas = var.replicas

    selector {
      match_labels = {
        app       = "cluster-control"
        component = "ui"
      }
    }

    template {
      metadata {
        labels = {
          app       = "cluster-control"
          component = "ui"
        }
        annotations = {
          "prometheus.io/scrape" = "true"
          "prometheus.io/port"   = "8080"
        }
      }

      spec {
        service_account_name = kubernetes_service_account.ui.metadata[0].name

        affinity {
          pod_anti_affinity {
            preferred_during_scheduling_ignored_during_execution {
              weight = 100
              pod_affinity_term {
                label_selector {
                  match_expressions {
                    key      = "app"
                    operator = "In"
                    values   = ["cluster-control"]
                  }
                }
                topology_key = "kubernetes.io/hostname"
              }
            }
          }
        }

        container {
          name              = "ui"
          image             = "${var.image_repository}:${var.image_tag}"
          image_pull_policy = "IfNotPresent"

          port {
            name           = "http"
            container_port = 8080
            protocol       = "TCP"
          }

          env {
            name  = "NODE_ENV"
            value = var.node_env
          }

          env {
            name  = "PORT"
            value = "8080"
          }

          resources {
            requests = {
              memory = var.resources.requests.memory
              cpu    = var.resources.requests.cpu
            }
            limits = {
              memory = var.resources.limits.memory
              cpu    = var.resources.limits.cpu
            }
          }

          liveness_probe {
            http_get {
              path = "/"
              port = "http"
            }
            initial_delay_seconds = 30
            period_seconds        = 10
            timeout_seconds       = 5
            failure_threshold     = 3
          }

          readiness_probe {
            http_get {
              path = "/"
              port = "http"
            }
            initial_delay_seconds = 5
            period_seconds        = 5
            timeout_seconds       = 3
            failure_threshold     = 3
          }

          security_context {
            run_as_non_root            = true
            run_as_user                = 1000
            allow_privilege_escalation = false
            read_only_root_filesystem  = true
            capabilities {
              drop = ["ALL"]
            }
          }

          volume_mount {
            name       = "tmp"
            mount_path = "/tmp"
          }

          volume_mount {
            name       = "cache"
            mount_path = "/.cache"
          }
        }

        volume {
          name = "tmp"
          empty_dir {}
        }

        volume {
          name = "cache"
          empty_dir {}
        }
      }
    }
  }

  timeouts {
    create = "3m"
    update = "3m"
  }
}

# ── Service ───────────────────────────────────────────────────────────────────

resource "kubernetes_service" "ui" {
  metadata {
    name      = "cluster-control-ui"
    namespace = kubernetes_namespace.cluster_control.metadata[0].name
    labels = {
      app                           = "cluster-control"
      component                     = "ui"
      "app.kubernetes.io/name"      = "cluster-control"
      "app.kubernetes.io/part-of"   = "monitoring-platform"
      "app.kubernetes.io/component" = "ui"
    }
  }

  spec {
    type = "ClusterIP"

    selector = {
      app       = "cluster-control"
      component = "ui"
    }

    port {
      name        = "http"
      port        = 80
      target_port = "http"
      protocol    = "TCP"
    }

    session_affinity = "ClientIP"
  }
}
