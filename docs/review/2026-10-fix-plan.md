# Fix plan for the October 2026 correctness review

This plan fixes the findings in [2026-10-correctness-review.md](2026-10-correctness-review.md). Work happens on `fix/correctness-review`, which branches from `review/correctness-audit`. The 11 failing regression tests on that branch are the acceptance criteria: each one must pass once its fix lands, and every existing test must keep passing.

## Principles

1. **Compaction is a storage optimisation, nothing more.** It must never lose a record, never change the date the next fetch starts from (the resume point), and give the same result however often it runs or wherever it is interrupted.
2. **The last day of a period decides when the period is complete.** A month or year is compacted only once its last day has actually been stored (or a later date has been). The wall clock alone is not enough. This makes `LastDate`'s reading of `YYYY-MM.jsonl` as "fetched through the last day" true by construction.
3. **Newer data wins.** When records with the same identity meet during compaction or repair, the one from the more recently written file is kept, so re-fetches can correct values.
4. **Failures are visible.** A failed job makes the command exit non-zero, warnings are logged by default, and compaction reports its errors.
5. **Tests first.** Each fix lands with a test that fails without it. Behaviour that is correct today and must survive gets a guard test.

## Why not fix `LastDate`

The colleague's analysis puts the CRAN month-end gap in `store.LastDate`, which assumes a compacted month covers the whole month. The analysis is right, but changing `LastDate` alone makes things much worse. I tested that variant: `LastDate` returned the latest date stored inside a compacted file. The narrow unit test passed, but the 13-month cron simulation went from 2–14 lost days to 364–424. The late day becomes a daily file in an already-compacted month, and compaction then rebuilds that month, and later the year, from that single file (A1). So the guard belongs in the compaction decision (principle 2), and the overwrite (A1) has to be fixed first.

## Work breakdown

Each item lists the change, the tests that prove it, and the commit it goes into. Items are grouped by area. Within my own work they land in the order shown.

### Phase 1 — Storage core (`internal/store`)

| # | Finding | Change | Tests |
|---|---|---|---|
| 1 | A1 | When compacting a period, merge the existing `YYYY-MM.jsonl` / `YYYY.jsonl` with the new inputs. Dedupe with *newest wins*: input files are ordered oldest-written first, the existing period file comes before its daily files, and the last occurrence of a key wins. Write atomically, then delete the inputs. | A1 regression tests; new property test: random layouts and interleaved late writes keep every record; newest-wins test |
| 2 | A2 | Compact a project's month only when its stored data (daily file names plus `_watermark.json`, as computed by `LastDate` *before* compaction) reaches the month's last day. Same rule for years, with 31 December. The wall-clock check stays as a sanity bound. Failed jobs and lagging sources therefore leave the month uncompacted until the missing day arrives, runs that cross midnight are harmless, and bounded `--end-date` backfills don't mark later days as fetched. | A2 regression tests (`TestAggregateDoesNotAdvanceResumePoint`, `TestFetchWithPastEndDateKeepsResumePoint`, `TestSimulatedDailyCronStoresEveryDay`); a sparse event source still compacts once later data exists |
| 3 | A3 (store) | `Aggregate` returns a joined error of per-project failures. Failing to delete an input is an error. | Unreadable input surfaces an error |
| 4 | A4 | The `total_*` "unchanged" baseline for date D only considers records dated before D, computed per date. | `TestWriteRecordsRefetchKeepsChangePointsOfThatDay`; existing totals tests |
| 5 | A5 | Temp files are named `.<target>.<random>.tmp`, without a `.jsonl` suffix, so no glob or reader picks them up. | Leftover temp file is not counted by the `metrics` view; atomic writes still work |
| 6 | A6 | Derive the category of a file from its position (`<data>/<category>/<source>/<project>/<file>`) instead of a `data/events/` substring. | `TestFixDuplicatesKeepsEventsOutsideDefaultDataDir`; event regroup/dedup fixes get their first tests |
| 7 | A7, A5 | `validate-data` reports overlapping period files (daily+monthly, monthly+yearly) and leftover temp files (both naming schemes). `--fix` merges overlaps through the same merge as compaction and deletes the temp files. | Detection and fix tests |
| 8 | L17 | The content duplicate check uses `(target, id)`, matching the upsert key. | False-positive test |
| 9 | L16 | Migrations read lines without a size limit and decode numbers with `UseNumber`, so integers round-trip exactly. | Line over 1 MB; value above 2^53 |
| 10 | L15 | Migration 3→4 fails with a clear message when the destination already exists. The user guide stops promising that `--force` migrates old data copied into the current layout, and describes the safe procedure instead. | Error message test |
| 11 | tests | First tests for the event migrations (3→4 `rewriteEventFields`, 6→7 `migrateEventTagsToExtra`). Tests for the mutation survivors M09 (event dedup by `user`), M18 (decreasing totals are recorded) and M20 (watermarks never move backwards). | — |

