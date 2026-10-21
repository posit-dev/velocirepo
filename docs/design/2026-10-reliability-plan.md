# Plan: detecting and recovering from data problems

Status: for review. Nothing is implemented yet.

This plan turns the [design](2026-10-reliability.md) into phases of work. Each phase ends with something that can be released and used in devrel-io, so problems with the design show up early. Within a phase, the work is split into streams that can be done at the same time, each in its own Git worktree.

## Overview

| Phase | What it delivers | Streams in parallel | Release |
|---|---|---|---|
| 0. Interfaces | The shared types and interfaces every stream builds on | 1 (sequential) | — |
| 1. Foundations | Days fetched once they've ended; the raw archive behind a flag; `velocirepo check` with data checks; live API tests | 4 | v1.1.0 |
| 2. Building blocks | Replay; diff checks; new concatenation rules; GitHub reconciliation; archive storage | 5 | v1.2.0 |
| 3. Report-only gate | `velocirepo gate` and the new workflows, reporting but holding nothing back; the heartbeat | 2 | v1.3.0 |
| 4. Enforcing gate | Holding back per project, the concatenation commit; per-month reconciliation and the quota budget; reconciliation for other sources | 3+ | v2.0.0 |
| 5. Completion | Weekly digest, external heartbeat, changelog monitoring, the incident replay suite, documentation | 4 | v2.1.0 |

devrel-io follows each release: measuring the archive after phase 1, running the gate in report-only mode after phase 3, and enforcing it after phase 4.

```
Phase 0 ── interfaces ─┬─► 1a day ends ──────────────────────────────────────────────┐
                       ├─► 1b transport + archive ─┬─► 2a replay                      │
                       │                           └─► 2e archive storage ──┐         │
                       ├─► 1c check + data checks ─┬─► 2b diff checks ──────┤         │
                       │                           ├─► 2d GitHub reconcile ─┼─► 3a gate (report-only)
                       │                           │                        │    3b heartbeat
                       │                           │   2c concatenation ────┘         │
                       └─► 1d live API tests                                          ▼
                                                    4a enforcing gate, 4b quota + monthly, 4c other sources
                                                                                      ▼
                                                    5a digest, 5b Healthchecks, 5c changelogs, 5d replay suite, 5e docs
```

## How the work is split

### Worktrees

Every stream gets a branch and a worktree off `main`, created once its phase starts:

```
git worktree add ../velocirepo-<stream> -b reliability/<stream> main
```

A stream is merged into `main` through a pull request once its tests pass. The next phase's streams branch off `main` after the streams they depend on are merged. The streams within a phase don't depend on each other, so their merge order doesn't matter, apart from the conflicts listed below.

The streams can be worked on by different people, or by separate Claude Code agents, each in its own worktree. In both cases, the review before merging is the same: the stream's tests, `go test -race ./...`, `go vet ./...` and `gofmt`.

### Avoiding conflicts

Phase 0 exists so that parallel streams rarely touch the same lines.

- **Per-source information lives in the source files.** Phase 0 defines optional interfaces that a source can implement in its own file: its time zone (`DayZone`), its archive policy (`ArchivePolicy`) and its reference totals (`Reconciler`). A stream that adds reconciliation for CRAN only changes `cran.go`, and a stream that adds time zones only changes the sources that need one.
- **Hotspots get one owner per phase:**

| File | Owned by |
|---|---|
| `internal/fetch/fetch.go` | 1a in phase 1, 2c in phase 2, 4a in phase 4 |
| `internal/fetch/stats.go` (the HTTP transport) | 1b |
| `internal/store/aggregate.go` | 2c |
| `cmd/velocirepo/cmd/ci_install.go` and `action.yml` | 3a, then 3b after 3a is merged |
| `cmd/velocirepo/cmd/root.go` | anyone; each new command adds one line, so conflicts are trivial |

- **New code goes into new packages:** `internal/archive` (1b), `internal/check` (1c), `internal/reconcile` (2d), `internal/gate` (3a).

### Definition of done

For every task:

