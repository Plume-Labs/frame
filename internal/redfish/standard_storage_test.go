package redfish

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStandardStorageListsDrivesOfABMCWithoutSmartStorage drives the DMTF
// fallback: a non-HPE system document (no Oem.Hp link) that advertises a
// Storage collection must yield its drives, and a drive document that fails
// must cost only that drive, never the probe.
func TestStandardStorageListsDrivesOfABMCWithoutSmartStorage(t *testing.T) {
	json := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/redfish/v1/{$}", json(`{"Systems":{"@odata.id":"/redfish/v1/Systems"},"Chassis":{"@odata.id":"/redfish/v1/Chassis"},"Managers":{"@odata.id":"/redfish/v1/Managers"}}`))
	mux.HandleFunc("/redfish/v1/Systems/{$}", json(`{"Members":[{"@odata.id":"/redfish/v1/Systems/S1"}]}`))
	mux.HandleFunc("/redfish/v1/Systems/S1/{$}", json(`{"PowerState":"On","Storage":{"@odata.id":"/redfish/v1/Systems/S1/Storage"}}`))
	mux.HandleFunc("/redfish/v1/Systems/S1/Storage/{$}", json(`{"Members":[{"@odata.id":"/redfish/v1/Systems/S1/Storage/C1"}]}`))
	mux.HandleFunc("/redfish/v1/Systems/S1/Storage/C1/{$}", json(`{"Drives":[{"@odata.id":"/d/1"},{"@odata.id":"/d/2"}]}`))
	mux.HandleFunc("/d/1/{$}", json(`{"Name":"Disk.Bay.0","Model":"ST500","SerialNumber":"SN1","Revision":"R1","MediaType":"HDD","Protocol":"SAS","CapacityBytes":500107862016,"RotationSpeedRPM":10000,"Status":{"Health":"OK"}}`))
	mux.HandleFunc("/d/2/{$}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(snap.Inventory.Drives) != 1 {
		t.Fatalf("Drives = %+v, want exactly the one readable drive", snap.Inventory.Drives)
	}
	d := snap.Inventory.Drives[0]
	if d.SerialNumber != "SN1" || d.SizeGB != 500 || d.RotationSpeedRPM != 10000 || d.Health != "OK" {
		t.Errorf("drive = %+v, want SN1 / 500 GB / 10000 rpm / OK", d)
	}
}
