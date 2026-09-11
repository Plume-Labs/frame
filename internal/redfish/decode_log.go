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
	"strconv"
	"strings"
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

	// Oem.Hp.Updated is the fallback timestamp for the 15 of 175 entries on
	// the real machine that carry no Created at all — every one of them a
	// "POST Information: ... DIMM could not be authenticated" entry (see
	// PROVENANCE.md's "Pagination du journal IML" section and
	// testdata/ilo4-real/systems_1_logservices_iml_entries_page6.json, ids
	// 151/152/153/158/165). Updated is present on all 175 entries in that
	// capture, including the ones with Created, so it is always safe to read
	// even when it will not end up being used.
	Oem struct {
		Hp struct {
			Updated time.Time `json:"Updated"`
		} `json:"Hp"`
	} `json:"Oem"`
}

// effectiveCreated is the timestamp readLog sorts and displays by: Created
// when the firmware sent one, Oem.Hp.Updated otherwise. An entry with
// neither (which no capture has shown, but nothing here guarantees can't
// happen) falls through to time.Time's zero value — year 1, not the Unix
// epoch a naive `time.Unix(0, 0)` fallback would render as 1970 — so it
// sorts to the oldest position instead of vanishing or panicking.
func (e logEntryJSON) effectiveCreated() time.Time {
	if !e.Created.IsZero() {
		return e.Created
	}
	return e.Oem.Hp.Updated
}

// logNextPageJSON mirrors the HP-proprietary links.NextPage object a
// paginated IML Entries collection carries on every page but the last —
// its absence, not its Count, is the only reliable end-of-log signal (see
// fetchLastLogPage). Count is the current page's own size and is not a
// stable page size to assume elsewhere: the last page's Count is 25 against
// every other page's 30 on the captured machine.
type logNextPageJSON struct {
	Count int `json:"count"`
	Page  int `json:"page"`
}

