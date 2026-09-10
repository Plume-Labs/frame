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
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// The types in this file mirror the on-the-wire shape of an iLO4's Redfish
// responses. They are deliberately separate from the exported value types in
// types.go: the wire shape carries Redfish/Hp idiosyncrasies (pointer fields
// for values a firmware revision may omit, "@odata.id" links, Hp-specific
// field names) that callers of this package should never see. The
// resource-walking methods below decode each document into the exported
// value types and, per resource, decide whether a 404 is tolerable (see
// client.go's Probe for which failures are not).

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
			// AllowableValues is what this specific machine's firmware
			// accepts as ResetType, not what the Redfish spec allows in
			// general. The captured iLO4 lists On, ForceOff, ForceRestart,
			// Nmi and PushPowerButton — no GracefulShutdown (Finding 2).
			AllowableValues []string `json:"ResetType@Redfish.AllowableValues"`
		} `json:"#ComputerSystem.Reset"`
	} `json:"Actions"`
	LogServices odataRef `json:"LogServices"`

	// Oem.Hp.PostState is where an iLO4 reports power-on-self-test progress.
	// It is the only reliable indicator, together with PowerState, of
	// whether Thermal/Power sensor readings describe the machine now or an
	// earlier moment the BMC is still replaying (Finding 1 — see
	// Snapshot.SensorsTrustworthy). iLO4 names the OEM block `Hp`; `Hpe` is
	// iLO5 and is not decoded here.
	Oem struct {
		Hp struct {
			PostState string `json:"PostState"`
		} `json:"Hp"`
	} `json:"Oem"`
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

// processorJSON, memoryJSON and ethernetInterfaceJSON mirror the standard
// (non-OEM) Redfish Processor, Memory and EthernetInterface schemas. These
// are core Redfish, not HPE extensions, so an iLO4 exposes them under
// Systems/{id}/Processors, Systems/{id}/Memory and
// Systems/{id}/EthernetInterfaces.

type processorJSON struct {
	Socket       string      `json:"Socket"`
	Model        string      `json:"Model"`
	TotalCores   int32       `json:"TotalCores"`
	TotalThreads int32       `json:"TotalThreads"`
	Status       statusBlock `json:"Status"`
}

// memoryJSON is declared in decode_memory.go, alongside readMemory and
// memoryModuleFrom: the older HPE schema an iLO4 actually sends (Finding 3)
// needs enough explanation that keeping it with its own type and reader,
// away from the rest of this file's DMTF-schema decoding, is worth a
// dedicated file rather than pushing decode.go over this lot's line ceiling.

type ethernetInterfaceJSON struct {
	Name       string      `json:"Name"`
	MACAddress string      `json:"MACAddress"`
	Status     statusBlock `json:"Status"`
}

// readServiceRoot fetches /redfish/v1/. A failure here always fails whatever
// the caller is doing — every other resource is reached through it.
func (c *client) readServiceRoot(ctx context.Context) (serviceRootJSON, error) {
	var root serviceRootJSON
	if err := c.get(ctx, "/redfish/v1/", &root); err != nil {
		return serviceRootJSON{}, fmt.Errorf("redfish: read service root: %w", err)
	}
	return root, nil
}

// firstMember fetches a Redfish collection and returns the "@odata.id" of
// its first member, or "" if the collection has none. collectionPath == ""
// (the service root didn't advertise the collection at all) is treated the
// same as an empty collection rather than an error.
func (c *client) firstMember(ctx context.Context, collectionPath string) (string, error) {
	if collectionPath == "" {
		return "", nil
	}
	var col collectionJSON
	if err := c.get(ctx, collectionPath, &col); err != nil {
		return "", err
	}
	if len(col.Members) == 0 {
		return "", nil
	}
	return col.Members[0].ODataID, nil
}

