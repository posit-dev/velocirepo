# Design: detecting and recovering from data problems

Status: proposal, October 2026.

velocirepo depends on third-party APIs that change without notice. So far, problems were found by chance, months after they started. This design makes every known kind of problem visible on the first night it happens, keeps bad data off `main`, and makes data that's found to be wrong later repairable. Everything it uses is free and open source.

## Why

What went wrong in devrel-io in 2026, and how long it went unnoticed:

| Incident | Kind | Unnoticed for |
|---|---|---|
| GitHub stopped listing stargazers. The fetch got an empty list with HTTP 200 and stored no stars. | Silent: the API changed | ~3 months |
| A full re-fetch (devrel-io `03f2f5e66`) failed for positron, quarto and rstudio on GitHub's 300-page comment limit. Their whole event history was deleted in the same commit. | Silent: data deleted | ~3 months |
| CRAN lost the last day of every month in concatenation. | Silent: incomplete data | months |
| Plausible backfills repeated some rows and missed others across pages. | Silent: wrong values | found by chance |
| Star history backfill replaced daily files and dropped 44,619 `total_reactions` records. | Silent: data deleted | caught by a manual check |
| After v0.9.0, the nightly run failed four nights in a row (2026-10-17 to 10-20) on the same three projects. | Loud, but nobody saw it | 4 days |

Three lessons:

1. **A successful run isn't a healthy run.** Most incidents returned HTTP 200 and exited 0. Checks have to look at the data, not just the exit code.
2. **A red run isn't a notification.** Even the loud failure went unseen.
3. **Late detection means lost data.** GitHub traffic keeps 14 days, PyPI about 180, snapshot sources (Homebrew, Open VSX, YouTube totals, `total_stars`, `total_reactions`) keep nothing. Detection has to be fast, or the responses have to be kept.

## Goals