### Phase 2 — Fetch orchestrator and CLI (`internal/fetch`, `cmd`)

| # | Finding | Change | Tests |
|---|---|---|---|
| 12 | tests | Inject the clock: `Options.Now` (default `time.Now`) drives the end date, the traffic window and compaction. `simulateCron` then runs through the real `runJobs` path. | Cron simulation through `Source`; tests for `resolveEndDate`/`resolveStartDate` (mutation survivors M23–M25) |
| 13 | A3 (CLI) | `fetch` and `fetch-*` still render results, write the fetch log, compact and rebuild the database, then exit non-zero if any job failed or compaction failed. Logging defaults to Warn. `--verbose` keeps Debug. | Command exit code tests; warning visible by default |
| 14 | L9 | An explicit `--start-date` after the end date is an error. | Test |
| 15 | S2 | Sources whose "yesterday" may still be in progress in their own time zone get a settle delay. Plausible's default end date becomes two days ago in UTC, which is complete in every time zone. An explicit `--end-date` is respected. | Default end date per source; explicit end date unchanged |
| 16 | L4 | When the 14-day GitHub traffic window cuts off unfetched days, the job logs a warning with the lost range. | Test |
| 17 | M27 | `runEventJob`: a failed repo discards the job, and nothing is written (intended all-or-nothing). | Pinned by a test |

### Phase 3 — GitHub (`internal/source/github_events.go`)

| # | Finding | Change | Tests |
|---|---|---|---|
| 18 | S1 | Issues and PRs are queried by `UPDATED_AT DESC`, stopping at `updatedAt < StartDate`. An open event is emitted when `createdAt` is in range, a close/merge event when `closedAt`/`mergedAt` is. Content is refreshed for every updated item. Reaction counts are refreshed for every item in the run's content (all updated in the window, closes included), replacing the "skip known closed" heuristic. | `TestGitHubEventsRecordsCloseAndMergeOfItemsOpenedEarlier`; existing fixtures updated; window boundaries |
| 19 | L5 | `doGraphQL` returns a typed error with the partial data. Reaction batches tolerate `NOT_FOUND` errors for individual aliases. Every other partial error still fails the job. | Partial-response tests |
| 20 | L6 | Comment paging stops at the first comment created before `StartDate` (results are sorted by creation, descending). | Page-count test |

### Phase 4 — Other sources (delegated, reviewed before merge)

