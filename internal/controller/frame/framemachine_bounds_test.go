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
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gothshoot/frame/internal/redfish"
)

// A BMC that reports more drives, or longer strings, than the CRD accepts
// must cost the excess only — never the whole status update.
func TestMapInventoryStaysWithinCRDBounds(t *testing.T) {
	long := strings.Repeat("é", 300)
	inv := &redfish.Inventory{Manufacturer: long, BMCFirmware: long}
	for range 40 {
		inv.Drives = append(inv.Drives, redfish.Drive{Firmware: long, SerialNumber: long, Location: long, MediaType: long})
	}
	for range 12 {
		inv.StorageControllers = append(inv.StorageControllers, redfish.StorageController{Model: long, CacheStatus: long})
	}
	out := mapInventory(inv)

	if got := len(out.Drives); got != maxDrives {
		t.Fatalf("drives = %d, want %d", got, maxDrives)
	}
	if got := len(out.StorageControllers); got != maxStorageControllers {
		t.Fatalf("storageControllers = %d, want %d", got, maxStorageControllers)
	}
	checks := map[string]struct {
		v   string
		max int
	}{
		"manufacturer":         {out.Manufacturer, 256},
		"bmcFirmware":          {out.BMCFirmware, 128},
		"drive.firmware":       {out.Drives[0].Firmware, 64},
		"drive.serialNumber":   {out.Drives[0].SerialNumber, 128},
		"drive.location":       {out.Drives[0].Location, 64},
		"drive.mediaType":      {out.Drives[0].MediaType, 32},
		"controller.model":     {out.StorageControllers[0].Model, 256},
		"controller.cacheStat": {out.StorageControllers[0].CacheStatus, 64},
	}
	for name, c := range checks {
		if n := utf8.RuneCountInString(c.v); n != c.max {
			t.Errorf("%s: %d characters, want clipped to %d", name, n, c.max)
		}
		if !utf8.ValidString(c.v) {
			t.Errorf("%s: clipped inside a character", name)
		}
	}
}

func TestClipKeepsShortStringsIntact(t *testing.T) {
	if got := clip("P440ar", 64); got != "P440ar" {
		t.Fatalf("clip changed a short string: %q", got)
	}
}
