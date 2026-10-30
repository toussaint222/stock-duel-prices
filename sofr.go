package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"time"
)

// SOFRURL is the New York Fed's free public reference-rate API: the last 30 SOFR fixings. Thirty
// (not one) so a missed night, or a same-day revision, is repaired by the next run.
const SOFRURL = "https://markets.newyorkfed.org/api/rates/secured/sofr/last/30.json"

// SOFRCredit is shown wherever the app's BONDS (SOFR) pick appears.
const SOFRCredit = "SOFR rate: New York Fed"

// SOFRDays is how many calendar days v1/sofr.json keeps.
const SOFRDays = 450

// SOFRFile is v1/sofr.json: SOFR in percent by New York date. Weekends and bond-market holidays
// carry the rate in effect (the previous fixing), so the app can look up any day directly.
type SOFRFile struct {
	V       int                `json:"v"`
	Source  string             `json:"source"`
	Credit  string             `json:"credit"`
	Rates   map[string]float64 `json:"rates"`
	Updated string             `json:"updated"`
}

type nyFedRates struct {
	RefRates []struct {
		EffectiveDate string  `json:"effectiveDate"`
		Type          string  `json:"type"`
		PercentRate   float64 `json:"percentRate"`
	} `json:"refRates"`
}

// UpdateSOFR merges the latest fixings into out/v1/sofr.json.
func UpdateSOFR(c config) error {
	resp, err := c.client.Get(c.sofrURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SOFR: %s", resp.Status)
	}
	var feed nyFedRates
	if err := json.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return fmt.Errorf("SOFR: %w", err)
	}
	path := filepath.Join(c.out, "v1", "sofr.json")
	file := SOFRFile{V: SchemaVersion, Source: "Federal Reserve Bank of New York", Credit: SOFRCredit, Rates: map[string]float64{}}
	readJSONIfPresent(path, &file)
	fixings := map[string]float64{}
	for d, r := range file.Rates {
		fixings[d] = r
	}
	for _, r := range feed.RefRates {
		if r.Type == "SOFR" && r.EffectiveDate != "" {
			fixings[r.EffectiveDate] = r.PercentRate
		}
	}
	file.Rates = fillSOFR(fixings, SOFRDays)
	file.Source, file.Credit = "Federal Reserve Bank of New York", SOFRCredit
	file.Updated = c.now.UTC().Format(time.RFC3339)
	return writeJSON(path, file)
}

// fillSOFR gives every calendar day from the first fixing to the last a rate, carrying the
// previous fixing over weekends and holidays, and keeps the newest `keep` days. It never fills
// past the last fixing: a day not published yet stays missing (NO DATA in the app).
func fillSOFR(fixings map[string]float64, keep int) map[string]float64 {
	if len(fixings) == 0 {
		return map[string]float64{}
	}
	days := make([]string, 0, len(fixings))
	for d := range fixings {
		days = append(days, d)
	}
	sort.Strings(days)
	out := map[string]float64{}
	rate := fixings[days[0]]
	for d := days[0]; d <= days[len(days)-1]; d = addDays(d, 1) {
		if r, ok := fixings[d]; ok {
			rate = r
		}
		out[d] = rate
	}
	if extra := len(out) - keep; extra > 0 {
		cut := addDays(days[0], extra)
		for d := range out {
			if d < cut {
				delete(out, d)
			}
		}
	}
	return out
}
