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

// Package updater is the decision core of the in-console update
// (docs/specs/2026-11-24-mise-a-jour-en-un-clic.md). It decides, from what is
// running and what a signed release says, whether an update may start and what
// it would change. It reads and calls nothing: no client, no registry, no
// clock. That is deliberate, so that the rules which keep an update safe can be
// tested exhaustively before any code is allowed to write to a cluster.
package updater

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version, without the leading "v".
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
}

// ParseVersion parses "1.2.3" or "1.2.3-beta.1". Build metadata ("+...") and a
// leading "v" are refused: the release manifest never carries them, and
// accepting them would give two spellings to one version.
func ParseVersion(s string) (Version, error) {
	var v Version
	if s == "" || strings.ContainsAny(s, "+ \t\n") || strings.HasPrefix(s, "v") {
		return v, fmt.Errorf("not a version: %q", s)
	}
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("not a version: %q", s)
	}
	nums := [3]*uint64{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("not a version: %q", s)
		}
		n, err := strconv.ParseUint(p, 10, 63)
		if err != nil {
			return Version{}, fmt.Errorf("not a version: %q", s)
		}
		*nums[i] = n
	}
	if hasPre {
		if pre == "" {
			return Version{}, fmt.Errorf("not a version: %q", s)
		}
		v.Pre = strings.Split(pre, ".")
		for _, id := range v.Pre {
			if id == "" {
				return Version{}, fmt.Errorf("not a version: %q", s)
			}
			for _, c := range id {
				if c != '-' && (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
					return Version{}, fmt.Errorf("not a version: %q", s)
				}
			}
		}
	}
	return v, nil
}

// Compare returns -1, 0 or 1 following semver precedence: a pre-release sorts
// before its release, numeric identifiers before alphanumeric ones, and a
// shorter identifier list before a longer one when the common part is equal.
func (v Version) Compare(o Version) int {
	for _, c := range [][2]uint64{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if c[0] != c[1] {
			if c[0] < c[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		if c := comparePreID(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.Pre) < len(o.Pre):
		return -1
	case len(v.Pre) > len(o.Pre):
		return 1
	}
	return 0
}

func comparePreID(a, b string) int {
	an, aerr := strconv.ParseUint(a, 10, 63)
	bn, berr := strconv.ParseUint(b, 10, 63)
	switch {
	case aerr == nil && berr == nil:
		if an < bn {
			return -1
		} else if an > bn {
			return 1
		}
		return 0
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// CompareVersions parses and compares two version strings.
func CompareVersions(a, b string) (int, error) {
	va, err := ParseVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := ParseVersion(b)
	if err != nil {
		return 0, err
	}
	return va.Compare(vb), nil
}
