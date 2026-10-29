package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	iex "github.com/timpalpant/go-iex"
	"github.com/timpalpant/go-iex/iextp/tops"
)

func ny(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04:05.000", s, NewYork)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func tradeMsg(t *testing.T, symbol, at string, price float64, flags uint8) *tops.TradeReportMessage {
	return &tops.TradeReportMessage{MessageType: tops.TradeReport, SaleConditionFlags: flags,
		Timestamp: ny(t, at), Symbol: symbol, Size: 100, Price: price}
}

func newDay(t *testing.T) *Aggregator {
	s, err := DefaultSession("2026-10-28")
	if err != nil {
		t.Fatal(err)
	}
	return NewAggregator(s, []string{"AAPL", "KO", "SPY"})
}

func fileFor(t *testing.T, files []DayFile, symbol string) DayFile {
	for _, f := range files {
		if f.Symbol == symbol {
			return f
		}
	}
	t.Fatalf("no file for %s", symbol)
	return DayFile{}
}

func val(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestMarksAreLastTradeAtOrBeforeEachMinute(t *testing.T) {
	a := newDay(t)
	a.Add(tradeMsg(t, "AAPL", "2026-10-28 09:30:00.000", 100, 0))       // exactly on the open: mark 0
	a.Add(tradeMsg(t, "AAPL", "2026-10-28 09:30:00.001", 101, 0))       // just after: mark 1
	a.Add(tradeMsg(t, "AAPL", "2026-10-28 09:30:59.999", 102, 0))       // still mark 1, and later
	a.Add(tradeMsg(t, "AAPL", "2026-10-28 09:33:00.000", 103.12345, 0)) // mark 3, rounded to 4 dp
	files, _ := a.Files()
	f := fileFor(t, files, "AAPL")
	if len(f.Marks) != 391 || f.Open != "09:30" || f.Close != "16:00" || f.Step != 60 {
		t.Fatalf("shape: %d marks %s–%s step %d", len(f.Marks), f.Open, f.Close, f.Step)
	}
	want := []any{100.0, 102.0, 102.0, 103.1235, 103.1235}
	for i, w := range want {
		if val(f.Marks[i]) != w {
			t.Errorf("mark %d = %v, want %v", i, val(f.Marks[i]), w)
		}
	}
	if val(f.Marks[390]) != 103.1235 || val(f.Last) != 103.1235 || f.Trades != 4 {
		t.Errorf("carry to close: %v last %v trades %d", val(f.Marks[390]), val(f.Last), f.Trades)
	}
}

func TestBeforeFirstTradeMarksAreNull(t *testing.T) {
	a := newDay(t)
	a.Add(tradeMsg(t, "KO", "2026-10-28 10:00:30.000", 60, 0))
	f := fileFor(t, func() []DayFile { f, _ := a.Files(); return f }(), "KO")
	if f.Marks[30] != nil || val(f.Marks[31]) != 60.0 {
		t.Errorf("10:00 = %v, 10:01 = %v", val(f.Marks[30]), val(f.Marks[31]))
	}
}

func TestIneligibleAndOffCatalogTradesAreIgnored(t *testing.T) {
	a := newDay(t)
	a.Add(tradeMsg(t, "AAPL", "2026-10-28 10:00:00.000", 100, 0))
	a.Add(tradeMsg(t, "AAPL", "2026-10-28 10:00:10.000", 999, 0x20)) // odd lot
	a.Add(tradeMsg(t, "AAPL", "2026-10-28 10:00:20.000", 999, 0x40)) // extended hours
	a.Add(tradeMsg(t, "AAPL", "2026-10-28 09:29:59.999", 999, 0))    // before the open
	a.Add(tradeMsg(t, "ZZZZ", "2026-10-28 10:00:30.000", 5, 0))      // not in the catalog
	files, noTrades := a.Files()
	f := fileFor(t, files, "AAPL")
	if val(f.Last) != 100.0 || f.Trades != 1 || f.Marks[0] != nil {
		t.Errorf("last %v trades %d open mark %v", val(f.Last), f.Trades, val(f.Marks[0]))
	}
	if len(files) != 1 || len(noTrades) != 2 || noTrades[0] != "KO" || noTrades[1] != "SPY" {
		t.Errorf("files %d noTrades %v", len(files), noTrades)
	}
}

func TestClosingAuctionIsTheLastMark(t *testing.T) {
	a := newDay(t)
	a.Add(tradeMsg(t, "SPY", "2026-10-28 15:58:30.000", 500, 0))      // counts toward 15:59
	cross := tradeMsg(t, "SPY", "2026-10-28 16:00:00.040", 501, 0x08) // single-price cross
	a.Add(cross)
	f := fileFor(t, func() []DayFile { f, _ := a.Files(); return f }(), "SPY")
	if val(f.Marks[390]) != 501.0 || val(f.Marks[389]) != 500.0 || val(f.Last) != 501.0 || f.LastTime != "16:00:00.040" {
		t.Errorf("15:59 %v 16:00 %v last %v at %s", val(f.Marks[389]), val(f.Marks[390]), val(f.Last), f.LastTime)
	}
}

func TestEarlyCloseComesFromSystemEvents(t *testing.T) {
	s, _ := DefaultSession("2026-11-27")
	a := NewAggregator(s, []string{"AAPL"})
	a.Add(&tops.SystemEventMessage{SystemEvent: tops.StartOfRegularMarketHours, Timestamp: ny(t, "2026-11-27 09:30:00.000")})
	a.Add(tradeMsg(t, "AAPL", "2026-11-27 12:59:00.000", 200, 0))
	a.Add(&tops.SystemEventMessage{SystemEvent: tops.EndOfRegularMarketHours, Timestamp: ny(t, "2026-11-27 13:00:00.120")})
	f := fileFor(t, func() []DayFile { f, _ := a.Files(); return f }(), "AAPL")
	if f.Close != "13:00" || len(f.Marks) != 211 || val(f.Marks[210]) != 200.0 {
		t.Errorf("close %s marks %d", f.Close, len(f.Marks))
	}
}

func TestAFullDayFileIsAbout5KB(t *testing.T) {
	a := newDay(t)
	for i := 0; i <= 390; i++ {
		at := ny(t, "2026-10-28 09:30:00.000").Add(time.Duration(i)*time.Minute - time.Second)
		if i == 0 {
			at = at.Add(time.Second)
		}
		a.trades(t, "AAPL", at, 1234.5678-float64(i)*0.0101)
	}
	f := fileFor(t, func() []DayFile { f, _ := a.Files(); return f }(), "AAPL")
	data, _ := json.Marshal(f)
	if len(data) > 6000 {
		t.Errorf("a full day is %d bytes, want about 5 KB", len(data))
	}
	t.Logf("full day file: %d bytes", len(data))
}

func (a *Aggregator) trades(t *testing.T, symbol string, at time.Time, price float64) {
	a.addTrade(symbol, price, at)
}

// The sample is IEX's own TOPS 1.6 test capture (go-iex testdata): five minutes of a test session.
// It checks the real wire format end to end without downloading a 10–14 GB file.
func TestSampleFile(t *testing.T) {
	const sample = "testdata/TOPS16-sample.pcapng.gz"
	s, _ := DefaultSession("2017-07-10")
	a := NewAggregator(s, []string{"AMZN", "AAPL", "QCOM", "NOPE"})
	in, err := os.Open(sample)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err := a.Scan(in); err != nil {
		t.Fatal(err)
	}
	files, noTrades := a.Files()
	if a.session.Open.Format("15:04") != "10:34" || a.session.Close.Format("15:04") != "10:37" {
		t.Errorf("session from system events: %v–%v", a.session.Open, a.session.Close)
	}
	if len(noTrades) != 1 || noTrades[0] != "NOPE" {
		t.Errorf("noTrades %v", noTrades)
	}

	// Recompute AMZN's last eligible regular-session trade straight from the feed.
	in2, _ := os.Open(sample)
	defer in2.Close()
	src, _ := iex.NewPcapDataSource(in2)
	sc := iex.NewPcapScanner(src)
	var want *tops.TradeReportMessage
	for {
		m, err := sc.NextMessage()
		if err == io.EOF {
			break
		}
		if tr, ok := m.(*tops.TradeReportMessage); ok && tr.Symbol == "AMZN" && tr.IsLastSaleEligible() && !tr.Timestamp.Before(a.session.Open) {
			want = tr
		}
	}
	f := fileFor(t, files, "AMZN")
	if want == nil || val(f.Last) != round4(want.Price) || f.LastTime != want.Timestamp.In(NewYork).Format("15:04:05.000") {
		t.Errorf("AMZN last %v at %s, want %v", val(f.Last), f.LastTime, want)
	}
	if f.V != 1 || len(f.Marks) != 4 || f.Marks[len(f.Marks)-1] == nil {
		t.Errorf("schema: %+v", f)
	}
}

// One nightly run: a holiday is skipped once a later day has a file, a day IEX hasn't posted yet
// stays pending, and the next run picks it up.
func TestCatchUpRetriesPendingDaysAndSkipsHolidays(t *testing.T) {
	posted := map[string]bool{"20170707": true, "20170711": true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file" {
			http.ServeFile(w, r, "testdata/TOPS16-sample.pcapng.gz")
			return
		}
		var out []map[string]string
		if d := r.URL.Query().Get("date"); posted[d] {
			out = append(out, map[string]string{"link": "http://" + r.Host + "/file", "date": d, "feed": "TOPS", "version": "1.6"},
				map[string]string{"link": "http://" + r.Host + "/deep", "date": d, "feed": "DEEP", "version": "1.0"})
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	out := t.TempDir()
	if err := writeLatest(out, &Latest{V: 1, Latest: "2017-07-06", Skipped: []string{}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "symbols.txt"), []byte("AMZN\nAAPL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := config{out: out, symbolsPath: filepath.Join(out, "symbols.txt"), listing: srv.URL + "/hist",
		maxDays: 3, now: ny(t, "2017-07-13 07:00:00.000"), client: srv.Client()}
	if err := run(c); err != nil {
		t.Fatal(err)
	}
	l, _ := readLatest(out)
	if l.Latest != "2017-07-11" || len(l.Skipped) != 1 || l.Skipped[0] != "2017-07-10" {
		t.Fatalf("after run 1: %+v", l)
	}
	for _, d := range []string{"2017-07-07", "2017-07-11"} {
		if _, err := os.Stat(filepath.Join(out, "v1", "days", d, "index.json")); err != nil {
			t.Errorf("%s not published: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "v1", "days", "2017-07-12")); err == nil {
		t.Error("2017-07-12 isn't posted yet, so it must stay pending")
	}

	posted["20170712"] = true // IEX posts it the next night
	c.now = ny(t, "2017-07-14 07:00:00.000")
	if err := run(c); err != nil {
		t.Fatal(err)
	}
	l, _ = readLatest(out)
	closes, _ := readCloses(out)
	if l.Latest != "2017-07-13" && l.Latest != "2017-07-12" {
		t.Fatalf("after run 2: %+v", l)
	}
	if got := closes.Days; len(got) != 3 || got[0] != "2017-07-07" || got[2] != "2017-07-12" {
		t.Errorf("closes days %v", got)
	}
}

func TestClosesStayInOrderAndTrimmed(t *testing.T) {
	c := &Closes{CloseOf: map[string][]*float64{}}
	p := func(v float64) *float64 { return &v }
	c.add("2026-10-28", map[string]*float64{"AAPL": p(3)}, []string{"AAPL"})
	c.add("2026-10-26", map[string]*float64{"AAPL": p(1)}, []string{"AAPL"}) // backfilled
	c.add("2026-10-27", map[string]*float64{"KO": p(9)}, []string{"AAPL", "KO"})
	c.add("2026-10-28", map[string]*float64{"AAPL": p(4)}, []string{"AAPL"}) // re-run replaces
	if c.AsOf != "2026-10-28" || len(c.Days) != 3 || c.Days[0] != "2026-10-26" {
		t.Fatalf("days %v asOf %s", c.Days, c.AsOf)
	}
	if val(c.CloseOf["AAPL"][0]) != 1.0 || c.CloseOf["AAPL"][1] != nil || val(c.CloseOf["AAPL"][2]) != 4.0 || val(c.CloseOf["KO"][1]) != 9.0 {
		t.Errorf("AAPL %v KO %v", c.CloseOf["AAPL"], c.CloseOf["KO"])
	}
	for i := 0; i < ClosesDays+5; i++ {
		c.add(addDays("2027-01-01", i), map[string]*float64{}, []string{"AAPL"})
	}
	if len(c.Days) != ClosesDays || len(c.CloseOf["KO"]) != ClosesDays {
		t.Errorf("kept %d days", len(c.Days))
	}
}