type logCollectionJSON struct {
	// A pointer distinguishes "the firmware didn't send a count" from "the
	// firmware sent a count of zero" — LogTotal falls back to len(members)
	// only in the former case. Unlike Total below, this is the DMTF
	// "Members@odata.count" field and — per the capture — carries the same
	// value (175) on every page, same as Total.
	Count *int `json:"Members@odata.count"`

	// Total is the HP-proprietary field carrying the log's real entry count,
	// independent of how many entries this particular page returned. It is
	// what fetchLastLogPage uses to compute a page number guaranteed to be
	// out of range (see its doc comment) without assuming a page size.
	Total int `json:"Total"`

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

	Links struct {
		NextPage *logNextPageJSON `json:"NextPage"`
	} `json:"links"`
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
// EventLogRetainCount newest entries, newest first, ranked by
// logEntryJSON.effectiveCreated (Created, falling back to Oem.Hp.Updated for
// the entries that carry no Created at all).
//
// LogTotal and LogCounts describe only the page this call actually used,
// never "the whole log" — see EventLogRetainCount's doc comment and I2. On
// the captured iLO4 the IML holds 175 entries across 6 HP-proprietary
// ?page=N pages (30 per page, 25 on the last), ascending by id, so the
// default first page this method used to stop at is the *oldest* 30 (ids
// 1-30, dated 2021-2022) — sorting and taking the newest 25 of that page
// produced a plausible-looking but wrong answer, never the true newest 25.
// fetchLastLogPage is what closes that gap: when the first page's own
// links.NextPage says there is more, it jumps straight to the true last
// page (see its doc comment for why a jump rather than a hop-by-hop walk)
// and readLog builds Log/LogCounts/LogTotal from that page instead.
func (c *client) readLog(ctx context.Context, path string, snap *Snapshot) error {
	var col logCollectionJSON
	err := c.get(ctx, path, &col)
	switch {
	case err == nil:
		// A non-nil NextPage means this page is not the log's last one, and
		// — per PROVENANCE.md's "Pagination du journal IML" — this iLO4
		// orders entries ascending by id, so the newest entries live on the
		// last page, not this one. If the jump fails for any reason, the
		// page already in hand is kept rather than left half-assembled: see
		// fetchLastLogPage's doc comment.
		if col.Links.NextPage != nil {
			if last, jumpErr := c.fetchLastLogPage(ctx, path, col.Total); jumpErr == nil {
				col = last
			}
		}

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
				Created:  m.effectiveCreated(),
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

// queryParameterOutOfRangeMessageID is the MessageID an iLO4 sends when a
// ?page= value exceeds the log's real page count (captured in
// testdata/ilo4-real/iml_entries_page_out_of_range_400.json). discoverMaxLogPage
// checks for it before trusting a 400 body's MessageArgs, so an unrelated
// 400 — nothing this package intentionally sends today, but not a
// possibility worth assuming away — can't be misread as a page count.
const queryParameterOutOfRangeMessageID = "Base.0.10.QueryParameterOutOfRange"

// queryOutOfRangeErrorJSON mirrors the body an iLO4 sends alongside HTTP 400
// for an out-of-range query parameter. Only the top-level Messages array is
// read; the "error"."@Message.ExtendedInfo" array the capture also carries
// duplicates the same content and is not decoded.
type queryOutOfRangeErrorJSON struct {
	Messages []struct {
		MessageID   string   `json:"MessageID"`
		MessageArgs []string `json:"MessageArgs"`
	} `json:"Messages"`
}

// fetchLastLogPage locates and fetches the IML log's true last page — the
// one holding the newest entries, since this iLO4 orders ascending by id —
// in exactly two requests, regardless of how many pages the log actually
// has. That bound is the point: this package is called from a reconcile
// loop on a fixed interval, and a hop-by-hop walk of links.NextPage (one
// request per page) would turn a machine with a much longer log into a
// request count that grows with the log's size — the "unbounded fetch"
// this task was explicit about avoiding. Landing on the exact page number
// in one extra request instead needs to know the log's total page count,
// which this BMC does not expose directly; discoverMaxLogPage gets it from
// the iLO4 itself rather than assuming a page size (this machine's pages
// are 30 entries except a 25-entry last page — a fixed size would already
// be wrong twice on this one machine) by requesting a page number
// guaranteed to be out of range and reading the true maximum back out of
// the resulting error.
//
// total+1 is that guaranteed-out-of-range page number: a real page cannot
// hold zero entries, so the log can never have more pages than it has
// entries, and asking for one page past that upper bound is always invalid
// regardless of how the log's pages actually happen to be sized.
//
// If total is not a usable bound, or the out-of-range probe does not come
// back the way PROVENANCE.md's capture says it will, this returns an error
// and readLog keeps the first page instead of guessing further — see its
// call site.
func (c *client) fetchLastLogPage(ctx context.Context, basePath string, total int) (logCollectionJSON, error) {
	if total <= 0 {
		return logCollectionJSON{}, fmt.Errorf("redfish: log reports more pages but no usable Total to bound a jump to the last one")
	}

	maxPage, err := c.discoverMaxLogPage(ctx, basePath, total+1)
	if err != nil {
		return logCollectionJSON{}, err
	}

	var last logCollectionJSON
	if err := c.get(ctx, logEntriesPageURL(basePath, maxPage), &last); err != nil {
		return logCollectionJSON{}, fmt.Errorf("redfish: read log page %d: %w", maxPage, err)
	}
	return last, nil
}

// discoverMaxLogPage asks for outOfRangePage — a page number the caller has
// already established is beyond the log's real last page — and reads the
// true maximum out of the iLO4's Base.0.10.QueryParameterOutOfRange error,
// whose third MessageArgs element is that maximum (captured in
// iml_entries_page_out_of_range_400.json: requesting page 7 of a 6-page log
// answers MessageArgs ["7","page","6"]).
func (c *client) discoverMaxLogPage(ctx context.Context, basePath string, outOfRangePage int) (int, error) {
	var errBody queryOutOfRangeErrorJSON
	err := c.get(ctx, logEntriesPageURL(basePath, outOfRangePage), &errBody)
	if err == nil {
		return 0, fmt.Errorf("redfish: page %d answered success, not the out-of-range error expected to bound the jump", outOfRangePage)
	}
	if !errors.Is(err, errBadRequest) {
		return 0, fmt.Errorf("redfish: probe for the log's last page: %w", err)
	}
	if len(errBody.Messages) == 0 || errBody.Messages[0].MessageID != queryParameterOutOfRangeMessageID {
		return 0, fmt.Errorf("redfish: page %d's error body did not carry %s", outOfRangePage, queryParameterOutOfRangeMessageID)
	}
	args := errBody.Messages[0].MessageArgs
	if len(args) < 3 {
		return 0, fmt.Errorf("redfish: %s had %d MessageArgs, want at least 3", queryParameterOutOfRangeMessageID, len(args))
	}
	maxPage, convErr := strconv.Atoi(args[2])
	if convErr != nil || maxPage <= 0 {
		return 0, fmt.Errorf("redfish: %s's max page %q is not a positive integer", queryParameterOutOfRangeMessageID, args[2])
	}
	return maxPage, nil
}

// logEntriesPageURL appends the HP-proprietary ?page=N query to basePath,
// ensuring a trailing slash first: the capture's own links.self.href always
// carries one before the "?" (".../Entries/?page=1"), and normalizePath
// leaves a path carrying a query string alone rather than adding one.
func logEntriesPageURL(basePath string, page int) string {
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	return fmt.Sprintf("%s?page=%d", basePath, page)
}
