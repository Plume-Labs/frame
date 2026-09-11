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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// This file holds the second layer of C1 — the pagination fix left open when
// the rest of the whole-branch review was closed, because at the time no one
// had confirmed the real machine's pagination protocol. It arrived from the
// hardware's owner on 2026-09-11: `?page=N` (one-based, HP-proprietary,
// `$skip`/`$top` are silently ignored), 175 entries across 6 pages ascending
// by id (30 per page, 25 on the last), `links.NextPage` absent only on the
// last page, and a deliberately out-of-range `?page=` answers HTTP 400 with
// the true maximum page in `MessageArgs[2]`. Plus a defect nobody had seen:
// 15 of the 175 entries carry no `Created` at all and must fall back to
// `Oem.Hp.Updated`. See PROVENANCE.md's "Pagination du journal IML" section
// and this task's report entry for the full writeup.

func loadIMLFixture(t *testing.T, name string) logCollectionJSON {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	var col logCollectionJSON
	if err := json.Unmarshal(body, &col); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return col
}

// The last page is recognised by links.NextPage's absence, not by its own
// Count: page 1 and page 2 both carry Count 30 (their own Items length) and
// a NextPage pointing at the next page; page 6 carries Count 25 — a
// different number from every other page — and no NextPage at all. A
// collector keyed off "Count == 30" or "Count == the first page's Count"
// would misjudge every one of these three files.
func TestLogCollectionRecognisesTheLastPageByNextPageAbsenceNotCount(t *testing.T) {
	page1 := loadIMLFixture(t, "systems_1_logservices_iml_entries.json")
	page2 := loadIMLFixture(t, "systems_1_logservices_iml_entries_page2.json")
	page6 := loadIMLFixture(t, "systems_1_logservices_iml_entries_page6.json")

	for _, tc := range []struct {
		name         string
		col          logCollectionJSON
		wantTotal    int
		wantItems    int
		wantNextPage *logNextPageJSON
	}{
		{"page1", page1, 175, 30, &logNextPageJSON{Count: 30, Page: 2}},
		{"page2", page2, 175, 30, &logNextPageJSON{Count: 30, Page: 3}},
		{"page6", page6, 175, 25, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.col.Total != tc.wantTotal {
				t.Errorf("Total = %d, want %d", tc.col.Total, tc.wantTotal)
			}
			if len(tc.col.Items) != tc.wantItems {
				t.Errorf("len(Items) = %d, want %d", len(tc.col.Items), tc.wantItems)
			}
			switch {
			case tc.wantNextPage == nil && tc.col.Links.NextPage != nil:
				t.Errorf("Links.NextPage = %+v, want absent (this is the last page)", *tc.col.Links.NextPage)
			case tc.wantNextPage != nil && tc.col.Links.NextPage == nil:
				t.Errorf("Links.NextPage absent, want %+v", *tc.wantNextPage)
			case tc.wantNextPage != nil && tc.col.Links.NextPage != nil && *tc.col.Links.NextPage != *tc.wantNextPage:
				t.Errorf("Links.NextPage = %+v, want %+v", *tc.col.Links.NextPage, *tc.wantNextPage)
			}
		})
	}

	// Members really is link-only on this iLO4 — a collector iterating
	// Members would make 175 requests where 6 suffice.
	for _, m := range page1.Members {
		if m.ID != "" || m.Severity != "" || m.Message != "" {
			t.Errorf("page1 Members entry decoded a body (%+v); Members should be link stubs on this iLO4", m)
		}
	}
}

