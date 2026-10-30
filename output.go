package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
	_ "time/tzdata" // New York time even on a runner without zoneinfo
)

// SchemaVersion is the "v" in every file and the "v1" in every path. Bump both together, and keep
// publishing the old version until the app that reads it is gone.
const SchemaVersion = 1

// Attribution is IEX's required citation, word for word (IEX Historical Data Terms of Use).
const Attribution = "Data provided for free by IEX. By accessing or using IEX Historical Data, you agree to the IEX Historical Data Terms of Use."

// ClosesDays is how many trading days closes.json keeps (about 13 months, enough for YTD).
const ClosesDays = 280

var NewYork = mustLoad("America/New_York")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// DayFile is v1/days/<date>/<SYMBOL>.json: one symbol's prices for one trading day.
type DayFile struct {
	V      int    `json:"v"`
	Symbol string `json:"symbol"`
	Date   string `json:"date"`  // New York trading day, "2006-01-02"
	Open   string `json:"open"`  // first mark, New York "15:04"
	Close  string `json:"close"` // last mark, New York "15:04" ("13:00" on early closes)
	Step   int    `json:"step"`  // seconds between marks
	// Marks[i] is the last regular-session, last-sale-eligible IEX trade at or before Open + i×Step,
	// or null before the day's first trade. The last mark also takes the closing auction.
	Marks    []*float64 `json:"marks"`
	Last     *float64   `json:"last"`     // the day's last trade (equals the last mark)
	LastTime string     `json:"lastTime"` // New York "15:04:05.000"
	Trades   int        `json:"trades"`   // eligible trades counted
}

// DayIndex is v1/days/<date>/index.json, written last. Until it exists the day is pending; once it
// does, a catalog symbol without a file is listed in NoTrades (the app shows NO DATA).
type DayIndex struct {
	V           int      `json:"v"`
	Date        string   `json:"date"`
	Open        string   `json:"open"`
	Close       string   `json:"close"`
	Symbols     []string `json:"symbols"`
	NoTrades    []string `json:"noTrades"`
	Source      string   `json:"source"`
	Attribution string   `json:"attribution"`
}

// Latest is v1/latest.json: the newest published day and the catch-up cursor.
type Latest struct {
	V       int      `json:"v"`
	Latest  string   `json:"latest"`  // newest trading day published
	Skipped []string `json:"skipped"` // weekdays IEX posted no file for (holidays), most recent last
	Updated string   `json:"updated"` // RFC 3339, UTC
}

// Closes is v1/closes.json in the app's PriceSnapshot format: each day's last trade per symbol,
// oldest first, so the app can refresh its suggestion lists when it opens.
type Closes struct {
	V       int                   `json:"v"`
	AsOf    string                `json:"asOf"`
	Source  string                `json:"source"`
	Days    []string              `json:"days"`
	CloseOf map[string][]*float64 `json:"closes"`
}

const source = "IEX HIST TOPS 1.6"

// Publish writes one day's files under out/v1 and updates closes.json. The index goes last, so a
// run that dies halfway leaves the day pending instead of half published.
func Publish(out string, agg *Aggregator) error {
	files, noTrades := agg.Files()
	dir := filepath.Join(out, "v1", "days", agg.session.Date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	index := DayIndex{
		V: SchemaVersion, Date: agg.session.Date, Open: hhmm(agg.session.Open), Close: hhmm(agg.session.Close),
		Symbols: []string{}, NoTrades: noTrades, Source: source, Attribution: Attribution,
	}
	if index.NoTrades == nil {
		index.NoTrades = []string{}
	}
	lasts := map[string]*float64{}
	for _, f := range files {
		if err := writeJSON(filepath.Join(dir, f.Symbol+".json"), f); err != nil {
			return err
		}
		index.Symbols = append(index.Symbols, f.Symbol)
		lasts[f.Symbol] = f.Last
	}
	closes, err := readCloses(out)
	if err != nil {
		return err
	}
	closes.add(agg.session.Date, lasts, append(index.Symbols, noTrades...))
	if err := writeJSON(filepath.Join(out, "v1", "closes.json"), closes); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "index.json"), index)
}

func readCloses(out string) (*Closes, error) {
	c := &Closes{V: SchemaVersion, Source: source, CloseOf: map[string][]*float64{}}
	data, err := os.ReadFile(filepath.Join(out, "v1", "closes.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	return c, nil
}

// add puts one day's closes in date order (backfilled days land before newer ones), replacing a
// day that's already there, and keeps the newest ClosesDays.
func (c *Closes) add(date string, lasts map[string]*float64, symbols []string) {
	for _, s := range symbols {
		if _, ok := c.CloseOf[s]; !ok {
			c.CloseOf[s] = make([]*float64, len(c.Days))
		}
	}
	i := sort.SearchStrings(c.Days, date)
	if i < len(c.Days) && c.Days[i] == date {
		for s, col := range c.CloseOf {
			col[i] = lasts[s]
		}
	} else {
		c.Days = append(c.Days[:i], append([]string{date}, c.Days[i:]...)...)
		for s, col := range c.CloseOf {
			c.CloseOf[s] = append(col[:i], append([]*float64{lasts[s]}, col[i:]...)...)
		}
	}
	if extra := len(c.Days) - ClosesDays; extra > 0 {
		c.Days = c.Days[extra:]
		for s, col := range c.CloseOf {
			c.CloseOf[s] = col[extra:]
		}
	}
	c.AsOf = c.Days[len(c.Days)-1]
}

// readJSONIfPresent fills v from path when the file exists and parses; otherwise leaves v alone.
func readJSONIfPresent(path string, v any) {
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, v)
	}
}

func readLatest(out string) (*Latest, error) {
	l := &Latest{V: SchemaVersion, Skipped: []string{}}
	data, err := os.ReadFile(filepath.Join(out, "v1", "latest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	return l, json.Unmarshal(data, l)
}

func writeLatest(out string, l *Latest, now time.Time) error {
	l.Updated = now.UTC().Format(time.RFC3339)
	if len(l.Skipped) > 30 {
		l.Skipped = l.Skipped[len(l.Skipped)-30:]
	}
	return writeJSON(filepath.Join(out, "v1", "latest.json"), l)
}

// writeJSON writes compact JSON through a temporary file, so readers never see half a file.
func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
