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

package controller

import (
	"context"
	"crypto/tls"
	"fmt"

	siderosX509 "github.com/siderolabs/crypto/x509"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	talosclientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// buildTalosClient creates an authenticated Talos gRPC client from a K8s Secret.
//
// The Secret must contain three keys (both forms accepted):
//   - "ca"  or "ca.crt"  — PEM-encoded CA certificate
//   - "crt" or "tls.crt" — PEM-encoded client certificate
//   - "key" or "tls.key" — PEM-encoded client private key
func buildTalosClient(ctx context.Context, kube client.Client, namespace, endpoint string, ref corev1.SecretReference) (*talosclient.Client, error) {
	ns := ref.Namespace
	if ns == "" {
		ns = namespace
	}

	var secret corev1.Secret
	if err := kube.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ns}, &secret); err != nil {
		return nil, fmt.Errorf("reading talos secret %s/%s: %w", ns, ref.Name, err)
	}

	ca := firstNonEmpty(secret.Data["ca"], secret.Data["ca.crt"])
	crt := firstNonEmpty(secret.Data["crt"], secret.Data["tls.crt"])
	key := firstNonEmpty(secret.Data["key"], secret.Data["tls.key"])

	if len(ca) == 0 || len(crt) == 0 || len(key) == 0 {
		return nil, fmt.Errorf("secret %s/%s must contain ca (or ca.crt), crt (or tls.crt), key (or tls.key)", ns, ref.Name)
	}

	cfg := talosclientconfig.NewConfig("frame", []string{endpoint}, ca, &siderosX509.PEMEncodedCertificateAndKey{
		Crt: crt,
		Key: key,
	})

	return talosclient.New(ctx, talosclient.WithConfig(cfg))
}

// buildTalosInsecureClient creates a Talos gRPC client for maintenance mode (port 50000).
//
// Talos maintenance mode intentionally uses a per-boot ephemeral self-signed certificate
// whose CA cannot be known ahead of time — this mirrors what `talosctl --insecure` does.
// Connections are only made to nodes on the private provisioning network before they join
// the cluster; once provisioned the node requires mutual TLS via buildTalosClient.
//
//nolint:gosec // InsecureSkipVerify required for Talos maintenance mode (see above)
func buildTalosInsecureClient(ctx context.Context, ip string) (*talosclient.Client, error) {
	return talosclient.New(ctx,
		talosclient.WithTLSConfig(&tls.Config{InsecureSkipVerify: true}),
		talosclient.WithEndpoints(ip+":50000"),
	)
}

func firstNonEmpty(vals ...[]byte) []byte {
	for _, v := range vals {
		if len(v) > 0 {
			return v
		}
	}
	return nil
}
