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

package updater

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Components are the images a release may name, in the order an update applies
// them: CRDs and the operator first (the rest depends on them), the console
// last (it is the one the person is looking at). The updater itself is applied
// by the operator, never by itself (spec §4.1), so it is not in this list.
var Components = []string{"controller", "provisiond", "authd", "uiproxy", "agent", "ui"}

var digestRef = regexp.MustCompile(`^[a-z0-9][a-z0-9./_:-]*@sha256:[0-9a-f]{64}$`)

// Manifest is release.json as hack/release-manifest.sh writes it.
type Manifest struct {
	Schema         int               `json:"schema"`
	Version        string            `json:"version"`
	Channel        string            `json:"channel"`
	PublishedAt    string            `json:"publishedAt"`
	Images         map[string]string `json:"images"`
	MinFrom        *string           `json:"minFrom"`
	RollbackSafeTo *string           `json:"rollbackSafeTo"`
}

// ParseManifest decodes and validates a manifest. It refuses unknown fields and
// unknown components: a manifest this code does not fully understand must not
// drive a write. Every image has to be pinned by digest, because a tag can be
// moved after the signature was made.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	if m.Schema != 1 {
		return Manifest{}, fmt.Errorf("manifest: unsupported schema %d", m.Schema)
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return Manifest{}, fmt.Errorf("manifest: version: %w", err)
	}
	if m.Channel != "stable" && m.Channel != "beta" {
		return Manifest{}, fmt.Errorf("manifest: unknown channel %q", m.Channel)
	}
	if strings.Contains(m.Version, "-") != (m.Channel == "beta") {
		return Manifest{}, fmt.Errorf("manifest: version %s does not belong on channel %s", m.Version, m.Channel)
	}
	known := map[string]bool{}
	for _, c := range Components {
		known[c] = true
	}
	if len(m.Images) == 0 {
		return Manifest{}, fmt.Errorf("manifest: no images")
	}
	for name, ref := range m.Images {
		if !known[name] {
			return Manifest{}, fmt.Errorf("manifest: unknown component %q", name)
		}
		if !digestRef.MatchString(ref) {
			return Manifest{}, fmt.Errorf("manifest: image %s is not pinned by digest: %q", name, ref)
		}
	}
	for label, p := range map[string]*string{"minFrom": m.MinFrom, "rollbackSafeTo": m.RollbackSafeTo} {
		if p == nil {
			continue
		}
		if _, err := ParseVersion(*p); err != nil {
			return Manifest{}, fmt.Errorf("manifest: %s: %w", label, err)
		}
	}
	return m, nil
}

// Release is what the planner knows about one release: the manifest's content
// plus what the updater established about it.
type Release struct {
	Version        string
	Images         map[string]string
	MinFrom        string
	RollbackSafeTo string
	// Revoked: withdrawn by the channel. Verified: manifest and image
	// signatures checked out against the pinned identity.
	Revoked  bool
	Verified bool
}

// ReleaseFromManifest wraps a parsed manifest. Verified starts false: only the
// code that actually checked the signatures may set it.
func ReleaseFromManifest(m Manifest) Release {
	r := Release{Version: m.Version, Images: m.Images}
	if m.MinFrom != nil {
		r.MinFrom = *m.MinFrom
	}
	if m.RollbackSafeTo != nil {
		r.RollbackSafeTo = *m.RollbackSafeTo
	}
	return r
}

// sortedComponents returns the names of images in apply order, then any others
// alphabetically (none can exist after ParseManifest; this keeps the planner
// deterministic for a hand-built Release).
func sortedComponents(images map[string]string) []string {
	rank := map[string]int{}
	for i, c := range Components {
		rank[c] = i
	}
	names := make([]string, 0, len(images))
	for n := range images {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		ri, iok := rank[names[i]]
		rj, jok := rank[names[j]]
		switch {
		case iok && jok:
			return ri < rj
		case iok:
			return true
		case jok:
			return false
		}
		return names[i] < names[j]
	})
	return names
}