- A test that fails without the change comes first, as in the October fixes.
- `gofmt`, `go vet ./...` and `go test -race ./...` pass.
- User-facing behaviour is documented in `user_guide/`, and the upgrade notes of the release mention it.
- Commits are small, with the reason in the message, as in the repository's history.

## Phase 0: interfaces

**Goal:** the types every stream shares, merged into `main` before any stream starts. No behaviour changes.

Sequential, in one branch (`reliability/interfaces`). Size: small.

| Task | Files | Contents |
|---|---|---|
| 0.1 Findings | `internal/check/finding.go` | The `Finding` type (`check`, `severity`, `source`, `project`, `target`, `message`, `paths`), its stable ID, and the JSON schema of `check.json` |
| 0.2 Source metadata | `internal/source/meta.go` | The optional interfaces `DayZone` (a source's time zone, per target), `ArchivePolicy` (full, or a filter that reduces a response to what's read) and `Reconciler` (reference totals for a target and a window), with no implementations yet |
| 0.3 Archive record | `internal/archive/record.go` | One archived exchange: time, method, redacted URL, redacted request body, status, selected headers, body. The manifest. The file layout `raw/<run-id>/<source>.jsonl.zst` |
| 0.4 Run results | `internal/fetch/results.go` | `fetch --results <file>` writes the run's `[]Result` as JSON, so that `check` and `gate` can read what the run did without parsing logs |

**Acceptance:** the packages build, the types have doc comments, and `Finding` IDs are stable in a test (same input, same ID; different target, different ID).

## Phase 1: foundations

**Goal:** four independent foundations. Release v1.1.0.

### 1a. Days fetched once they've ended