- Each incident in the table above is caught on the first night it happens. Each one becomes a test (see [Testing the checks](#testing-the-checks)).
- Bad data doesn't reach `main`. Data that passes the checks lands as it does now.
- Data that's found to be wrong later can be rebuilt from the responses that produced it.
- One notification when something breaks and one when it's resolved, not one per night.
- Free and open source only. No hosted services that aren't self-hostable.

Out of scope: real-time alerting (nightly is the cadence), and dashboards beyond a weekly digest.

## Overview

Eight measures. This document designs the first two in detail, because they change where data is stored and how it reaches `main`. The others plug into them and are outlined in [Further measures](#further-measures).

1. **Raw response archive:** every API response is kept, so any processing error can be repaired by replaying it.
2. **Data gate:** the nightly run checks its own output and only pushes what passes to `main`. What fails is held back in a pull request, and a tracking issue says why.
3. **Reconciliation** against totals the APIs compute themselves.
4. **Fetch each day once it has ended** in its source's time zone, instead of re-fetch windows, and a quota budget.
5. **Live API tests and upstream change monitoring.**
6. **Heartbeat** with escalation.
7. **Weekly digest** for a human.
8. **Tests for the checks themselves.**

```
fetch ──► raw archive (1) ──► stored data
               │                   │
               ▼                   ▼
            replay         velocirepo check (2, 3) ──► findings
                                                          │
                              ┌───────────────────────────┤
                              ▼                           ▼
                pass: data commit to main      fail: hold back in PR,
                              │                update tracking issue ──► heartbeat (6)
                              ▼
               on the 8th: concatenation commit
```

## 1. Raw response archive

### What's captured

One record per HTTP exchange:

- time, method and URL, with secrets redacted;
- the request body, redacted;
- the status code;
- selected response headers: rate-limit headers, `Link`, `Deprecation`, `Sunset`, `Warning`;
- the response body.

Request headers are not captured, so `Authorization` and cookies never reach the archive.

Redaction removes known secret query parameters (`key`, `token`, `access_token`, `api_key`, `client_secret`) and known secret fields in request bodies. A test fails if an archive written in tests contains any of the test secrets. In CI, the same check runs against the real secret values before the archive is uploaded, and the upload is skipped if one is found.

### Where it's captured

Every source sends its requests through `doRequest` (`internal/source/http_json.go`), using the HTTP client from `fetch.CountingClient` (`internal/fetch/stats.go`). The capture is a `http.RoundTripper` next to `countingTransport`, so no source has to change. It buffers each response body, records it, and hands the source an equivalent body.

### Format

- **Layout:** one directory per run, `raw/<run-id>/`.
- **Files:** one file per source, `raw/<run-id>/<source>.jsonl.zst`, with one exchange per line. It's compressed with zstd using `github.com/klauspost/compress` (pure Go, BSD-3).
- **Manifest:** `raw/<run-id>/manifest.json` lists the velocirepo version, configuration hash, request counts and the SHA-256 of each file.

### Size

The nightly devrel-io run on 2026-10-20 made 4,321 requests (4,191 to GitHub). Response sizes vary widely: GraphQL pages with issue bodies are large, star history and CRAN responses small. Assuming an average of 30 KB, that's about 130 MB a night, or 10–20 MB compressed, so 4–7 GB a year. **This is an estimate.** The first rollout step measures it.

That rules out storing it as files in the data repository: its pack is already 522 MB, and every clone would pay for the archive.

### Where it's stored

| Option | Free | Retention | Notes |
|---|---|---|---|
| Files in the data repository | yes | forever | Too big; every clone downloads it. |
| A separate Git repository | yes | forever | Git handles gigabytes of binary history badly, and the workflow's token can't write to another repository. |
| GitHub Actions artifacts | yes | at most 90 days | Already part of the workflow; good for recent runs. |
| Release assets in the data repository | yes | forever | Not part of clones or the Git history. The workflow's `GITHUB_TOKEN` can create releases in its own repository, so nothing has to be set up. One release per month, one asset per run and source; files up to 2 GB. Relies on GitHub keeping release storage free. |
| Self-hosted S3-compatible storage (Garage, SeaweedFS) | yes, with your own machine | forever | Independent of GitHub, but needs a server. |

**Recommendation:** upload every run as an Actions artifact (90 days), and add the long-term part as release assets in the data repository itself. The releases are named `raw-YYYY-MM`, so they're easy to tell apart from anything else in the repository. The storage backend sits behind a small interface, so moving to self-hosted S3 storage later only changes configuration.

### Retention

What's worth keeping forever depends on whether the API keeps the history itself:

| Tier | Sources | Retention |
|---|---|---|
| Can't be re-fetched | GitHub traffic, PyPI (older than ~180 days), Homebrew, Open VSX, YouTube totals, LinkedIn, `total_stars`, `total_reactions` | forever |
| Can be re-fetched | CRAN, Plausible, GitHub events and star history, RSS | 90 days |

For the second tier, re-fetching is the repair path. Ninety days is far more than the detection delay once the gate is in place.

### Replay

`velocirepo replay <run-id> [--source <name>] [--project <id>]` rebuilds the records of a run from its archive:

- It runs the normal fetch code against a transport that serves the archived responses instead of the network.
- Requests are matched by method, redacted URL and a hash of the redacted body.
- A request that isn't in the archive is an error, not a network call.

Because it's the same code path as fetch, replay has two uses:

- **Repairs:** fix the bug, replay the affected runs, then run the gate as usual.
- **Tests:** archived responses become realistic fixtures, next to the canned httptest responses.

Sources must not depend on the clock beyond `FetchOptions`, so that a replay is deterministic.

### Privacy

devrel-io is public, and so is its data. Release assets have the visibility of their repository, so the archive is exactly as public as the data. Raw responses can contain more than the stored data, though:

- **Public APIs** (GitHub, CRAN, PyPI, Open VSX, Homebrew, RSS, YouTube's Data API) return what anyone can see. Their responses are archived in full.
- **Private analytics** that velocirepo stores, such as Plausible and GitHub traffic, only return the metrics velocirepo asks for. They're archived in full too.
- **LinkedIn** is different. The posts endpoint returns an organisation's posts with their `visibility` and `lifecycleState`, and velocirepo only stores public, published ones. The share statistics have more fields than the six it stores. So for LinkedIn the archive keeps only what velocirepo reads, after the same filter as the stored data: no drafts, no non-public posts, only the stored statistics.

A source declares which of these it is. A new source is archived in full only if it declares itself public.

## 2. Data gate

### Today

The nightly run fetches, commits everything, and pushes to `main`, even when jobs failed (`if: !cancelled()`). Nothing looks at what was committed.

### Proposed flow

All in the nightly job:

1. Check out `main`.
2. Fetch, without concatenating (`--no-concatenate`). The raw archive is written alongside the data.
3. Upload the archive, also when the fetch failed.
4. Run `velocirepo check`. It compares the working tree with `HEAD` and the stored data with its rules, then writes `check.json` and a Markdown summary.
5. Decide, for each source and project:
   - If it has no blocking findings, its changes go into the **data commit** on `main`.
   - If it has blocking findings, its changes go into a commit on the `velocirepo/held-back` branch. A pull request from that branch to `main` lists the reasons.
6. On the concatenation day, concatenate and make a separate **concatenation commit** (see [Concatenation](#concatenation)).
7. Push `main`, update the tracking issue and write the job summary.

Keeping new data and concatenation in separate commits gives each a check that fits it. The data commit should only add records, so the diff guard can be strict. The concatenation commit should only move records between files, so the set of records before and after it must be identical.

The gate runs in the same job rather than as required status checks on a pull request. The reason: pushes and pull requests made with the workflow's `GITHUB_TOKEN` don't start other workflows. Required checks on a bot's pull request would never run without a personal token or a GitHub App. In the same job, the gate also has the fetch results and the archive at hand.

### Holding back per source and project

One broken repository shouldn't hold back everything. Findings refer to a source, a project and the files they concern. The gate restores the files of a held-back project to their `main` versions (`git restore --source=HEAD -- <paths>`) before committing to `main`, and commits them on the held-back branch instead. Findings that don't belong to a project, such as an unreadable config or a failed archive upload, hold back the whole run.

### What happens to held-back data

- **The branch is rebuilt every night:** `velocirepo/held-back` is `main` plus that night's held-back changes. Since `main` doesn't get those days, the next run's resume point fetches them again, so the pull request always shows the complete pending diff.
- **If the next run passes,** the data lands on `main`, and the gate closes the pull request and deletes the branch.
- **If the findings are wrong,** merge the pull request by hand, and accept the findings (see below) so they don't come back.
- **For sources that can't be re-fetched,** the held-back commits and the raw archive keep the data until it's released.

### Concatenation

Today a month is concatenated in the same run that stores its last day, as part of `fetch`. In the workflow, the nightly `fetch` runs with `--no-concatenate` instead, and concatenation becomes a step of the gate with its own commit, once a month. It's not a separate command: nobody needs to run it by hand. A `fetch` without the gate, such as a local run, still concatenates automatically, using the first two conditions below.

A project's month is concatenated when all of these hold:

1. **The calendar:** it's the concatenation day, the 8th of the following month by default (`[settings] concatenate_on_day`).
2. **The data:** a day after the month's last day has been stored, so every daily file of the month has been committed at least once. With the nightly run scheduled after every source's day has ended, that's on the 2nd for every source.
3. **The gate:** the project has no open findings and no held-back data for that month.

A year is concatenated when December is, and in the same run: December's daily files become `YYYY-12.jsonl`, and then the twelve monthly files become `YYYY.jsonl`. Yearly files are kept as they are.

Why one week after the month ends:

- **Corrections stay small.** Most problems surface in the first days: a held-back pull request, a failed night, a source running behind. Fixed before concatenation, a correction only touches daily files. After concatenation, it rewrites a monthly or yearly file.
- **It covers every source delay.** A source that's still behind on the 8th has a freshness problem, which `data.freshness` reports long before then.
- **It's predictable.** One concatenation commit per month, "Concatenate October 2026", on a known day. January's also concatenates the previous year.
- **It's cheap.** At most about 38 daily files per project at a time, and every record still moves into a period file exactly once, and into a yearly file once more.

A month that misses its concatenation day, because one of the conditions doesn't hold yet, is concatenated on the first run where all three hold.

The resume point and `LastDate` work as they do now: a monthly or yearly file still counts as stored through the last day of its period.

### Commit messages

The gate writes the commit messages, instead of the workflow's shell. Today the two disagree: devrel-io's workflow dates its commits with yesterday, while the workflow `install-ci` generates uses today.

The subject carries the date of the run, in UTC, since that's the one date that's the same for every source and always true. The body lists how far each source's data now goes, because that can differ per source:

```
Fetch metrics on 2026-10-21

Stored through:
- cran, pypi, homebrew, github, github-traffic: 2026-10-20
- github star history, plausible (plotnine.org): 2026-10-19

Held back: positron/github (diff.records-removed), see #12.
```

This example is a manual run at 06:00 UTC, when 2026-10-20 hadn't ended yet in US Pacific time. The scheduled run at 09:30 UTC has the same date for every source.

The other commits follow the same pattern: "Concatenate October 2026" for concatenation, and "Hold back metrics fetched on 2026-10-21" on the held-back branch, with the findings in the body.

### Findings

Each check produces findings of this shape:

```json
{"id": "a3f9…", "check": "diff.records-removed", "severity": "error",
 "source": "github", "project": "positron", "target": "posit-dev/positron",
 "message": "57,155 events removed, 0 added", "paths": ["data/events/github/positron/"]}
```

- **`severity`:** `error` holds data back, `warning` lets it land but is reported.
- **`id`:** a stable hash of the check, source, project, target and a check-specific key. It's used to detect when a finding is new or resolved, and to accept findings.
- **Accepting findings:** known false positives, such as two bot comments posted in the same second, go in `velocirepo/checks.toml`, with a reason and optionally an expiry date. Since that file is in Git, accepting a finding is a reviewed change.

### Checks in the gate

| Check | Error when | Would have caught |
|---|---|---|
| `fetch.failed` | a job failed | the four failed nights |
| `diff.records-removed` | stored records disappear, compared by key over each changed project (not by file, since concatenation moves records between files) | the July deletion; the 44,619 `total_reactions` |
| `diff.values-changed` | a stored value changes. Nightly runs don't re-read stored days (see [Fetch each day once it has ended](#4-fetch-each-day-once-it-has-ended)), so a changed value is either a repair, which goes through a pull request, or a bug. Records dated within the run's own window, such as a second run on the same day, are exempt. | the Plausible duplicates, which changed stored values |
| `concatenate.records-moved` | the concatenation commit changes the set of records instead of only moving them between files | the CRAN month ends and the overwritten months (A1) |
| `data.integrity` | `validate-data` finds duplicates, overlapping period files or temp files | Plausible duplicates |
| `data.freshness` | a target's latest stored day is further behind today than the source allows: CRAN and PyPI 2 days, Plausible and the star history 2, GitHub events 1, GitHub traffic 1 | stalled sources |
| `data.volume` | a series that usually has data has none for several days, or drops far below its 28-day median | positron's missing events |
| `data.completeness` | a day is missing inside a stored range (month ends) | CRAN month ends |
| `reconcile.*` | stored totals differ from the API's own totals (measure 3) | stars, positron, Plausible, CRAN |

Comparing records instead of files is how the October repair was checked by hand, with DuckDB. `check` reads `HEAD` from a `git worktree` of the same repository and only scans projects with changed files, so its cost grows with the night's changes, not with the size of the repository.

A source can declare that some removals are normal. GitHub events, for instance, disappear when comments or forks are deleted. Those removals become warnings up to a threshold.

### Notifications

- **One tracking issue,** "Data health", opened by the workflow while there are open findings.
  - Its body is rewritten every night with the current findings.
  - A comment is added only when findings appear or are resolved, so a stable problem doesn't notify every night.
  - The issue closes itself when no findings are left.
- **Notifying the owners:** the issue is assigned to the owners listed in `velocirepo.toml` (`[settings] owners`), and mentions them. Being assigned notifies a person whatever their watch settings are, and watchers are notified as usual.
  - `install-ci` fills in `owners` with the GitHub login of whoever runs it, and asks whether to add others.
  - Teams can be listed too (`@posit-dev/devrel`). They're mentioned, since issues can't be assigned to teams.
  - Owners must have access to the repository to be assigned. `install-ci` checks that.
  - Without owners, the issue only notifies watchers, and `check` reports a warning about it.
- **Workflow failure emails:** GitHub's own emails for failed scheduled workflows go to whoever last changed the schedule, depending on their settings. The four failed nights show they can't be relied on alone. The gate makes them redundant.
- **Escalation:** if the issue stays open for more than a day, the heartbeat (measure 6) escalates, for example to ntfy.

### Job summary

Every run writes a summary to `$GITHUB_STEP_SUMMARY`:
- records per source;
- the requests made and the quota left;
- held-back projects and their findings;
- warnings.

Errors and warnings also become `::error::` and `::warning::` annotations, so they're visible on the run's page.

### Where it lives

- **Gate logic:** in velocirepo. That's `velocirepo check`, plus `velocirepo gate`, which does steps 5 to 7, including concatenation. The gate calls GitHub's REST API directly, as `sync-secrets` does, so the workflow doesn't depend on `gh`.
- **Workflow:** the action runs them, and `install-ci` generates the new workflows, so every user gets the gate, not just devrel-io.
- **Permissions:** the workflow needs `contents: write` (commits and the archive releases), `pull-requests: write` and `issues: write`.

### Commits made by hand

The gate only checks the nightly run. The worst incident, the deleted history of 2026-07-28, was a commit made by hand: a full re-fetch run locally and pushed. So the same checks also run on pushes that don't come from the nightly run:

- **On every push to `main` by a person,** a workflow (`velocirepo-check.yml`, generated by `install-ci`) runs `velocirepo check` against the previous commit, with reconciliation's all-time totals. Pushes made with the workflow's `GITHUB_TOKEN` don't start other workflows, so the nightly run's own pushes don't trigger it; the gate has already checked those.
- **A failed check can't be undone by holding back,** since the commit is already on `main`. It opens or updates the tracking issue instead, with the commit and its findings, so the commit can be reverted before the next nightly run builds on it.
- **To keep bad commits off `main` entirely,** a branch protection rule can require pull requests, with `velocirepo-check` as a required status check. That's optional, because it changes how people work with the repository, but the workflow supports it: on a pull request it runs the same checks.

## For other velocirepo users

Everything in this design ships with velocirepo, so it works for any project that uses it, not just devrel-io. What a user has to do:

| Measure | Setup |
|---|---|
| Raw archive | None. Capture is part of `fetch`, and the archive is stored as artifacts and release assets in the user's own data repository. |
| Data gate, concatenation and tracking issue | Regenerate the workflows with `install-ci` once, for the new steps and permissions, and the check on pushes made by hand. Optionally, list owners in `velocirepo.toml` to be assigned to the tracking issue. |
| Reconciliation and other checks | None. They're part of `velocirepo check`. |
| Day ends per time zone, quota budget | None. `install-ci` schedules the nightly run after the latest time zone of the configured sources. |
| Live API tests, changelog monitoring | None. They run in velocirepo's own CI, and protect users through releases. |
| Heartbeat workflow | None; `install-ci` generates it. The external heartbeat is optional and needs a Healthchecks instance. |
| Weekly digest | None; `install-ci` generates it. |

The upgrade notes of the release that introduces the gate say that the workflow must be regenerated. Until it is, the old workflow keeps working without the gate.

## Further measures

These plug into the archive and the gate.

### 3. Reconciliation

Each source can provide reference totals that the API computes independently of the data velocirepo stores. They're fetched in the same run, so they're archived too. Two levels:

- **Every night, all-time totals.** For GitHub, one GraphQL query returns a repository's numbers of issues, pull requests, merged pull requests, forks and stars, and many repositories fit in one query. Two repositories cost 1 point of the 5,000 an hour, so all 465 of devrel-io's take roughly ten requests. Compared with the stored events, this finds missing or duplicated history right away. It would have shown 8,172 positron issues on GitHub against none stored.
- **Every week, per-month totals.** The search API's counts of issues and pull requests opened, closed and merged in the last complete month find gaps that all-time totals don't, such as a missing week that happens to balance out. Older months are covered by the all-time totals. That's four searches per repository, about 1,860 for devrel-io. The search API allows 30 requests a minute, so they're spread over the nights of the week: about 270 searches, or 9 minutes, a night.

**Quota:** reconciliation may use at most 10% of each API's hourly quota per run, and gives way first when the quota runs low (see the quota budget in measure 4).

| Source | Reference total | Verified |
|---|---|---|
| GitHub events | search API counts of issues and PRs opened, closed and merged per month | yes, in the October repair |
| GitHub stars | `stargazerCount` against summed `daily_stars` | yes |
| GitHub forks | `forkCount` against fork events | yes; deleted forks make the stored count slightly higher |
| GitHub issues and pull requests, all time | `issues`, `pullRequests` and `pullRequests(states: MERGED)` total counts | yes |
| Plausible | aggregate query over a date range, without the day dimension | to verify |
| CRAN | cranlogs totals over a range | to verify |
| PyPI | pypistats recent totals (day, week, month) | to verify |
| Open VSX, YouTube, Homebrew | snapshot totals are the data itself; check the history for gaps instead | — |

Tolerances belong to each source: stars, for example, differ by the stars given since the end date, and fork events by the forks that were deleted since.

Every source either provides reference totals or declares that it has none, with a reason. A test enforces that, like the test that keeps badge presets in line with the metrics sources emit, so a new source can't skip reconciliation by accident.

### 4. Fetch each day once it has ended

An earlier version of this design re-read every source's last days on each run. That's dropped, because the problems it would fix are already handled better:

| Problem | Handled by |
|---|---|
| A failed night | The resume point. A failed job doesn't move it, so the next run fetches the missing days. |
| Data that's published late | The same: a day that isn't available yet isn't stored, so it's fetched the next night. |
| A day that's still in progress when it's fetched | Fetching a day only once it has ended in the source's time zone (below). |

Stored values don't change once their day is complete. In the October repair, re-fetched CRAN month ends were identical to the stored ones, and 206,477 of about 210,000 re-fetched Plausible records were. The rest were partial days and sampled values. Re-reading would also cost API quota and rewrite concatenated files, which grows the data repository's history.

**Days end in different time zones.** Most sources count days in UTC, but not all:

| Source | Days end in | Yesterday is over at |
|---|---|---|
| CRAN, PyPI, GitHub events and traffic, Open VSX, YouTube | UTC | 00:00 UTC |
| GitHub star history | US Pacific time | 07:00 UTC in summer, 08:00 UTC in winter |
| Plausible | each site's own time zone, from the Sites API (`GET /api/v1/sites/{site}`) | depends on the site; for devrel-io's 15 sites, the latest is `plotnine.org` on `America/Los_Angeles` |

A fetch stores a source's day only once that day has ended in the source's time zone, plus a margin of an hour for processing. The default end date becomes the latest day that has ended, per source and site, instead of "yesterday" for every source. This replaces `settleDays` (`internal/fetch/fetch.go`), which delays Plausible by a whole day regardless of the time of the run, and it replaces the star history's 7-day re-read. When a site's time zone can't be read, the fetch assumes UTC−12, the latest time zone there is.

**The nightly schedule.** `install-ci` schedules the nightly run after the latest time zone among the configured sources, plus the margin: 09:30 UTC for devrel-io, which covers US Pacific time in winter. At that time every source's yesterday has ended, so a nightly run fetches all sources through the same day. Runs at other times, such as manual ones, still only store days that have ended, so they never store a partial day either.

devrel-io's schedule moves from 06:00 to 09:30 UTC, so its data arrives three and a half hours later than now.

**Accepted drift.** GitHub lowers a past day of the star history when a star given that day is removed. Those corrections aren't fetched, so summed `daily_stars` slowly drifts above `total_stars`, by a few stars a year for a large repository. `total_stars` stays exact, and `reconcile.stars` allows for the drift.

**Quota.** The fetch reads rate-limit headers and keeps a budget per API. When a run would exceed it, reconciliation (measure 3) gives way first, and the run reports a `quota.low` warning.

**GitHub traffic.** GitHub's traffic API only keeps the last 14 days of views and clones. A missed day is fetched by the resume point as long as it's within those 14 days, and lost for good after that. That's why its freshness limit in `data.freshness` is the strictest: one day.

### 5. Live API tests and upstream changes

- **Live tests:** a scheduled workflow in the velocirepo repository fetches known public targets from every API each day. It asserts the response shape, the types, plausible ranges and known limits (the 300-page cap, the 30-week history pages). Recorded responses are kept as replay fixtures.
- **Deprecation headers:** they're archived (measure 1), and `check` reports them as warnings.
- **Changelogs:** a configuration in the velocirepo repository fetches the GitHub and Plausible changelogs with `fetch-rss`, and flags entries that mention an endpoint velocirepo uses. GitHub announced both the stargazer restriction and the star history endpoint in its changelog.

### 6. Heartbeat

The nightly job can't report that it didn't run at all, for example because GitHub disabled the schedule or a token expired. Two levels:

- **Heartbeat workflow (default).** `install-ci` also generates a second scheduled workflow, `velocirepo-heartbeat.yml`, that runs at noon. It fails and opens the tracking issue if the latest data commit is more than 30 hours old, or if the issue has been open for more than a day. It also runs reconciliation's all-time totals against `main`, about ten requests for devrel-io, as a daily safety net for anything that got past the gate and the check on pushes. It needs no setup. It does share GitHub Actions with the nightly job, so it can't report an Actions outage.
- **External heartbeat (optional).** For independence from GitHub, a self-hosted [Healthchecks](https://github.com/healthchecks/healthchecks) (BSD-3) instance on other infrastructure. The workflow pings it when it starts and when it finishes, with success or failure, and Healthchecks alerts on a missing ping and escalates to ntfy or email. It's enabled by setting a ping URL as a secret.

### 7. Weekly digest

A scheduled workflow renders a view with one sparkline per source and project over the last 8 weeks, plus the week's findings, and posts it as an issue comment. It's for the problems no check was written for. Three months of zero positron events would have stood out in the first week.

### 8. Testing the checks

Each incident in [Why](#why) becomes a test. It replays recorded or synthetic responses and asserts that `check` reports an error:

| Incident | Test |
|---|---|
| Hidden stargazers | Star sources return an empty list for a repository with stars, and `reconcile.stars` fails. |
| Deleted history | A job fails and its project's files are deleted, and `diff.records-removed` fails. |
| CRAN month ends | A month-end day is missing, and `data.completeness` fails. |
| Plausible pages | Pages overlap, and `data.integrity` and `reconcile.plausible` fail. |
| Replaced daily files | A daily file loses records, and `diff.records-removed` fails. |
| Failed nights | A job fails, `fetch.failed` fails, and the tracking issue is opened. |
| Deleted history in a commit made by hand | A pushed commit removes a project's events, and the check on pushes reports `reconcile.github` and `diff.records-removed`, and opens the tracking issue. |

#### Reconciliation tests

Reconciliation is the check that catches what nobody anticipated, so it gets its own tests. They use httptest servers with canned reference totals, like the source tests, and stored data written with the store functions.

| What | Test |
|---|---|
| Reading reference totals | Each source's reference totals are parsed from canned responses: GitHub's batched counts, including a batch with a repository that no longer exists, Plausible's aggregate, and the totals of CRAN and PyPI. |
| Matching totals | Stored data that matches the reference totals reports nothing. |
| Within tolerance | Differences up to the source's tolerance report nothing: new items since the end date, deleted forks, removed stars. |
| Beyond tolerance, both ways | Missing data (fewer stored than the API reports) and duplicated data (more stored) both report an error, with both numbers in the message. |
| Missing history | A project with no stored events for a repository that has thousands reports an error. That's the positron case. |
| No reference total | When the reference request fails, `reconcile.unavailable` is a warning, never a silent pass, and never a match. |
| A reference total that's suddenly zero | A repository that reported thousands of issues yesterday and 0 today is reported as a suspicious reference total, not as stored data that's now too high. |
| Per-month windows | Month boundaries are compared in the same time zone as the stored days, so items near midnight don't cause false differences. Stars use US Pacific time. |
| Weekly rotation | Over seven simulated nights, every repository's last complete month is checked exactly once, and no night exceeds the search API's limit. |
| Quota | When the budget is used up, the remaining checks are skipped with a `quota.low` warning that names them, and they're picked up on a later night. |
| Every source accounted for | Every registered source provides reference totals or declares that it has none. |
| On pushes and in the heartbeat | The check workflow and the heartbeat workflow run reconciliation and open the tracking issue on a mismatch. |

The live API tests (measure 5) also cover the reference endpoints, so a change in, for example, the shape of GitHub's counts is noticed as a broken reference, not as a mismatch in the data.

Mutation testing of the checks themselves (removing a check must fail a test) is part of the existing mutation suite.

## Rollout

1. **Fetch days once they've ended:** per-source time zones (UTC, US Pacific for the star history, each Plausible site's own), replacing `settleDays` and the star history's 7-day re-read, and the later nightly schedule in `install-ci`. It's independent of everything else and stops stored days from changing.
2. **Measure:** archive capture behind a flag, uploaded as an artifact only. Measure size and redaction for a week.
3. **Replay:** add `velocirepo replay`, and test it against the measured archives. It must rebuild the stored records exactly.
4. **Concatenation:** the three conditions as a step of the gate, and the concatenation commit with its `concatenate.records-moved` check.
5. **Report-only gate:** `check`, the tracking issue and the check on pushes made by hand, with the gate reporting findings but holding nothing back. Thresholds are tuned for two weeks, and false positives are accepted or fixed.
6. **Enforcing gate:** holding back per project, with the held-back branch and its pull request.
7. **Further measures:** reconciliation with its tests from [Reconciliation tests](#reconciliation-tests), the quota budget, live tests, the heartbeat workflow and the digest, each with its tests from the tables above. Reconciliation's all-time totals for GitHub come first: they're the cheapest check with the widest reach.

## Decisions

The questions this design left open, each with the default it now uses, an alternative, and why the default was chosen.

### Private analytics in the archive

- **Default:** archive public APIs and the private analytics velocirepo stores in full, and LinkedIn only as far as velocirepo reads it, after the same filter as the stored data. See [Privacy](#privacy).
- **Alternative:** archive everything in full, in storage that isn't public (a private repository or self-hosted S3 storage).
- **Why the default:** the archive's main use is replaying fixes, and almost every bug so far was in public APIs, which are kept in full. For LinkedIn, an accidentally published draft or non-public post can't be taken back, while a replay that misses a field only means re-fetching. The alternative would also need a second repository or server, and a token, for every user.

### Hold-back granularity

- **Default:** hold back per source and project. The rest of the night lands on `main`.
- **Alternative:** hold back the whole night when any project has a blocking finding.
- **Why the default:** with 465 projects, one broken repository or one false positive would otherwise stop all data, every night until it's resolved. That would also put pressure on accepting findings quickly instead of fixing them. The cost is attributing findings to files, which `check` needs anyway to say what's wrong. Findings that don't belong to a project still hold back everything.

### Who gets notified

- **Default:** the owners in `[settings] owners`, assigned to the tracking issue. `install-ci` fills it in with the login of whoever runs it; teams can be mentioned.
- **Alternative:** only the repository's watchers, with nothing to configure.
- **Why the default:** whether watchers get notified depends on each person's settings, and the four failed nights showed that GitHub's default notifications can't be relied on. Assignment always notifies. Watchers still get notified too, so the default adds to the alternative rather than replacing it.

### Quota for reconciliation

- **Default:** at most 10% of each API's hourly quota per run. All-time totals every night, which cost about ten GitHub requests for devrel-io, and per-month search counts spread over the week.
- **Alternative:** a separate token for reconciliation, which doubles the quota, or reconciliation only once a week.
- **Why the default:** the nightly totals cost almost nothing and catch most problems on the first night, as missing history did. The per-month counts are what's expensive, and spreading them over the week keeps them within the search API's limit without delaying the fetch. A second token is more setup for every user, and weekly-only checks would let problems go unnoticed for up to a week.
