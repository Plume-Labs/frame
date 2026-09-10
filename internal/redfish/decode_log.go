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

// This file holds the IML event log decoding split out of decode.go to keep
// that file under this lot's line ceiling, the same reason decode_memory.go
// was split out. It is the one resource whose on-the-wire shape most
// diverges from what this lot was originally written against (C1): the
// captured iLO4 does not embed entry bodies under "Members" the way the
// DMTF collection schema (and this lot's own synthetic ilo4 fixture) does —
// it puts them in a top-level "Items" array, HP's legacy REST collection
// shape, leaving "Members" as bare "@odata.id" link stubs.

type logEntryJSON struct {
	ID       string    `json:"Id"`
	Severity string    `json:"Severity"`
	Created  time.Time `json:"Created"`
	Message  string    `json:"Message"`
}

type logCollectionJSON struct {
	// A pointer distinguishes "the firmware didn't send a count" from "the
	// firmware sent a count of zero" — LogTotal falls back to len(members)
	// only in the former case.
	Count *int `json:"Members@odata.count"`

	// Members is the DMTF shape this lot was originally written against:
	// each member is the entry body itself. The captured iLO4 does not use
	// this shape for the IML — its Members are "@odata.id" link stubs (no
	// Severity/Created/Message at all) — so decoding them as logEntryJSON
	// silently produces entries that are present but blank. It is kept,
	// and used as readLog's fallback, because the synthetic ilo4 fixture
	// (and, per the design doc, possibly a newer iLO) does embed entry
	// bodies directly under Members.
	Members []logEntryJSON `json:"Members"`

	// Items is where the captured iLO4 actually puts the entry bodies —
	// HP's legacy REST collection shape (MemberType "LogEntry.1"), sitting
	// alongside the DMTF-shaped Members link stubs in the same document.
	// readLog prefers this whenever it is non-empty.
	Items []logEntryJSON `json:"Items"`
}

// logServiceJSON mirrors the LogService document a LogServices collection
// member points at (e.g. /redfish/v1/Systems/1/LogServices/IML/), read only
// for its ClearLog action target — discovered rather than guessed from a
// string template, per testdata/ilo4-real/systems_1_logservices_iml.json.
type logServiceJSON struct {
	Actions struct {
		ClearLog struct {
			Target string `json:"target"`
		} `json:"#LogService.ClearLog"`
	} `json:"Actions"`
}

// readLog fills in the log fields of snap. LogCounts and Log are built from
// every returned entry body; LogTotal is the collection's own count when the
// firmware sent one, and len(members) otherwise. Log keeps at most the
// EventLogRetainCount newest entries, newest first.
//
// LogTotal and LogCounts describe only the page this call actually got back,
// never "the whole log" — see EventLogRetainCount's doc comment and I2. On
// the captured iLO4 that page is 30 of 175 entries, and — because the
// collection returns no nextLink and the 30 are the oldest 30 (ids 1-30,
// dated 2021-2022) rather than the newest — sorting and taking the newest 25
// of *that page* is not the same as the newest 25 of the whole log. That
// second problem needs a pagination parameter this iLO4 has not yet been
// confirmed to accept, and is deliberately not guessed at here.
func (c *client) readLog(ctx context.Context, path string, snap *Snapshot) error {
	var col logCollectionJSON
	err := c.get(ctx, path, &col)
	switch {
	case err == nil:
		// Items is HP's legacy REST shape and is where the captured iLO4
		// actually puts entry bodies; Members there are "@odata.id" link
		// stubs that decode to blank logEntryJSON values. Members is used
		// only when Items is empty — the synthetic ilo4 fixture (and,
		// untested, a newer iLO per the design doc) embeds entry bodies
		// directly under Members instead.
		members := col.Items
		if len(members) == 0 {
			members = col.Members
		}

		if col.Count != nil {
			snap.LogTotal = *col.Count
		} else {
			snap.LogTotal = len(members)
		}

		entries := make([]LogEntry, 0, len(members))
		for _, m := range members {
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
		if len(entries) > EventLogRetainCount {
			entries = entries[:EventLogRetainCount]
		}
		snap.Log = entries
		return nil
	case errors.Is(err, errNotFound):
		return nil
	default:
		return fmt.Errorf("redfish: read log: %w", err)
	}
}
