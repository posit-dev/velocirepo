# velocirepo correctness review — October 2026

- **Reviewed:** `main` at `deabcc6`, including the two unpushed fetch-log commits.
- **Branch:** `review/correctness-audit`. It holds this report plus regression tests ([Appendix A](#appendix-a--tests-added-on-this-branch)).
- **Method:** lead review of aggregation, storage and the fetch flow, plus five parallel area reviews. Every finding below was re-verified before inclusion. Claims that didn't hold up are listed in [Appendix C](#appendix-c--claims-rejected-or-downgraded).

## 1. Verdict

**Monthly and yearly aggregation is not correct.** It has two independent defects, and failing tests on this branch reproduce both.

1. **Compaction overwrites period files it already wrote (A1, Critical).** `YYYY-MM.jsonl` and `YYYY.jsonl` are rebuilt from whichever daily or monthly files happen to be present, and the result replaces the existing file. Any data that lands in an already-compacted period deletes the rest of that month or year. Examples: a `--start-date` backfill, a second backfill chunk, or a crash during cleanup. A single run can silently lose a whole year.
2. **Compaction marks days as fetched that never were (A2, Critical).** A month is compacted as soon as the wall clock passes its end. A compacted month is then read back as "fetched through its last day". Any day not stored by that moment is never fetched again. In normal unattended operation this happens:
   - every month for CRAN, whose data lags more than 34 hours behind the 01:00 UTC cron;
   - whenever a source fails on the 1st;
   - whenever a run crosses midnight UTC.

The two defects compound: the natural repair for an A2 gap (a `--start-date` backfill) triggers A1. Neither is visible, because `fetch` exits 0 when jobs fail and every warning is suppressed by default (A3).

**The worst problem outside aggregation is in the GitHub source (S1, Critical).** On incremental runs, an issue close or PR merge is only recorded if the item was also opened inside the same fetch window. As a result:
- `daily_issues_closed` and `daily_prs_merged` are close to zero after the initial backfill;
- issue and PR state and reaction counts are never refreshed.

**The test suite is healthy, but it doesn't test the failure modes that matter.** It is green, race-free, order-independent and offline, and I found no incorrect assertion. The problems are about coverage of scenarios:
- Aggregation has 67–100% statement coverage per function, yet two data-loss bugs survive. No test starts from an existing monthly or yearly file, a missing last day, or an item opened before the fetch window.
- The fetch orchestrator is barely tested: `resolveStartDate` 14%, `runEventJob` 0%, `fetch.All` 0%.
- No test runs fetch and compaction together.
- Mutation testing: 10 of 28 realistic bugs injected into the store and fetch core went unnoticed.

## 2. Scope and method

- **Lead review.**
  - `internal/store`: aggregation, JSONL I/O, change-point encoding of `total_*` metrics, watermarks, migrations, DuckDB views.
  - `internal/fetch` and the `fetch` commands.
  - The generated CI workflow.
  - The user guide, used as the specification.
- **Area reviews** (run in parallel and re-verified by the lead reviewer):
  - GitHub sources;
  - the other sources;
  - security and release plumbing;
  - CLI, config and presentation;
  - the store package outside aggregation.
- **Baseline.**
  - `go vet`, `go test -race ./...` and `go test -shuffle=on -count=3 ./...` all clean.
  - `golangci-lint`: 32 minor issues, 30 of them unchecked errors, mostly in tests.
- **Evidence.**
  - 11 failing regression tests and 5 passing guard tests.
  - A 13-month simulation of the daily cron through the real orchestrator.
  - Runs of the built binary against scratch data.
  - Read-only probes of the cranlogs and pypistats APIs ([Appendix B](#appendix-b--live-api-observations-2026-10-07)).
  - 28 hand-written mutants run against the original test suite.
- **Not covered.**
  - Production data: none was available, and the local `data/` is a sandbox.
  - The Quarto views.
  - The Python wheel beyond its packaging.

## 3. Findings at a glance

| ID | Severity | Finding | Evidence |
|---|---|---|---|
| A1 | Critical | Compaction overwrites existing monthly and yearly files | 5 failing tests |
| A2 | Critical | Compaction marks unfetched days as fetched, so they are never fetched | 3 failing tests (7 cases), simulation, live API |
| S1 | Critical | GitHub never records closes or merges of items opened before the fetch window; their state and reactions are never refreshed | failing test |
| A3 | High | Failures are invisible: exit code 0, all logs suppressed, `Aggregate` never returns an error | code, binary |
| S2 | High (plausible) | Plausible: "yesterday" is still in progress for sites west of UTC, and the partial value is frozen | API docs + code |
| A4 | Medium | Re-fetching a day drops the `total_*` change points recorded on that day | failing test |
| A5 | Medium | Leftover `.tmp-*.jsonl` files are counted twice and never cleaned up | binary |
| A6 | Medium | `validate-data --fix` destroys event files when the data dir isn't named `…data` | failing test |
| A7 | Medium | `validate-data` doesn't detect overlapping daily/monthly/yearly files | reviewer probe |
| S3 | Medium | Homebrew records are dated today, so Homebrew is fetched only every other day | code |
| C1 | Medium | `badge downloads` and `badge pageviews` render `<nil>` | binary |
| C2 | Medium | `export --project` exports an empty content table | code, reviewer probe |
| X1 | Medium | `install.sh` and the GitHub Action install the binary without verifying its checksum | code |
| L1–L20 | Low | See [§5.4](#54-low) | |

## 4. Aggregation findings

### A1 — Compaction overwrites existing period files · Critical · confirmed

**Where:** `internal/store/aggregate.go:123-156`. `aggregateFiles` collects only the files that match the daily (or monthly) name pattern for a period. `spec.write(outPath, items)` then renames the result over `YYYY-MM.jsonl` or `YYYY.jsonl`. The existing period file is never read.

**What breaks.** Each scenario is reproduced at `deabcc6`:

| Scenario | Result | Test |
|---|---|---|
| `fetch --start-date 2025-01-31` after January was compacted (e.g. to backfill a newly added target) | January drops from 31 records to 1 | `TestAggregateMergesLateDailyFileIntoExistingMonth` |
| The same after the year was compacted | 2024 drops from 366 records to 1 | `TestAggregateMergesLateDataIntoExistingYear` |
| The same for events (e.g. `fetch-github --start-date …`) | same loss | `TestAggregateMergesLateEventsIntoExistingMonth` |
| Backfilling history in two chunks: `2025-01-01..01-20`, then `01-21..02-28` | the first chunk is deleted (20 of 59 days) | `TestFetchBackfillInChunksKeepsAllData` |
| A crash or cancelled job part-way through deleting the daily files | the month is rebuilt from the 12 leftover files (19 days lost) | `TestAggregateRecoversFromCrashDuringDelete` |

The daily cron alone doesn't trigger A1: sources honour `[StartDate, EndDate]`, and the start date always comes after the last stored date. Any manual re-fetch of a past period does trigger it, and nothing warns about it.

**Fix.**
1. When compacting a period, read the existing period file (if any) together with the new files.
2. Deduplicate with *newest wins*: files written later override older values for the same key. Today `dedupBy` keeps the first, i.e. oldest, occurrence.
3. Write the result atomically, then delete the inputs.

This makes compaction idempotent and makes both crash cases self-healing. Until it lands, don't run `fetch --start-date` for past months.

### A2 — Compaction marks unfetched days as fetched · Critical · confirmed

**Where:**
- `internal/store/aggregate.go:198-214`. A period counts as complete once `now` is past its end, and nothing checks that its days were actually fetched. The user guide promises compaction "once all days in January 2026 have been fetched".
- `internal/fetch/fetch.go:220-224`. Compaction runs after every fetch, even when every job failed. It also reads the clock again at the end of the run, whereas the end date was fixed at the start.
- `internal/store/jsonl.go:137-139`. When the next run picks its start date, it treats `YYYY-MM.jsonl` as fetched through the month's last day and `YYYY.jsonl` as fetched through 31 December.

**What breaks.** `TestSimulatedDailyCronStoresEveryDay` drives the real orchestrator once per simulated day from December 2024 to January 2026:

| Scenario | Days never stored |
|---|---|
| Source publishes a day late (yesterday isn't available at the 01:00 UTC run) | the last day of every month: 14 days |
| A fetch fails on the 1st of a month (rate limit, outage, expired token) | 2025-02-28 and 2025-12-31 |
| A run starts before midnight UTC and finishes after it | the last day of every month: 13 days |

Two more tests cover this:
- `TestFetchWithPastEndDateKeepsResumePoint`: after `fetch --start-date 2025-01-01 --end-date 2025-01-20`, the next normal fetch resumes at 2026-01-01. It silently skips 21 January – 31 December.
- `TestAggregateDoesNotAdvanceResumePoint` states the invariant directly: compaction must not change `LastDate`.

**CRAN is affected every month.** At 10:13 UTC on 2026-10-07, cranlogs returned data only through 2026-10-05. Days it hasn't published yet are omitted, not returned as zero. With the default `0 1 * * *` cron, the last day of each month (very likely the last two) isn't published when the month is compacted, so it is never stored. PyPI's lag at 01:00 UTC is unknown; by 10:13 UTC it did have yesterday.

**Fix,** in increasing order of robustness:
1. Compact a `(source, project)` pair only if its job succeeded in this run, and use the run's end date instead of `time.Now()` as the cut-off.
2. Persist an explicit per-`(source, project)` "fetched through" date, written only after a successful job (`_watermark.json` already exists for totals). Use it both to pick the next start date and to decide when a period is complete, instead of deriving fetch state from file names. Compaction then becomes a pure storage optimisation that is safe to run at any time.
3. For sources that publish with a lag (CRAN, possibly PyPI, and Plausible, see S2), re-request the last few days on every run. This is only safe once A1 is fixed.

### A3 — Failures are invisible · High · confirmed

- `fetch` and every `fetch-*` command exit 0 when individual jobs fail (`cmd/velocirepo/cmd/fetch_all.go:21-25`, `fetch.go:161-165`). The generated workflow then commits whatever was fetched, and the run shows green.
- The default log level is `slog.LevelError + 1` (`cmd/velocirepo/cmd/root.go:184`), so every warning and error log is suppressed unless `--verbose` is set. That includes:
  - "concatenation failed";
  - "remove source file";
  - "write content failed";
  - "write records failed" for GitHub reactions, even though the job still reports success;
  - "write fetch log".
- `store.Aggregate` always returns `nil` (`aggregate.go:27-31`), so the error branch at `fetch.go:221-223` is dead code.

As a result, the A2 gaps open silently on exactly the days that matter.

**Fix:**
- Exit non-zero when any job fails; the fetch log already records the details.
- Make `Aggregate` return a joined error.
- Default the log level to Warn.

### A4 — Re-fetching a day drops `total_*` change points of that day · Medium · confirmed

**What happens:** `WriteRecords` drops a `total_*` record whose value equals the last recorded value (`jsonl.go:48`, `dedup_totals.go:41-103`). But that baseline includes the file being rewritten.

**Example:** on a day where `total_likes` went from 4 to 5, a re-fetch compares 5 with 5, drops it, and rewrites the file without it. The series silently reverts to 4 (`TestWriteRecordsRefetchKeepsChangePointsOfThatDay`).

**When:** any `--start-date`/`--end-date` re-run of a day, including snapshot sources and GitHub reactions.

**Fix:** compute the baseline only from records dated before the day being written.

### A5 — Leftover temp files are counted twice and never cleaned up · Medium · confirmed

**What happens:** atomic writes create a `.tmp-*.jsonl` file next to the target (`jsonl_io.go:44`). An interrupted write leaves it behind, and every DuckDB glob matches it (`*.jsonl` in `duckdb.go:184,340,380,534` and `build_db.go`). With the built binary, one leftover file doubled a project's downloads: 3000 instead of 1500.

**It never goes away:**
- compaction ignores it because the name doesn't match its patterns;
- `lastRecordedTotalsFor` and `ScanProjectDir` read it too;
- `validate-data` doesn't report it.

**Who is affected:** in CI, the commit step doesn't run after a failed fetch. This mostly affects interrupted local runs that get committed.

**Fix:**
- Name temp files `*.jsonl.tmp`, or write them to a sibling directory.
- Have `validate-data` flag leftover temp files.

### A6 — `validate-data --fix` destroys event files outside a `…data` directory · Medium · confirmed

**What happens:** `isEventPath` (`validate.go:437-440`) decides whether a file holds events by searching its path for `data/events/`. Suppose `[data] dir` is set to something that doesn't end in `data`, such as `metrics-store`. Then `FixDuplicates` and `FixDateMismatches` treat event files as metric records and rewrite them.

**Evidence:** in `TestFixDuplicatesKeepsEventsOutsideDefaultDataDir`, three events (one of them a duplicate) became one row with `type`, `datetime`, `user` and `ref` erased.

**Fix:** pass the category explicitly. The validator already knows whether it is walking `metrics/` or `events/`.

### A7 — `validate-data` doesn't detect overlapping period files · Medium · confirmed (reviewer probe)

**What happens:** sometimes a daily file sits next to its month's file. That happens after an interrupted compaction, or after a `--no-concatenate` backfill into a compacted month. Every query then double-counts that data, and `validate-data` reports no issue. This is also exactly the state in which the next compaction destroys data (A1).

**Fix:** report daily+monthly and monthly+yearly overlaps, as well as leftover temp files (A5).

### Checked and correct

- **Period boundaries are exact.**
  - A month counts as complete one nanosecond after its last instant.
  - Leap-year February works.
  - Compacting December and the whole year in a single run works (`TestPeriodCompletenessBoundaries`, `TestAggregateDecemberAndYearInOneRun`).
- **Compaction is idempotent.** A crash *before* any daily file is deleted heals itself on the next run (`TestAggregateIsIdempotent`, `TestAggregateRecoversFromCrashBetweenWriteAndDelete`).
- **Writes are atomic** (temp file + rename).
- **Deduplication keeps distinct rows.** Its keys include target, extra, ref and user (but see M09 in §6.3).
- **Daily event counts use the UTC date** whatever the machine's time zone (verified under PDT, CEST and JST).
- **Concurrent fetch jobs don't interfere.** Each writes to its own directories, and the shared statistics are mutex-protected.

## 5. Other findings

### S1 — GitHub loses closes, merges, state and reactions of older items · Critical · confirmed

**Where:** `fetchIssues` and `fetchPullRequests` sort by `CREATED_AT DESC` and stop at the first node created before `StartDate` (`github_events.go:293`, `:411`).

**What happens:** on the daily schedule the fetch window is yesterday. An issue opened earlier and closed yesterday is therefore never examined. That has three consequences:
- **No close or merge event is emitted.** `daily_issues_closed` and `daily_prs_merged` only count items opened and closed on the same day.
- **Content entries go stale.** State, title and labels are never refreshed, so issues and PRs look open forever in `content`.
- **Reaction counts freeze.** They are only requested for items in the current run's content (`github_events.go:90-91, 533-543`), so `total_reactions` stops changing after an item's first day.

**Evidence:** `TestGitHubEventsRecordsCloseAndMergeOfItemsOpenedEarlier` uses an issue opened on 2025-01-10 and a PR opened on 2025-06-08, both closed on 2025-06-10. The fetch records zero events. Every existing fixture creates its items inside the window, so the bug can't show up there.

**Fix:**
1. Sort issues and PRs by `UPDATED_AT` and stop at `updatedAt < StartDate`.
2. Emit open events when `createdAt` is in range, and close/merge events when `closedAt`/`mergedAt` is.
3. Refresh content and reactions for every updated item.
4. Once A1 is fixed, re-fetch the history to recover the missing events.

### S2 — Plausible freezes partial days for sites west of UTC · High · plausible

**What happens:** Plausible reports dates "in the site's Reporting Timezone" (Stats API docs). The request covers `[start, yesterday in UTC]` (`plausible.go:54-57`). At the 01:00 UTC run, "yesterday" is still in progress in every time zone west of UTC−1; for US sites the last 4–8 hours are missing. The partial value is never re-fetched, because the next run starts at the following day.

**Status:** not reproduced against a live site. To verify, compare a stored day with the Plausible dashboard.

**Fix:** end Plausible requests at the last *completed* day in the site's time zone, or re-request the last few days on every run.

### S3 — Homebrew is dated today and fetched every other day · Medium · confirmed

**What happens:** `homebrew.go:43` dates records with `time.Now()` instead of `opts.EndDate`, so they fall outside the requested window. The next start date becomes tomorrow. The next day's run finds start > end and skips as "already up to date", so Homebrew snapshots are stored only every other day.

**Tests:** `TestHomebrewFetch` never checks record dates.

**Fix:** use `opts.EndDate`, as the other snapshot sources do.

### C1, C2, X1 — Medium

- **C1. The badge presets use stale metric names** (`internal/badge/presets.go:12-13`).
  - `downloads` matches `metric = 'downloads'` and `pageviews` matches `metric = 'pageviews'`. Migrations 1 and 3 renamed these to `daily_downloads` and `daily_site_pageviews`.
  - For a PyPI/Plausible project, the built binary renders `downloads: <nil>` and `pageviews: <nil>`.
  - The `--project` filter is also appended after an `OR` without parentheses (`cmd/velocirepo/cmd/badge.go:81-84`).
- **C2. `export --project` filters content on the wrong column.** `buildExportQuery` uses `target` instead of `project` for content (`export.go:93-95`), so `export --project X` writes an empty content table.
- **X1. The installers don't verify checksums.** `install.sh:57` and `action.yml:47` pipe the release tarball straight into `tar` without checking `SHA256SUMS`, unlike `velocirepo update`. Every CI run of every user of the action installs an unverified binary.

### 5.4 Low

| ID | Finding | Location |
|---|---|---|
| L1 | LinkedIn follower-count errors are silently dropped. The watermark still advances, so the series looks flat. | `linkedin.go:58-61` |
| L2 | RSS 1.0/RDF feeds are routed to the RSS 2.0 parser and always fail to parse | `rss.go:127-131, 275-277` |
| L3 | YouTube durations with a day component (`P1DT2H`) parse wrong or to nil | `youtube.go:323-349` |
| L4 | GitHub traffic: after more than 14 days without a run, the resulting gap goes unreported | `fetch.go:85-90` |
| L5 | A GraphQL response with both `errors` and partial `data` fails the whole GitHub job | `github_events.go:1073` |
| L6 | The comments fetch pages through every recently edited old comment (efficiency, rate limits) | `github_events.go:506` |
| L7 | LinkedIn pagination stops after one page if `paging.total` is absent (plausible); API version `202406` may be past its support window (plausible) | `linkedin.go:82, 170` |
| L8 | Plausible doesn't drop rows outside the requested range, unlike the other daily sources | `plausible.go:68-87` |
| L9 | A `--start-date` later than `--end-date` silently skips every job | `fetch.go:240` |
| L10 | Project rename doesn't rewrite `project_id` in `data/log/*.jsonl` | `project_rename.go:83-120` |
| L11 | Errors from rolling back a failed rename are discarded | `project_rename.go:105-115` |
| L12 | Config edits use a non-atomic `os.WriteFile` | `internal/config/write.go` |
| L13 | The generated workflow has no `concurrency:` group and pushes without pulling first | `ci_install.go:16-48` |
| L14 | `build-db` deletes the old database before building the new one | `build_db.go:22-23` |
| L15 | `migrate --force` doesn't do what the docs promise for old data copied into the current layout. Migrations 1–3 only look at legacy paths, and migration 4's directory rename fails onto a non-empty directory (found by reading the code). | `migrate.go:164-262` |
| L16 | Migrations still read lines with a 1 MB limit (52c1cdc fixed only the reader), and they round-trip numbers through float64 | `migrate.go:273-274` and similar |
| L17 | `validate-data` flags content entries with the same ID from different targets as duplicates; the merge itself uses `(target, id)` | `validate.go:1082` |
| L18 | `sync-secrets` doesn't unescape values that `auth` wrote with escapes (`$ " ` ! \`), and env values containing newlines are truncated | `ci_sync_secrets.go:157`, `dotenv.go:93` |
| L19 | The MCP `query` tool has no upper bound on `limit`, and the MCP `export` tool (write mode) accepts any directory | `sql_guard.go:78`, `handlers.go:598` |
| L20 | The docs workflow builds with an unpinned `great-docs` from git; the user guide is out of date (`tags` is now `extra`, `user` is top-level, the "once all days … have been fetched" promise, the list of content sources) | `docs.yml:31`, `user_guide/data-storage.qmd` |

## 6. Test suite assessment

### 6.1 What's good

- The suite is green, race-free under `-race`, order-independent (3× `-shuffle=on`), offline and fast.
- Tests use `httptest` and `t.TempDir()` throughout and pin dates explicitly.
- No assertion depends on the wall clock. The few `time.Now()` uses are relative token expiries and checks that only look at request headers.
- Dedup identity, `LastDate` for each file layout, change-point encoding of totals and the `metrics_filled` forward fill each have focused tests.
- No existing assertion is wrong.

### 6.2 Where it falls short

1. **The dangerous starting states are never set up.** No test starts from any of these:
   - an existing monthly or yearly file;
   - a missing last day;
   - an interrupted compaction;
   - an item opened before the fetch window;
   - a non-default data directory;
   - a leftover temp file.

   That is why A1, A2, A5, A6 and S1 survive despite high coverage.
2. **Tests check counts, not contents.** All seven tests in `aggregate_test.go` assert only `len()`. For example, `TestAggregateDailyToMonthly` writes the values 100/200/300 and never checks them. Nothing checks the order of a compacted file (mutant M06 survives).
3. **Two tests encode the A2 assumption.** `TestLastDateMonthly` and `TestLastDateYearly` assert that a monthly (yearly) file means "fetched through the last day". They will need to change along with the fix.
4. **Fetch and compaction are never tested together.**
   - Both orchestrator tests run with `NoConcatenate: true`.
   - Coverage: `resolveStartDate` 14%, `resolveEndDate` 40%, `runEventJob` 0%, `All`/`runDescriptor` 0%, CLI fetch commands 0%.
   - The orchestrator reads the wall clock directly, which makes simulating the cron awkward. This branch works around it by passing `EndDate` explicitly and calling `Aggregate` itself.
5. **Fixtures don't exercise what matters.**
   - `TestHomebrewFetch` never checks record dates (S3).
   - Every GitHub fixture creates its items inside the fetch window (S1).
   - The badge presets and `export --project` for content have no tests (C1, C2).
6. **Some code is entirely untested.**
   - The event migrations `rewriteEventFields` and `migrateEventTagsToExtra` (0%).
   - `internal/projectdata` (no tests at all).
   - `materializeRestrictedTables` (0%).
   - `deduplicateEventsFile` and `regroupEventsFile` (0%).
7. **CI checks little.** It runs only `go build` and `go test`, with no `-race`, `go vet`, gofmt or lint step, and 17 files aren't gofmt-formatted.

### 6.3 Mutation testing

I injected 28 realistic bugs into `deabcc6`, one at a time, and ran the original suite against each.

**Caught (18):**
- source files not deleted after compaction;
- deduplication disabled;
- dedup key ignoring target, extra or ref;
- daily files grouped by year;
- the yearly step skipped;
- events not compacted;
- `LastDate` misreading monthly or yearly files;
- change-point encoding disabled;
- watermarks not written;
- the oldest total used as the baseline;
- forward fill limited to one day;
- `inDateRange` excluding the end day;
- the "start ≥ end" skip off by one;
- a metric job writing partial data;
- the current month treated as complete.

**Survived (10):**

| Mutant | Injected bug |
|---|---|
| M01 | A month counts as complete after 28 days |
| M03 | A year counts as complete on 1 December |
| M06 | Compacted rows written in reverse order |
| M09 | Event deduplication ignores `user` |
| M18 | A decreasing `total_*` is never recorded |
| M20 | A watermark can move backwards |
| M23 | The default end date is today instead of yesterday |
| M24 | Each run re-fetches the last stored day |
| M25 | The traffic window is clamped to 30 days instead of 14 |
| M27 | The GitHub job writes partial data when one repo fails |

The guard tests on this branch catch M01, M03 and M06.

### 6.4 Tests to add beyond this branch

1. **A property test for compaction.** Generate random daily/monthly/yearly layouts with late writes interleaved, and assert that the set of records is unchanged and `LastDate` doesn't move.
2. **A clock in the orchestrator.** Inject a clock (`Options.Now`) and test fetch plus compaction across month and year boundaries through `runJobs`. This replaces the workaround in `simulateCron`.
3. **CLI behaviour.** Exit code when a job fails; warnings visible by default.
4. **GitHub fixtures with items opened before the window,** including content and reaction refreshes.
5. **Snapshot sources:** assert that record dates equal `EndDate`.
6. **`validate-data`:** overlap and temp-file detection, and event fixes with a non-default data dir.
7. **Badge presets and `export --project`:** test them against the metric names sources actually emit. For example, fail if a preset matches no emitted metric.
8. **The event migrations** (3→4 and 6→7).

## 7. Recommended order of work

1. **A1: merge on compact.** A small change confined to `aggregateFiles`, and it makes every later step safe.
2. **A3: make failures visible.** Non-zero exit code; Warn-level logging by default.
3. **A2: stop marking unfetched days as fetched.**
   - At minimum, skip compaction for jobs that failed and use the run's end date.
   - Then add an explicit "fetched through" state.
   - Add a re-fetch of the last few days for CRAN.
4. **S1 and S2.**
5. **Repair existing data.**
   - Periods overwritten by A1 can be recovered from git history, since `data/` is committed.
   - Month-end gaps from A2 and missing GitHub events from S1 can be re-fetched once A1 is fixed, as long as the API still has the data. CRAN and Plausible keep full history, PyPI about 180 days, GitHub traffic 14 days.
   - This heuristic query (tested with `velocirepo query`) lists daily series whose month has no record for its last day:

   ```sql
   WITH m AS (
     SELECT source, project, target, metric,
            date_trunc('month', date) AS month, max(date) AS last_day
     FROM metrics
     WHERE metric IN ('daily_downloads', 'daily_site_pageviews', 'daily_views')
     GROUP BY ALL
   )
   SELECT * FROM m
   WHERE last_day < last_day(month) AND month < date_trunc('month', current_date)
   ORDER BY source, project, target, month;
   ```
6. **Then the Medium findings** (A4–A7, S3, C1, C2, X1), **and the Low list.**

## Appendix A — Tests added on this branch

| Test | File | At `deabcc6` | Finding |
|---|---|---|---|
| `TestAggregateMergesLateDailyFileIntoExistingMonth` | `internal/store/aggregate_regression_test.go` | fails | A1 |
| `TestAggregateMergesLateDataIntoExistingYear` | same | fails | A1 |
| `TestAggregateMergesLateEventsIntoExistingMonth` | same | fails | A1 |
| `TestAggregateRecoversFromCrashDuringDelete` | same | fails | A1 |
| `TestAggregateDoesNotAdvanceResumePoint` (3 cases) | same | fails | A2 |
| `TestWriteRecordsRefetchKeepsChangePointsOfThatDay` | same | fails | A4 |
| `TestAggregateRecoversFromCrashBetweenWriteAndDelete` | same | passes | guard |
| `TestAggregateIsIdempotent` | same | passes | guard |
| `TestPeriodCompletenessBoundaries` | same | passes | guard |
| `TestAggregateDecemberAndYearInOneRun` | same | passes | guard |
| `TestFixDuplicatesKeepsEventsOutsideDefaultDataDir` | `internal/store/validate_regression_test.go` | fails | A6 |
| `TestFetchWithPastEndDateKeepsResumePoint` | `internal/fetch/compaction_regression_test.go` | fails | A2 |
| `TestFetchBackfillInChunksKeepsAllData` | same | fails | A1 |
| `TestSimulatedDailyCronStoresEveryDay` (3 cases) | same | fails | A2 |
| `TestSimulatedDailyCronHarness` | same | passes | guard |
| `TestGitHubEventsRecordsCloseAndMergeOfItemsOpenedEarlier` | `internal/source/github_events_regression_test.go` | fails | S1 |

By design, `go test ./...` on this branch fails in `internal/store`, `internal/fetch` and `internal/source`. Every pre-existing test passes. Each failing test describes the intended behaviour, so it should pass once the matching fix lands. `TestLastDateMonthly` and `TestLastDateYearly` may need to change along with A2.

## Appendix B — Live API observations (2026-10-07)

- **cranlogs**, `downloads/daily/2026-10-04:2026-10-07/ggplot2` at 10:13 UTC: returned only 10-04 and 10-05. Days not yet published are omitted, not zero.
- **pypistats**, `packages/plotnine/overall` at 10:13 UTC: returned 182 days, from 2026-04-08 to 2026-10-06.

## Appendix C — Claims rejected or downgraded

- **"Homebrew is permanently skipped after the first fetch."** Downgraded: it alternates days (S3, Medium).
- **"cranlogs returns 0 for days it hasn't processed yet."** Rejected: the live API omits those days. The real CRAN problem is A2.
- **"A failing target discards the other targets' data"** (metric and event jobs). Rejected: this all-or-nothing behaviour is intended, the job is retried on the next run, and an existing test asserts it.
- **"`status` computes ages in local time."** Rejected: `Sub` compares instants.
- **`query()`/`query_table()` pass the MCP SQL guard in scalar position.** Informational only: DuckDB rejects them there, and external access is disabled anyway.
- **Leftover temp files rated Critical.** Downgraded to Medium (A5): it requires an interrupted write that then gets committed.
- **`since=` is formatted with a literal `Z`.** Informational: start dates are always UTC.
- **`install.sh` lacks `pipefail`.** Rejected: it is a POSIX `sh` script, and when `curl` fails, `tar` receives no input and fails too, which trips the surrounding `if !`.
- **The lead reviewer's own early hypotheses.** Dropped:
  - "`Datetime[:10]` can panic": unreachable, because every event timestamp is parsed before use.
  - "the sort is unstable": the output is still deterministic and date-ordered.

## Appendix D — Also verified correct

- **MCP SQL guard.** It rejects file and network table functions, `COPY`, `ATTACH`, `PRAGMA`, multiple statements and non-`main` schemas. Restricted queries run with `enable_external_access=false`, which can't be re-enabled within the session.
- **`velocirepo update`.**
  - It checks the SHA-256 against `SHA256SUMS` before extracting.
  - Extraction uses `filepath.Base` and accepts only regular files, so archive paths can't escape the target directory.
  - It never downgrades.
- **OAuth.** It uses a random `state`, checks it on the callback, and listens only on 127.0.0.1.
- **Path safety.** Project IDs must match `^[a-z0-9]+(-[a-z0-9]+)*$`, which rules out path traversal. View scaffolding rejects escaping paths and symlinks.
- **Config edits** keep comments and unknown keys.
- **Content upserts** are keyed by `(target, id)`, and the fetch log appends rather than overwrites.
- **Version strings** are validated in `release.yml` and `action.yml` before use in shell.
