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

import "time"

// The types in this file mirror the on-the-wire shape of an iLO4's Redfish
// responses. They are deliberately separate from the exported value types in
// types.go: the wire shape carries Redfish/Hp idiosyncrasies (pointer fields
// for values a firmware revision may omit, "@odata.id" links, Hp-specific
// field names) that callers of this package should never see.

type odataRef struct {
	ODataID string `json:"@odata.id"`
}

type statusBlock struct {
	Health string `json:"Health"`
	State  string `json:"State"`
}

type serviceRootJSON struct {
	Systems  odataRef `json:"Systems"`
	Chassis  odataRef `json:"Chassis"`
	Managers odataRef `json:"Managers"`
}

type collectionJSON struct {
	Count   int        `json:"Members@odata.count"`
	Members []odataRef `json:"Members"`
}

type computerSystemJSON struct {
	ID            string `json:"Id"`
	Manufacturer  string `json:"Manufacturer"`
	Model         string `json:"Model"`
	SerialNumber  string `json:"SerialNumber"`
	PowerState    string `json:"PowerState"`
	IndicatorLED  string `json:"IndicatorLED"`
	BiosVersion   string `json:"BiosVersion"`
	MemorySummary struct {
		TotalSystemMemoryGiB int32 `json:"TotalSystemMemoryGiB"`
	} `json:"MemorySummary"`
	ProcessorSummary struct {
		Count int32  `json:"Count"`
		Model string `json:"Model"`
	} `json:"ProcessorSummary"`
	Actions struct {
		Reset struct {
			Target string `json:"target"`
		} `json:"#ComputerSystem.Reset"`
	} `json:"Actions"`
	LogServices odataRef `json:"LogServices"`
}

type temperatureJSON struct {
	Name                   string      `json:"Name"`
	ReadingCelsius         int32       `json:"ReadingCelsius"`
	UpperThresholdCritical *int32      `json:"UpperThresholdCritical"`
	Status                 statusBlock `json:"Status"`
}

type fanJSON struct {
	FanName        string      `json:"FanName"`
	CurrentReading int32       `json:"CurrentReading"`
	Units          string      `json:"Units"`
	Status         statusBlock `json:"Status"`
}

type thermalJSON struct {
	Temperatures []temperatureJSON `json:"Temperatures"`
	Fans         []fanJSON         `json:"Fans"`
}

type powerControlJSON struct {
	PowerConsumedWatts int32 `json:"PowerConsumedWatts"`
}

type powerSupplyJSON struct {
	Name                 string      `json:"Name"`
	Status               statusBlock `json:"Status"`
	LastPowerOutputWatts int32       `json:"LastPowerOutputWatts"`
}

type powerJSON struct {
	PowerControl  []powerControlJSON `json:"PowerControl"`
	PowerSupplies []powerSupplyJSON  `json:"PowerSupplies"`
}

type managerJSON struct {
	FirmwareVersion string `json:"FirmwareVersion"`
	Model           string `json:"Model"`
}

type logEntryJSON struct {
	ID       string    `json:"Id"`
	Severity string    `json:"Severity"`
	Created  time.Time `json:"Created"`
	Message  string    `json:"Message"`
}

type logCollectionJSON struct {
	// A pointer distinguishes "the firmware didn't send a count" from "the
	// firmware sent a count of zero" — LogTotal falls back to len(Members)
	// only in the former case.
	Count   *int           `json:"Members@odata.count"`
	Members []logEntryJSON `json:"Members"`
}

// copyInt32 returns a fresh pointer holding the same value as p, or nil if p
// is nil. Used when moving a decoded, possibly-absent number into an exported
// value type so the exported struct never aliases the decoder's memory.
func copyInt32(p *int32) *int32 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// lastPathSegment returns the final non-empty segment of a Redfish
// "@odata.id" style path, e.g. "/redfish/v1/Systems/1/" -> "1".
func lastPathSegment(path string) string {
	end := len(path)
	for end > 0 && path[end-1] == '/' {
		end--
	}
	start := end
	for start > 0 && path[start-1] != '/' {
		start--
	}
	return path[start:end]
}
