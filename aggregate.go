package main

import (
	"errors"
	"io"
	"math"
	"time"

	iex "github.com/timpalpant/go-iex"
	"github.com/timpalpant/go-iex/iextp/tops"
)

// Step is the spacing of the price marks in a day file.
const Step = time.Minute

// Session is one trading day's regular session, in New York time.
type Session struct {
	Date  string // "2006-01-02"
	Open  time.Time
	Close time.Time
}

// DefaultSession is 9:30 to 16:00 New York on the given day. The feed's own system events
// (start and end of regular market hours) replace it when present, which covers 1 PM early closes.
func DefaultSession(date string) (Session, error) {
	day, err := time.ParseInLocation("2006-01-02", date, NewYork)
	if err != nil {
		return Session{}, err
	}
	return Session{
		Date:  date,
		Open:  day.Add(9*time.Hour + 30*time.Minute),
		Close: day.Add(16 * time.Hour),
	}, nil
}

type trade struct {
	price float64
	at    time.Time
}

// symbolDay collects one symbol's trades as the last eligible trade in each minute (keyed by
// the minute mark it counts toward), so the session can still move when system events arrive.
type symbolDay struct {
	byMark map[int64]*trade // Unix minute of the mark → last trade at or before it
	counts map[int64]int
}

// Aggregator keeps, for each catalog symbol, the last-sale-eligible trades of the regular session.
// Mark i of a day is the last such trade at or before Open + i×Step. A trade after the last mark
// (the closing auction prints a few milliseconds after 16:00:00) counts toward the last mark, so the
// last mark is always the day's last trade.
type Aggregator struct {
	session Session
	symbols map[string]bool
	days    map[string]*symbolDay
}

func NewAggregator(session Session, symbols []string) *Aggregator {
	a := &Aggregator{session: session, symbols: map[string]bool{}, days: map[string]*symbolDay{}}
	for _, s := range symbols {
		a.symbols[s] = true
	}
	return a
}

func (a *Aggregator) marks() int {
	return int(a.session.Close.Sub(a.session.Open)/Step) + 1
}

// Add takes one message from the feed. Anything other than system events and trades is ignored.
func (a *Aggregator) Add(msg any) {
	switch m := msg.(type) {
	case *tops.SystemEventMessage:
		at := m.Timestamp.In(NewYork).Truncate(Step)
		switch m.SystemEvent {
		case tops.StartOfRegularMarketHours:
			a.session.Open = at
		case tops.EndOfRegularMarketHours:
			a.session.Close = at
		}
	case *tops.TradeReportMessage:
		// Odd lots and extended-hours trades don't set the last sale price.
		if a.symbols[m.Symbol] && m.IsLastSaleEligible() {
			a.addTrade(m.Symbol, m.Price, m.Timestamp)
		}
	}
}

func (a *Aggregator) addTrade(symbol string, price float64, at time.Time) {
	d := a.days[symbol]
	if d == nil {
		d = &symbolDay{byMark: map[int64]*trade{}, counts: map[int64]int{}}
		a.days[symbol] = d
	}
	// A trade exactly on a mark belongs to that mark; otherwise to the next one.
	mark := at.Truncate(Step)
	if !mark.Equal(at) {
		mark = mark.Add(Step)
	}
	key := mark.Unix()
	t := &trade{price: round4(price), at: at}
	if b := d.byMark[key]; b == nil || !t.at.Before(b.at) {
		d.byMark[key] = t
	}
	d.counts[key]++
}

// Scan reads every message from a pcap or pcap-ng stream (gzipped or not) into the aggregator.
// It never buffers more than one packet, so a 14 GB file streams straight from the network.
func (a *Aggregator) Scan(r io.Reader) error {
	src, err := iex.NewPcapDataSource(r)
	if err != nil {
		return err
	}
	scanner := iex.NewPcapScanner(src)
	for {
		msg, err := scanner.NextMessage()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		a.Add(msg)
	}
}

// Files builds one day file per symbol that traded in the session, and lists the catalog
// symbols that didn't. Trades before the open don't count.
func (a *Aggregator) Files() (files []DayFile, noTrades []string) {
	n := a.marks()
	open := a.session.Open.Unix()
	for _, symbol := range sortedKeys(a.symbols) {
		d := a.days[symbol]
		buckets := make([]*trade, n)
		count := 0
		if d != nil {
			for key, t := range d.byMark {
				// The opening mark's minute also holds pre-open trades; only one exactly at the open counts.
				if key < open || key == open && t.at.Before(a.session.Open) {
					continue
				}
				i := min(int((key-open)/int64(Step/time.Second)), n-1)
				if b := buckets[i]; b == nil || !t.at.Before(b.at) {
					buckets[i] = t
				}
				count += d.counts[key]
			}
		}
		if count == 0 {
			noTrades = append(noTrades, symbol)
			continue
		}
		f := DayFile{
			V: SchemaVersion, Symbol: symbol, Date: a.session.Date,
			Open: hhmm(a.session.Open), Close: hhmm(a.session.Close), Step: int(Step / time.Second),
			Marks: make([]*float64, n), Trades: count,
		}
		var carry *trade
		for i, b := range buckets {
			if b != nil {
				carry = b
			}
			if carry != nil {
				p := carry.price
				f.Marks[i] = &p
			}
		}
		last := carry.price
		f.Last = &last
		f.LastTime = carry.at.In(NewYork).Format("15:04:05.000")
		files = append(files, f)
	}
	return files, noTrades
}

func round4(p float64) float64 { return math.Round(p*10_000) / 10_000 }

func hhmm(t time.Time) string { return t.In(NewYork).Format("15:04") }
