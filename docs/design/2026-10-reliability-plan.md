# Plan: detecting and recovering from data problems

Status: for review. Nothing is implemented yet.

This plan turns the [design](2026-10-reliability.md) into phases of work. Each phase ends with a minor release that devrel-io adopts before the next phase builds on it, so problems with the design show up early. Within a phase, the work is split into streams that can be done at the same time, each in its own Git worktree.

## Overview

| Phase | What it delivers | Streams in parallel | Release |
|---|---|---|---|
| 0. Interfaces | The shared types, per-source declarations and a hook into fetch that every stream builds on | 1 (sequential) | — |
| 1. Foundations | Days fetched once they've ended; the raw archive behind a flag; `velocirepo check` with data checks; live API tests | 4 | v1.1.0 |
| 2. Building blocks | Replay; diff checks; new concatenation rules; GitHub reconciliation; archive storage | 5 | v1.2.0 |
| 3. Report-only gate | `velocirepo gate` with concatenation, the new workflows, reporting but holding nothing back; the heartbeat | 2 | v1.3.0 |
| 4. Enforcing gate | Holding back per project on a branch; per-month reconciliation and the quota budget; reconciliation for other sources | 3+ | v1.4.0 |
| 5. Completion | Weekly digest, external heartbeat, changelog monitoring, the incident replay suite, documentation | 5 | v1.5.0 |

devrel-io follows each release: measuring the archive after phase 1, running `check` by hand after phase 2, running the gate in report-only mode after phase 3, and enforcing it after phase 4.

