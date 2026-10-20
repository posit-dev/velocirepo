# Design: detecting and recovering from data problems

Status: proposal, October 2026.

velocirepo depends on third-party APIs that change without notice. So far, problems were found by chance, months after they started. This design makes every known kind of problem visible on the first night it happens, keeps bad data off `main`, and makes data that's found to be wrong later repairable. Everything it uses is free and open source.

## Why

What went wrong in devrel-io in 2026, and how long it went unnoticed:

| Incident | Kind | Unnoticed for |
|---|---|---|
| GitHub stopped listing stargazers. The fetch got an empty list with HTTP 200 and stored no stars. | Silent: the API changed | ~3 months |
| A full re-fetch (devrel-io `03f2f5e66`) failed for positron, quarto and rstudio on GitHub's 300-page comment limit. Their whole event history was deleted in the same commit. | Silent: data deleted | ~3 months |
| CRAN lost the last day of every month in compaction. | Silent: incomplete data | months |
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
4. **Re-fetch windows** for every source, within a quota budget.
5. **Live API tests and upstream change monitoring.**
6. **External heartbeat** with escalation.
7. **Weekly digest** for a human.
8. **Tests for the checks themselves.**

```
fetch ──► raw archive (2) ──► stored data
               │                   │
               ▼                   ▼
            replay         velocirepo check (2, 3, 4) ──► findings
                                                             │
                              ┌──────────────────────────────┤
                              ▼                              ▼
                     pass: push to main          fail: hold back in PR,
                                                 update tracking issue ──► heartbeat (6)
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

That rules out the data repository: its pack is already 522 MB, and every clone would pay for the archive.

### Where it's stored

| Option | Free | Retention | Notes |
|---|---|---|---|
| The data repository | yes | forever | Too big; every clone downloads it. |
| A separate Git repository | yes | forever | Git handles gigabytes of binary history badly. |
| GitHub Actions artifacts | yes | at most 90 days | Already part of the workflow; good for recent runs. |
| Release assets in a separate archive repository | yes | forever | One release per month, one asset per run and source; files up to 2 GB. Relies on GitHub keeping release storage free. |
| Self-hosted S3-compatible storage (Garage, SeaweedFS) | yes, with your own machine | forever | Independent of GitHub, but needs a server. |

**Recommendation:** upload every run as an Actions artifact (90 days), and add the long-term part to release assets in a separate archive repository with the same visibility as the data repository. The storage backend sits behind a small interface, so moving to self-hosted S3 storage later only changes configuration.

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

devrel-io is public, and so is its data. Raw responses can contain more than the stored data does, for example full issue bodies, or fields of private analytics APIs (YouTube, LinkedIn) that velocirepo doesn't store. The archive gets the same visibility as the data repository at most. For private analytics sources, there's an option to keep only the fields that velocirepo reads. Which sources need it is an open question.

## 2. Data gate

### Today

The nightly run fetches, commits everything, and pushes to `main`, even when jobs failed (`if: !cancelled()`). Nothing looks at what was committed.

### Proposed flow

All in the nightly job:

1. Check out `main`.
2. Fetch, as now. The raw archive is written alongside the data.
3. Upload the archive, also when the fetch failed.
4. Run `velocirepo check`. It compares the working tree with `HEAD` and the stored data with its rules, then writes `check.json` and a Markdown summary.
5. Decide, for each source and project:
   - If it has no blocking findings, its changes are committed and pushed to `main`.
   - If it has blocking findings, its changes go into a commit on the `velocirepo/held-back` branch. A pull request from that branch to `main` lists the reasons.
6. Update the tracking issue and write the job summary.

The gate runs in the same job rather than as required status checks on a pull request. The reason: pushes and pull requests made with the workflow's `GITHUB_TOKEN` don't start other workflows. Required checks on a bot's pull request would never run without a personal token or a GitHub App. In the same job, the gate also has the fetch results and the archive at hand.

### Holding back per source and project

One broken repository shouldn't hold back everything. Findings refer to a source, a project and the files they concern. The gate restores the files of a held-back project to their `main` versions (`git restore --source=HEAD -- <paths>`) before committing to `main`, and commits them on the held-back branch instead. Findings that don't belong to a project, such as an unreadable config or a failed archive upload, hold back the whole run.

### What happens to held-back data

- **The branch is rebuilt every night:** `velocirepo/held-back` is `main` plus that night's held-back changes. Since `main` doesn't get those days, the next run's resume point fetches them again, so the pull request always shows the complete pending diff.
- **If the next run passes,** the data lands on `main`, and the gate closes the pull request and deletes the branch.
- **If the findings are wrong,** merge the pull request by hand, and accept the findings (see below) so they don't come back.
- **For sources that can't be re-fetched,** the held-back commits and the raw archive keep the data until it's released.

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
| `diff.records-removed` | stored records disappear, compared by key over each changed project (not by file, since compaction moves records between files) | the July deletion; the 44,619 `total_reactions` |
| `diff.values-changed` | `daily_*` values change outside the source's re-read window (warning) | — |
| `data.integrity` | `validate-data` finds duplicates, overlapping period files or temp files | Plausible duplicates |
| `data.freshness` | a target's latest stored day is older than the source allows (CRAN 2 days, Plausible 3, GitHub 1, traffic 1) | stalled sources |
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
- **Notifying the owner:** GitHub doesn't notify people about their own actions. Issues created by `github-actions[bot]` notify the repository's watchers, but the body also mentions the owners listed in `velocirepo.toml` (`[settings] owners`) and assigns them, so a notification doesn't depend on watch settings.
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

- **Gate logic:** in velocirepo. That's `velocirepo check`, plus `velocirepo gate`, which does steps 5 and 6 using `gh`.
- **Workflow:** the action runs them, and `install-ci` generates the new workflow, so every user gets the gate, not just devrel-io.
- **Permissions:** the workflow needs `contents: write`, `pull-requests: write` and `issues: write`.

## Further measures

These plug into the archive and the gate.

### 3. Reconciliation

Each source can provide reference totals that the API computes independently of the data velocirepo stores. They're fetched in the same run, so they're archived too. `check` compares them with the stored data over a rolling window every night, and over the full history in weekly slices to stay within the quota.

| Source | Reference total | Verified |
|---|---|---|
| GitHub events | search API counts of issues and PRs opened, closed and merged per month | yes, in the October repair |
| GitHub stars | `stargazerCount` against summed `daily_stars` | yes |
| GitHub forks | `forkCount` against fork events | to verify |
| Plausible | aggregate query over a date range, without the day dimension | to verify |
| CRAN | cranlogs totals over a range | to verify |
| PyPI | pypistats recent totals (day, week, month) | to verify |
| Open VSX, YouTube, Homebrew | snapshot totals are the data itself; check the history for gaps instead | — |

Tolerances belong to each source: stars, for example, differ by the stars given since the end date.

### 4. Re-fetch windows

Every source re-reads its last days on each run, like the star history does now, so late or corrected data repairs itself:
- CRAN, Plausible and PyPI: 7 days;
- GitHub events: 2 days;
- traffic: the full 14 days.

The fetch reads rate-limit headers and keeps a budget per API. When a run would exceed it, the re-read windows shrink first, and the run reports a `quota.low` warning.

### 5. Live API tests and upstream changes

- **Live tests:** a scheduled workflow in the velocirepo repository fetches known public targets from every API each day. It asserts the response shape, the types, plausible ranges and known limits (the 300-page cap, the 30-week history pages). Recorded responses are kept as replay fixtures.
- **Deprecation headers:** they're archived (measure 1), and `check` reports them as warnings.
- **Changelogs:** a configuration in the velocirepo repository fetches the GitHub and Plausible changelogs with `fetch-rss`, and flags entries that mention an endpoint velocirepo uses. GitHub announced both the stargazer restriction and the star history endpoint in its changelog.

### 6. External heartbeat

A self-hosted [Healthchecks](https://github.com/healthchecks/healthchecks) (BSD-3) instance, running somewhere other than GitHub:
- The workflow pings it when it starts, and again when it finishes, with success or failure.
- A missing ping means the run didn't happen. That covers a disabled schedule, an Actions outage or an expired token, none of which an in-GitHub check can report.
- It also escalates open tracking issues to ntfy or email.

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

Mutation testing of the checks themselves (removing a check must fail a test) is part of the existing mutation suite.

## Rollout

1. **Measure:** archive capture behind a flag, uploaded as an artifact only. Measure size and redaction for a week.
2. **Replay:** add `velocirepo replay`, and test it against the measured archives. It must rebuild the stored records exactly.
3. **Report-only gate:** `check` and the tracking issue, with the gate reporting findings but holding nothing back. Thresholds are tuned for two weeks, and false positives are accepted or fixed.
4. **Enforcing gate:** holding back per project, with the held-back branch and its pull request.
5. **Further measures:** reconciliation, re-fetch windows, live tests, heartbeat and digest, each with its tests from the table above.

## Open questions

1. **Long-term storage:** release assets in an archive repository, or self-hosted S3 storage from the start?
2. **Private analytics:** which sources should keep only the fields velocirepo reads in the archive (YouTube, LinkedIn)?
3. **Hold-back granularity:** per source and project, as proposed, or everything for simplicity?
4. **Who gets notified:** only the owners listed in the config, or also the repository's watchers?
5. **Quota:** how much of the GitHub quota may reconciliation use? The nightly run already uses about 4,200 of 5,000 requests an hour.
