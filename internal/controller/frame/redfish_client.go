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
	"crypto/x509"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	"github.com/rmocq/frame/internal/redfish"
)

// BuildRedfishClient mirrors buildTalosClient: it is the one place the
// controller reads a credential, so the Secret never travels further.
func BuildRedfishClient(ctx context.Context, kube client.Client, namespace string, bmc framev1beta1.BMCSpec) (redfish.Client, error) {
	var sec corev1.Secret
	if err := kube.Get(ctx, types.NamespacedName{Name: bmc.CredentialsRef, Namespace: namespace}, &sec); err != nil {
		return nil, err
	}
	user := string(sec.Data["username"])
	pass := string(sec.Data["password"])
	if user == "" || pass == "" {
		return nil, fmt.Errorf("secret %s/%s must carry both username and password", namespace, bmc.CredentialsRef)
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case bmc.TLS.InsecureSkipVerify:
		tlsCfg.InsecureSkipVerify = true
	case bmc.TLS.CABundleRef != "":
		var cm corev1.ConfigMap
		if err := kube.Get(ctx, types.NamespacedName{Name: bmc.TLS.CABundleRef, Namespace: namespace}, &cm); err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cm.Data["ca.crt"])) {
			return nil, fmt.Errorf("configmap %s/%s has no usable ca.crt", namespace, bmc.TLS.CABundleRef)
		}
		tlsCfg.RootCAs = pool
	}

	return redfish.New("https://"+bmc.Address, user, pass, tlsCfg), nil
}
