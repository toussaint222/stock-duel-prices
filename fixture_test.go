package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestWriteFixture writes a small site in the exact Pages schema for testing the app, by running
// deterministic synthetic trades through the same Aggregator and Publish as the nightly job.
// It runs only when asked:
//
//	FIXTURE_OUT=/tmp/site FIXTURE_DAYS=2026-10-27,2026-10-28 go test -run TestWriteFixture
//
// On 2026-10-28, KO has no trades (NO DATA) and JPM is halted from 10:30 to 12:00.
func TestWriteFixture(t *testing.T) {
	out := os.Getenv("FIXTURE_OUT")
	if out == "" {
		t.Skip("set FIXTURE_OUT to write the fixture site")
	}
	days := strings.Split(os.Getenv("FIXTURE_DAYS"), ",")
	symbols, err := readSymbols("symbols.txt")
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := readLatest(out)
	for di, day := range days {
		session, err := DefaultSession(day)
		if err != nil {
			t.Fatal(err)
		}
		a := NewAggregator(session, symbols)
		for si, s := range symbols {
			if s == "KO" && day == "2026-10-28" {
				continue
			}
			base := 40 + float64(si)*15 + float64(di)
			for m := 0; m < 390; m++ {
				if s == "JPM" && day == "2026-10-28" && m >= 60 && m < 150 {
					continue
				}
				// One trade 30 s into each minute, a small deterministic wiggle per symbol.
				at := session.Open.Add(time.Duration(m)*time.Minute + 30*time.Second)
				a.addTrade(s, base*(1+0.0005*float64((m*7+si*13)%41-20)), at)
			}
		}
		if err := Publish(out, a); err != nil {
			t.Fatal(err)
		}
		if day > latest.Latest {
			latest.Latest = day
		}
	}
	if err := writeLatest(out, latest, time.Now()); err != nil {
		t.Fatal(err)
	}
}
