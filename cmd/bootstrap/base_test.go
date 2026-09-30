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
	"path/filepath"
	"strings"
	"testing"

	"github.com/gothshoot/frame/internal/provision"
)

// A mirror may change where the ISO comes from, never which ISO it is: the
// checksum stays DefaultBase's, only an https URL ending in the same file
// name is accepted, and an empty variable leaves the default untouched.
func TestBaseSourceOverrideOnlyMovesTheURL(t *testing.T) {
	def := provision.DefaultBase()
	name := filepath.Base(def.URL)

	got, err := baseSource("")
	if err != nil || got != def {
		t.Fatalf("baseSource(\"\") = %+v, %v; want the default %+v", got, err, def)
	}

	mirror := "https://mirror.example.net/ubuntu-releases/24.04/" + name
	got, err = baseSource(mirror)
	if err != nil {
		t.Fatalf("baseSource(%q): %v", mirror, err)
	}
	if got.URL != mirror || got.SHA256 != def.SHA256 {
		t.Fatalf("baseSource(%q) = %+v; want URL %q with the default SHA256 %s", mirror, got, mirror, def.SHA256)
	}

	for _, bad := range []string{
		"http://mirror.example.net/24.04/" + name,
		"ftp://mirror.example.net/24.04/" + name,
		"https:///24.04/" + name,
		"https://mirror.example.net/24.04/ubuntu-24.04.4-live-server-amd64.iso",
		"https://mirror.example.net/24.04/" + name + "?x=1",
		"https://user:secret@mirror.example.net/24.04/" + name,
		"https://user@mirror.example.net/24.04/" + name,
		"http://user:secret@mirror.example.net/24.04/" + name,
		"https://user:sec ret@mirror.example.net/24.04/" + name,
		"https://mirror.example.net/24.04/" + name + "?token=secret",
		"https://mirror.example.net/24.04/" + name + "#secret",
		"https://mirror.example.net/24.04/%zzsecret/" + name,
	} {
		if _, err := baseSource(bad); err == nil || !strings.Contains(err.Error(), baseISOURLEnv) {
			t.Errorf("baseSource(%q) error = %v; want a refusal naming %s", bad, err, baseISOURLEnv)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("baseSource(%q) error %q repeats the password", bad, err)
		}
	}
}
