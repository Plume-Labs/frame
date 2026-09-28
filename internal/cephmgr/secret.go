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

package cephmgr

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SecretName is the Secret holding the dashboard account, in the manager's
// namespace. Keys: url (https://host:port), username, password, ca.crt
// (the dashboard's certificate, PEM), serverName (the name in it).
const SecretName = "frame-ceph-dashboard"

// SecretLoader reads the account from the Secret on every call, through a
// non-caching reader: the manager must not watch every Secret of the
// cluster to read one.
func SecretLoader(r client.Reader, namespace string) func(context.Context) (Config, error) {
	return func(ctx context.Context) (Config, error) {
		if namespace == "" {
			return Config{}, ErrNotConfigured
		}
		var s corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: SecretName}, &s); err != nil {
			if apierrors.IsNotFound(err) {
				return Config{}, ErrNotConfigured
			}
			return Config{}, fmt.Errorf("reading Secret %s/%s: %w", namespace, SecretName, err)
		}
		user := strings.TrimSpace(string(s.Data["username"]))
		pass := strings.TrimRight(string(s.Data["password"]), "\r\n")
		// NUL separates username from password in the account MAC
		// (accountMAC): with one inside, two different accounts could
		// share a MAC, and one's refusal would block, or free, the other.
		if strings.ContainsRune(user, 0) || strings.ContainsRune(pass, 0) {
			return Config{}, fmt.Errorf("Secret %s/%s: username or password contains a NUL byte; not used", namespace, SecretName)
		}
		return Config{
			BaseURL:    strings.TrimSpace(string(s.Data["url"])),
			Username:   user,
			Password:   pass,
			CAPEM:      s.Data["ca.crt"],
			ServerName: strings.TrimSpace(string(s.Data["serverName"])),
			Version:    s.ResourceVersion,
		}, nil
	}
}