// collectionMembers fetches a Redfish collection and returns every member's
// "@odata.id", in the order the collection listed them.
func (c *client) collectionMembers(ctx context.Context, collectionPath string) ([]string, error) {
	var col collectionJSON
	if err := c.get(ctx, collectionPath, &col); err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(col.Members))
	for _, m := range col.Members {
		refs = append(refs, m.ODataID)
	}
	return refs, nil
}

// resolveSystemPath walks service root -> Systems collection -> first member
// and returns that member's path (e.g. "/redfish/v1/Systems/1/"). It is used
// by every method that needs to reach the system document, independently of
// Probe, since callers may invoke Reset/ClearLog/SetIndicatorLED without ever
// having called Probe first.
func (c *client) resolveSystemPath(ctx context.Context) (string, error) {
	root, err := c.readServiceRoot(ctx)
	if err != nil {
		return "", err
	}
	path, err := c.firstMember(ctx, root.Systems.ODataID)
	if err != nil {
		return "", fmt.Errorf("redfish: resolve system: %w", err)
	}
	if path == "" {
		return "", fmt.Errorf("redfish: no system members")
	}
	return path, nil
}

// readManager fills in the BMC firmware version from /redfish/v1/Managers/1/.
// A 404 means this firmware doesn't expose it; the field is left empty.
func (c *client) readManager(ctx context.Context, snap *Snapshot) error {
	var mgr managerJSON
	err := c.get(ctx, "/redfish/v1/Managers/1/", &mgr)
	switch {
	case err == nil:
		snap.Inventory.BMCFirmware = mgr.FirmwareVersion
		return nil
	case errors.Is(err, errNotFound):
		return nil
	default:
		return fmt.Errorf("redfish: read manager: %w", err)
	}
}

// readSensors resolves the first chassis member and, if one exists, reads
// its Thermal and Power sub-resources. A 404 at any step — the chassis
// collection, or either sub-resource — leaves Sensors at its zero value
// rather than failing Probe.
func (c *client) readSensors(ctx context.Context, chassisCollection string, snap *Snapshot) error {
	chassisPath, err := c.firstMember(ctx, chassisCollection)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return nil
		}
		return fmt.Errorf("redfish: resolve chassis: %w", err)
	}
	if chassisPath == "" {
		return nil
	}

	if err := c.readThermal(ctx, chassisPath, snap); err != nil {
		return err
	}
	return c.readPower(ctx, chassisPath, snap)
}

func (c *client) readThermal(ctx context.Context, chassisPath string, snap *Snapshot) error {
	var th thermalJSON
	err := c.get(ctx, chassisPath+"Thermal/", &th)
	switch {
	case err == nil:
		for _, t := range th.Temperatures {
			snap.Sensors.Temperatures = append(snap.Sensors.Temperatures, Temperature{
				Name:          t.Name,
				Celsius:       t.ReadingCelsius,
				UpperCritical: copyInt32(t.UpperThresholdCritical),
				Health:        t.Status.Health,
			})
		}
		for _, f := range th.Fans {
			snap.Sensors.Fans = append(snap.Sensors.Fans, Fan{
				Name:    f.FanName,
				Reading: f.CurrentReading,
				Units:   f.Units,
				Health:  f.Status.Health,
			})
		}
		return nil
	case errors.Is(err, errNotFound):
		return nil
	default:
		return fmt.Errorf("redfish: read thermal: %w", err)
	}
}

func (c *client) readPower(ctx context.Context, chassisPath string, snap *Snapshot) error {
	var pw powerJSON
	err := c.get(ctx, chassisPath+"Power/", &pw)
	switch {
	case err == nil:
		if len(pw.PowerControl) > 0 {
			snap.Sensors.PowerConsumedWatts = pw.PowerControl[0].PowerConsumedWatts
		}
		for _, ps := range pw.PowerSupplies {
			snap.Sensors.PowerSupplies = append(snap.Sensors.PowerSupplies, PowerSupply{
				Name:                 ps.Name,
				Health:               ps.Status.Health,
				State:                ps.Status.State,
				LastPowerOutputWatts: ps.LastPowerOutputWatts,
			})
		}
		return nil
	case errors.Is(err, errNotFound):
		return nil
	default:
		return fmt.Errorf("redfish: read power: %w", err)
	}
}

