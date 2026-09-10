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
	"path"
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

// logEntryJSON, logCollectionJSON, logServiceJSON and readLog are declared in
// decode_log.go, split out for the same reason decode_memory.go was: keeping
// this file, and that one, well under this lot's line ceiling.

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

// statusStateAbsent is the Status.State value an iLO4 uses for a sensor
// socket that simply isn't populated — a temperature probe or fan bay that
// isn't there, as opposed to one that is present and reads a low or zero
// value. The captured chassis_1_thermal_postcomplete.json carries 21 of 46
// temperature entries and 5 of 8 fans in this state; rendering them as
// readings produces phantom rows at 0 with no health, sharing a blank React
// key (C2). This is deliberately narrower than the Status.State guard this
// lot rejected elsewhere (Finding 1): that guard would have to separate a
// live reading from a replayed one, and State is "Enabled" in all three
// power states for a real, present sensor — it says nothing about
// liveness. What it does say, reliably, is whether the socket is populated
// at all, which is the question here.
const statusStateAbsent = "Absent"

func (c *client) readThermal(ctx context.Context, chassisPath string, snap *Snapshot) error {
	var th thermalJSON
	err := c.get(ctx, joinPath(chassisPath, "Thermal"), &th)
	switch {
	case err == nil:
		for _, t := range th.Temperatures {
			if t.Status.State == statusStateAbsent {
				continue
			}
			snap.Sensors.Temperatures = append(snap.Sensors.Temperatures, Temperature{
				Name:          t.Name,
				Celsius:       t.ReadingCelsius,
				UpperCritical: sanitizeUpperCritical(t.UpperThresholdCritical),
				Health:        t.Status.Health,
			})
		}
		for _, f := range th.Fans {
			if f.Status.State == statusStateAbsent {
				continue
			}
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
	err := c.get(ctx, joinPath(chassisPath, "Power"), &pw)
	switch {
	case err == nil:
		if len(pw.PowerControl) > 0 {
			snap.Sensors.PowerConsumedWatts = pw.PowerControl[0].PowerConsumedWatts
		}
		for _, ps := range pw.PowerSupplies {
			if ps.Status.State == statusStateAbsent {
				continue
			}
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

// readComponentInventory walks the three standard (non-OEM) Redfish
// component collections this package knows how to read: Processors, Memory,
// and EthernetInterfaces. Each is independent — a 404 on one collection
// still lets the others populate.
//
// It joins onto systemPath itself — the path the caller already resolved via
// the Systems collection's "@odata.id" — rather than rebuilding
// "/redfish/v1/Systems/<id>/" from scratch. Reconstructing the base path was
// itself a bug the two literal segments couldn't reveal here, since this
// package only knows a numeric-looking iLO4 machine, but it duplicated a
// path this method was already handed, and would silently diverge from it
// the moment a Systems collection member's id wasn't a bare number.
func (c *client) readComponentInventory(ctx context.Context, systemPath string, snap *Snapshot) error {
	if err := c.readProcessors(ctx, joinPath(systemPath, "Processors"), snap); err != nil {
		return err
	}
	if err := c.readMemory(ctx, joinPath(systemPath, "Memory"), snap); err != nil {
		return err
	}
	return c.readEthernetInterfaces(ctx, joinPath(systemPath, "EthernetInterfaces"), snap)
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

// sanitizeUpperCritical copies a decoded UpperThresholdCritical, treating a
// non-positive value as absent (nil) rather than as a real threshold. The
// captured iLO4 sends UpperThresholdCritical: 0 on 23 of 46 temperature
// sensors — including two live ones, "10-P/S 1" and "11-P/S 2", both
// Health: OK at 40 C — and a non-nil zero makes src/lib/machines.ts's
// temperatureSeverity read every reading at or above 0 C as critical (C2).
// A genuine "alarm at 0 C or above" threshold would already be tripped on
// every machine in the room, so 0 here means "this firmware did not
// populate a threshold", never a real value to alarm on.
func sanitizeUpperCritical(p *int32) *int32 {
	if p == nil || *p <= 0 {
		return nil
	}
	v := *p
	return &v
}

// joinPath joins a Redfish "@odata.id"-style base path with one or more
// further segments, the way normalizePath's own doc comment already
// promises callers: independent of whether base carries a trailing slash.
// decode.go and client.go used to build these paths by string
// concatenation (chassisPath+"Thermal/", ODataID+"IML/Entries/", a
// hand-rebuilt "/redfish/v1/Systems/"+id+"/"+…) — correct only when the
// "@odata.id" the BMC sent happened to carry a trailing slash, and silently
// wrong (a 404, swallowed as "this firmware doesn't expose it") the moment
// one doesn't. path.Join collapses the seam regardless of which shape the
// base arrived in; normalizePath still adds the final trailing slash before
// the request goes out.
func joinPath(base string, elem ...string) string {
	return path.Join(append([]string{base}, elem...)...)
}