// Direct unit coverage of the Created/Oem.Hp.Updated fallback, independent
// of readLog's sort/retain logic.
func TestLogEntryEffectiveCreatedFallsBackToOemUpdated(t *testing.T) {
	created := time.Date(2022, 10, 10, 17, 38, 0, 0, time.UTC)
	updated := time.Date(2026, 9, 10, 20, 36, 0, 0, time.UTC)

	cases := []struct {
		name string
		e    logEntryJSON
		want time.Time
	}{
		{"Created present", logEntryJSON{Created: created}, created},
		{
			"Created absent, Oem.Hp.Updated present",
			func() logEntryJSON {
				var e logEntryJSON
				e.Oem.Hp.Updated = updated
				return e
			}(),
			updated,
		},
		{"neither present", logEntryJSON{}, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.e.effectiveCreated(); !got.Equal(tc.want) {
				t.Errorf("effectiveCreated() = %v, want %v", got, tc.want)
			}
		})
	}

	// The "neither present" case must not render as the Unix epoch — a
	// fallback of the form time.Unix(0, 0) would print 1970-01-01, which is
	// indistinguishable in a UI from a real (if implausible) timestamp. The
	// zero time.Time prints as year 1, which nothing on real hardware could
	// ever produce, and is what this package should surface instead.
	if zero := (logEntryJSON{}).effectiveCreated(); zero.Year() == 1970 {
		t.Errorf("effectiveCreated() on an entry with neither timestamp rendered as the Unix epoch (%v); want the zero time.Time, not 1970", zero)
	}
}