// readLog fills in the log fields of snap. LogCounts and Log are built from
// every returned member; LogTotal is the collection's own count when the
// firmware sent one, and len(Members) otherwise. Log keeps at most the 25
// newest entries, newest first.
func (c *client) readLog(ctx context.Context, path string, snap *Snapshot) error {
	var col logCollectionJSON
	err := c.get(ctx, path, &col)
	switch {
	case err == nil:
		if col.Count != nil {
			snap.LogTotal = *col.Count
		} else {
			snap.LogTotal = len(col.Members)
		}

		entries := make([]LogEntry, 0, len(col.Members))
		for _, m := range col.Members {
			snap.LogCounts[m.Severity]++
			entries = append(entries, LogEntry{
				ID:       m.ID,
				Severity: m.Severity,
				Message:  m.Message,
				Created:  m.Created,
			})
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Created.After(entries[j].Created)
		})
		if len(entries) > 25 {
			entries = entries[:25]
		}
		snap.Log = entries
		return nil
	case errors.Is(err, errNotFound):
		return nil
	default:
		return fmt.Errorf("redfish: read log: %w", err)
	}
}

// readComponentInventory walks the three standard (non-OEM) Redfish
// component collections this package knows how to read: Processors, Memory,
// and EthernetInterfaces. Each is independent — a 404 on one collection
// still lets the others populate.
func (c *client) readComponentInventory(ctx context.Context, systemPath string, snap *Snapshot) error {
	id := lastPathSegment(systemPath)
	base := "/redfish/v1/Systems/" + id + "/"

	if err := c.readProcessors(ctx, base+"Processors/", snap); err != nil {
		return err
	}
	if err := c.readMemory(ctx, base+"Memory/", snap); err != nil {
		return err
	}
	return c.readEthernetInterfaces(ctx, base+"EthernetInterfaces/", snap)
}

func (c *client) readProcessors(ctx context.Context, collectionPath string, snap *Snapshot) error {
	refs, err := c.collectionMembers(ctx, collectionPath)
	switch {
	case err == nil:
		for _, ref := range refs {
			var p processorJSON
			if gerr := c.get(ctx, ref, &p); gerr != nil {
				if errors.Is(gerr, errNotFound) {
					continue
				}
				return fmt.Errorf("redfish: read processor %s: %w", ref, gerr)
			}
			snap.Inventory.Processors = append(snap.Inventory.Processors, Processor{
				Socket:  p.Socket,
				Model:   p.Model,
				Cores:   p.TotalCores,
				Threads: p.TotalThreads,
			})
		}
		return nil
	case errors.Is(err, errNotFound):
		return nil
	default:
		return fmt.Errorf("redfish: read processors: %w", err)
	}
}

// readMemory is declared in decode_memory.go.

func (c *client) readEthernetInterfaces(ctx context.Context, collectionPath string, snap *Snapshot) error {
	refs, err := c.collectionMembers(ctx, collectionPath)
	switch {
	case err == nil:
		for _, ref := range refs {
			var e ethernetInterfaceJSON
			if gerr := c.get(ctx, ref, &e); gerr != nil {
				if errors.Is(gerr, errNotFound) {
					continue
				}
				return fmt.Errorf("redfish: read network adapter %s: %w", ref, gerr)
			}
			snap.Inventory.NetworkAdapters = append(snap.Inventory.NetworkAdapters, NetworkAdapter{
				Name:   e.Name,
				MAC:    e.MACAddress,
				Status: e.Status.Health,
			})
		}
		return nil
	case errors.Is(err, errNotFound):
		return nil
	default:
		return fmt.Errorf("redfish: read ethernet interfaces: %w", err)
	}
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
