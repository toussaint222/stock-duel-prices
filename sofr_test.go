package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSOFRFillsWeekendsAndHolidaysButNotTheFuture(t *testing.T) {
	got := fillSOFR(map[string]float64{"2026-10-30": 3.94, "2026-11-02": 3.92, "2026-11-04": 3.95}, 450)
	want := map[string]float64{"2026-10-30": 3.94, "2026-10-31": 3.94, "2026-11-01": 3.94,
		"2026-11-02": 3.92, "2026-11-03": 3.92, "2026-11-04": 3.95}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for d, r := range want {
		if got[d] != r {
			t.Errorf("%s = %v, want %v", d, got[d], r)
		}
	}
	if _, ok := got["2026-11-05"]; ok {
		t.Error("never fills past the last fixing")
	}
	if n := len(fillSOFR(map[string]float64{"2025-01-01": 4, "2026-10-30": 3.9}, 30)); n != 30 {
		t.Errorf("kept %d days, want 30", n)
	}
}

func TestUpdateSOFRMergesTheNewYorkFedFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"refRates":[{"effectiveDate":"2026-10-29","type":"SOFR","percentRate":3.94},
			{"effectiveDate":"2026-10-27","type":"SOFR","percentRate":3.95}]}`))
	}))
	defer srv.Close()
	out := t.TempDir()
	os.MkdirAll(filepath.Join(out, "v1"), 0o755)
	writeJSON(filepath.Join(out, "v1", "sofr.json"), SOFRFile{V: 1, Rates: map[string]float64{"2026-10-23": 3.9}})
	c := config{out: out, sofrURL: srv.URL, client: srv.Client(), now: time.Now()}
	if err := UpdateSOFR(c); err != nil {
		t.Fatal(err)
	}
	var f SOFRFile
	data, _ := os.ReadFile(filepath.Join(out, "v1", "sofr.json"))
	json.Unmarshal(data, &f)
	if f.V != 1 || f.Credit != "SOFR rate: New York Fed" || f.Source != "Federal Reserve Bank of New York" {
		t.Errorf("header %+v", f)
	}
	if f.Rates["2026-10-23"] != 3.9 || f.Rates["2026-10-25"] != 3.9 || f.Rates["2026-10-28"] != 3.95 || f.Rates["2026-10-29"] != 3.94 {
		t.Errorf("rates %v", f.Rates)
	}
}
