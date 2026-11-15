package redfish

import (
	"context"
	"log/slog"
)

// storageCollectionDoc is the DMTF Storage collection of a ComputerSystem.
type storageCollectionDoc struct {
	Members []odataRef `json:"Members"`
}

// storageDoc is one DMTF Storage resource (a controller, or an enclosure
// presented as one). Newer firmware lists drives in Drives; very old ones
// have no such list and are left without drives here.
type storageDoc struct {
	Name   string     `json:"Name"`
	Drives []odataRef `json:"Drives"`
}

// standardDriveDoc is the DMTF Drive resource, as iDRAC 8/9 and other
// non-HPE BMCs serve it.
type standardDriveDoc struct {
	Name          string      `json:"Name"`
	Model         string      `json:"Model"`
	SerialNumber  string      `json:"SerialNumber"`
	Revision      string      `json:"Revision"`
	MediaType     string      `json:"MediaType"`
	Protocol      string      `json:"Protocol"`
	CapacityBytes *float64    `json:"CapacityBytes"`
	RotationSpeed *float64    `json:"RotationSpeedRPM"`
	Status        statusBlock `json:"Status"`
}

// readStandardStorage fills snap.Inventory.Drives from the DMTF
// Systems/{id}/Storage tree. It is the fallback for BMCs with no HPE
// SmartStorage tree (a Dell R320's iDRAC, for instance), which used to
// leave the inventory empty.
//
// Unlike readSmartStorage it never fails the probe: this path is new and
// runs against firmware this package has no capture of, so an unreadable
// branch (any error, not just a 404) costs that branch's drives and a debug
// line, never the power state, sensors and event log of the same snapshot.
func (c *client) readStandardStorage(ctx context.Context, storagePath string, snap *Snapshot) {
	if storagePath == "" {
		return
	}
	var coll storageCollectionDoc
	if err := c.get(ctx, storagePath, &coll); err != nil {
		slog.Debug("redfish: standard storage collection unreadable", "path", storagePath, "error", err)
		return
	}
	for _, member := range coll.Members {
		var st storageDoc
		if err := c.get(ctx, member.ODataID, &st); err != nil {
			slog.Debug("redfish: storage controller unreadable", "path", member.ODataID, "error", err)
			continue
		}
		for _, ref := range st.Drives {
			var d standardDriveDoc
			if err := c.get(ctx, ref.ODataID, &d); err != nil {
				slog.Debug("redfish: drive unreadable", "path", ref.ODataID, "error", err)
				continue
			}
			var sizeGB int32
			if d.CapacityBytes != nil {
				sizeGB = int32(*d.CapacityBytes / 1e9)
			}
			snap.Inventory.Drives = append(snap.Inventory.Drives, Drive{
				Name:             d.Name,
				Model:            d.Model,
				SizeGB:           sizeGB,
				Protocol:         d.Protocol,
				Health:           d.Status.Health,
				SerialNumber:     d.SerialNumber,
				Location:         d.Name,
				MediaType:        d.MediaType,
				Firmware:         d.Revision,
				RotationSpeedRPM: plainInt32(d.RotationSpeed),
			})
		}
	}
}
