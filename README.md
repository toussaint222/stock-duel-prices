# stock-duel-prices

Nightly price files for the Stock Duel iOS app, built from **IEX's free next-day HIST data**. A Go
program streams each trading day's TOPS file from IEX, keeps the trades for the app's catalog, and
writes one small JSON file per symbol per day. GitHub Pages serves them. There's no server, no
account, no key and no cost.

## Attribution (required by IEX, word for word)

> Data provided for free by IEX. By accessing or using IEX Historical Data, you agree to the IEX Historical Data Terms of Use.

"IEX Historical Data Terms of Use" links to <https://www.iex.io/legal/hist-data-terms>. Every day
index file carries the same line in its `attribution` field, and the app shows it with the link.
IEX prices reflect trading on IEX only and are a reference point, not the consolidated market price.

## The $0 rules

1. **No payment method anywhere.** Not on GitHub, not on any host. Never add a card to "unlock"
   something; with no card on file, the worst case is a skipped night, never a bill.
2. **Standard runners only.** The workflow uses `ubuntu-latest`. Never switch to a larger runner:
   larger runners are billed even on public repositories.
3. **The repository stays public.** Standard runners and GitHub Pages are free on public
   repositories. Pages on a private repository needs a paid plan.
4. **No Actions artifacts.** Pages serves the committed `docs/` folder ("Deploy from a branch"), so
   nothing is uploaded as an artifact.
5. **Nothing big touches disk.** A TOPS file is 10–14 GB compressed, bigger than the runner's SSD. It
   streams from IEX through gzip and the pcap reader; only the small JSON output is written.

## What runs each night

`.github/workflows/nightly.yml`, at 13:15 UTC Tuesday to Saturday:

1. `go test ./...` on the bundled sample.
2. `go run . -out docs -max-days 3 -backfill 2`:
   - Publishes every weekday after the cursor in `docs/v1/latest.json` whose TOPS 1.6 file IEX has
     posted (at most 3 per run).
   - A weekday with no file is recorded as a holiday once a later day has a file. If no later day
     has one yet, it's **pending**: the cursor stops before it, and the next night tries again.
   - Then adds up to 2 older days to `closes.json`, until it holds 280 trading days.
   - Then (`-refill 2`) redoes up to 2 already-published days, newest first, whose `index.json`
     doesn't list every catalog symbol. After the catalog grows, that gives the new symbols files
     and closes for past days instead of nulls. Days that already cover the catalog are skipped.
   - Then (`-keep-days 90`) deletes every day folder but the newest 90 trading days (about 126
     calendar days). It counts folders, not the clock, so a stalled pipeline deletes nothing.
     `closes.json` keeps its full 280 days: it's carried forward on its own, never rebuilt from day
     folders. Backfill past the window adds the day to `closes.json` only, so nothing is written just
     to be deleted, and `-refill` skips those closes-only days. 90 trading days, not calendar days,
     because a 3-month duel (up to 92 calendar days) reads its start day's files when it settles, up
     to 72 hours late. A duel whose files are gone settles as NO DATA after that 72-hour timeout.
3. Commits `docs/`, even after a failed run, so finished days are kept. `latest.json` changes every
   run, so there's always a commit, and GitHub never pauses the schedule for 60 idle days.

Run it by hand from the Actions tab ("Run workflow"), with a bigger `backfill` to fill history faster.

## Files and schema (v1)

All paths are under `https://<owner>.github.io/<repo>/v1/`. Times are New York. Every file has
`"v": 1`; a breaking change goes to `v2/` while `v1/` keeps being published.

| File | What it holds |
|---|---|
| `days/<yyyy-mm-dd>/<SYMBOL>.json` | One symbol's day (~4 KB for a full day). |
| `days/<yyyy-mm-dd>/index.json` | Written **last**. Until it exists the day is pending. It lists `symbols` (files written) and `noTrades` (catalog symbols with no eligible trade that day: the app shows NO DATA). |
| `latest.json` | `latest` (newest published day), `skipped` (holidays seen), `updated`. |
| `sofr.json` | SOFR in percent by New York date, from the New York Fed's free API (`markets.newyorkfed.org/api/rates/secured/sofr/last/30.json`), for the app's BONDS (SOFR) pick. Weekends and holidays carry the rate in effect; a day not published yet is missing, and the app then uses the most recent earlier published rate (a duel never waits on SOFR). A failed fetch never blocks prices. Credit wherever it's shown: **SOFR rate: New York Fed** (source: Federal Reserve Bank of New York). |
| `closes.json` | Each day's last trade per symbol, oldest first, 280 trading days, in the app's `PriceSnapshot` format (`asOf`, `days`, `closes`). The app refreshes its suggestion lists from it. |

A day file:

```json
{"v":1,"symbol":"AAPL","date":"2026-10-28","open":"09:30","close":"16:00","step":60,
 "marks":[null,231.02,231.05,…],"last":232.4,"lastTime":"16:00:00.041","trades":21873}
```

- `marks[i]` is the last regular-session, last-sale-eligible IEX trade **at or before**
  `open + i × step` seconds, or `null` before the day's first trade. Odd lots and extended-hours
  trades are left out. A trade exactly on a mark belongs to that mark.
- The last mark also takes the closing auction (it prints a few milliseconds after 16:00:00), so
  the last mark always equals `last`.
- `open` and `close` come from the feed's start and end of regular market hours, so a 1 PM early
  close has `"close":"13:00"` and 211 marks.

How the app settles a duel: a duel locked or ending at time T uses the mark for T rounded down to
the minute. If that mark is `null`, or T is outside the session, it uses the previous trading day's
`last`. If the day's `index.json` isn't published yet, the result is pending.

## Local use

```
go test ./...                      # unit tests + the bundled sample
go run . -out /tmp/site -date 2017-07-10 -input testdata/TOPS16-sample.pcapng.gz
```

The catalog is `symbols.txt`: every S&P 500, Nasdaq-100 and Dow 30 component plus SPY and QQQ
(520 symbols, IEX symbology such as `BRK.B`). Its source is `engine/catalog/index-members.csv` in
the app repository. When index membership changes, edit that file and regenerate:
`python3 app/tools/make-catalog.py && swift run --package-path engine sdsnap symbols > pipeline/symbols.txt`.
A full day is about 1.9 MB (520 files of ~3 KB); `closes.json` at 280 days is about 1.1 MB
(about 0.4 MB gzipped, as Pages serves it). With 90 day folders the site stays near 170 MB.

## Licences

- **timpalpant/go-iex** (LGPL-3.0) parses the IEX feed. It's used only in this pipeline, never in
  the app. The pipeline is run, not distributed as a binary.
- **IEX data:** the IEX Historical Data Terms of Use, with the attribution above.
- `testdata/TOPS16-sample.pcapng.gz` is IEX's TOPS 1.6 sample capture from go-iex's testdata (see
  `testdata/README.md`).
