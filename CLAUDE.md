# velocirepo

Go CLI tool for fetching and aggregating open-source project metrics. Uses a flat command structure (verb-first, no subcommands).

## Build

```
go build -o bin/velocirepo ./cmd/velocirepo
```

## Test

```
go test ./...
```

All tests run offline using httptest — no network access required.

## Project layout

- `cmd/velocirepo/` — CLI entrypoint and Cobra commands
- `internal/config/` — TOML config loading (BurntSushi/toml)
- `internal/source/` — Fetcher implementations (GitHub Events, GitHub Traffic, PyPI, CRAN, Homebrew, Plausible, OpenVSX, YouTube)
- `internal/store/` — JSONL read/write, concatenation, DuckDB build
- `internal/fetch/` — The fetch orchestrator: which days each target is fetched (`plan.go`), watermarks, repairs, replay, freshness and the concatenation policy
- `internal/archive/` — Raw response archive: capture, replay transport, release-asset storage, pending artifacts
- `internal/check/` — `velocirepo check`: findings, the diff checks (via `git cat-file`), integrity, volume, freshness, reconciliation
- `internal/reconcile/` — Reference totals compared with stored data, the quota budget and the weekly rotation
- `internal/gate/` — `velocirepo gate`: the nightly run that only adds data, held-back data, repairs, the tracking issue, the heartbeat and the digest
- `internal/version/` — Version vars injected via ldflags

## Conventions

- Each source implements the `source.Source` interface
- Source fields in config accept a string or array of strings (`config.StringList`)
- Tests use `net/http/httptest` with canned responses — no mocking frameworks
- Config file is `velocirepo.toml`, discovered by walking up from cwd
- Data stored as JSONL files at `data/metrics/<source>/<project-id>/<date>.jsonl`, `data/events/<source>/<project-id>/<date>.jsonl`, and `data/content/<source>/<project-id>/<name>.jsonl`
- Sources that implement `source.ContentProvider` also write entity data (e.g., video listings) to `data/content/`
- Concatenation (daily→monthly→yearly, metrics and events only) runs after `fetch` on or after `[settings] concatenate_on_day` (8 by default), once every configured target is fetched past the month's end; content files use upsert-by-id. Data for a concatenated period is written into the period file
- Each source registers its metadata in `init()` (`source.Register`): freshness limits, archive mode, reference totals or the reason it has none. A test requires it of every source
- A day is fetched once it has ended in its source's time zone (`source.DayZoner`), through the day the source has published (`source.Publisher`), and never again; watermarks are per target and series (`_watermark.json`, `_watermark-stars.json`)
- Live API tests are behind the `live` build tag: `go test -tags live ./internal/source -run Live`
