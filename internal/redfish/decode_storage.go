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

package redfish

import (
	"encoding/json"
	"math"
)

// HPE's SmartStorage tree is OEM, not standard Redfish, and it spells its
// link objects in lowercase "links" with an "href" inside — not the
// standard "Links"/"@odata.id". Both spellings appear on the same machine,
// which is why these documents get their own decode file rather than
// reusing decode.go's shapes.

type hpLink struct {
	Href string `json:"href"`
}

// smartStorageDoc is /redfish/v1/Systems/{id}/SmartStorage/.
type smartStorageDoc struct {
	Links struct {
		ArrayControllers hpLink `json:"ArrayControllers"`
		HostBusAdapters  hpLink `json:"HostBusAdapters"`
	} `json:"links"`
}

// hpCollection is any HpSmartStorage*Collection: the members are standard
// @odata.id entries even though the link objects around them are not.
type hpCollection struct {
	Members []struct {
		ODataID string `json:"@odata.id"`
	} `json:"Members"`
}

// arrayControllerDoc is one ArrayControllers/{id}/ document. PhysicalDrives
// is the collection of every drive attached to the controller, configured
// or not; UnconfiguredDrives is a subset of it and is deliberately not
// walked — on the captured machine all eight drives appear in both, and
// reading the subset would silently drop any drive that is part of a
// logical volume.
type arrayControllerDoc struct {
	Links struct {
		PhysicalDrives hpLink `json:"PhysicalDrives"`
	} `json:"links"`

	Location                string          `json:"Location"`
	Model                   string          `json:"Model"`
	FirmwareVersion         hpFirmware      `json:"FirmwareVersion"`
	BackupPowerSourceStatus string          `json:"BackupPowerSourceStatus"`
	CacheModuleStatus       json.RawMessage `json:"CacheModuleStatus"`
	Status                  struct {
		Health string `json:"Health"`
	} `json:"Status"`
}

// hpFirmware is HPE's {"Current": {"VersionString": "..."}} shape, shared
// by controller and disk documents.
type hpFirmware struct {
	Current struct {
		VersionString string `json:"VersionString"`
	} `json:"Current"`
}

// cacheStatus reads CacheModuleStatus, which the captured controller (in
// HBA mode) does not carry at all. HPE schemas are seen with either a bare
// string or a {"Health": ...} object there; both are accepted, anything
// else reads as "not reported" rather than failing the storage walk.
func (d arrayControllerDoc) cacheStatus() string {
	if len(d.CacheModuleStatus) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(d.CacheModuleStatus, &s) == nil {
		return s
	}
	var obj struct {
		Health string `json:"Health"`
	}
	if json.Unmarshal(d.CacheModuleStatus, &obj) == nil {
		return obj.Health
	}
	return ""
}

// diskDriveDoc is one DiskDrives/{id}/ document.
type diskDriveDoc struct {
	Model                  string     `json:"Model"`
	SerialNumber           string     `json:"SerialNumber"`
	Location               string     `json:"Location"`
	CapacityGB             int32      `json:"CapacityGB"`
	InterfaceType          string     `json:"InterfaceType"`
	MediaType              string     `json:"MediaType"`
	DiskDriveStatusReasons []string   `json:"DiskDriveStatusReasons"`
	FirmwareVersion        hpFirmware `json:"FirmwareVersion"`
	RotationalSpeedRpm     *float64   `json:"RotationalSpeedRpm"`
	InterfaceSpeedMbps     *float64   `json:"InterfaceSpeedMbps"`

	// The three below are nullable on the wire and decoded as float so a
	// firmware that sends 35.0 does not fail the whole storage walk. See
	// reported() for why 0 is dropped.
	CurrentTemperatureCelsius         *float64 `json:"CurrentTemperatureCelsius"`
	PowerOnHours                      *float64 `json:"PowerOnHours"`
	SSDEnduranceUtilizationPercentage *float64 `json:"SSDEnduranceUtilizationPercentage"`
	Status                            struct {
		Health string `json:"Health"`
		State  string `json:"State"`
	} `json:"Status"`
}

// reported turns an HPE nullable counter into "reported or not". Null,
// absent, zero, negative, a fraction below one, NaN, and out-of-range all
// read as nil.
// Zero is the load-bearing case: the captured iLO4 answers
// CurrentTemperatureCelsius 0 for every SAS drive, including drives it
// separately reports a 62 °C maximum for, so a 0 is a controller that did
// not measure — publishing it as a temperature would show a running disk
// at freezing point.
func reported(v *float64) *int32 {
	// One guard, "< 1": it rejects 0, negatives, and a fraction that would
	// truncate to 0 in the conversion below.
	if v == nil || math.IsNaN(*v) || *v < 1 || *v > math.MaxInt32 {
		return nil
	}
	out := int32(*v)
	return &out
}

// plainInt32 is for fields where 0 already means "not reported" and the
// API omits it (rotation speed, link speed).
func plainInt32(v *float64) int32 {
	if p := reported(v); p != nil {
		return *p
	}
	return 0
}
