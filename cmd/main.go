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

package main

import (
	"crypto/tls"
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	controller "github.com/rmocq/frame/internal/controller/frame"
	servicescontroller "github.com/rmocq/frame/internal/controller/services"
	"github.com/rmocq/frame/internal/services/provider"
	"github.com/rmocq/frame/internal/services/provider/inference"
	webhookv1alpha1 "github.com/rmocq/frame/internal/webhook/frame/v1alpha1"
	webhookservicesv1alpha1 "github.com/rmocq/frame/internal/webhook/services/v1alpha1"
	// +kubebuilder:scaffold:imports
)

const enableWebhooksEnv = "ENABLE_WEBHOOKS"

// webhooksDisabled is the ENABLE_WEBHOOKS value that turns admission webhooks off.
const webhooksDisabled = "false"

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(framev1alpha1.AddToScheme(scheme))
	utilruntime.Must(servicesv1alpha1.AddToScheme(scheme))
	utilruntime.Must(framev1beta1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var inferenceGPUMemoryMiB int64
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.Int64Var(&inferenceGPUMemoryMiB, "inference-gpu-memory-mib", 7680,
		"Usable GPU memory per card, in MiB, that inference instances are sized against. "+
			"Defaults to the Tesla P4's 7680.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsServerOptions,
		// Secrets are read through the live apiserver, never the cache. By
		// default controller-runtime serves a client Get from an informer, and
		// starting an informer over a type is a cluster-wide List+Watch of it —
		// so a single by-name Get of one Secret would silently require, and
		// then continuously exercise, the right to read every Secret in the
		// cluster, holding them all in memory besides.
		//
		// Every Secret this manager touches is named: the Talos PKI Secret in
		// buildTalosClient, the API-key Secret the inference provider owns, and
		// the binding coordinates in internal/controller/services/binding.go.
		// None of them is enumerated, so none of them needs a cache. Disabling
		// it here is what lets the rbac markers on those controllers drop
		// list and watch on secrets; putting Secrets back in the cache would
		// break the manager at startup with a Forbidden on the initial List,
		// not silently degrade.
		//
		// This is the same reasoning already applied one level down to
		// PersistentVolumeClaims, which the inference provider reaches through
		// mgr.GetAPIReader() for exactly this reason.
		Client: client.Options{
			Cache: &client.CacheOptions{
				DisableFor: []client.Object{&corev1.Secret{}},
			},
		},
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "b9bf5a0e.plume-labs.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	// Built once and shared: the controller resolves spec.type through it on
	// every reconcile, and the webhook (when enabled) validates admission
	// against the same set, so the two can never disagree on what a valid
	// type is.
	serviceRegistry := provider.NewRegistry(inference.New(inferenceGPUMemoryMiB, mgr.GetClient(), mgr.GetAPIReader()))

	// mgr.GetEventRecorderFor is deprecated in favour of GetEventRecorder, but
	// that method returns the newer events/v1 events.EventRecorder, whose
	// Eventf(regarding, related, eventtype, reason, action, note, args...)
	// signature and semantics differ from the record.EventRecorder.Event(...)
	// every reconciler's Recorder field and every controller test in this
	// package (via record.NewFakeRecorder) is written against. Migrating would
	// mean re-typing Recorder across all reconcilers, rewriting every
	// r.Recorder.Event(...) call site, and could change the events actually
	// emitted — a behaviour change several tests assert on. Deferring until
	// that migration is done deliberately, not as a side effect of a lint pass.
	if err := (&controller.FrameNodeReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("framenode"), //nolint:staticcheck
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "framenode")
		os.Exit(1)
	}
	if err := (&controller.FrameJobReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("framejob"), //nolint:staticcheck
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "framejob")
		os.Exit(1)
	}
	if err := (&controller.SchedulingPolicyReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("schedulingpolicy"), //nolint:staticcheck
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "schedulingpolicy")
		os.Exit(1)
	}
	if err := (&controller.FrameResourceQuotaReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("frameresourcequota"), //nolint:staticcheck
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "frameresourcequota")
		os.Exit(1)
	}
	if err := (&controller.TalosMachineConfigReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("talosmachineconfig"), //nolint:staticcheck
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "talosmachineconfig")
		os.Exit(1)
	}
	if err := (&controller.TalosUpgradeReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("talosupgrade"), //nolint:staticcheck
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "talosupgrade")
		os.Exit(1)
	}
	if os.Getenv(enableWebhooksEnv) != webhooksDisabled {
		if err := webhookv1alpha1.SetupFrameNodeWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "FrameNode")
			os.Exit(1)
		}
	}
	if os.Getenv(enableWebhooksEnv) != webhooksDisabled {
		if err := webhookv1alpha1.SetupFrameJobWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "FrameJob")
			os.Exit(1)
		}
	}
	if os.Getenv(enableWebhooksEnv) != webhooksDisabled {
		if err := webhookv1alpha1.SetupSchedulingPolicyWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "SchedulingPolicy")
			os.Exit(1)
		}
	}
	if os.Getenv(enableWebhooksEnv) != webhooksDisabled {
		if err := webhookv1alpha1.SetupFrameUserWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "FrameUser")
			os.Exit(1)
		}
	}
	if os.Getenv(enableWebhooksEnv) != webhooksDisabled {
		if err := webhookv1alpha1.SetupFrameResourceQuotaWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "FrameResourceQuota")
			os.Exit(1)
		}
	}
	if os.Getenv(enableWebhooksEnv) != webhooksDisabled {
		if err := webhookv1alpha1.SetupTalosMachineConfigWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "TalosMachineConfig")
			os.Exit(1)
		}
	}
	if os.Getenv(enableWebhooksEnv) != webhooksDisabled {
		if err := webhookv1alpha1.SetupTalosUpgradeWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "TalosUpgrade")
			os.Exit(1)
		}
	}
	if err := (&servicescontroller.FrameServiceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("services-frameservice"), //nolint:staticcheck
		Registry: serviceRegistry,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "services-frameservice")
		os.Exit(1)
	}
	if os.Getenv(enableWebhooksEnv) != webhooksDisabled {
		if err := webhookservicesv1alpha1.SetupFrameServiceWebhookWithManager(mgr, serviceRegistry); err != nil {
			setupLog.Error(err, "Failed to create webhook", "webhook", "FrameService")
			os.Exit(1)
		}
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
