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

// Package agent implements the Frame node agent's observe half: reading back
// what the node actually reports, never what Frame last wrote. See
// docs/superpowers/specs/2026-08-16-frame-node-tuning-operator-design.md for
// the founding bug (a drop-in on disk with systemd still reporting the old
// value) that this package exists to never reproduce.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// memoryKSMCachePath is where the agent's apply half (Task 3) caches the
// result of `systemctl show <unit> -p MemoryKSM --value` after every apply
// and restart cycle. Observe reads this cache, never the systemd drop-in
// file: the drop-in only says what was asked for, and the whole point of this
// design is to report what took effect instead.
const memoryKSMCachePath = "run/frame-agent/memory-ksm"

const (
	ksmGeneralProfitPath   = "sys/kernel/mm/ksm/general_profit"
	ksmPagesSharingPath    = "sys/kernel/mm/ksm/pages_sharing"
	tunedActiveProfilePath = "etc/tuned/active_profile"
)

// Observe reads the node's measured tuning state under root (normally "/",
// or a fixture tree in tests) and returns it as an ObservedTuning. A file
// that does not exist yields that field's zero value and no error: an
// unconfigured node — the normal case for ksm.enabled=false — is not a
// broken one. A file that exists but cannot be parsed is reported as an
// error, since that does indicate something is actually wrong on the node.
func Observe(root string) (framev1beta1.ObservedTuning, error) {
	memoryKSM, err := readBool(filepath.Join(root, memoryKSMCachePath))
	if err != nil {
		return framev1beta1.ObservedTuning{}, fmt.Errorf("reading MemoryKSM cache: %w", err)
	}
	generalProfit, err := readInt64(filepath.Join(root, ksmGeneralProfitPath))
	if err != nil {
		return framev1beta1.ObservedTuning{}, fmt.Errorf("reading KSM general_profit: %w", err)
	}
	pagesSharing, err := readInt64(filepath.Join(root, ksmPagesSharingPath))
	if err != nil {
		return framev1beta1.ObservedTuning{}, fmt.Errorf("reading KSM pages_sharing: %w", err)
	}
	tunedProfile, err := readString(filepath.Join(root, tunedActiveProfilePath))
	if err != nil {
		return framev1beta1.ObservedTuning{}, fmt.Errorf("reading tuned active profile: %w", err)
	}

	return framev1beta1.ObservedTuning{
		KSM: &framev1beta1.ObservedKSM{
			MemoryKSM:     memoryKSM,
			GeneralProfit: generalProfit,
			PagesSharing:  pagesSharing,
		},
		TunedProfile: tunedProfile,
	}, nil
}

// readBool reads a file whose content is systemd's "yes"/"no" boolean
// vocabulary (as produced by `systemctl show -p MemoryKSM --value`). A
// missing file means "no", not an error.
func readBool(path string) (bool, error) {
	s, ok, err := readTrimmed(path)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return s == "yes" || s == "true", nil
}

// readInt64 reads a file whose content is a single sysfs integer counter. A
// missing file means 0, not an error.
func readInt64(path string) (int64, error) {
	s, ok, err := readTrimmed(path)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: not an integer: %q", path, s)
	}
	return v, nil
}

// readString reads a plain text file, trimmed. A missing file means "", not
// an error.
func readString(path string) (string, error) {
	s, _, err := readTrimmed(path)
	return s, err
}

// readTrimmed reads path and trims surrounding whitespace, reporting ok=false
// (never an error) when the file does not exist.
func readTrimmed(path string) (s string, ok bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("%s: %w", path, err)
	}
	return strings.TrimSpace(string(raw)), true, nil
}
