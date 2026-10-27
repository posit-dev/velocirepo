# Plan: detecting and recovering from data problems

Status: for review. Nothing is implemented yet.

This plan turns the [design](2026-10-reliability.md) into phases of work. Each phase ends with a minor release that devrel-io adopts before the next phase builds on it, so problems with the design show up early. Within a phase, the work is split into streams that can be done at the same time, each in its own Git worktree.

## Overview

| Phase | What it delivers | Streams in parallel | Release |
|---|---|---|---|
| 0. Interfaces | Shared types, per-source declarations, injection points in fetch, and the `install-ci` `@v0` fix | 1 (sequential) | v1.0.1 (the fix only) |
| 1. Foundations | Days fetched once they've ended, with resume points per target; archive capture behind a flag; `velocirepo check` with its first checks; live API tests | 4 | v1.1.0 |
| 2. Building blocks | Replay; diff checks; concatenation rules and freshness; GitHub reconciliation; archive storage | 5 | v1.2.0 |
| 3. Report-only gate | `velocirepo gate` and the new workflows, reporting what it would do; the heartbeat | 2 | v1.3.0 |
| 4. Enforcing gate | Restoring, holding back, releasing and repairing; per-month reconciliation and the quota budget; reconciliation for other sources | 3+ | v1.4.0 |
| 5. Completion | Weekly digest, external heartbeat, changelog monitoring, the incident suite, documentation | 5 | v1.5.0 |

devrel-io follows each release: moving its schedule and measuring the archive after phase 1, running `check` by hand after phase 2, running the gate in report-only mode after phase 3, and enforcing it after phase 4.

```
Phase 0 ── interfaces ─┬─► 1a day ends ─────────────────► 2c concatenation + freshness ─┐
                       ├─► 1b archive capture ─┬─► 2a replay                            │
                       │                       ├─► 2e archive storage ─────────────────┤
                       │                       └──────────┐                             │
                       ├─► 1c check ───────────┬─► 2b diff checks ─────────────────────┼─► 3a gate (report-only) ─► 3b heartbeat
                       │                       └─► 2d GitHub reconciliation ◄──┘        │          │
                       └─► 1d live API tests                                           ─┘          ▼
                                                     4a enforcing gate, 4b quota + monthly, 4c other sources
                                                                                                   ▼
                                                     5a digest, 5b Healthchecks, 5c changelogs, 5d incident suite → 5e docs
```

## How the work is split

### Worktrees

Every stream gets a branch and a worktree off `main`, created once its phase starts:

```
git worktree add ../velocirepo-<stream> -b reliability/<stream> main
```

A stream is merged into `main` through a pull request once its tests pass. A phase's streams branch off `main` after the streams they depend on are merged. The streams within a phase don't depend on each other, so their merge order doesn't matter, apart from the overlaps listed below.

The streams can be worked on by different people, or by separate Claude Code agents, each in its own worktree. Either way, the review before merging is the same: the stream's tests, `go test -race ./...`, `go vet ./...` and `gofmt`.

### Avoiding conflicts

Phase 0 exists so that parallel streams rarely touch the same lines.

- **Per-source information lives in the source files, and phase 0 declares it for every source:** its time zone per target and series (`DayZone`), how far it has published (`PublishedThrough`), which of its requests are archived and how (`ArchivePolicy`), and its reference totals (`Reconciler`). Later streams only change the declarations of the sources they work on, in those sources' files.
- **Injection points in fetch.** Phase 0 adds to `fetch.Options`: an observer called for every job and target with its resolved options, a wrapper for the HTTP transport, per-job option overrides, and the concatenation policy, all defaulting to today's behaviour. The archive (1b) installs its capture through the wrapper and records jobs through the observer, replay (2a) uses the overrides, and concatenation (2c) sets the policy.
- **Hotspots get one owner per phase:**

| File | Owned by |
|---|---|
| `internal/fetch/fetch.go` | phase 0 (injection points), then 1a in phase 1, then 2a in phase 2 |
| `internal/fetch/stats.go` (the HTTP transport) | 1b |
| `internal/store/aggregate.go` and `internal/store/jsonl.go` | 2c |
| `internal/store/watermark.go` | 1a |
| `internal/source/github_events.go` | phase 0 (the snapshot unit), then 1a |
| `cmd/velocirepo/cmd/check.go` | 1c; in phase 2, 2b and 2d each add flags, and the second to merge rebases |
| `cmd/velocirepo/cmd/ci_install.go` | phase 0 (the `@v0` fix), 1a (the schedule only), then 3a, then 3b after 3a is merged, then 5a–5c in separate functions |
| `action.yml` | 1b (a `fetch-args` input), 2e (the artifact fallback), then 3a, then 3b |
| `internal/config/config.go` | phase 0 declares the gate and archive settings; 1a, 2c and 3a each add one more, in different phases |
| `cmd/velocirepo/cmd/root.go` | anyone; each new command adds one line |

- **New code goes into new packages:** `internal/archive` (1b), `internal/check` (1c), `internal/reconcile` (2d), `internal/gate` (3a).
- **One known overlap:** 2a may change sources that still read the clock directly, and 2d changes the GitHub source's reconciliation declaration. Both are small; if they touch the same file, the second to merge rebases.

### Definition of done

For every task:

- A test that fails without the change comes first, as in the October fixes.
- `gofmt`, `go vet ./...` and `go test -race ./...` pass.
- User-facing behaviour is documented in `user_guide/`, and the release's upgrade notes mention it.
- Commits are small, with the reason in the message, as in the repository's history.

## Phase 0: interfaces

**Goal:** the shared types and injection points, merged into `main` before any stream starts. No existing behaviour changes, apart from the `@v0` fix.

Sequential, in one branch (`reliability/interfaces`). Size: small.