Worktree `reliability/day-ends`. Size: medium. Design: [measure 4](2026-10-reliability.md#4-fetch-each-day-once-it-has-ended).

| Task | Files |
|---|---|
| Per-source end date: the latest day that has ended in the source's time zone, plus an hour's margin, replacing `settleDays` | `internal/fetch/fetch.go` |
| `DayZone` for the star history (US Pacific) | `internal/source/github_events.go` |
| `DayZone` for Plausible: the site's time zone from `GET /api/v1/sites/{site}`, cached per run, UTC−12 if it can't be read | `internal/source/plausible.go` |
| Remove the star history's 7-day re-read | `internal/source/github_events.go` |
| `install-ci` schedules the nightly run after the latest time zone of the configured sources | `cmd/velocirepo/cmd/ci_install.go` (one function; 3a later rewrites the template) |

**Tests:**
- A run at 06:00 UTC fetches CRAN through yesterday, and plotnine's Plausible site and the star history through the day before.
- A run at 09:30 UTC fetches all of them through yesterday.
- Daylight saving time: the margin covers both PDT and PST.
- An unreadable Plausible time zone falls back to UTC−12.
- An explicit `--end-date` is used as given.
- The star history no longer writes days before the start date.

**Acceptance:** at 09:30 UTC, devrel-io's sources all end on the same day. A replay of today's run stores no star day that's already stored.

### 1b. HTTP transport and raw archive

Worktree `reliability/archive`. Size: large. Design: [raw response archive](2026-10-reliability.md#1-raw-response-archive).

| Task | Files |
|---|---|
| A transport chain in place of `countingTransport`: counting, rate-limit tracking (remaining quota per API from response headers) and capture | `internal/fetch/stats.go`, `internal/archive/capture.go` |
| Redaction of secret query parameters and body fields, and a check that refuses an archive containing a known secret | `internal/archive/redact.go` |
| `ArchivePolicy`: public sources in full; LinkedIn reduced to the fields it reads, after its post filter | `internal/archive/policy.go`, `internal/source/linkedin.go` |
| The archive writer: zstd JSONL per source, and the manifest | `internal/archive/writer.go` |
| `fetch --archive <dir>`, off by default | `cmd/velocirepo/cmd/fetch.go` |
| Report `Deprecation` and `Sunset` headers in the fetch log | `internal/archive/capture.go`, `internal/store/log.go` |

**Tests:**
- Every source's requests are captured.
- Bodies reach the source unchanged.
- Each secret is redacted: in URLs, request bodies, and Plausible's and YouTube's parameters.
- An archive containing a test secret is refused.
- LinkedIn drafts and non-public posts never reach the archive.
- Rate-limit headers are tracked per API.
- Large responses are streamed, not held in memory twice.

**Acceptance:** a full devrel-io fetch with `--archive` produces an archive in which no secret from `.env` appears. Its size is recorded.

### 1c. `velocirepo check` with data checks

Worktree `reliability/check`. Size: medium. Design: [checks in the gate](2026-10-reliability.md#checks-in-the-gate).

| Task | Files |
|---|---|
| The `check` command: runs the registered checks, writes `check.json` and a Markdown summary, exits non-zero on errors | `internal/check/check.go`, `cmd/velocirepo/cmd/check.go` |
| Accepted findings from `velocirepo/checks.toml`, with reason and expiry | `internal/check/accept.go` |
| `fetch.failed`, from `fetch --results` | `internal/check/fetch.go` |
| `data.integrity`, reusing `store.ValidateData` | `internal/check/integrity.go` |
| `data.freshness`, with the limits per source | `internal/check/freshness.go` |
| `data.completeness`: missing days inside a stored range | `internal/check/completeness.go` |
| `data.volume`: a series at zero or far below its 28-day median | `internal/check/volume.go` |

**Tests:**
- Each check reports exactly the findings of a fixture: month ends missing for completeness, positron's empty months for volume, stale traffic for freshness.
- An accepted finding is reported as accepted; an expired one isn't.
- The Markdown summary is stable (golden file).
- The exit code is non-zero only on unaccepted errors.

**Acceptance:**
- Run on devrel-io as it was before the October repair (commit `f8e345cde`), `check` reports the CRAN month ends and positron, quarto and rstudio.
- Run on today's devrel-io, it reports only the known bot-comment duplicate, and accepting that finding makes it pass.

### 1d. Live API tests

Worktree `reliability/live-tests`. Size: medium. Design: [measure 5](2026-10-reliability.md#5-live-api-tests-and-upstream-changes).

| Task | Files |
|---|---|
| Tests with the build tag `live`, one per source, fetching known public targets: response shape, types, plausible ranges, known limits (the 300-page comment cap, 30-week star history pages) | `internal/source/*_live_test.go` |
| A scheduled workflow that runs them daily with the repository's secrets, and opens an issue in velocirepo when they fail | `.github/workflows/live.yml` |

**Tests:** these are the tests. They're skipped without the `live` tag, so `go test ./...` stays offline, as `CLAUDE.md` requires.

**Acceptance:** the workflow passes against the real APIs. Changing an expected field name makes it fail and open an issue.

### Release v1.1.0 and devrel-io

- **Release:** 1a changes when sources are fetched. The upgrade notes say so: Plausible and stars arrive on the same day as the other sources if the workflow runs after 09:30 UTC, and `install-ci` suggests the new schedule.
- **devrel-io:** move the nightly run to 09:30 UTC. Fetch with `--archive` and upload the archive as an artifact (a manual workflow edit, until 3a generates it). Measure the archive's size and check its redaction for a week (rollout step 2 of the design).

## Phase 2: building blocks

**Goal:** everything the gate needs. Release v1.2.0.

Five streams in parallel. All start after phase 1 is merged.

### 2a. Replay

Worktree `reliability/replay`. Depends on 1b. Size: medium.

| Task | Files |
|---|---|
| A transport that serves archived responses, matched by method, redacted URL and body hash; an unmatched request is an error | `internal/archive/replay.go` |
| `velocirepo replay <run-id> [--source] [--project]`, writing records like `fetch` | `cmd/velocirepo/cmd/replay.go` |
| Remove remaining clock dependencies from sources, so replays are deterministic | `internal/source/*.go` |

**Tests:**
- A fetch followed by a replay of its archive writes identical files, for every source.
- An unmatched request fails.
- A replay with a fixed bug writes the corrected records (fixture: the Plausible pagination bug).

**Acceptance:** replaying a week of devrel-io archives rebuilds the stored records exactly.

### 2b. Diff checks

Worktree `reliability/diff-checks`. Depends on 1c. Size: medium.

| Task | Files |
|---|---|
| Read `HEAD` through a temporary `git worktree`, and compare records by key over the projects with changed files | `internal/check/diff.go` |
| `diff.records-removed`, with per-source allowances (deleted comments and forks) | `internal/check/diff.go` |
| `diff.values-changed`, exempting records dated within the run's window | `internal/check/diff.go` |
| `concatenate.records-moved`: the record sets before and after concatenation are identical | `internal/check/concatenate.go` |

**Tests:**
- Replays of the incidents report errors: the July deletion (`03f2f5e66`), the 44,619 `total_reactions`, and the Plausible duplicates.
- A normal nightly commit (`bc6b5d81b`) reports nothing.
- Records moving between files during concatenation aren't reported as removed.

**Acceptance:** on devrel-io's history since July, these checks report exactly the known incidents.

### 2c. Concatenation rules

Worktree `reliability/concatenation`. Size: small. Design: [concatenation](2026-10-reliability.md#concatenation).

| Task | Files |
|---|---|
| A month is concatenated from the concatenation day on (`[settings] concatenate_on_day`, default 8), and once a day after its last day is stored | `internal/store/aggregate.go`, `internal/config/config.go` |
| `store.Concatenate(dataDir, now, allow func(project) bool)`, so the gate can exclude projects with findings | `internal/store/aggregate.go` |

**Tests:**
- Before the 8th nothing is concatenated; on the 8th the month and, in January, the year are.
- A source that's behind waits.
- An excluded project keeps its daily files.
- `LastDate` and the resume point are unchanged.
- The existing concatenation tests pass with the day set to 1.

**Acceptance:** run on a copy of devrel-io on 2026-11-08, it concatenates October in one step and `concatenate.records-moved` passes.

### 2d. GitHub reconciliation

Worktree `reliability/reconcile-github`. Depends on 1c. Size: medium. Design: [reconciliation](2026-10-reliability.md#3-reconciliation).

| Task | Files |
|---|---|
| The reconciliation engine: reference totals from each `Reconciler`, compared with stored data within the source's tolerance, as `reconcile.*` findings | `internal/reconcile/reconcile.go`, `internal/check/reconcile.go` |
| GitHub all-time totals, batched in GraphQL: issues, pull requests, merged pull requests, forks, stars | `internal/source/github_reconcile.go` |
| Every source implements `Reconciler` or declares that it has none | `internal/source/*.go` (a one-line declaration each) |

**Tests:** the [reconciliation tests](2026-10-reliability.md#reconciliation-tests) that apply to all-time totals:
- matching totals and tolerances;
- missing and duplicated data;
- a failed or suddenly zero reference total;
- every source accounted for.

**Acceptance:**
- On devrel-io as of `f8e345cde`, it reports positron, quarto and rstudio, and the stars of every repository.
- On today's devrel-io, it reports nothing.
- All 465 repositories take at most 15 requests.

### 2e. Archive storage

Worktree `reliability/archive-storage`. Depends on 1b. Size: small.

| Task | Files |
|---|---|
| A storage interface with two implementations: Actions artifacts (through the action) and release assets in the data repository (`raw-YYYY-MM`) | `internal/archive/store.go`, `internal/archive/release.go` |
| Retention by tier: release assets only for sources that can't be re-fetched | `internal/archive/store.go` |
| `velocirepo archive download <run-id>` for replays | `cmd/velocirepo/cmd/archive.go` |

**Tests:**
- Uploads and downloads go through an httptest server that imitates GitHub's release API.
- The tiers are applied.
- An upload that would exceed the 2 GB asset limit is split.

**Acceptance:** a devrel-io archive is uploaded as a release asset, and downloaded and replayed.

### Release v1.2.0 and devrel-io

- **Release:** replay, `check` with diff and reconciliation checks, and archive storage. Concatenation isn't affected yet: until the gate exists, `fetch` concatenates with the new rule.
- **devrel-io:** run `velocirepo check` manually after a few nightly runs, and accept or fix what it reports, before the gate depends on it.

## Phase 3: report-only gate

**Goal:** the gate and the new workflows, in report-only mode. Release v1.3.0. Rollout step 5 of the design.

### 3a. Gate and workflows

Worktree `reliability/gate`. Depends on everything in phase 2. Size: large.

| Task | Files |
|---|---|
| `velocirepo gate --report-only`: runs `check`, writes the commit messages, makes the data commit, updates the tracking issue, writes the job summary and annotations. It calls GitHub's REST API directly, like `sync-secrets`, and runs `git` for commits and branches | `internal/gate/*.go`, `cmd/velocirepo/cmd/gate.go` |
| The tracking issue: body rewritten each night, comments only when findings change, closed when none are left, owners assigned | `internal/gate/issue.go` |
| `[settings] owners`, filled in by `install-ci` | `internal/config/config.go`, `cmd/velocirepo/cmd/ci_install.go` |
| The nightly workflow template: `fetch --no-concatenate --archive --results`, artifact upload, `gate`, the new permissions | `cmd/velocirepo/cmd/ci_install.go`, `action.yml` |
| The check on pushes made by hand (`velocirepo-check.yml`) | `cmd/velocirepo/cmd/ci_install.go` |

**Tests:**
- GitHub's API (issues, pull requests, assignees) is an httptest server, as in the existing `sync-secrets` tests. Git operations run against a real temporary repository.
- The issue lifecycle: opened, updated without a comment, commented on a change, closed.
- Commit messages: the date and the per-source dates.
- Report-only mode commits everything.
- The generated workflows match golden files. CI also runs `actionlint` (MIT) on them.

**Acceptance:** devrel-io's nightly run makes the data commit and opens an issue for an injected finding. A manual push with a deleted project file opens the issue.

### 3b. Heartbeat workflow

Worktree `reliability/heartbeat`. Depends on 3a's template structure: branch off 3a, or start once 3a's templates are merged. Size: small.

| Task | Files |
|---|---|
| `velocirepo heartbeat`: fails if the latest data commit is older than 30 hours or the issue has been open for more than a day, and runs reconciliation's all-time totals against `main` | `internal/gate/heartbeat.go`, `cmd/velocirepo/cmd/heartbeat.go` |
| The heartbeat workflow template | `cmd/velocirepo/cmd/ci_install.go` |

**Tests:**
- A stale commit opens the issue.
- An issue open too long escalates.
- A reconciliation mismatch on `main` opens the issue.

**Acceptance:** disabling devrel-io's nightly schedule makes the heartbeat open the issue the next day.

### Release v1.3.0 and devrel-io

- **Release:** the upgrade notes ask users to regenerate their workflows with `install-ci`.
- **devrel-io:** regenerate the workflows and run report-only for two weeks. Tune the thresholds, accept the false positives in `velocirepo/checks.toml`, and fix real findings.

## Phase 4: enforcing gate

**Goal:** bad data stays off `main`. Release v2.0.0: the nightly workflow's behaviour changes, so this is a major version.

### 4a. Enforcing gate

Worktree `reliability/gate-enforce`. Size: medium.

| Task | Files |
|---|---|
| Hold back per source and project: restore their files from `HEAD`, commit them on `velocirepo/held-back`, open or update its pull request, close it when nothing is held back | `internal/gate/holdback.go` |
| The concatenation step on the concatenation day, excluding projects with findings, with its own commit and `concatenate.records-moved` | `internal/gate/concatenate.go` |
| Findings that don't belong to a project hold back the whole run | `internal/gate/holdback.go` |

**Tests:**
- One project with an error is held back while the others land.
- The held-back branch is rebuilt nightly, and the next run re-fetches the held-back days.
- The pull request closes once data passes.
- Concatenation skips projects with findings.
- A global finding holds back everything.

**Acceptance:** an injected error in one devrel-io project holds back only that project, for two nights, and its data lands once the error is fixed.

### 4b. Quota budget and per-month reconciliation

Worktree `reliability/reconcile-monthly`. Size: medium.

| Task | Files |
|---|---|
| The quota budget: reconciliation uses at most 10% of each API's hourly quota and gives way first, with `quota.low` | `internal/reconcile/budget.go` |
| Per-month search counts for the last complete month, rotated over the nights of the week within the search API's limit | `internal/source/github_reconcile.go`, `internal/reconcile/rotation.go` |

**Tests:** the remaining [reconciliation tests](2026-10-reliability.md#reconciliation-tests):
- per-month windows in the right time zone;
- the weekly rotation;
- the quota.

**Acceptance:** over a week of devrel-io runs, every repository's last month is checked once, and no run uses more than 10% of the GitHub quota for reconciliation.

### 4c. Reconciliation for other sources

One worktree per source, all in parallel: `reliability/reconcile-<source>`. Size: small each. Each stream first checks that the reference total exists and agrees with stored data. The design marks these "to verify". If it doesn't, the stream ends with a declaration that the source has no reference totals, and the reason.

| Source | Reference total to verify |
|---|---|
| Plausible | aggregate query without the day dimension |
| CRAN | cranlogs totals over a range |
| PyPI | pypistats recent totals |

**Tests:** per source, the reference totals are parsed from canned responses and compared within tolerance.

### Release v2.0.0 and devrel-io

- **Release:** the upgrade notes explain holding back, the held-back pull request, and the concatenation commit on the 8th.
- **devrel-io:** switch from report-only to enforcing. The first concatenation commit after that is checked by hand as well.

## Phase 5: completion

**Goal:** the remaining measures. Release v2.1.0. Four streams in parallel, plus documentation.

| Stream | Worktree | Contents | Size |
|---|---|---|---|
| 5a Weekly digest | `reliability/digest` | A view with a sparkline per source and project over 8 weeks and the week's findings, posted as a comment on the tracking issue by a weekly workflow | medium |
| 5b External heartbeat | `reliability/healthchecks` | Optional Healthchecks pings at start and end, enabled by a secret | small |
| 5c Changelog monitoring | `reliability/changelogs` | A `fetch-rss` configuration in velocirepo's repository for the GitHub and Plausible changelogs, flagging entries that mention endpoints velocirepo uses | small |
| 5d Incident replay suite | `reliability/incidents` | The [incident table](2026-10-reliability.md#8-testing-the-checks) as one test file, each incident replayed and asserted to produce its error; mutation tests for the checks | medium |
| 5e Documentation | `reliability/docs` | A user guide chapter on the gate, the archive, findings and repairs; `CLAUDE.md` updated | medium |

5a, 5b and 5c each add a template to `ci_install.go`, but in different functions, so their conflicts are small. 5e goes last.

## Parallelism at a glance

| Phase | Can run at the same time | Has to wait for |
|---|---|---|
| 0 | — | — |
| 1 | 1a, 1b, 1c, 1d | phase 0 |
| 2 | 2a, 2b, 2c, 2d, 2e | 2a and 2e: 1b; 2b and 2d: 1c; 2c: nothing beyond phase 0 |
| 3 | 3a, then 3b | everything in phase 2; 3b also 3a's templates |
| 4 | 4a, 4b, and every 4c | phase 3, and two weeks of report-only runs |
| 5 | 5a, 5b, 5c, 5d, then 5e | phase 4 |

2c and 1d depend on little, and can start earlier if there's capacity. Phase 3 is the bottleneck: the gate brings everything together, so it gets the most review.

## Risks

| Risk | Effect | Mitigation |
|---|---|---|
| The archive is much larger than estimated | Storage and upload time | Phase 1 measures it before anything depends on it; tiers and the storage interface allow other choices. |
| GitHub's Sites, search or release limits differ from what's documented | Reconciliation or storage hits limits | The live tests (1d) and the acceptance runs on devrel-io check each limit before it's relied on. |
| Plausible's Sites API isn't available to every user's token | Plausible falls back to UTC−12 and arrives a day later | The fallback is safe; the upgrade notes explain it; a `timezone` setting per Plausible target can override it. |
| False positives make the gate a nuisance | Findings are accepted instead of fixed | Two weeks of report-only runs, and accepted findings expire. |
| The gate's Git operations fail in CI | Nothing is pushed | The gate is tested against a real temporary repository; the heartbeat notices missing commits. |
| Phase 3 is large | Long review | 3a is split into commits per task, each reviewed on its own. |

## What's not in this plan

- Holding back per target instead of per project.
- A separate token for reconciliation.
- Notifications outside GitHub other than the optional Healthchecks escalation.

The design explains why for each.