| # | Finding | Change |
|---|---|---|
| 21 | S3 | Homebrew records are dated `opts.EndDate`. |
| 22 | L1 | LinkedIn follower-count errors fail the job instead of being dropped. |
| 23 | L7 | LinkedIn pagination works when `paging.total`/`count` are absent. The API version becomes a named constant (the value is unchanged: it can't be verified without credentials). |
| 24 | L8 | Plausible drops rows outside `[StartDate, EndDate]`. |
| 25 | L2 | RSS 1.0 / RDF feeds are parsed. |
| 26 | L3 | YouTube durations with day and week components parse correctly. |
| 27 | tests | Snapshot sources (OpenVSX, YouTube, LinkedIn, RSS) are tested to date records with `EndDate`. |

### Phase 5 — Commands and presentation (delegated, reviewed before merge)

| # | Finding | Change |
|---|---|---|
| 28 | C1 | Badge presets use the metric names sources emit today, and the `--project` filter is parenthesised. A test fails if a preset matches no metric any source emits. |
| 29 | C2 | `export --project` filters content by `project`. |
| 30 | L10, L11 | Project rename rewrites `project_id` in the fetch log, and rollback failures are reported. |
| 31 | L12 | Config edits are written atomically. |
| 32 | L13 | The generated workflow gets a `concurrency:` group, rebases before pushing, and commits even when `fetch` exits non-zero (`if: ${{ !cancelled() }}`), so data from successful jobs is kept while the run still shows the failure. |
| 33 | L14 | `build-db` builds into a temp file and replaces the old database only on success. |
| 34 | tests | `internal/projectdata` gets tests. |

### Phase 6 — Security and supply chain (delegated, reviewed before merge)

| # | Finding | Change |
|---|---|---|
| 35 | X1 | `install.sh` and `action.yml` verify the archive against `SHA256SUMS` before extracting. |
| 36 | L18 | `sync-secrets` reads `.env` values with the same unquoting `auth` writes, and values with newlines round-trip. |
| 37 | L19 | MCP `query` caps `limit`. MCP `export` only writes inside the project directory. `query`/`query_table` are added to the forbidden functions (defence in depth). |
| 38 | L20 | `docs.yml` pins `great-docs` to a commit. |
| 39 | tests | A test that restricted queries can't read files once external access is disabled. |

### Phase 7 — Hygiene, CI and docs

| # | Change |
|---|---|
| 40 | `gofmt` the 17 unformatted files (separate commit, no behaviour change). |
| 41 | CI runs `go vet`, `go test -race` and a gofmt check. |
| 42 | User guide: compaction semantics, `extra`/`user`, content sources, safe backfills and repairs, exit codes, the workflow change. |
| 43 | Re-run the mutation suite and coverage. Run the built binary end to end against a scratch project. Update the review report with each finding's resolution. |

## Out of scope, with reasons

- **Changing the LinkedIn API version.** It can't be verified without credentials. It becomes a named constant with a note instead.
- **Automatic repair of past gaps.** That's data surgery; it's better done deliberately with the runbook below once the fixes are deployed.
- **golangci-lint in CI.** 30 pre-existing errcheck findings would need their own change.

## Data repair runbook (after deploying the fixes)

1. Pull the latest data repository and confirm a clean `velocirepo validate-data`. Fix overlaps and temp files with `--fix`.
2. Find month-end gaps with the query in §7 of the review. The CRAN gaps reported so far are 2026-07-31, 2026-08-31 and 2026-09-30.
3. Re-fetch each missing day, e.g. `velocirepo fetch-cran --start-date 2026-07-31 --end-date 2026-07-31`. With the merge fix, the day is merged into `2026-07.jsonl`.
4. GitHub close/merge events: re-fetch the history once with `velocirepo fetch-github --start-date <first tracked date>`. Fetching by `UPDATED_AT` covers every item touched since then.
5. Plausible partial days: re-fetch the affected range. Newest-wins compaction replaces the partial values.
6. Commit the result. Check the diff for unexpected deletions before pushing.

## Execution policy

- **Branches.** One branch, `fix/correctness-review`. Delegated clusters work on temporary branches in separate worktrees. I review every commit and cherry-pick it in, then delete the temporary branches.
- **Commits.** Small and focused: one finding per commit, imperative subject, the reason in the body, no attribution trailers.
- **Before every commit.** `gofmt` and `go vet`. `go test ./...` must pass except for the regression tests whose fix hasn't landed yet.
- **At milestones and at the end.** `go test -race ./...`, `-shuffle=on -count=3`, the mutation suite, coverage, and the built binary end to end.
