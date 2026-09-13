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
}

// diskDriveDoc is one DiskDrives/{id}/ document.
type diskDriveDoc struct {
	Model                  string   `json:"Model"`
	SerialNumber           string   `json:"SerialNumber"`
	Location               string   `json:"Location"`
	CapacityGB             int32    `json:"CapacityGB"`
	InterfaceType          string   `json:"InterfaceType"`
	MediaType              string   `json:"MediaType"`
	DiskDriveStatusReasons []string `json:"DiskDriveStatusReasons"`
	Status                 struct {
		Health string `json:"Health"`
		State  string `json:"State"`
	} `json:"Status"`
}
