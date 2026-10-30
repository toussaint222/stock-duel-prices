// stock-duel-prices turns IEX's free next-day HIST files into small JSON price files for Stock Duel.
//
// Nightly (catch-up) mode, as GitHub Actions runs it:
//
//	go run . -out docs
//
// publishes every trading day after v1/latest.json's cursor whose TOPS file IEX has posted, at most
// -max-days per run. A day whose file isn't up yet stays pending and is tried again next run.
//
// One day from a local file (tests, debugging):
//
//	go run . -out /tmp/site -date 2017-07-10 -input testdata/TOPS16-sample.pcapng.gz
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type config struct {
	out, symbolsPath, date, input, listing, sofrURL string
	maxDays, backfill                               int
	now                                             time.Time
	client                                          *http.Client
}

func main() {
	var c config
	flag.StringVar(&c.out, "out", "docs", "site folder (GitHub Pages serves it)")
	flag.StringVar(&c.symbolsPath, "symbols", "symbols.txt", "catalog symbols, one per line")
	flag.StringVar(&c.date, "date", "", "publish only this New York trading day (2006-01-02)")
	flag.StringVar(&c.input, "input", "", "read this local pcap(.gz) instead of downloading (needs -date)")
	flag.StringVar(&c.listing, "listing", "https://iextrading.com/api/1.0/hist", "IEX HIST listing endpoint")
	flag.StringVar(&c.sofrURL, "sofr-url", SOFRURL, "New York Fed SOFR endpoint")
	flag.IntVar(&c.maxDays, "max-days", 3, "most new days to publish in one run")
	flag.IntVar(&c.backfill, "backfill", 0, "older days to add to closes.json once caught up")
	flag.Parse()
	c.now = time.Now()
	c.client = &http.Client{Timeout: 0} // a TOPS file streams for many minutes
	if err := run(c); err != nil {
		log.Fatal(err)
	}
}

func run(c config) error {
	symbols, err := readSymbols(c.symbolsPath)
	if err != nil {
		return err
	}
	if c.date != "" {
		return publishDay(c, c.date, symbols, c.input)
	}
	err = catchUp(c, symbols)
	// SOFR is separate from the IEX days: a failed fetch is logged and retried next run, and
	// never blocks prices (the app shows the bond pick as NO DATA until the rate is there).
	if c.sofrURL != "" {
		if serr := UpdateSOFR(c); serr != nil {
			log.Printf("SOFR not updated: %v", serr)
		}
	}
	return err
}

// catchUp publishes the weekdays after the cursor, oldest first. A weekday with no file is a
// holiday once a later day has one; until then it's pending, and the cursor stops before it.
func catchUp(c config, symbols []string) error {
	latest, err := readLatest(c.out)
	if err != nil {
		return err
	}
	today := c.now.In(NewYork).Format("2006-01-02")
	day := latest.Latest
	if day == "" {
		day = addDays(today, -4) // first run: start a few days back
	}
	var unconfirmed []string
	published := 0
	for d := nextWeekday(day); d < today && published < c.maxDays; d = nextWeekday(d) {
		link, err := topsLink(c, d)
		if err != nil {
			return err
		}
		if link == "" {
			unconfirmed = append(unconfirmed, d)
			continue
		}
		if err := publishDay(c, d, symbols, link); err != nil {
			return fmt.Errorf("%s: %w", d, err)
		}
		published++
		latest.Skipped = append(latest.Skipped, unconfirmed...)
		unconfirmed = nil
		latest.Latest = d
		if err := writeLatest(c.out, latest, c.now); err != nil {
			return err
		}
	}
	if len(unconfirmed) > 0 {
		log.Printf("pending (no file yet): %s", strings.Join(unconfirmed, ", "))
	}
	if err := backfill(c, symbols); err != nil {
		return err
	}
	return writeLatest(c.out, latest, c.now)
}

// backfill adds up to -backfill older trading days to closes.json (and their day files), so the
// suggestion lists have a year of history without waiting a year.
func backfill(c config, symbols []string) error {
	closes, err := readCloses(c.out)
	if err != nil || c.backfill == 0 || len(closes.Days) == 0 || len(closes.Days) >= ClosesDays {
		return err
	}
	d := closes.Days[0]
	for added, tries := 0, 0; added < c.backfill && tries < 3*c.backfill; tries++ {
		d = prevWeekday(d)
		link, err := topsLink(c, d)
		if err != nil {
			return err
		}
		if link == "" {
			continue
		}
		if err := publishDay(c, d, symbols, link); err != nil {
			return fmt.Errorf("backfill %s: %w", d, err)
		}
		added++
	}
	return nil
}

func publishDay(c config, date string, symbols []string, input string) error {
	session, err := DefaultSession(date)
	if err != nil {
		return err
	}
	agg := NewAggregator(session, symbols)
	r, err := open(c, input)
	if err != nil {
		return err
	}
	defer r.Close()
	start := time.Now()
	if err := agg.Scan(bufio.NewReaderSize(r, 1<<20)); err != nil {
		return err
	}
	log.Printf("%s: scanned in %s", date, time.Since(start).Round(time.Second))
	return Publish(c.out, agg)
}

// open streams a URL (never saved to disk) or opens a local file.
func open(c config, input string) (io.ReadCloser, error) {
	if !strings.HasPrefix(input, "http://") && !strings.HasPrefix(input, "https://") {
		return os.Open(input)
	}
	resp, err := c.client.Get(input)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", input, resp.Status)
	}
	return resp.Body, nil
}

type histEntry struct {
	Link, Date, Feed, Version string
}

// topsLink asks IEX's HIST listing for the day's TOPS 1.6 file; "" when it isn't posted.
func topsLink(c config, date string) (string, error) {
	resp, err := c.client.Get(c.listing + "?date=" + strings.ReplaceAll(date, "-", ""))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HIST listing for %s: %s", date, resp.Status)
	}
	var entries []histEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return "", fmt.Errorf("HIST listing for %s: %w", date, err)
	}
	for _, e := range entries {
		if e.Feed == "TOPS" && e.Version == "1.6" {
			return e.Link, nil
		}
	}
	return "", nil
}

func readSymbols(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if s := strings.TrimSpace(line); s != "" && !strings.HasPrefix(s, "#") {
			out = append(out, s)
		}
	}
	return out, nil
}

func addDays(date string, n int) string {
	t, _ := time.Parse("2006-01-02", date)
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

func nextWeekday(date string) string {
	for d := addDays(date, 1); ; d = addDays(d, 1) {
		if wd := weekday(d); wd != time.Saturday && wd != time.Sunday {
			return d
		}
	}
}

func prevWeekday(date string) string {
	for d := addDays(date, -1); ; d = addDays(d, -1) {
		if wd := weekday(d); wd != time.Saturday && wd != time.Sunday {
			return d
		}
	}
}

func weekday(date string) time.Weekday {
	t, _ := time.Parse("2006-01-02", date)
	return t.Weekday()
}