// TestProbeRetainsTheNewestEntriesAcrossIMLPagination wires a mux that
// answers the IML Entries collection according to the real protocol
// PROVENANCE.md records: no page (or page=1) serves the real first-page
// capture, page=176 (Total+1 for this 175-entry log — see
// fetchLastLogPage) answers the captured 400, and page=6 serves the real
// last-page capture. Any other page value is a test failure: this
// package's chosen strategy is a direct jump, and requesting any page
// other than the deliberately-out-of-range probe and the resolved last
// page means it is walking instead, which is exactly the unbounded request
// growth this task was told to avoid.
func TestProbeRetainsTheNewestEntriesAcrossIMLPagination(t *testing.T) {
	page1Body, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", "systems_1_logservices_iml_entries.json"))
	if err != nil {
		t.Fatalf("read page1 fixture: %v", err)
	}
	page6Body, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", "systems_1_logservices_iml_entries_page6.json"))
	if err != nil {
		t.Fatalf("read page6 fixture: %v", err)
	}
	outOfRangeBody, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", "iml_entries_page_out_of_range_400.json"))
	if err != nil {
		t.Fatalf("read out-of-range fixture: %v", err)
	}

	routes := realRoutes("postcomplete")
	delete(routes, "/redfish/v1/Systems/1/LogServices/IML/Entries/")

	mux := http.NewServeMux()
	for p, file := range routes {
		body, ferr := os.ReadFile(filepath.Join("testdata", "ilo4-real", file))
		if ferr != nil {
			t.Fatalf("fixture %s: %v", file, ferr)
		}
		mux.HandleFunc(p+"{$}", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}

	oobRequests, page6Requests, otherPageRequests := 0, 0, 0
	mux.HandleFunc("/redfish/v1/Systems/1/LogServices/IML/Entries/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch page := r.URL.Query().Get("page"); page {
		case "":
			_, _ = w.Write(page1Body)
		case "176": // Total(175)+1 — the guaranteed-out-of-range probe.
			oobRequests++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(outOfRangeBody)
		case "6":
			page6Requests++
			_, _ = w.Write(page6Body)
		default:
			otherPageRequests++
			t.Errorf("unexpected ?page=%s requested — the jump strategy should only ever request the out-of-range probe and the resolved last page", page)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	if oobRequests != 1 {
		t.Errorf("out-of-range probe requested %d times, want exactly 1", oobRequests)
	}
	if page6Requests != 1 {
		t.Errorf("last page requested %d times, want exactly 1", page6Requests)
	}
	if otherPageRequests != 0 {
		t.Errorf("%d requests hit a page other than the probe and page 6 — see the failure above", otherPageRequests)
	}

	// The retained entries must be page 6's (the newest, ids 151-175), never
	// page 1's (the oldest, ids 1-30, dated 2021-2022 — the exact failure
	// this task exists to close).
	if len(snap.Log) != EventLogRetainCount {
		t.Fatalf("len(Log) = %d, want %d", len(snap.Log), EventLogRetainCount)
	}
	byID := map[string]LogEntry{}
	for _, e := range snap.Log {
		byID[e.ID] = e
	}
	for _, oldID := range []string{"1", "29", "30"} {
		if _, ok := byID[oldID]; ok {
			t.Errorf("retained the oldest page's id %s — pagination did not jump to the last page", oldID)
		}
	}
	if _, ok := byID["175"]; !ok {
		t.Errorf("id 175 (the newest entry on the machine) is not among the retained entries")
	}
	if snap.Log[0].ID != "175" {
		t.Errorf("Log[0].ID = %q, want %q (newest first)", snap.Log[0].ID, "175")
	}

	// Five of page 6's 25 entries (151, 152, 153, 158, 165) carry no Created
	// at all — they must still be present, and must carry their
	// Oem.Hp.Updated fallback (2026-09-10T20:36:00Z), not a zero time.
	for _, id := range []string{"151", "152", "153", "158", "165"} {
		e, ok := byID[id]
		if !ok {
			t.Errorf("entry %s (no Created, Oem.Hp.Updated fallback) is missing from the retained log", id)
			continue
		}
		wantFallback := "2026-09-10T20:36:00Z"
		if got := e.Created.UTC().Format("2006-01-02T15:04:05Z"); got != wantFallback {
			t.Errorf("entry %s Created = %q, want the Oem.Hp.Updated fallback %q", id, got, wantFallback)
		}
	}

	// The real, active faults on this machine (a failing Smart Storage
	// battery, a masked drive) must be present and unaltered — these are not
	// fixture noise, they are the reason someone would be looking at this
	// screen. id 173 is a live "313-HPE Smart Storage Battery 1 Failure"
	// Warning.
	battery, ok := byID["173"]
	if !ok {
		t.Fatalf("id 173 (the live Smart Storage Battery failure) is missing from the retained log")
	}
	if battery.Severity != "Warning" {
		t.Errorf("entry 173 Severity = %q, want %q — a real active fault, not noise to be dropped", battery.Severity, "Warning")
	}

	if snap.LogPossiblyStale {
		t.Errorf("LogPossiblyStale = true, want false — the jump to the last page succeeded")
	}
}

// TestReadLogKeepsTheFirstPageWhenTheLastPageCannotBeReached is requirement
// 4's negative case made concrete: if the out-of-range probe does not answer
// the way this iLO4 does — here, a server that always returns page 1
// regardless of ?page=, the shape a firmware ignoring the query parameter
// entirely would produce — readLog must not fabricate a page, mis-jump, or
// fail the whole Probe. It falls back to the first page, correctly read, per
// the task's own instruction: "reading only the last page correctly is far
// better than reading six pages wrongly" applies just as much to falling
// back to the first page correctly when the last one can't be confirmed.
func TestReadLogKeepsTheFirstPageWhenTheLastPageCannotBeReached(t *testing.T) {
	routes := realRoutes("postcomplete")
	srv := serveFixturesFrom(t, "ilo4-real", routes)

	snap, err := insecureClient(srv.URL).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	// serveFixturesFrom's handler ignores query strings entirely (Go's
	// ServeMux matches on path only), so the out-of-range probe gets back
	// page 1's own body with HTTP 200 — not the 400 discoverMaxLogPage
	// requires — and the jump is correctly abandoned. LogTotal still comes
	// from the machine's own Members@odata.count (175), but the retained
	// entries are whatever the first page held: the newest of the returned
	// page, not a wrong assembly of pages that were never actually reached.
	if snap.LogTotal != 175 {
		t.Errorf("LogTotal = %d, want 175 even though the jump was abandoned", snap.LogTotal)
	}
	if len(snap.Log) != EventLogRetainCount {
		t.Fatalf("len(Log) = %d, want %d", len(snap.Log), EventLogRetainCount)
	}
	if snap.Log[0].ID != "29" {
		t.Errorf("Log[0].ID = %q, want %q — the newest entry of the first page (ids 1-30), "+
			"confirming the fallback used page 1 rather than fabricating page 6's content", snap.Log[0].ID, "29")
	}
	if !snap.LogPossiblyStale {
		t.Error("LogPossiblyStale = false, want true — the jump to the last page was abandoned, " +
			"and a caller reading only Log/LogTotal has no other way to tell these are the oldest entries")
	}
}

// TestReadLogFlagsPossiblyStaleAgainstDivergentBMCErrorShapes is the four
// scenarios the round-2 review exercised: a 400 whose body carries a
// different MessageID than Base.0.10.QueryParameterOutOfRange, a 400 with
// no parseable body at all, a plain 500, and a 404 on the out-of-range
// ?page= request. In every one of them, before LogPossiblyStale existed,
// Probe returned a nil error and a fully-populated Log/LogCounts/LogTotal
// built from page 1 — the machine's *oldest* 25 entries, indistinguishable
// in the snapshot from a correct read of the newest 25. Each case here
// confirms Probe still succeeds (a firmware quirk in the pagination
// discovery step must not fail the whole probe) but LogPossiblyStale is now
// true, and the retained entries are still page 1's (proving the fallback
// didn't silently succeed some other way).
func TestReadLogFlagsPossiblyStaleAgainstDivergentBMCErrorShapes(t *testing.T) {
	page1Body, err := os.ReadFile(filepath.Join("testdata", "ilo4-real", "systems_1_logservices_iml_entries.json"))
	if err != nil {
		t.Fatalf("read page1 fixture: %v", err)
	}

	cases := []struct {
		name    string
		respond func(w http.ResponseWriter)
	}{
		{
			name: "400 with a different MessageID",
			respond: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"Messages":[{"MessageID":"Base.0.10.GeneralError","MessageArgs":["oops"]}]}`))
			},
		},
		{
			name: "400 with no body",
			respond: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusBadRequest)
			},
		},
		{
			name: "500",
			respond: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusInternalServerError)
			},
		},
		{
			name: "404 on ?page=N",
			respond: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusNotFound)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := realRoutes("postcomplete")
			delete(routes, "/redfish/v1/Systems/1/LogServices/IML/Entries/")
			mux := http.NewServeMux()
			for p, file := range routes {
				body, ferr := os.ReadFile(filepath.Join("testdata", "ilo4-real", file))
				if ferr != nil {
					t.Fatalf("fixture %s: %v", file, ferr)
				}
				mux.HandleFunc(p+"{$}", func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(body)
				})
			}
			mux.HandleFunc("/redfish/v1/Systems/1/LogServices/IML/Entries/{$}", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("page") == "" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(page1Body)
					return
				}
				// Every non-empty ?page= — the out-of-range probe (176) and,
				// were the implementation to regress into walking pages, any
				// other page number too — gets this scenario's divergent
				// response.
				tc.respond(w)
			})
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
			srv := httptest.NewTLSServer(mux)
			t.Cleanup(srv.Close)

			snap, err := insecureClient(srv.URL).Probe(context.Background())
			if err != nil {
				t.Fatalf("Probe: %v (a firmware quirk in the pagination-discovery step must not fail the whole probe)", err)
			}
			if !snap.LogPossiblyStale {
				t.Error("LogPossiblyStale = false, want true — the jump could not be confirmed against this BMC's error shape")
			}
			if len(snap.Log) != EventLogRetainCount {
				t.Fatalf("len(Log) = %d, want %d", len(snap.Log), EventLogRetainCount)
			}
			if snap.Log[0].ID != "29" {
				t.Errorf("Log[0].ID = %q, want %q — page 1's newest id, confirming the fallback "+
					"used page 1 (the machine's oldest page) rather than fabricating anything else", snap.Log[0].ID, "29")
			}
		})
	}
}

// TestLogEntrySortGuardDiscriminatesOnPage6 is the RED/GREEN proof this
// task's brief asked for: it runs the same "sort newest first, keep the top
// N" logic readLog uses over the real, decoded page 6 capture, once with a
// sort key that ignores the Oem.Hp.Updated fallback (RED — what readLog did
// before this fix, and what it would do again if the guard were removed)
// and once with the real effectiveCreated (GREEN). N is deliberately smaller
// than page 6's own 25 entries (EventLogRetainCount) so the guard's effect
// is visible in this test regardless of the fact that this particular
// machine's last page happens to be exactly the retain count — with N=25 no
// entry would ever be dropped by either sort key, which would prove nothing.
func TestLogEntrySortGuardDiscriminatesOnPage6(t *testing.T) {
	page6 := loadIMLFixture(t, "systems_1_logservices_iml_entries_page6.json")
	items := page6.Items
	if len(items) != 25 {
		t.Fatalf("page 6 fixture has %d Items, want 25 (has PROVENANCE.md's capture changed?)", len(items))
	}

	noCreatedIDs := map[string]bool{"151": true, "152": true, "153": true, "158": true, "165": true}
	for id := range noCreatedIDs {
		found := false
		for _, it := range items {
			if it.ID == id {
				found = true
				if !it.Created.IsZero() {
					t.Fatalf("entry %s has a Created value in the fixture; the premise of this test (it has none) no longer holds", id)
				}
			}
		}
		if !found {
			t.Fatalf("entry %s not found in page 6 fixture", id)
		}
	}

	const keepTop = 20 // < 25, so the guard's effect is observable.

	sortedByKey := func(key func(logEntryJSON) time.Time) []logEntryJSON {
		sorted := append([]logEntryJSON(nil), items...)
		sort.Slice(sorted, func(i, j int) bool { return key(sorted[i]).After(key(sorted[j])) })
		if len(sorted) > keepTop {
			sorted = sorted[:keepTop]
		}
		return sorted
	}
	survivorIDs := func(entries []logEntryJSON) map[string]bool {
		ids := make(map[string]bool, len(entries))
		for _, e := range entries {
			ids[e.ID] = true
		}
		return ids
	}

	// RED: a sort key that reads Created directly, exactly as readLog did
	// before this fix — the 5 no-Created entries carry the zero time.Time,
	// which sorts oldest, so a fixed top-20 cut pushes every one of them out.
	brokenKey := func(e logEntryJSON) time.Time { return e.Created }
	brokenSurvivors := survivorIDs(sortedByKey(brokenKey))
	brokenSurvivorCount := 0
	for id := range noCreatedIDs {
		if brokenSurvivors[id] {
			brokenSurvivorCount++
			t.Errorf("RED run: entry %s survived the broken (Created-only) sort's top %d; expected it to be pushed out", id, keepTop)
		}
	}
	t.Logf("RED run (Created-only sort, top %d): %d of %d no-Created entries survived (want 0)",
		keepTop, brokenSurvivorCount, len(noCreatedIDs))

	// GREEN: the real effectiveCreated fallback — the same 5 entries carry
	// Oem.Hp.Updated (2026-09-10T20:36:00Z), which ranks them ahead of all
	// but the machine's 3 newest dated entries, so they belong in the top 20
	// and the guard keeps them there.
	greenSurvivors := survivorIDs(sortedByKey(logEntryJSON.effectiveCreated))
	greenSurvivorCount := 0
	for id := range noCreatedIDs {
		if greenSurvivors[id] {
			greenSurvivorCount++
		} else {
			t.Errorf("GREEN run: entry %s did not survive the guarded sort's top %d; expected the Oem.Hp.Updated fallback to rank it in", id, keepTop)
		}
	}
	t.Logf("GREEN run (effectiveCreated sort, top %d): %d of %d no-Created entries survived (want %d)",
		keepTop, greenSurvivorCount, len(noCreatedIDs), len(noCreatedIDs))
}
