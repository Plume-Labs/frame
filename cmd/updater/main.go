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

// Command updater applies FrameUpdate requests: it moves Hearthrack's own
// components to a signed FrameRelease, checks that each comes up healthy, and
// puts them back if one does not (docs/specs/2026-11-24-mise-a-jour-en-un-clic.md).
//
// Its write surface is the fixed table in internal/updater.DefaultWorkloads:
// the image of named containers, nothing else. It never creates or deletes a
// workload and never writes FrameRelease.status.verified, which belongs to
// whatever creates releases after checking their signature.
package main

import (
	"flag"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	"github.com/Plume-Labs/frame/internal/updater"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(framev1beta1.AddToScheme(scheme))
}

func main() {
	var (
		namespace   string
		probeAddr   string
		metricsAddr string
		leaderElect bool
	)
	flag.StringVar(&namespace, "namespace", "frame-system", "Namespace of the FrameUpdate and FrameRelease objects.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address the probe endpoint binds to.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "Address the metrics endpoint binds to; 0 disables it.")
	flag.BoolVar(&leaderElect, "leader-elect", true, "Only one updater may act at a time.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("updater")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          leaderElect,
		LeaderElectionID:        "frame-updater.plume-labs.io",
		LeaderElectionNamespace: namespace,
	})
	if err != nil {
		log.Error(err, "Could not create the manager")
		os.Exit(1)
	}

	// Reads that decide an update go straight to the API server, never through
	// the manager's cache: a cache one write behind must not be able to make
	// the updater believe a component is ready, or a request is still pending.
	direct, err := client.New(mgr.GetConfig(), client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "Could not create the API client")
		os.Exit(1)
	}

	r := &updater.Reconciler{
		Client:    direct,
		Namespace: namespace,
		Cluster: &updater.KubeCluster{
			Client:    direct,
			Workloads: updater.DefaultWorkloads(),
		},
		Activity: updater.ClusterActivity(direct),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		log.Error(err, "Could not set up the controller")
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "Could not add the health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "Could not add the readiness check")
		os.Exit(1)
	}

	log.Info("Starting the updater", "namespace", namespace)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "The updater stopped")
		os.Exit(1)
	}
}