```
Phase 0 ── interfaces ─┬─► 1a day ends ───────────► 2c concatenation ──────┐
                       ├─► 1b transport + archive ─┬─► 2a replay            │
                       │                           └─► 2e archive storage ──┤
                       ├─► 1c check + data checks ─┬─► 2b diff checks ──────┼─► 3a gate (report-only) ─► 3b heartbeat
                       │                           └─► 2d GitHub reconcile ─┘          │
                       └─► 1d live API tests                                           ▼
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

A stream is merged into `main` through a pull request once its tests pass. A phase's streams branch off `main` after the streams they depend on are merged. The streams within a phase don't depend on each other, so their merge order doesn't matter, apart from the conflicts listed below.

The streams can be worked on by different people, or by separate Claude Code agents, each in its own worktree. Either way, the review before merging is the same: the stream's tests, `go test -race ./...`, `go vet ./...` and `gofmt`.

### Avoiding conflicts

Phase 0 exists so that parallel streams rarely touch the same lines.

- **Per-source information lives in the source files, and phase 0 declares it for every source.** Phase 0 defines optional interfaces a source implements in its own file: its time zone (`DayZone`), its archive policy (`ArchivePolicy`) and its reference totals (`Reconciler`). It also gives every existing source its initial declarations: UTC, its archive policy, and "no reference totals yet". Later streams only change the declarations of the sources they work on, in those sources' files.
- **Injection points in fetch.** Phase 0 adds to `fetch.Options`: an observer called for every job with its resolved `FetchOptions` and time zones, a wrapper for the HTTP transport, per-job option overrides, and the concatenation policy. The archive (1b) installs its capture through the transport wrapper and records jobs through the observer, replay (2a) uses the overrides, and concatenation (2c) sets the policy, so none of them has to edit `internal/fetch/fetch.go` beyond what its owner allows.
- **Hotspots get one owner per phase:**

| File | Owned by |
|---|---|
| `internal/fetch/fetch.go` | phase 0 (the injection points), then 1a in phase 1, then 2a in phase 2 (2c rebases if it needs a change) |
| `internal/fetch/stats.go` (the HTTP transport) | 1b |
| `internal/store/aggregate.go` | 2c |
| `cmd/velocirepo/cmd/ci_install.go` and `action.yml` | 1a (the schedule only), then 3a, then 3b after 3a is merged, then 5a–5c in separate functions |
| `internal/config/config.go` | anyone; 1a, 2c and 3a each add one setting, in different phases |
| `cmd/velocirepo/cmd/root.go` | anyone; each new command adds one line, so conflicts are trivial |

- **New code goes into new packages:** `internal/archive` (1b), `internal/check` (1c), `internal/reconcile` (2d), `internal/gate` (3a).
- **One known overlap:** 2a may change sources that still read the clock directly, and 2d changes the GitHub source's reconciliation declaration. Both are small; if they touch the same file, the second to merge rebases.

### Definition of done

For every task:

- A test that fails without the change comes first, as in the October fixes.
- `gofmt`, `go vet ./...` and `go test -race ./...` pass.
- User-facing behaviour is documented in `user_guide/`, and the release's upgrade notes mention it.
- Commits are small, with the reason in the message, as in the repository's history.

## Phase 0: interfaces

**Goal:** the shared types, merged into `main` before any stream starts. No existing behaviour changes, apart from the `@v0` fix; the only additions are the injection points and the `--results` flag.

Sequential, in one branch (`reliability/interfaces`). Size: small.

| Task | Files | Contents |
|---|---|---|
| 0.1 Findings | `internal/check/finding.go` | The `Finding` type (`check`, `severity`, `source`, `project`, `target`, `message`, `paths`), its stable ID, and the JSON schema of `check.json` |
| 0.2 Source metadata | `internal/source/meta.go`, every `internal/source/*.go` | The optional interfaces. `DayZone(ctx, target, series) (*time.Location, error)`, per target and series, since the GitHub source has UTC events and a Pacific star history, and a Plausible site's zone is read over the network. `ArchivePolicy`, which gives the archive mode (full, or read fields only) and the tier of each request. `Reconciler`. And per-metric declarations of each metric's kind (dense, sparse or change-only) and tier (can or can't be re-fetched). Every existing source declares UTC, its archive policy (full for public APIs and Plausible and GitHub traffic, read fields only for LinkedIn), its metrics' kinds and tiers, and "no reference totals yet". A source without an `ArchivePolicy` isn't archived. |
| 0.3 Registry tests | `internal/source/meta_test.go` | Every registered source declares an archive policy, a reconciliation status, and a kind and tier for every metric it emits, like the test that keeps badge presets in line with the metrics sources emit |
| 0.4 Archive record | `internal/archive/record.go` | One archived exchange: time, method, redacted URL, redacted request body, status, selected headers, body. The manifest, with each job's start date, end date, run time and resolved time zones. The file layout `raw/<run-id>/<source>.jsonl.zst` |
| 0.5 Injection points and results | `internal/fetch/fetch.go`, `internal/fetch/results.go`, `cmd/velocirepo/cmd/fetch.go` | The observer, transport wrapper, per-job overrides and concatenation policy in `fetch.Options`, with today's behaviour as their defaults. `fetch --results <file>` writes the run's `[]Result` as JSON, with the quota left per API, so `check` and `gate` can read what the run did. |
| 0.6 Action reference | `cmd/velocirepo/cmd/ci_install.go` | `install-ci` maps "latest" to `@v0` (`ci_install.go:141`), so workflows generated with v1.0.0 reference the v0 action. Map it to the current major version instead. This is a bug today, so it can also be released on its own, as v1.0.1. |

**Acceptance:**
- The packages build, and the types have doc comments.
- `Finding` IDs are stable: the same input gives the same ID, a different target gives a different one, and the same finding with different counts in its message keeps its ID.
- The registry tests pass.
- Existing tests are unchanged.

## Phase 1: foundations

**Goal:** four independent foundations. Release v1.1.0.

### 1a. Days fetched once they've ended

Worktree `reliability/day-ends`. Size: medium. Design: [measure 4](2026-10-reliability.md#4-fetch-each-day-once-it-has-ended).

| Task | Files |
|---|---|
| Per-source end date: the latest day that has ended in the source's time zone, plus an hour's margin, replacing `settleDays` | `internal/fetch/fetch.go` |
| `DayZone` for the star history (US Pacific) | `internal/source/github_events.go` |
| `DayZone` for Plausible: the site's time zone from `GET /api/v1/sites/{site}`, cached per run; `[settings.timezones]` overrides it; UTC−12 if neither is available | `internal/source/plausible.go`, `internal/config/config.go` |
| Resume points per target and series: a watermark of the day each target was fetched through, using the existing per-target watermarks (`store.MarkFetched`), with the star history as its own series. It replaces the star history's 7-day re-read and generalises its backfill: no star-history watermark means the whole history | `internal/fetch/fetch.go`, `internal/store/watermark.go`, `internal/source/github_events.go` |
| `install-ci` schedules the nightly run after the latest time zone of the configured sources | `cmd/velocirepo/cmd/ci_install.go` (one function; 3a later rewrites the template) |

**Tests:**
- **End dates per run time:** a run at 06:00 UTC fetches CRAN through yesterday, and the star history and plotnine's Plausible site through the day before. A run at 09:30 UTC fetches all of them through yesterday.
- **No gap:** a run at 06:00 UTC followed by one at 09:30 UTC the next day leaves no missing star day. The same holds for a project with two Plausible sites in different time zones: neither site misses a day.
- **Quiet repositories:** a repository without stars for months still moves its star-history watermark every night, and stores no star days.
- **Daylight saving time:** the margin covers both PDT and PST.
- **Time zone sources:** `[settings.timezones]` overrides the Sites API. Without either, Plausible falls back to UTC−12. Invalid zone names are rejected by config validation.
- **Explicit end date:** an explicit `--end-date` is used as given.
- **Star days are written once:** the star history writes no day it has already stored.

**Acceptance:** at 09:30 UTC, devrel-io's sources all end on the same day.

### 1b. HTTP transport and raw archive

Worktree `reliability/archive`. Size: large. Design: [raw response archive](2026-10-reliability.md#1-raw-response-archive).

| Task | Files |
|---|---|
| A transport chain in place of `countingTransport`: counting, rate-limit tracking (quota left per API, from response headers) and capture | `internal/fetch/stats.go`, `internal/archive/capture.go` |
| Redaction of secret query parameters and body fields, and a check that refuses an archive containing a known secret | `internal/archive/redact.go` |
| Applying each source's `ArchivePolicy`: the mode and the tier of every request; LinkedIn reduced to the fields it reads, after its post filter | `internal/archive/policy.go`, `internal/source/linkedin.go` |
| The archive writer: zstd JSONL per source, and the manifest with each job's options from the phase 0 hook | `internal/archive/writer.go` |
| `fetch --archive <dir>`, off by default | `cmd/velocirepo/cmd/fetch.go` |
| `Deprecation` and `Sunset` headers in the fetch log | `internal/archive/capture.go`, `internal/store/log.go` |

**Tests:**
- **Capture:** every source's requests are captured, and bodies reach the source unchanged.
- **Redaction:** each secret is redacted, in URLs and request bodies, including Plausible's and YouTube's parameters. An archive containing a test secret is refused.
- **Archive policy:** a source without an archive policy isn't archived. LinkedIn drafts and non-public posts never reach the archive.
- **Manifest:** it records each job's options, and every archived request has a tier.
- **Quota:** rate-limit headers are tracked per API, and appear in `--results`.
- **Memory:** large responses are streamed, not held in memory twice.

**Acceptance:** a full devrel-io fetch with `--archive` produces an archive in which no secret from `.env` appears. Its size is recorded.

### 1c. `velocirepo check` with data checks

Worktree `reliability/check`. Size: medium. Design: [checks in the gate](2026-10-reliability.md#checks-in-the-gate).

| Task | Files |
|---|---|
| The `check` command: runs the registered checks, writes `check.json` and a Markdown summary, exits non-zero on errors | `internal/check/check.go`, `cmd/velocirepo/cmd/check.go` |
| Accepted findings from `velocirepo/checks.toml`, with reason and expiry | `internal/check/accept.go` |
| `fetch.failed`, from `fetch --results` | `internal/check/fetch.go` |
| `data.integrity`, reusing `store.ValidateData` | `internal/check/integrity.go` |
| `data.freshness`, from each target's watermark, with a limit for every source | `internal/check/freshness.go` |
| `data.completeness`: missing days inside a stored range, for dense metrics only | `internal/check/completeness.go` |
| `data.volume`: a series that usually has data (on at least 20 of the last 28 days) at zero or far below its 28-day median | `internal/check/volume.go` |
| `fetch.failed` also reads a past run's results from the fetch log (`data/log/`), so historical runs can be checked | `internal/check/fetch.go` |

**Tests:**
- **Fixtures per check:** each check reports exactly the findings of a fixture: month ends missing for completeness, a source that stops returning data for volume, stale traffic for freshness.
- **Quiet series report nothing:** a quiet repository, a small Plausible site and an unchanged total each have a fixture that must report nothing.
- **Every source has a freshness limit:** a test requires one for every registered source.
- **Accepted findings:** an accepted finding is reported as accepted; an expired one isn't.
- **Output:** the Markdown summary is stable (a golden file), and the exit code is non-zero only on unaccepted errors.

**Acceptance:**
- Run on devrel-io as it was before the October repair (commit `f8e345cde`), `check` reports the CRAN month ends. Run on the fetch logs of 2026-10-17 to 10-20, `fetch.failed` reports the four failed nights. positron, quarto and rstudio are caught by reconciliation (2d), since three months without events leave `data.volume` nothing to compare with.
- Run on today's devrel-io, it reports only the known bot-comment duplicate, and accepting that finding makes it pass.

### 1d. Live API tests

Worktree `reliability/live-tests`. Size: medium. Design: [measure 5](2026-10-reliability.md#5-live-api-tests-and-upstream-changes).

| Task | Files |
|---|---|
| Tests with the build tag `live`, one per source, fetching known public targets. They check the response shape, types, plausible ranges and known limits: the 300-page comment cap, 30-week star history pages, and the Plausible Sites API | `internal/source/*_live_test.go` |
| A scheduled workflow that runs them daily with the repository's secrets, and opens an issue in velocirepo when they fail | `.github/workflows/live.yml` |

**Tests:** these are the tests. Without the `live` tag they're skipped, so `go test ./...` stays offline, as `CLAUDE.md` requires.

Each source's target and credentials:

| Source | Target | Secret in velocirepo's repository |
|---|---|---|
| GitHub events, star history, reconciliation | a public repository, such as velocirepo itself | `GH_TOKEN` |
| GitHub traffic | velocirepo itself (needs admin access) | `GH_TOKEN` with admin access to velocirepo |
| CRAN, PyPI, Homebrew, Open VSX, RSS | public packages and feeds | none |
| Plausible | none public | skipped, unless a `PLAUSIBLE_TOKEN` for a test site is added |
| YouTube | a public channel | `YOUTUBE_TOKEN` |
| LinkedIn | none public | skipped: its tokens expire after 60 days |

A test whose secret is missing reports itself as skipped with the reason, so a missing secret is never mistaken for a pass.

**Acceptance:** the workflow passes against the real APIs, and changing an expected field name makes it fail and open an issue.

### Release v1.1.0 and devrel-io

- **Release:** 1a changes when sources are fetched. The upgrade notes say:
  - Plausible and stars arrive on the same day as the other sources, if the workflow runs after the latest time zone.
  - `install-ci` suggests the new schedule.
  - Accounts without Sites API access can set `[settings.timezones]`.
- **devrel-io:**
  - Move the nightly run to 09:30 UTC.
  - Fetch with `--archive` and upload the archive as an artifact. That's a manual workflow edit, until 3a generates it.
  - For a week, measure the archive's size and check its redaction.

## Phase 2: building blocks

**Goal:** everything the gate needs. Release v1.2.0.

Five streams in parallel, all branching off `main` after phase 1 is merged.

### 2a. Replay

Worktree `reliability/replay`. Depends on 1b. Owns `internal/fetch/fetch.go` in phase 2. Size: medium.

| Task | Files |
|---|---|
| A transport that serves archived responses, matched by method, redacted URL and body hash; an unmatched request is an error | `internal/archive/replay.go` |
| `velocirepo replay <run-id> [--source] [--project]`, which runs each job with the options recorded in the manifest, through phase 0's per-job overrides, and writes records like `fetch` | `cmd/velocirepo/cmd/replay.go` |
| Remove remaining clock dependencies from sources, if any, so replays are deterministic | `internal/source/*.go` |

**Tests:**
- **Round trip:** a fetch followed by a replay of its archive writes identical files, for every source.
- **Later replays:** a replay after newer data has been stored still makes the original requests, because it uses the manifest's options instead of the resume point.
- **Concatenated periods:** a replay into a month that's already concatenated merges with newest-wins.
- **Unmatched requests:** they fail.
- **Fixes:** a replay with a fixed bug writes the corrected records. The fixture is the Plausible pagination bug.

**Acceptance:** replaying a week of devrel-io archives rebuilds the stored records exactly.

### 2b. Diff checks

Worktree `reliability/diff-checks`. Depends on 1c. Size: medium.

| Task | Files |
|---|---|
| Read the `HEAD` versions of the changed projects' files with `git cat-file --batch`, without a checkout, and compare records by key. A second mode, `--base <commit> --head <commit>`, compares two commits the same way, for the check on pushes; without a run window it exempts nothing except commits with a `Velocirepo-Repair:` trailer | `internal/check/diff.go` |
| `diff.records-removed`, with per-source allowances (deleted comments and forks) | `internal/check/diff.go` |
| `diff.values-changed`, exempting records dated within the run's window and content files | `internal/check/diff.go` |
| `diff.content-removed`: an ID that disappears from a content file | `internal/check/diff.go` |
| `concatenate.records-moved`: the record sets before and after concatenation are identical | `internal/check/concatenate.go` |

**Tests:**
- **Incidents:** replays of the incidents report errors: the July deletion (`03f2f5e66`), the 44,619 `total_reactions`, and the Plausible duplicates.
- **Normal nights:** a normal nightly commit (`bc6b5d81b`) reports nothing.
- **Concatenation:** records that move between files during concatenation aren't reported as removed.
- **Content files:** content updated in place isn't reported, but a content entry that disappears is.
- **Cost:** only the changed projects' files are read.
- **Two commits:** `--base` and `--head` find a deletion spread over three commits.

**Acceptance:** on devrel-io's history since July, `diff.records-removed` and `diff.content-removed` report exactly the known incidents. `diff.values-changed` is left out of this historical check: before 1a, nightly runs rewrote star days in the 7-day re-read window by design.

### 2c. Concatenation rules

Worktree `reliability/concatenation`. Depends on 1a (both change `config.go`, and 2c's data condition uses 1a's end dates). Size: small. Design: [concatenation](2026-10-reliability.md#concatenation).

| Task | Files |
|---|---|
| A month is concatenated on or after the concatenation day (`[settings] concatenate_on_day`, default 8), and only once a day after its last day is stored | `internal/store/aggregate.go`, `internal/config/config.go` |
| `store.Concatenate(dataDir, now, allow func(project) bool)`, so the gate can exclude projects, passed to fetch through phase 0's concatenation policy | `internal/store/aggregate.go` |

**Tests:**
- **The calendar:** before the 8th nothing is concatenated; on the 8th the month is, and in January also the year.
- **The data:** a source that's behind waits.
- **Exclusion:** an excluded project keeps its daily files.
- **Unchanged behaviour:** `LastDate` and the resume point are unchanged, and the existing concatenation tests pass with the day set to 1.

**Acceptance:** run on a copy of devrel-io dated 2026-11-08, it concatenates October in one step.

### 2d. GitHub reconciliation

Worktree `reliability/reconcile-github`. Depends on 1b (capture and quota tracking) and 1c. Size: medium. Design: [reconciliation](2026-10-reliability.md#3-reconciliation).

| Task | Files |
|---|---|
| The reconciliation engine: reference totals from each `Reconciler`, compared with stored data within the source's tolerance, as `reconcile.*` findings | `internal/reconcile/reconcile.go`, `internal/check/reconcile.go` |
| `check --archive <dir> --run-id <id>`: reference requests go through the same capture as fetch and are archived with the run; without these flags they aren't archived | `internal/check/reconcile.go`, `cmd/velocirepo/cmd/check.go` |
| The quota left per API, read from `fetch --results` and updated from the reference responses' headers | `internal/reconcile/reconcile.go` |
| GitHub all-time totals, batched in GraphQL: issues, pull requests, merged pull requests, forks, stars | `internal/source/github_reconcile.go`, and the GitHub source's declaration |

**Tests:** the [reconciliation tests](2026-10-reliability.md#reconciliation-tests) that apply to all-time totals:
- matching totals and tolerances;
- missing and duplicated data;
- a failed or suddenly zero reference total.

Plus:
- **Archiving:** reference requests are archived when `--archive` is given, and not otherwise.

**Acceptance:**
- On devrel-io as of `f8e345cde`, it reports positron, quarto and rstudio, and the stars of every repository.
- On today's devrel-io, it reports nothing.
- All 465 repositories take at most 15 requests.

### 2e. Archive storage

Worktree `reliability/archive-storage`. Depends on 1b. Size: small.

| Task | Files |
|---|---|
| A storage interface with two implementations: Actions artifacts (through the action) and release assets in the data repository (`raw-YYYY-MM`), uploaded with the workflow token | `internal/archive/store.go`, `internal/archive/release.go` |
| Retention per tier, from each request's tier: release assets only for requests that can't be re-fetched. Artifact retention is 90 days in public repositories and 14 in private ones, set by `[settings] archive_retention_days`. A failed upload is a warning, not a finding that holds data back | `internal/archive/store.go`, `internal/config/config.go` |
| `velocirepo download-archive <run-id>`, for replays, named verb-first like the other commands | `cmd/velocirepo/cmd/download_archive.go` |

**Tests:**
- **Upload and download:** both go through an httptest server that imitates GitHub's release API.
- **Tiers:** they're applied per request: in one GitHub run, the snapshot requests go into release assets and the event requests only into the artifact.
- **Private repositories:** the default retention is 14 days, and a failed upload is a warning.
- **Errors:** a 403 names the missing permission.
- **Size limit:** an upload over the 2 GB asset limit is split.

**Acceptance:** a devrel-io archive is uploaded as a release asset, downloaded, and replayed.

### Release v1.2.0 and devrel-io

- **Release:** replay, `check` with diff and reconciliation checks, and archive storage. `fetch` now concatenates with the new rule: on or after the 8th of the month, and once a day after the month's last day has been stored. The upgrade notes explain that daily files stay in place a week longer than before.
- **devrel-io:** run `velocirepo check` by hand after a few nightly runs, and accept or fix what it reports, before the gate depends on it.

## Phase 3: report-only gate

**Goal:** the gate and the new workflows, in report-only mode. Release v1.3.0.

### 3a. Gate and workflows

Worktree `reliability/gate`. Depends on everything in phase 2. Size: large.

| Task | Files |
|---|---|
| `velocirepo gate --report-only`. It runs `check`, writes the commit messages, makes the data commit, updates the tracking issue, and writes the job summary and annotations. It calls GitHub's REST API directly, like `sync-secrets`, and runs `git` for commits | `internal/gate/*.go`, `cmd/velocirepo/cmd/gate.go` |
| Tokens: `GITHUB_TOKEN` keeps meaning the personal access token, as `fetch` and `sync-secrets` read it today. The gate gets the workflow token through `--repo-token` or `VELOCIREPO_REPO_TOKEN`, from the action's new `repo-token` input, which defaults to `github.token` | `internal/gate/*.go`, `action.yml` |
| The action's new steps are opt-in: its `mode` input defaults to `fetch`, which runs `velocirepo fetch` as today; workflows generated by `install-ci` set `mode: gate` | `action.yml` |
| The concatenation step: on the concatenation day, the gate concatenates with the calendar and data conditions and makes the concatenation commit, checked by `concatenate.records-moved`. Phase 4 adds the exclusion of projects with findings | `internal/gate/concatenate.go` |
| The tracking issue: findings grouped by origin in a hidden JSON block, each writer replacing only its own, comments only when findings change, closed when no origin has findings left, owners assigned | `internal/gate/issue.go` |
| Concurrency and Git: one concurrency group for every writing workflow; explicit fetches of the checked ref, the push's commits and the held-back branch; when the push to `main` is rejected, rebase, and rerun the diff checks for projects the new commits touch | `internal/gate/push.go`, `cmd/velocirepo/cmd/ci_install.go` |
| `[settings] owners`, filled in by `install-ci` | `internal/config/config.go`, `cmd/velocirepo/cmd/ci_install.go` |
| The nightly workflow template: `fetch --no-concatenate --archive --results`, the artifact upload, `gate`, and the permissions `contents: write`, `issues: write` and `actions: read` | `cmd/velocirepo/cmd/ci_install.go`, `action.yml` |
| The check on pushes made by hand (`velocirepo-check.yml`): `check --base --head` from `refs/velocirepo/checked` to the pushed commit, then moving the ref; the gate also moves it past its own pushes | `cmd/velocirepo/cmd/ci_install.go`, `internal/gate/push.go` |

**Tests:**
- **GitHub's API** (issues, assignees, comments) is an httptest server, as in the existing `sync-secrets` tests. Git operations run against a real temporary repository.
- **The issue's lifecycle:** opened, updated without a comment, commented on a change, closed.
- **Tokens:** with `GITHUB_TOKEN` and `VELOCIREPO_REPO_TOKEN` set to different values, each call uses the right one, and the personal token is never used for issues, pushes or releases. A 403 from the issues API becomes a finding that names the missing permission.
- **Old workflows:** the old workflow template, rendered against the new `action.yml`, still fetches, concatenates and commits as before.
- **Issue ownership:** the check on pushes opens the issue for a bad commit, a clean nightly run follows, and the issue stays open.
- **A moving `main`:** a commit pushed during the run, touching another project, is rebased over; one touching the same project reruns its checks.
- **Cancelled checks:** two pushes in quick succession, one check cancelled by GitHub, and the next check still covers both.
- **Commit messages:** the run's date, and the dates per source.
- **Report-only mode:** it commits everything.
- **Concatenation:** it happens on the 8th in its own commit, also in report-only mode.
- **Pushes made by hand:** a push of three commits is checked as a whole, from the checked ref.
- **Workflows:** the generated workflows match golden files. CI also runs `actionlint` (MIT) on them.

**Acceptance:**
- devrel-io's nightly run makes the data commit, and opens an issue for an injected finding.
- A push by hand that deletes a project's file opens the issue.
- No repository setting has to change.

### 3b. Heartbeat workflow

Worktree `reliability/heartbeat`. Depends on 3a's templates and issue code: branch off `main` after 3a is merged. Size: small.

| Task | Files |
|---|---|
| `velocirepo check-heartbeat`. It asks GitHub's Actions API whether the nightly workflow has completed a run since its scheduled time that day, fails and opens the issue if not, adds one escalation comment once the issue has been open for three days (not in report-only mode), and runs reconciliation's all-time totals against `main` | `internal/gate/heartbeat.go`, `cmd/velocirepo/cmd/check_heartbeat.go` |
| The heartbeat workflow template, scheduled by `install-ci` three hours after the nightly run, with `actions: read` | `cmd/velocirepo/cmd/ci_install.go` |

**Tests:**
- **A single missed night:** with no nightly run since that day's scheduled time, the heartbeat fails and opens the issue the same day.
- **The schedule:** `install-ci` puts the heartbeat after the nightly run, also when the nightly run is late because of the UTC−12 fallback.
- **An open issue:** it doesn't fail the heartbeat. The escalation comment is added once, after three days, and never in report-only mode.
- **A reconciliation mismatch on `main`:** it opens the issue.

**Acceptance:** disabling devrel-io's nightly schedule makes the heartbeat open the issue the same day.

### Release v1.3.0 and devrel-io

- **Release:** the upgrade notes ask users to regenerate their workflows with `install-ci`, and say that no repository settings need to change.
- **devrel-io:** regenerate the workflows, and run in report-only mode for two weeks. During that time:
  - tune the thresholds;
  - accept the false positives in `velocirepo/checks.toml`;
  - fix real findings.

## Phase 4: enforcing gate

**Goal:** bad data stays off `main`. Release v1.4.0. The nightly workflow's behaviour changes, but only for users who regenerate their workflows with `install-ci`; existing workflows keep working as before. The upgrade notes put this first.

### 4a. Enforcing gate

Worktree `reliability/gate-enforce`. Size: medium. Design: [what happens to held-back data](2026-10-reliability.md#what-happens-to-held-back-data).

| Task | Files |
|---|---|
| Hold back per source and project: restore their files (metrics, events, content) from `HEAD`, and commit that night's diff on `velocirepo/held-back`, one commit per night, source, project and tier | `internal/gate/holdback.go` |
| Applying a night: for the tier that can be re-fetched, the new commit replaces the previous one; for the tier that can't, commits accumulate. A commit never deletes files outside the night's diff | `internal/gate/holdback.go` |
| Carry held-back commits forward: rebase them onto the new `main` every night | `internal/gate/holdback.go` |
| Release, per tier: once a project passes, drop its superseded re-fetchable commit, and land its other commits, oldest first, after checking them with the current rules. Data that ages out of the re-fetchable tier, such as traffic held back for more than 14 days, is moved to the other tier. Delete the branch when it's empty | `internal/gate/release.go` |
| Repairs: a manual run with `repair: <run-id>` replays that run in the gate, reports value changes that match the replay as `diff.repaired` warnings, and commits them with a `Velocirepo-Repair:` trailer | `internal/gate/repair.go`, `cmd/velocirepo/cmd/ci_install.go` |
| A manual run with `release: <project>` (`workflow_dispatch`), which lands a project's held-back commits regardless of findings and records that in the issue | `internal/gate/release.go`, `cmd/velocirepo/cmd/ci_install.go` |
| The issue links to `compare/main...velocirepo/held-back` | `internal/gate/issue.go` |
| Concatenation excludes projects with findings or held-back data | `internal/gate/concatenate.go` |
| Findings that don't belong to a project hold back the whole run | `internal/gate/holdback.go` |

**Tests:**
- **Per project:** one project with an error is held back, while the others land.
- **Snapshots in a mixed source:** a GitHub project is held back for two nights and then passes. Its `total_stars` from both nights lands on `main`, and its events land from the re-fetch. This is the case both reviews found.
- **Snapshot sources:** a Homebrew project held back for two nights lands both nights.
- **Re-fetchable data held back for two nights:** the branch keeps only the latest night's commit, which is dropped once the re-fetched data lands.
- **Ageing out:** traffic held back for 15 days moves to the tier that can't be re-fetched, and lands.
- **Repairs:** the Plausible pagination incident is repaired from start to finish through `repair: <run-id>`.
- **Rebasing:** the rebase onto a new `main` is clean.
- **Manual release:** `release: <project>` lands that project's data and records the release.
- **Concatenation:** it skips projects that are held back.
- **Global findings:** they hold back everything.

**Acceptance:** an injected error in one devrel-io project holds back only that project for two nights, including its Homebrew or YouTube snapshots. All of its data lands once the error is fixed.

### 4b. Quota budget and per-month reconciliation

Worktree `reliability/reconcile-monthly`. Size: medium.

| Task | Files |
|---|---|
| The quota budget: reconciliation uses at most 10% of each shared quota (GitHub REST and GraphQL, Plausible) and gives way first, with `quota.low`. The search API, which only reconciliation uses, has its own budget of 270 searches a night at no more than 30 a minute | `internal/reconcile/budget.go` |
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

- **Release:** the upgrade notes explain holding back, the held-back branch and its comparison, the manual release, and the concatenation commit on the 8th.
- **devrel-io:** switch from report-only to enforcing. The first concatenation commit after that is also checked by hand.

## Phase 5: completion

**Goal:** the remaining measures. Release v1.5.0. Four streams in parallel, then documentation.

| Stream | Worktree | Contents | Size |
|---|---|---|---|
| 5a Weekly digest | `reliability/digest` | A view with a sparkline per source and project over 8 weeks, plus the week's findings, posted as a comment on the tracking issue by a weekly workflow | medium |
| 5b External heartbeat | `reliability/healthchecks` | Optional Healthchecks pings at start and end, enabled by a secret, with escalation to ntfy or email | small |
| 5c Changelog monitoring | `reliability/changelogs` | A `fetch-rss` configuration in velocirepo's repository for the GitHub and Plausible changelogs, flagging entries that mention endpoints velocirepo uses | small |
| 5d Incident replay suite | `reliability/incidents` | The [incident table](2026-10-reliability.md#8-testing-the-checks) as one test file, each incident replayed and asserted to produce its error; mutation tests for the checks | medium |
| 5e Documentation | `reliability/docs` | A user guide chapter on the gate, the archive, findings, held-back data and repairs; `CLAUDE.md` updated | medium |

5a, 5b and 5c each add a template to `ci_install.go`, in different functions, so their conflicts are small. 5e goes last.

## Parallelism at a glance

| Phase | Can run at the same time | Has to wait for |
|---|---|---|
| 0 | — | — |
| 1 | 1a, 1b, 1c, 1d | phase 0 |
| 2 | 2a, 2b, 2c, 2d, 2e | 2a and 2e: 1b; 2b: 1c; 2d: 1b and 1c; 2c: 1a |
| 3 | 3a, then 3b | everything in phase 2; 3b also 3a |
| 4 | 4a, 4b, and every 4c | phase 3, and two weeks of report-only runs |
| 5 | 5a, 5b, 5c, 5d, then 5e | phase 4 |

1d depends only on phase 0, so it can start early if there's capacity. 4c's verification of each reference total (does it exist, does it agree?) can also be done early, as a spike. Phase 3 is the bottleneck: the gate brings everything together, so it gets the most review.

## Risks

| Risk | Effect | Mitigation |
|---|---|---|
| The archive is much larger than estimated | Storage and upload time | Phase 1 measures it before anything depends on it; tiers and the storage interface allow other choices. |
| GitHub's search or release limits differ from what's documented | Reconciliation or storage hits limits | The live tests (1d) and the acceptance runs on devrel-io check each limit before it's relied on. |
| A user's Plausible token has no Sites API access | Plausible falls back to UTC−12 and arrives a day later | `[settings.timezones]` (1a), and the upgrade notes explain it. |
| A user can't change repository settings | Features that need them don't work | The gate needs none: no pull requests, only the workflow token with `contents: write`, `issues: write` and `actions: read`, which the workflow grants itself. |
| False positives make the gate a nuisance | Findings are accepted instead of fixed | Two weeks of report-only runs, accepted findings expire, and a manual release is always possible. |
| The gate's Git operations fail in CI | Nothing is pushed | The gate is tested against a real temporary repository, and the heartbeat notices missing commits. |
| Phase 3 is large | Long review | 3a is split into commits per task, each reviewed on its own. |
| A private repository runs out of free artifact storage | Archive uploads fail | 14 days of retention by default in private repositories, a setting to change it, and failed uploads are warnings. |
| GitHub cancels a pending check on pushes | A commit made by hand isn't checked | The check covers everything since `refs/velocirepo/checked`, so the next check catches up. |

## What's not in this plan

- Holding back per target instead of per project.
- A separate token for reconciliation.
- Pull requests for held-back data.
- Notifications outside GitHub, other than the optional Healthchecks escalation.

The design explains why for each.