| Task | Files | Contents |
|---|---|---|
| 0.1 Findings | `internal/check/finding.go` | The `Finding` type (`check`, `severity`, `source`, `project`, `target`, `message`, `paths`), its stable ID from the check, source, project, target and a check-specific key that never contains counts or values, and the JSON schema of `check.json` |
| 0.2 Source metadata | `internal/source/meta.go`, every `internal/source/*.go` | The optional interfaces `DayZone(ctx, target, series) (*time.Location, error)`, `PublishedThrough(ctx) (time.Time, error)` (asked once per source and run, not per target), `ArchivePolicy` (for each request: archived or not, and in full or with only the read fields) and `Reconciler`. Each source also declares its freshness limit, which both concatenation and `data.freshness` read. Every existing source declares UTC, "published through the end date", its freshness limit, its archive policy (archived: snapshots, traffic, PyPI, LinkedIn with read fields only, and read fields only for targets that aren't public) and "no reference totals yet". A source without an `ArchivePolicy` isn't archived. |
| 0.3 Registry tests | `internal/source/meta_test.go` | Every registered source declares an archive policy and a reconciliation status, like the test that keeps badge presets in line with the metrics sources emit |
| 0.4 Archive record and manifest | `internal/archive/record.go` | One archived exchange: time, job, target, method, redacted URL, redacted request body, status, selected headers, body. The manifest, with every job's and target's options (start date, end date, run time, resolved time zones), and for GitHub the issue and pull request numbers its reaction counts were requested for. The layout `raw/<run-id>/<source>.jsonl.zst` |
| 0.5 Watermark series | `internal/store/watermark.go` | A series key in the per-target watermarks, so the star history can have its own. Reading old watermark files without it keeps working. |
| 0.5b Settings | `internal/config/config.go` | `[settings] gate` (`report` or `enforce`, default `report`) and `[settings] archive` (default on), read by later phases. Declared here so that no stream has to add them. |
| 0.6 Injection points and results | `internal/fetch/fetch.go`, `internal/fetch/results.go`, `cmd/velocirepo/cmd/fetch.go` | The observer, transport wrapper, per-job overrides and concatenation policy in `fetch.Options`. `fetch --results <file>` writes the run's `[]Result` as JSON, with the quota left per API. |
| 0.6b GitHub's snapshot unit | `internal/source/github_events.go` | Fetch the repository info first and the reaction counts after it, as a unit whose requests don't depend on the event requests: the issue and pull request numbers for the reaction counts are passed in and recorded in the manifest. No change in what's fetched or stored. |
| 0.7 Action reference | `cmd/velocirepo/cmd/ci_install.go` | `install-ci` maps "latest" to `@v0` (`ci_install.go:141`), so workflows generated with v1.0.0 reference the v0 action. Map it to the current major version. This is a bug today, so it's released on its own as v1.0.1. |

**Acceptance:**
- The packages build, and the types have doc comments.
- `Finding` IDs are stable: the same input gives the same ID, a different target gives a different one, and the same finding with different counts in its message keeps its ID.
- The registry tests pass, old watermark files still load, and existing tests are unchanged.
- `install-ci` generates `@v1`.

## Phase 1: foundations

**Goal:** four independent foundations. Release v1.1.0.

### 1a. Days fetched once they've ended

Worktree `reliability/day-ends`. Size: large. Design: [measure 4](2026-10-reliability.md#4-fetch-each-day-once-it-has-ended).

| Task | Files |
|---|---|
| A target's end date: the latest day that has ended in its time zone, plus an hour's margin, replacing `settleDays`. An explicit `--end-date` that hasn't ended is moved back, with a warning | `internal/fetch/fetch.go` |
| `DayZone` for the star history (US Pacific) | `internal/source/github_events.go` |
| `DayZone` for Plausible: the site's time zone from `GET /api/v1/sites/{site}`, cached per run; `[settings.timezones]` overrides it; UTC−12 if neither is available | `internal/source/plausible.go`, `internal/config/config.go` |
| Resume points per target and series: watermarks of the day each target was fetched through, with the star history as its own series, replacing the source-and-project resume point and the star history's 7-day re-read | `internal/fetch/fetch.go`, `internal/store/watermark.go`, `internal/source/github_events.go` |
| `PublishedThrough`, asked once per source and run: CRAN from cranlogs' `last-day` endpoint, PyPI from pypistats' latest day for a reference package (`pip`). A watermark moves to the earlier of the end date and the published day; a source that can't tell moves it to the latest day it returned | `internal/source/cran.go`, `internal/source/pypi.go`, `internal/fetch/fetch.go` |
| Seeding without a migration: a target without a watermark gets one from its stored data the first time it's fetched, the star history from its latest stored `daily_stars` day and the others from today's resume point; a target without stored data starts from the beginning. The schema version stays the same | `internal/fetch/fetch.go` |
| `install-ci` schedules the nightly run after the latest time zone of the configured sources | `cmd/velocirepo/cmd/ci_install.go` (one function; 3a later rewrites the template) |

**Tests:**
- **End dates per run time:** a run at 06:00 UTC fetches GitHub events through yesterday, and the star history and plotnine's Plausible site through the day before. A run at 09:30 UTC fetches all of them through yesterday.
- **No gap:** a run at 06:00 UTC followed by one at 09:30 UTC the next day leaves no missing star day. The same holds for a project with two Plausible sites in different time zones.
- **Unpublished days:** CRAN reports `last-day` as two days ago, and the watermark stops there. The next run fetches the missing day.
- **Days without data:** a repository without stars for months, and a PyPI package without downloads, still move their watermarks, and store nothing for those days.
- **Daylight saving time:** the margin covers both PDT and PST.
- **Time zone sources:** `[settings.timezones]` overrides the Sites API, and without either Plausible falls back to UTC−12. Invalid zone names are rejected by config validation.
- **Explicit end dates:** an end date that hasn't ended is moved back, with a warning.
- **Upgrading:** the old workflow template, run with the new binary on a copy of devrel-io's data, succeeds without `migrate`, and makes about as many requests as a normal night, not a full re-read of every star history.
- **New targets:** a target added to the config later gets its full history.
- **A single quiet PyPI package:** run on its own with `--project`, its watermark still moves, because the published day comes from the reference package.

**Acceptance:** at 09:30 UTC, devrel-io's real-time sources all end on the same day, and CRAN ends on its published day.

### 1b. Archive capture

Worktree `reliability/archive`. Size: medium. Design: [raw response archive](2026-10-reliability.md#1-raw-response-archive).

| Task | Files |
|---|---|
| A transport chain in place of `countingTransport`: counting, rate-limit tracking (quota left per API, from response headers), and capture of the requests each source's `ArchivePolicy` marks as archived | `internal/fetch/stats.go`, `internal/archive/capture.go` |
| Redaction of secret query parameters and body fields, and a check that refuses an archive containing a known secret | `internal/archive/redact.go` |
| Read-fields-only archiving: for LinkedIn after its post filter, and for targets that aren't public or whose visibility isn't known yet | `internal/archive/policy.go`, `internal/source/linkedin.go` |
| The archive writer: zstd JSONL per source, and the manifest from the phase 0 observer | `internal/archive/writer.go` |
| `fetch --archive <dir>`: capture is off unless a directory is given. The action gets a `fetch-args` input, so a workflow can pass it before the gate exists | `cmd/velocirepo/cmd/fetch.go`, `action.yml` |
| `Deprecation` and `Sunset` headers of every request, archived or not, in the fetch log | `internal/archive/capture.go`, `internal/store/log.go` |

**Tests:**
- **Capture:** only the requests marked as archived are captured, and every body reaches its source unchanged.
- **Redaction:** each secret is redacted, in URLs and request bodies, including Plausible's and YouTube's parameters. An archive containing a test secret is refused.
- **Archive policy:**
  - A source without an archive policy isn't archived.
  - LinkedIn drafts and non-public posts never reach the archive.
  - A private repository's responses keep only the read fields, also before its visibility is known.
- **Explicit capture:** `fetch` without `--archive` writes no archive.
- **Manifest:** it records every job's and target's options.
- **Quota:** rate-limit headers are tracked per API, and appear in `--results`.

**Acceptance:** a full devrel-io fetch with `--archive` produces an archive in which no secret from `.env` appears, and no comment bodies. Its size is recorded.

### 1c. `velocirepo check` with its first checks

Worktree `reliability/check`. Size: medium. Design: [checks in the gate](2026-10-reliability.md#checks-in-the-gate).

| Task | Files |
|---|---|
| The `check` command: runs the registered checks, writes `check.json` and a Markdown summary, exits non-zero on unaccepted errors | `internal/check/check.go`, `cmd/velocirepo/cmd/check.go` |
| Accepted findings from `velocirepo/checks.toml`, with reason and expiry | `internal/check/accept.go` |
| `fetch.failed`, from `fetch --results`, or for a past run from the fetch log (`data/log/`) | `internal/check/fetch.go` |
| `data.integrity`, reusing `store.ValidateData` | `internal/check/integrity.go` |
| `data.volume`: a target that usually has data (on at least 20 of the last 28 days) gets none for several days | `internal/check/volume.go` |

`data.freshness` comes in 2c, since it needs 1a's watermarks.

**Tests:**
- **Fixtures per check:** each check reports exactly the findings of a fixture: duplicates for integrity, a source that stops returning data for volume.
- **Quiet targets report nothing:** a quiet repository and a small Plausible site have a fixture that must report nothing.
- **Accepted findings:** an accepted finding is reported as accepted; an expired one isn't.
- **Output:** the Markdown summary is stable (a golden file), and the exit code is non-zero only on unaccepted errors.

**Acceptance:**
- Run on the fetch logs of 2026-10-17 to 10-20, `fetch.failed` reports the four failed nights.
- Run on devrel-io at the Plausible commit `ee4cf305e`, `data.integrity` reports the 126 duplicates.
- Run on today's devrel-io, it reports only the known bot-comment duplicate, and accepting that finding makes it pass.
- Its runtime on devrel-io is recorded, and stays under five minutes.

### 1d. Live API tests

Worktree `reliability/live-tests`. Size: medium. Design: [measure 5](2026-10-reliability.md#5-live-api-tests-and-upstream-changes).

| Task | Files |
|---|---|
| Tests with the build tag `live`, one per source. They check the response shape, types, plausible ranges and known limits: the 300-page comment cap, 30-week star history pages, the Sites API and cranlogs' `last-day` | `internal/source/*_live_test.go` |
| A scheduled workflow that runs them daily with the repository's secrets, and opens an issue in velocirepo when they fail | `.github/workflows/live.yml` |

Each source's target and credentials:

| Source | Target | Secret in velocirepo's repository |
|---|---|---|
| GitHub events, star history, reconciliation | a public repository, such as velocirepo itself | `GH_TOKEN` |
| GitHub traffic | velocirepo itself | a separate fine-grained token, limited to velocirepo, with only the permission the traffic API needs, stored as its own secret and used only by `live.yml` |
| CRAN, PyPI, Homebrew, Open VSX, RSS | public packages and feeds | none |
| Plausible | none public | skipped, unless a `PLAUSIBLE_TOKEN` for a test site is added |
| YouTube | a public channel | `YOUTUBE_TOKEN` |
| LinkedIn | none public | skipped: its tokens expire after 60 days |

**Tests:** these are the tests. Without the `live` tag they're skipped, so `go test ./...` stays offline, as `CLAUDE.md` requires. A test whose secret is missing reports itself as skipped with the reason, so it's never mistaken for a pass.

**Acceptance:** the workflow passes against the real APIs, and changing an expected field name makes it fail and open an issue.

### Release v1.1.0 and devrel-io

- **Release:** 1a changes when sources are fetched. The upgrade notes say:
  - Plausible and stars arrive on the same day as the other real-time sources, if the workflow runs after the latest time zone, and `install-ci` suggests the new schedule.
  - CRAN is stored through the day cranlogs has published.
  - Accounts without Sites API access can set `[settings.timezones]`.
- **devrel-io:**
  - Move the nightly run to 09:30 UTC.
  - Pass `fetch-args: --archive ${{ runner.temp }}/raw` to the action, and add an `actions/upload-artifact` step for that directory, for now (a manual workflow edit, until 3a generates it).
  - For a week, measure the archive's size and check its redaction.

## Phase 2: building blocks

**Goal:** everything the gate needs. Release v1.2.0.

Five streams in parallel, all branching off `main` after phase 1 is merged.

### 2a. Replay

Worktree `reliability/replay`. Depends on 1b. Owns `internal/fetch/fetch.go` in phase 2. Size: medium.

| Task | Files |
|---|---|
| A transport that serves archived responses, matched by method, redacted URL and body hash; an unmatched request is an error | `internal/archive/replay.go` |
| `velocirepo replay <run-id> [--source] [--project]`, which runs each job with the options from the manifest, through phase 0's per-job overrides, and writes records like `fetch` | `cmd/velocirepo/cmd/replay.go` |
| Remove remaining clock dependencies from sources, if any, so replays are deterministic | `internal/source/*.go` |

**Tests:**
- **Round trip:** a fetch followed by a replay of its archive writes identical records, for every archived source.
- **Later replays:** a replay after newer data has been stored still makes the original requests, because it uses the manifest's options instead of the resume points.
- **Unmatched requests:** they fail.
- **GitHub:** a GitHub run is replayed through its snapshot unit, without any of its event requests.
- **Fixes:** a replay with a fixed processing bug writes the corrected records. The fixture is the October `total_reactions` bug, where a write replaced daily files.
- **Ranges:** a repair range selects every run whose manifest's end dates fall in it, oldest first.

**Acceptance:** replaying a week of devrel-io archives rebuilds the archived sources' records exactly.

### 2b. Diff checks

Worktree `reliability/diff-checks`. Depends on 1c. Size: medium.

| Task | Files |
|---|---|
| Read the `HEAD` versions of the changed projects' files with `git cat-file --batch`, without a checkout, and compare records by key. A second mode, `--base <commit> --head <commit>`, compares two commits the same way, for commits made by hand | `internal/check/diff.go`, `cmd/velocirepo/cmd/check.go` |
| Only record and content files are read; watermark files, the fetch log, the schema version and the gate's state file are excluded | `internal/check/diff.go` |
| `diff.records-removed`, with per-source allowances (deleted comments and forks). Removals under project IDs no longer in the config are information; records moving from an old project ID to a new one are recognised as a rename; a commit that changes the schema version is compared after mapping old metric names to new ones | `internal/check/diff.go` |
| `diff.values-changed`, exempting content files | `internal/check/diff.go` |
| `diff.content-removed`: an ID that disappears from a content file | `internal/check/diff.go` |
| `diff.repaired`: changes within the scope of a `Velocirepo-Repair: <source>/<project> <from>..<to>` trailer, or of a repair run, removals included, as warnings. Diffs compare sets of keys | `internal/check/diff.go` |
| `concatenate.records-moved`: the record sets before and after concatenation are identical | `internal/check/concatenate.go` |

**Tests:**
- **Incidents:** the July deletion (`03f2f5e66`) and the 44,619 `total_reactions` are reported as removed records.
- **Normal nights:** a normal nightly commit (`bc6b5d81b`) reports nothing.
- **Concatenation:** records that move between files during concatenation aren't reported as removed.
- **Content files:** content updated in place isn't reported, but a content entry that disappears is.
- **Repairs:** changes inside a repair's scope are warnings, including records that move to another date and events whose `Extra` changes; outside it, they're errors.
- **Two commits:** `--base` and `--head` find a deletion spread over three commits.
- **velocirepo's own removals:** a commit made after `rename-project`, one after `remove-project --delete-data`, and one after `migrate` report no errors.
- **Excluded files:** a night that only moves watermarks reports nothing.
- **Cost:** only the changed projects' files are read.

**Acceptance:** on devrel-io's history since July, `diff.records-removed` and `diff.content-removed` report exactly the known incidents. `diff.values-changed` is left out of this historical check: before 1a, nightly runs rewrote star days in the 7-day re-read window by design.

### 2c. Concatenation, writing into period files, and freshness

Worktree `reliability/concatenation`. Depends on 1a (watermarks, and both change `config.go`). Size: medium. Design: [concatenation](2026-10-reliability.md#concatenation) and [writing records](2026-10-reliability.md#writing-records).

| Task | Files |
|---|---|
| Records for a period that's already concatenated are written directly into its period file, instead of as daily files. Every write, to a daily file or a period file, replaces for each target and date it covers the stored records of the metrics or event types it contains | `internal/store/jsonl.go`, `internal/store/aggregate.go` |
| A month is concatenated on or after the concatenation day (`[settings] concatenate_on_day`, default 8), once every target and series of the project in the current config has a watermark past its last day, or is further behind than its freshness limit; set through phase 0's concatenation policy | `internal/store/aggregate.go`, `internal/config/config.go` |
| `data.freshness` from each target's watermark, with the limits declared in the source metadata | `internal/check/freshness.go` |

**Tests:**
- **The calendar:** before the 8th nothing is concatenated; on or after the 8th the month is, and in January also the year.
- **The data:** a project with one lagging target waits, until that target is further behind than its freshness limit. A target removed from the config doesn't count.
- **Late records:** a backfill, a new target in an already concatenated project, and a replay all go into the existing period files, and `data.integrity` finds no overlap.
- **Replacing per metric:** a write of an event with a changed `Extra` into a concatenated month leaves only the new event. A star history write for a date leaves that date's `total_reactions` in place.
- **Missed concatenation day:** a month whose run on the 8th is missed is concatenated on the next run.
- **Freshness:**
  - Every registered source has a limit, defined as the most days allowed between its watermark and today in UTC.
  - Each source has a boundary test: at its limit nothing is reported, one day more is. For CRAN, three days behind is fine and four is reported.
  - A quiet repository with a current watermark reports nothing.
- **Unchanged behaviour:** `LastDate` and the resume points are unchanged, and the existing concatenation tests pass with the day set to 1.

**Acceptance:**
- Run on a copy of devrel-io dated 2026-11-08, it concatenates October in one step, and `concatenate.records-moved` passes.
- `data.freshness` on today's devrel-io reports nothing.

### 2d. GitHub reconciliation

Worktree `reliability/reconcile-github`. Depends on 1b (rate-limit tracking) and 1c. Size: medium. Design: [reconciliation](2026-10-reliability.md#3-reconciliation).

| Task | Files |
|---|---|
| The reconciliation engine: reference totals from each `Reconciler`, compared with stored data within the source's tolerance, as `reconcile.*` findings | `internal/reconcile/reconcile.go`, `internal/check/reconcile.go` |
| The quota left per API, read from `fetch --results` and updated from the reference responses' headers | `internal/reconcile/reconcile.go` |
| GitHub all-time totals, batched in GraphQL: issues, pull requests, merged pull requests, forks, stars | `internal/source/github_reconcile.go`, and the GitHub source's declaration |

**Tests:** the [reconciliation tests](2026-10-reliability.md#reconciliation-tests) that apply to all-time totals:
- matching totals and tolerances;
- missing and duplicated data;
- a failed or suddenly zero reference total.

**Acceptance:**
- On devrel-io as of `f8e345cde`, it reports positron, quarto and rstudio, and the stars of every repository.
- On today's devrel-io, it reports nothing.
- All 465 repositories take at most 15 requests, and the comparison with stored data runs as DuckDB aggregates. Its runtime on devrel-io is recorded.

### 2e. Archive storage

Worktree `reliability/archive-storage`. Depends on 1b. Size: small.

| Task | Files |
|---|---|
| A storage interface with one implementation: release assets in the data repository (`raw-YYYY-MM`), uploaded with the workflow token, kept forever. Releases are created with `prerelease: true` and `make_latest: false`. `[settings] archive = false` turns the archive off | `internal/archive/store.go`, `internal/archive/release.go` |
| A failed upload is retried; if it still fails, the gate sets an output with the archive's path, and the action saves it with an `actions/upload-artifact` step, kept for 7 days. The next run uploads every artifact that has no matching release asset, idempotently by asset name. An archive pending for six days is an error. An archive in which a secret is found is neither uploaded nor saved, and is reported as an error naming the source | `internal/archive/store.go`, `internal/archive/pending.go`, `action.yml` |
| `velocirepo download-archive <run-id>`, for replays, named verb-first like the other commands | `cmd/velocirepo/cmd/download_archive.go` |

**Tests:**
- **Upload and download:** both go through an httptest server that imitates GitHub's release API.
- **Release parameters:** every release is created as a pre-release that isn't the latest.
- **Errors:** a 403 becomes a warning that names the missing permission.
- **Catching up:** starting from a fresh workspace, a run after a failed upload finds the pending artifact and uploads both runs. An archive that was already uploaded is skipped. An archive pending for six days is an error.
- **Secrets:** an archive containing a secret is neither uploaded nor saved, and is reported.

**Acceptance:** a devrel-io archive is uploaded as a release asset, downloaded with `download-archive`, and replayed.

### Release v1.2.0 and devrel-io

- **Release:** replay, `check` with diff, freshness and reconciliation checks, and archive storage. `fetch` now concatenates on or after the 8th of the month, once every target has been fetched past the month's end. The upgrade notes explain that daily files stay in place a week longer than before.
- **devrel-io:** run `velocirepo check` by hand after a few nightly runs, and accept or fix what it reports, before the gate depends on it.

## Phase 3: report-only gate

**Goal:** the gate and the new workflows, reporting what they would do without changing what's committed. Release v1.3.0.

### 3a. Gate and workflows

Worktree `reliability/gate`. Depends on everything in phase 2. Size: large. Design: [data gate](2026-10-reliability.md#2-data-gate).

| Task | Files |
|---|---|
| `velocirepo gate`. It checks the commits since `refs/velocirepo/checked`, runs the fetch and `check` in-process (so that phase 4 can overlay held-back data), uploads the archive, concatenates on or after the concatenation day in its own commit, makes the data commit, pushes, moves the checked ref, updates the tracking issue, and writes the job summary and annotations. It only enforces with `[settings] gate = "enforce"`, which is the only switch; in this phase it always commits everything, as today, and reports what it would restore, keep and hold back | `internal/gate/*.go`, `cmd/velocirepo/cmd/gate.go` |
| The gate's state: `velocirepo/gate-state.json`, committed when it changes, with the open findings and, for findings about commits made by hand, their commit. The first run creates the checked ref at `HEAD`; a ref that's no longer an ancestor of `main` is reported and reset | `internal/gate/state.go` |
| A push rejected because someone pushed during the run: fetch the new `main`, check its new commits, rebase, rerun the checks for projects both touched, push, and only then move the checked ref | `internal/gate/push.go` |
| Commit messages: the run's date in the subject, and how far each source is stored in the body | `internal/gate/commit.go` |
| The tracking issue, found by the label `velocirepo-health`: body rewritten each night, comments only when findings change, closed when none are left, owners assigned, teams mentioned | `internal/gate/issue.go` |
| `[settings] owners`, filled in by `install-ci`, which checks that owners have access | `internal/config/config.go`, `cmd/velocirepo/cmd/ci_install.go` |
| Tokens: `GITHUB_TOKEN` keeps meaning the personal access token. The gate gets the workflow token through `--repo-token` or `VELOCIREPO_REPO_TOKEN`, from the action's new `repo-token` input, which defaults to `github.token` | `internal/gate/*.go`, `action.yml` |
| The action's modes: `mode` defaults to `fetch`, which runs `velocirepo fetch` as today; `gate`, `check-heartbeat` and `digest` run those commands | `action.yml` |
| The nightly workflow template: `mode: gate`, its own concurrency group, the history back to the checked ref, the permissions `contents: write`, `issues: write` and `actions: read`, and the `release`, `discard` and `repair` inputs. Until 4a, those inputs only report that they're available from v1.4.0 | `cmd/velocirepo/cmd/ci_install.go` |

**Tests:**
- **GitHub's API** (issues, assignees, comments, releases) is an httptest server, as in the existing `sync-secrets` tests. Git operations run against a real temporary repository.
- **Commits made by hand:** a commit that deletes a project's events is reported. The finding stays open until the records are back.
- **The issue's lifecycle:** opened, updated without a comment, commented on a change, closed.
- **Tokens:** with `GITHUB_TOKEN` and `VELOCIREPO_REPO_TOKEN` set to different values, each call uses the right one. A 403 from the issues API becomes a finding that names the missing permission.
- **A moving `main`:**
  - A commit pushed during the run that touches another project is checked and rebased over.
  - One that touches the same project reruns its checks.
  - The checked ref never skips an unchecked commit.
- **Old workflows:** the old workflow template, rendered against the new `action.yml`, still fetches, concatenates and commits as before.
- **Report-only mode:** it commits everything, and the summary lists what enforcing would have done. A config without `[settings] gate` reports, also with a later binary.
- **State:** the first run creates the checked ref at `HEAD`; a force-pushed `main` makes the ref a non-ancestor, which is reported and reset; findings keep their state across runs.
- **Concatenation:** it happens on or after the 8th, in its own commit, checked by `concatenate.records-moved`; a missed 8th is caught up the next night.
- **The switch:** a v1.3.0 template with `[settings] gate = "enforce"` enforces on v1.4.0, and without the setting it reports.
- **Workflows:** the generated workflows match golden files. CI also runs `actionlint` (MIT) on them.

**Acceptance:**
- devrel-io's nightly run commits as today, and opens an issue for an injected finding.
- A commit made by hand that deletes a project's file is reported the next night.
- No repository setting has to change.

### 3b. Heartbeat workflow

Worktree `reliability/heartbeat`. Depends on 3a: branch off `main` after 3a is merged. Size: small.

| Task | Files |
|---|---|
| `velocirepo check-heartbeat`. It asks GitHub's Actions API whether the nightly workflow has started a scheduled run (`event=schedule`) since its scheduled time that day (a run in progress counts, manual runs don't), and fails and comments on the issue (or opens it) if not. Once the issue has been open for three days, it adds one escalation comment. It never rewrites the issue's body | `internal/gate/heartbeat.go`, `cmd/velocirepo/cmd/check_heartbeat.go` |
| The heartbeat workflow template, scheduled by `install-ci` three hours after the nightly run, running the action with `mode: check-heartbeat`, with `issues: write` and `actions: read` | `cmd/velocirepo/cmd/ci_install.go` |

**Tests:**
- **A single missed night:** with no nightly run since that day's scheduled time, the heartbeat fails and reports it the same day.
- **An open issue:** it doesn't fail the heartbeat. The escalation comment is added once, after three days.
- **A long nightly run:** a run still in progress counts as present.
- **Manual runs:** a `release` run on a day without a scheduled run doesn't hide the missed night.
- **Permissions:** the generated workflow grants what its calls need, checked with golden files and `actionlint`.
- **The schedule:** `install-ci` puts the heartbeat after the nightly run, also when the nightly run is late because of the UTC−12 fallback.

**Acceptance:** disabling devrel-io's nightly schedule makes the heartbeat report it the same day.

### Release v1.3.0 and devrel-io

- **Release:** the upgrade notes ask users to regenerate their workflows with `install-ci`, and say that no repository settings need to change.
- **devrel-io:** regenerate the workflows, and run in report-only mode for two weeks. During that time:
  - tune the thresholds;
  - accept the false positives in `velocirepo/checks.toml`;
  - fix real findings.

## Phase 4: enforcing gate

**Goal:** bad data stays out of the stored data. Release v1.4.0. The nightly workflow's behaviour changes, but only for users who regenerate their workflows with `install-ci`; existing workflows keep working as before. The upgrade notes put this first.

### 4a. Enforcing gate

Worktree `reliability/gate-enforce`. Size: medium. Design: [the rule](2026-10-reliability.md#the-rule) and [held-back data](2026-10-reliability.md#held-back-data).

| Task | Files |
|---|---|
| The rule: removed records are restored from `HEAD`, and changed values keep their stored value, both reported | `internal/gate/rule.go` |
| Holding back: for a source and project with an error finding about the night's new records, everything the night changed for it (new records, watermark entries, content changes) goes into `velocirepo/held-back/<source>/<project>/`, and its paths in `data/` stay as on `HEAD` | `internal/gate/holdback.go` |
| Overlaying: before the fetch, a held-back project's records are overlaid on its stored data through the store's write path, so they go into period files that were concatenated in the meantime, so its watermarks, resume points and the filter for unchanged totals include them; after the fetch, its new records are checked as a whole, and land through the store with newest-wins once they pass | `internal/gate/holdback.go` |
| Manual runs: `release: <source>/<project>` lands held-back data regardless of findings; `discard: <source>/<project>` deletes it, watermarks included; both are recorded in the issue | `internal/gate/release.go`, `cmd/velocirepo/cmd/ci_install.go` |
| Repairs: `repair: <source> <project> <from> <to>` re-fetches the requests of the range that can be re-fetched, replays the archived ones from the runs in the range (or skips them without an archive), never re-fetches snapshots for a past range, reports the changes in scope as `diff.repaired`, and commits them with a `Velocirepo-Repair:` trailer | `internal/gate/repair.go` |
| Which findings block: only errors about the night's new records, attributed by the keys added that night, per source and project; errors that don't belong to a project hold back everything; warnings never block | `internal/gate/rule.go` |
| Enforcing only with `[settings] gate = "enforce"` | `internal/gate/rule.go` |

**Tests:**
- **Removals and changes:** a night that deletes records or changes values leaves the stored data as it was, and reports both.
- **Per source and project:** a GitHub error holds back that project's GitHub data, while its Plausible data and the other projects land.
- **Watermarks:** after `discard`, the stored watermark is unchanged, and the next night fetches the discarded days again.
- **Attribution:** a duplicate that already existed in a file touched tonight is reported, but doesn't hold anything back. A night that both removes records and adds new ones restores the removed ones and lands the new ones.
- **Mixed sources:** a GitHub project held back for two nights, then passing, lands its events and its `total_stars` of both nights.
- **Totals that change back:** a total that goes from 100 to 105 on the first held-back night and back to 100 on the second lands as 100.
- **Continuing from held-back data:** the second night of a held-back project doesn't re-fetch the first night's days.
- **New targets in concatenated projects:** a target added to a project whose months are concatenated lands its history in the period files.
- **Repairs:** the October `total_reactions` incident is repaired by replay, and the Plausible pagination incident by re-fetch, from start to finish. Re-fetching a past GitHub range leaves the stored `total_stars` and `total_reactions` unchanged, or replays them from the archive.
- **Held back across the 8th:** a project held back before the concatenation day and passing after it lands its records in the new monthly file.
- **Failed jobs:** a GitHub job that fails stores nothing, holds nothing back, and is fetched again the next night.
- **Manual runs:** `release` lands held-back data, and `discard` makes the next run re-fetch it.
- **Opt-in:** without `[settings] gate = "enforce"`, the v1.4.0 gate still only reports.
- **Global findings:** a global error holds back everything, and a global warning holds back nothing.

**Acceptance:** an injected error in one devrel-io project holds back only that project for two nights. All of its data lands once the error is fixed, including its snapshots, and the stored data never loses a record.

### 4b. Quota budget and per-month reconciliation

Worktree `reliability/reconcile-monthly`. Size: medium.

| Task | Files |
|---|---|
| The quota budget: reconciliation uses at most 10% of each shared quota (GitHub REST and GraphQL, Plausible) and gives way first, with `quota.low`. The search API, which only reconciliation uses, has its own budget of 1,500 searches a night (270 until velocirepo 1.4.0) at no more than 25 a minute (30 until 2026-10-22, which GitHub refused at the 31st search when its minute reset late; a refused search now waits and tries again) | `internal/reconcile/budget.go` |
| Per-month search counts for the last complete month, rotated over seven nights | `internal/source/github_reconcile.go`, `internal/reconcile/rotation.go` |

**Tests:** the remaining [reconciliation tests](2026-10-reliability.md#reconciliation-tests):
- per-month windows in the right time zone;
- the weekly rotation;
- the quota.

**Acceptance:** over a week of devrel-io runs, every repository's last month is checked once. No run uses more than 10% of the shared GitHub quotas, or more than 270 searches.

### 4c. Reconciliation for other sources

One worktree per source, all in parallel: `reliability/reconcile-<source>`. Size: small each. Each stream first checks that the reference total exists and agrees with stored data, since the design marks these "to verify". If it doesn't, the stream ends by changing the source's declaration to "no reference totals", with the reason.

| Source | Reference total to verify |
|---|---|
| Plausible | aggregate query without the day dimension |
| CRAN | cranlogs totals over a range |
| PyPI | pypistats recent totals |

**Tests:** per source, the reference totals are parsed from canned responses and compared within tolerance.

### Release v1.4.0 and devrel-io

- **Release:** the upgrade notes explain:
  - that nightly runs no longer remove or change stored data;
  - holding back, and the held-back directory;
  - the manual runs: release, discard and repair.
- **devrel-io:** switch from report-only to enforcing by setting `[settings] gate = "enforce"`. The first concatenation commit after that is also checked by hand.

## Phase 5: completion

**Goal:** the remaining measures. Release v1.5.0. Four streams in parallel, then documentation.

| Stream | Worktree | Contents | Size |
|---|---|---|---|
| 5a Weekly digest | `reliability/digest` | A view with a sparkline per source and project over 8 weeks, plus the week's findings, posted as a comment on a long-lived "Weekly digest" issue with the label `velocirepo-digest`, by a workflow running the action with `mode: digest` | medium |
| 5b External heartbeat | `reliability/healthchecks` | Optional Healthchecks pings at start and end, enabled by a secret, with escalation to ntfy or email | small |
| 5c Changelog monitoring | `reliability/changelogs` | A `fetch-rss` configuration in velocirepo's repository for the GitHub and Plausible changelogs, flagging entries that mention endpoints velocirepo uses | small |
| 5d Incident suite | `reliability/incidents` | The [incident table](2026-10-reliability.md#8-testing-the-checks) as one test file. Each incident is replayed or applied and must produce its finding while the stored data is kept. Each check gets a mutation test in plain Go: with the check disabled, its incident test must fail. The CRAN and Plausible reconciliation cases depend on what 4c verified | medium |
| 5e Documentation | `reliability/docs` | A user guide chapter on the gate, the archive, findings, held-back data, releases and repairs; `CLAUDE.md` updated | medium |

5a, 5b and 5c each add a template to `ci_install.go`, in different functions, so their conflicts are small. 5e goes last.

## Parallelism at a glance

| Phase | Can run at the same time | Has to wait for |
|---|---|---|
| 0 | — | — |
| 1 | 1a, 1b, 1c, 1d | phase 0 |
| 2 | 2a, 2b, 2c, 2d, 2e | 2a and 2e: 1b; 2b: 1c; 2c: 1a; 2d: 1b and 1c |
| 3 | 3a, then 3b | everything in phase 2; 3b also 3a |
| 4 | 4a, 4b, and every 4c | phase 3, and two weeks of report-only runs |
| 5 | 5a, 5b, 5c, 5d, then 5e | phase 4 |

1d depends only on phase 0, so it can start early if there's capacity. 4c's verification of each reference total (does it exist, does it agree?) can also be done early, as a spike. Phase 3 is the bottleneck: the gate brings everything together, so it gets the most review.

## Risks

| Risk | Effect | Mitigation |
|---|---|---|
| The archive is larger than estimated | Storage and upload time | Phase 1 measures it before anything depends on it, and the storage interface allows other backends. |
| GitHub's search or release limits differ from what's documented | Reconciliation or storage hits limits | The live tests (1d) and the acceptance runs on devrel-io check each limit before it's relied on. |
| A source's published day is wrong or missing | A day is skipped or requested again | Without a published day, the watermark only moves to the latest day returned, which is always safe. The live tests check cranlogs' `last-day`. |
| A user's Plausible token has no Sites API access | Plausible falls back to UTC−12 and arrives a day later | `[settings.timezones]` (1a), and the upgrade notes explain it. |
| A user can't change repository settings | Features that need them don't work | The gate needs none: no pull requests, only the workflow token with `contents: write`, `issues: write` and `actions: read`, which the workflow grants itself. |
| False positives make the gate a nuisance | Findings are accepted instead of fixed | Two weeks of report-only runs, accepted findings expire, and a manual release is always possible. |
| The gate's Git operations fail in CI | Nothing is pushed | The gate is tested against a real temporary repository, and the heartbeat notices a missing run. |
| Phase 3 is large | Long review | 3a is split into commits per task, each reviewed on its own. |

## What's not in this plan

- Holding back per target instead of per project.
- A separate token for reconciliation.
- Pull requests or a branch for held-back data.
- A separate workflow for commits made by hand.
- Notifications outside GitHub, other than the optional Healthchecks escalation.

The design explains why for each.

## Deviations during implementation

What was built differs from this plan in these places, mostly because the data showed something the plan didn't expect:

- **Replay** has a runner of its own (`fetch.Replay`) that runs each job of a manifest with its recorded options, instead of per-job overrides in `fetch.Options`. GitHub is replayed through its snapshot unit only.
- **Deprecation notices** go into `fetch --results`, not the fetch log.
- **GitHub's archived requests** are GraphQL responses, which only contain the requested fields, so no visibility detection was needed.
- **The 44,619 `total_reactions`** were never in a commit: the buggy write dropped them within the run that fetched them, and the October repair added them back. The diff checks can't see a loss like that; reconciliation and replay can. A unit test covers the case where such a loss does cross commits.
- **Schema migrations** are compared by running the real migrations on a copy of the base's files, instead of mapping old metric names, which also covers renamed fields.
- **GitHub's fork total** is `forks.totalCount`, the direct forks velocirepo stores; `forkCount` is the whole fork network. Deleted forks stay stored, so only missing forks are reported.
- **All-time totals include today,** which isn't fetched yet, so their tolerance adds two of the target's busiest recent days. On devrel-io, the remaining differences were exactly the pull requests opened that day.
- **CRAN and Plausible** are reconciled per month, on the same weekly rotation as GitHub's searches, since their totals are over ranges: cranlogs' and Plausible's (pageviews and visits; visitors don't add up over days) matched devrel-io's stored data exactly. **PyPI** declares no reference totals: pypistats' recent week and month totals don't match its daily data.
- **The rotation** spreads targets over the nights by their place in the sorted list rather than a hash, so every night gets an equal share and stays within the search budget.
- **Blocking errors:** the only error about the night's new records that the checks produce today is a duplicate added that night (`data.integrity`, from the diff checks' multiplicities). A duplicate that was stored already isn't the night's.
- **Held-back data** is kept as the night's new records and events, whole changed content files, and the watermark maps, and overlaid through the store, rather than as copies of files.
- **A repair** is a manual run of its own: it replaces the nightly fetch and doesn't concatenate.
- **The commit message** lists how far each source is stored, without the "published through" note.
- **The heartbeat** reads the nightly workflow's schedule from its file, and needs no config, so its workflow only checks out `.github/workflows`.
- **The digest** marks a source and project as stopped when it had data in at least four of the six earlier weeks and none in the last two; quieter projects with the odd active week aren't marked.
- **Changelog monitoring** follows Plausible through its GitHub releases feed, which is where it announces API changes.
- **In `fetch` mode,** the action only archives with `archive: true`; the gate archives by default.
- **Concatenation and stale targets:** on a copy of devrel-io where every watermark lagged by weeks, the stale targets' months were concatenated without their last days. That's the rule as designed, and harmless, since late data goes into the period file, but it means the data condition only holds back months for targets within their freshness limits.
