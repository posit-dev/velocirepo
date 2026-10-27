# Design: detecting and recovering from data problems

Status: proposal, October 2026.

velocirepo depends on third-party APIs that change without notice. So far, problems were found by chance, months after they started. This design makes every known kind of problem visible on the first night it happens, keeps bad data out of the stored data, and makes data that's found to be wrong later repairable. Everything it uses is free and open source.

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

- Each incident in the table above is caught on the first night it happens. Each one becomes a test (see [Testing the checks](#8-testing-the-checks)).
- A nightly run can't lose or corrupt stored data: removals and changes are undone, and new data with errors (such as duplicates) is held back. Other anomalies, such as missing or zero data, are reported on the first night. Data that passes the checks lands as it does now.
- Data that's found to be wrong later can be repaired: by re-fetching it, or, where the API no longer has it, by replaying the responses that produced it.
- One notification when something breaks and one when it's resolved, not one per night.
- Free and open source only. No hosted services that aren't self-hostable.
- Nothing to set up for other velocirepo users beyond regenerating their workflows, and no repository settings to change.

Out of scope: real-time alerting (nightly is the cadence), and dashboards beyond a weekly digest.

## Overview

Eight measures. The first two change where data is stored and how it reaches `main`, so they're designed in detail. The others plug into them.

1. **Raw response archive:** the responses of data that can't be re-fetched are kept, so a processing error can be repaired by replaying them.
2. **Data gate:** the nightly run checks its own output and the commits made since the previous run. Stored data is never removed or changed by a nightly run, and new data that fails a check is held back until it passes. A tracking issue says what's wrong.
3. **Reconciliation** against totals the APIs compute themselves.
4. **Fetching each day once it has ended,** with resume points per target that only move as far as the source has published.
5. **Live API tests and upstream change monitoring.**
6. **Heartbeat** for runs that didn't happen.
7. **Weekly digest** for a human.
8. **Tests for the checks themselves.**

```
                 ┌─ commits since the last run ─┐
                 ▼                              │
fetch ──► stored data + held-back data ──► velocirepo check (2, 3) ──► findings ──► tracking issue
  │                                             │                                      ▲
  ▼                                             ▼                                      │
raw archive (1)                    gate: new data that passes lands;          heartbeat (6)
  │                                removals and changes are undone;
  ▼                                the rest is held back
replay                                          │
                                                ▼
                                  on the 8th: concatenation commit
```

## 1. Raw response archive

### What's archived

Only the responses of data that can't be re-fetched. For everything else, a repair re-fetches the data from the API, which is simpler and covers bugs in how requests are built as well as in how responses are processed. Replay can only fix the latter: the Plausible pagination bug, for example, was fixed by changing the request, and the missing rows never appeared in any response.

Each source declares, for every request it makes, whether its data can be re-fetched:

| Archived (can't be re-fetched) | Not archived (can be re-fetched) |
|---|---|
| Snapshots: GitHub's repository info (`total_stars`) and reaction counts (`total_reactions`), Homebrew, Open VSX, YouTube totals, RSS feeds | GitHub events, comments and star history |
| GitHub traffic (the API keeps 14 days) | CRAN |
| PyPI (the API keeps about 180 days) | Plausible |
| LinkedIn (statistics are snapshots) | |

Traffic and PyPI are archived in full, not just their older days: their requests are few and small, and it keeps the rule simple.

**Archived requests have to be replayable on their own.** Replay serves only archived responses, so a job whose archived requests depend on requests that aren't archived can't be replayed. GitHub is such a job: its reaction counts are requested for the issues and pull requests it has just listed, and that listing isn't archived. So the GitHub source fetches its snapshots as a separate unit: repository info first, then the reaction counts, for issue and pull request numbers that the manifest records. Replaying GitHub replays only that unit.

### What's captured

One record per HTTP exchange:

- time, method and URL, with secrets redacted;
- the request body, redacted;
- the status code;
- selected response headers: rate-limit headers, `Link`, `Deprecation`, `Sunset`, `Warning`;
- the response body;
- the job and target it belongs to.

Request headers aren't captured, so `Authorization` and cookies never reach the archive. Redaction removes known secret query parameters (`key`, `token`, `access_token`, `api_key`, `client_secret`) and known secret fields in request bodies. A test fails if an archive written in tests contains any of the test secrets. In CI, the same check runs against the real secret values before the archive is uploaded. If one is found, the archive is neither uploaded nor kept as an artifact, and the gate reports an error that names the source, since that night's archived data is lost.

### Where it's captured

Every source sends its requests through `doRequest` (`internal/source/http_json.go`), using the HTTP client from `fetch.CountingClient` (`internal/fetch/stats.go`). The capture is an `http.RoundTripper` next to `countingTransport`, so most sources don't have to change. The exceptions are GitHub, whose snapshots become a unit of their own (see above), and LinkedIn, whose responses are filtered before they're archived. The capture buffers each archived response body, records it, and hands the source an equivalent body.

### Format and size

- **Layout:** one directory per run, `raw/<run-id>/`, with one file per source, `<source>.jsonl.zst`, one exchange per line. It's compressed with zstd using `github.com/klauspost/compress` (pure Go, BSD-3, already an indirect dependency).
- **Manifest:** `raw/<run-id>/manifest.json` lists the velocirepo version, the configuration hash, the options of every job and target (start date, end date, run time, resolved time zones), request counts, and the SHA-256 of each file.
- **Size:** for devrel-io, the archived requests are mostly GitHub's repository info and reaction counts, a few hundred small GraphQL responses a night, plus traffic and the snapshot sources. That's probably a few megabytes a night compressed, under 1 GB a year. **This is an estimate**; the first rollout step measures it.

### Where it's stored

As release assets in the data repository itself, one release per month (`raw-YYYY-MM`), one asset per run and source, kept forever. The releases are created as pre-releases and never marked as the latest release (`prerelease: true`, `make_latest: false`), so they don't notify people who watch releases, and don't change what `releases/latest` points to. That matters when the data lives in a project's own code repository. `[settings] archive = false` turns the archive off.

- **Not in the Git history:** release assets aren't part of clones or the history, so the data repository doesn't grow.
- **No setup:** the workflow token can create releases in its own repository.
- **No storage limits to worry about:** release assets don't count toward GitHub's artifact storage, which is limited for private repositories.
- **Replaceable:** the storage sits behind a small interface, so self-hosted S3-compatible storage (Garage, SeaweedFS) can replace it by configuration.

Capture is explicit: `fetch` only archives with `--archive <dir>`. The gate passes a directory outside the committed tree (`$RUNNER_TEMP`) when `[settings] archive` is on, so raw responses are never committed by accident, also not by a workflow that runs `git add velocirepo/`.

A failed upload doesn't hold data back: the data itself is fine. But on GitHub's hosted runners, the workspace is deleted when the job ends, so the archive has to survive until it can be uploaded:

- **The gate retries** the upload within the run.
- **If it still fails,** the gate reports a warning and sets an output with the archive's path. The action then saves it with an `actions/upload-artifact` step, since a Go binary can't upload artifacts itself, with a retention of 7 days.
- **The next run** lists the workflow's artifacts through the Actions API, and uploads every archive that has no matching release asset yet. Uploads are idempotent by asset name, so an archive that was already uploaded is skipped, and artifacts don't need to be deleted.
- **An archive that's still pending after six days** becomes an error, since its artifact is about to expire and the data in it can't be re-fetched.

### Privacy

devrel-io is public, and so is its data. Release assets have the visibility of their repository, so the archive is exactly as public as the data. Raw responses can contain more than velocirepo stores, though:

- **Public responses** about public targets are archived in full.
- **Responses about targets that aren't public,** such as a private repository tracked with the personal access token, are archived with only the fields velocirepo reads. A target's visibility has to be known before its responses are captured: for GitHub, the repository info, which comes first in the snapshot unit, says so. A target whose visibility isn't known yet is archived with only the read fields.
- **LinkedIn** is archived with only the fields velocirepo reads, after the same filter as the stored data: its posts endpoint also returns drafts and non-public posts, and its statistics have more fields than the six velocirepo stores.

Every source declares its archive policy. A source without one isn't archived at all, and a test requires a declaration from every source.

### Replay

`velocirepo replay <run-id> [--source <name>] [--project <id>]` rebuilds the records of an archived run:

- It runs the normal fetch code against a transport that serves the archived responses instead of the network, with each job's options from the manifest, so it makes exactly the requests the original run made.
- Requests are matched by method, redacted URL and a hash of the redacted body. A request that isn't in the archive is an error, not a network call.
- Replayed records are written like fetched ones (see [Writing records](#writing-records)).

Replay repairs bugs in how archived responses are processed. Its test fixture is the `total_reactions` bug from October, where a write replaced daily files, replayed through GitHub's snapshot unit. Sources must not depend on the clock beyond `FetchOptions`, so that a replay is deterministic. A repair of a date range (see [Repairs](#repairs)) replays the archived requests of every run whose manifest's end dates fall in that range, oldest first.

## 2. Data gate

### Today

The nightly run fetches, commits everything, and pushes to `main`, even when jobs failed (`if: !cancelled()`). Nothing looks at what was committed.

### The rule

**A nightly run only adds data.** It never removes a stored record and never changes a stored value. The only exceptions are explicit repairs (see [Repairs](#repairs)) and content files, which are updated in place by design. So the gate only has to decide about new records, and the worst outcome of any bug in a night's fetch is new data that's held back, not data that's lost.

### Proposed flow

All in the nightly job:

1. Check out `main`, including `refs/velocirepo/checked`, the last commit the gate checked.
2. **Check the commits since then.** Commits made by people, such as a manual re-fetch, are checked with the diff and reconciliation checks before anything else. Findings about them go in the tracking issue (see [Commits made by hand](#commits-made-by-hand)).
3. **Fetch** with `--no-concatenate`. A project with held-back data is fetched as "stored plus held-back" (see [Held-back data](#held-back-data)). Archived responses are uploaded, also when the fetch failed.
4. **Check** the night's changes with `velocirepo check`. It writes `check.json` and a Markdown summary.
5. **Apply the rule:**
   - **Removed records** are restored from `HEAD`, and reported as `diff.records-removed`.
   - **Changed values** keep their stored value, and are reported as `diff.values-changed`.
   - **New records** of a project with an error finding about them are held back. The other new records go into the **data commit**.
6. **Concatenate** on or after the concatenation day, once the data condition holds, in a separate **concatenation commit** (see [Concatenation](#concatenation)).
7. **Push** `main`, move `refs/velocirepo/checked` to the pushed commit, update the tracking issue, and write the job summary.

Keeping new data and concatenation in separate commits gives each a check that fits it. The data commit only adds records. The concatenation commit only moves records between files, so the set of records before and after it must be identical (`concatenate.records-moved`).

The gate runs in the same job rather than as required status checks on a pull request. Pushes made with the workflow token don't start other workflows, so required checks would never run on them. Creating pull requests with the workflow token also needs the repository setting "Allow GitHub Actions to create and approve pull requests", which is off by default and which not every user can change. So the gate doesn't use pull requests at all.

### Which findings block

- **Only error findings about the night's new records hold data back,** and only for their source and project: a GitHub error doesn't hold back the same project's Plausible data. With 465 projects, one broken repository or one false positive shouldn't stop all data. A finding is about the night's new records when its keys were added that night. Each check's severity and scope are in [Checks in the gate](#checks-in-the-gate).
- **Findings about data already stored** don't hold anything back, since holding new data back doesn't fix old data. They're reported until they're resolved, for example by a repair. Reconciliation differences and stale targets are such findings.
- **Error findings that don't belong to a project,** such as an unreadable config, hold back the whole run. Warnings never hold anything back.
- **A failed job** (`fetch.failed`) has nothing to hold back: as today, a job that fails stores nothing and doesn't move its watermarks, so the next run fetches the same days again. It's an error for the tracking issue, not for holding back.

### Held-back data

Held-back records are stored on `main`, outside the data, in `velocirepo/held-back/<source>/<project>/`, in the same file format as the data. Queries, views and `build-db` only read `velocirepo/data/`, so held-back records are invisible to them.

What's held back is everything the night changed for that source and project: its new records, its watermark entries and its content changes. Its paths in `velocirepo/data/` stay as they are on `HEAD`. So the stored watermarks never move past data that isn't stored, and discarding held-back data can't leave a gap.

- **A held-back project is fetched and checked as a whole.** Before the fetch, the gate overlays the project's held-back records on its stored data through the store's write path, so records for a month that has been concatenated in the meantime merge into its period file instead of sitting next to it, so the resume points, watermarks and the filter for unchanged totals see "stored plus held-back". The fetch then continues from the held-back data instead of re-fetching it, and a total that changes and changes back is filtered correctly.
- **After the fetch,** the project's new records, both the held-back ones and the night's, are checked against the stored data. If they pass, they all land in the data commit, merged through the store functions with newest-wins, and the project's held-back directory is removed. If not, the night's new records join the held-back directory, and the stored data stays as it was.
- **Releasing by hand:** a manual run of the nightly workflow with `release: <source>/<project>` lands the held-back data regardless of its findings, and records the release in the tracking issue. Accepting the finding in `velocirepo/checks.toml` keeps it from coming back.
- **Discarding:** a manual run with `discard: <source>/<project>` deletes the held-back data, watermarks included. The resume points then fall back to the stored watermarks, so data that can be re-fetched is fetched again the next night.

The tracking issue links to each held-back directory.

### Writing records

New records for a period that's already concatenated, which happens with backfills, repairs and new targets, are written directly into the period file instead of as daily files. So velocirepo never creates daily files next to a monthly or yearly file. When `data.integrity` finds such an overlap, something outside velocirepo created it.

Whether a write goes to a daily file or a period file, it replaces the same thing: for every target and date it covers, the stored records of the metrics or event types it contains. That's what `WriteEvents` already does for daily files. A write that covers a day therefore says what's true for that day, including that an event is gone, while other metrics of the same target and date stay. Without that rule a repair couldn't remove an event: events have no value field, so merging them by key would keep the old event next to the corrected one.

### Concatenation

Today a month is concatenated in the same run that stores its last day, as part of `fetch`. In the workflow, the nightly `fetch` runs with `--no-concatenate` instead, and concatenation becomes a step of the gate with its own commit. It's not a separate command: nobody needs to run it by hand. A `fetch` without the gate, such as a local run, still concatenates automatically with the same two conditions:

1. **The calendar:** it's on or after the concatenation day, the 8th of the following month by default (`[settings] concatenate_on_day`).
2. **The data:** every target and series of the project has a watermark past the month's last day, so every daily file of the month has been stored, and committed at least once. The targets are the ones in the current config, so a removed repository or site doesn't count. A target that's further behind than its freshness limit doesn't block either: `data.freshness` reports it, and its late data is merged into the period file once it arrives.

A year is concatenated when December is, in the same run: December's daily files become `YYYY-12.jsonl`, and the twelve monthly files become `YYYY.jsonl`. Yearly files are kept.

Findings don't block concatenation: held-back records aren't in the data, so concatenating the stored data can't fold them in.

Why one week after the month ends:

- **Corrections stay small.** Most problems surface in the first days: held-back data, a failed night, a source running behind. Fixed before concatenation, a correction only touches daily files.
- **It covers every source delay.** CRAN, for example, publishes two days late. A source that's still behind on the 8th has a freshness problem, which `data.freshness` reports long before then.
- **It's predictable:** one concatenation commit per month on a known day, such as "Concatenate October 2026". January's also concatenates the previous year.
- **It's cheap:** at most about 38 daily files per project at a time, and every record still moves into a monthly file once, and into a yearly file once more.

### Repairs

A repair deliberately changes stored values, so it's the one exception to the rule.

- **From the workflow:** a manual run with `repair: <source> <project> <from> <to>` re-fetches that range with the current velocirepo version, for the requests that can be re-fetched: events, comments, the star history, CRAN, Plausible, and traffic and PyPI while they're within the API's 14 and about 180 days. Archived requests, such as snapshots, are never re-fetched for a past range, since that would store today's values on past dates: they're replayed from the runs in the range, or skipped where there's no archive. Changes within that source, project and range, removals included, are reported as `diff.repaired`, a warning that lists what changed, instead of being undone. A repair has to be able to remove records: an event has no value to correct, so a corrected event, or a record moved to another date, is a removal and an addition. Outside the repair's scope, removals are still restored and reported. The repair is its own commit, "Repair plausible/dplyr 2026-06-22..2026-10-16", with a trailer in the same form: `Velocirepo-Repair: plausible/dplyr 2026-06-22..2026-10-16`.
- **By hand:** a commit with the same trailer, for example after a local `fetch` or `replay`, is treated the same way when the gate checks the commits since the previous run.

A test repairs the October `total_reactions` incident by replay, and the Plausible pagination incident by re-fetch, from start to finish.

### The gate's state

The gate keeps a small state file, `velocirepo/gate-state.json`, committed with the data commit whenever it changes. It holds the open findings with their IDs, so the gate knows which ones are new or resolved, and, for findings about commits made by hand, the commit they're about. The records such a commit removed don't have to be stored: they're recomputed from the commit's parent whenever they're needed.

- **The first run** creates `refs/velocirepo/checked` at `HEAD`, without checking the history before it.
- **A checked ref that's no longer an ancestor of `main`,** for example after a force-push, is reported as a finding and reset to `HEAD`.

### Commits made by hand

The worst incident, the deleted history of 2026-07-28, was a commit made by hand: a full re-fetch run locally and pushed. The gate checks every commit since `refs/velocirepo/checked` before it fetches, with the diff checks (comparing each commit with its parent) and reconciliation.

Some of velocirepo's own commands remove data on purpose, and those removals aren't findings:
- **`remove-project` and `rename-project`:** removals under a project ID that's no longer in the config are reported as information. Records that move from an old project ID to a new one, with otherwise the same keys, are recognised as a rename.
- **Schema migrations:** a commit that changes the data schema version is checked for lost records after mapping old metric names to new ones, as `migrate` does.

- **A bad commit is reported within a day,** in the tracking issue, with the commit and its findings, so it can be reverted. Since the commit is already on `main`, the gate can't undo it.
- **Such a finding stays open** until it's accepted, or until the stored data contains the removed records or the original values again, for example after a revert or a repair. The gate checks that every night.
- **Commits the gate makes itself** (data, concatenation, repairs, releases) aren't checked again: the ref is moved past them.
- **When someone pushes during a run,** the gate's push is rejected. It fetches the new `main`, checks the new commits like any other commits made by hand, rebases its own commits onto them, reruns the checks for projects that both touched, and pushes. The ref is moved to the pushed commit only after every commit before it has been checked.

### Commit messages

The gate writes the commit messages, instead of the workflow's shell. Today the two disagree: devrel-io's workflow dates its commits with yesterday, while the workflow `install-ci` generates uses today.

The subject carries the date of the run, in UTC, since that's the one date that's the same for every source and always true. The body lists how far each source's data now goes, because that can differ per source:

```
Fetch metrics on 2026-10-21

Stored through:
- pypi, homebrew, github, github-traffic, plausible: 2026-10-20
- cran: 2026-10-19 (published through 2026-10-19)

Held back: github/positron (data.integrity), see velocirepo/held-back/github/positron/
```

The other commits follow the same pattern: "Concatenate October 2026" for concatenation, and "Repair …" and "Release held-back github/positron" for the manual runs.

### Findings

Each check produces findings of this shape:

```json
{"id": "a3f9…", "check": "diff.records-removed", "severity": "error",
 "source": "github", "project": "positron", "target": "posit-dev/positron",
 "message": "57,155 events removed, 0 added", "paths": ["data/events/github/positron/"]}
```

- **`severity`:** an `error` about new records holds them back; a `warning` is only reported.
- **`id`:** a stable hash of the check, source, project, target and a check-specific key. The key identifies what the finding is about, such as a metric, a date range or a path, and never includes counts or values, so the same problem keeps its ID from night to night even when the numbers in its message change.
- **Accepting findings:** known false positives, such as two bot comments posted in the same second, go in `velocirepo/checks.toml`, with a reason and optionally an expiry date. Since that file is in Git, accepting a finding is a reviewed change.

### Checks in the gate

| Check | Reports | Severity | Scope | Would have caught |
|---|---|---|---|---|
| `fetch.failed` | a job failed | error | its source and project | the four failed nights |
| `diff.records-removed` | stored records disappear, compared as sets of keys over each changed project (not by file, since concatenation moves records between files). The records are restored. | error, or a warning within allowances | stored data | the July deletion; the 44,619 `total_reactions` |
| `diff.values-changed` | a stored value changes; the stored value is kept. Content files are exempt, since they're updated in place. | error | stored data | the Plausible duplicates, which changed stored values |
| `diff.content-removed` | an ID disappears from a content file; it's restored | error | stored data | — |
| `diff.repaired` | the changes of a repair | warning | the repair's range | — |
| `concatenate.records-moved` | the concatenation commit changes the set of records instead of only moving them between files | error | the concatenation commit, which isn't pushed | the months that concatenation overwrote, fixed in v0.9.0 |
| `data.integrity` | `validate-data` finds duplicates, overlapping period files or temp files | error for new records; warning for stored data | both | the Plausible duplicates |
| `data.freshness` | a target's watermark is further behind today than its source allows (see below) | warning | stored data | stalled sources |
| `data.volume` | a target that usually has data (on at least 20 of the last 28 days) gets none for several days | warning | stored data | a source that stops returning data |
| `reconcile.*` | stored totals differ from the API's own totals (measure 3) | warning | stored data | the stars, positron, the Plausible duplicates, the CRAN month ends |

Errors about stored data don't hold anything back (see [Which findings block](#which-findings-block)); they're reported until they're resolved. The diff checks undo what they find instead: removals are restored, and changed values keep their stored value.

**Freshness limits** are the most days allowed between a target's watermark and today, in UTC. A watermark of yesterday is one day behind. Each source declares its limit in its metadata, which both `data.freshness` and concatenation read, and a test requires one from every source:

| Source | Limit | Why |
|---|---|---|
| GitHub events and traffic | 1 | fetched through yesterday every night; traffic only keeps 14 days |
| Plausible, the star history, PyPI, Homebrew, Open VSX, YouTube, LinkedIn, RSS | 2 | fetched through yesterday by the scheduled run, a day of slack |
| CRAN | 3 | published two days late, a day of slack |

The diff checks read the `HEAD` versions of the changed projects' files with `git cat-file --batch`, without checking out the repository, so their cost grows with the night's changes, not with the size of the repository. Reconciliation, `data.integrity` and `data.volume` read all stored data, through DuckDB aggregates; their runtime on devrel-io is measured in the plan's acceptance criteria. Comparing records instead of files is how the October repair was checked by hand, with DuckDB.

The diff checks only read record and content files. Watermark files, the fetch log, the schema version and the gate's state file change every night by design, and are excluded.

A source can declare that some removals are normal. GitHub events, for instance, disappear when comments or forks are deleted. Those removals are warnings up to a threshold.

There's no check for missing days. CRAN, PyPI and Plausible all leave out days without downloads or visits, so a missing day usually means zero. Gaps are prevented instead: resume points only move as far as a source has published (see [measure 4](#4-fetch-each-day-once-it-has-ended)), and reconciliation compares the sum of the stored days with the API's own totals.

### Notifications

- **One tracking issue,** "Data health", with the label `velocirepo-health`, opened by the gate while there are open findings. The gate and the heartbeat find it by that label.
  - Its body is rewritten each night with the open findings, grouped by project, with links to held-back directories.
  - A comment is added only when findings appear or are resolved, so a stable problem doesn't notify every night. That works because finding IDs don't change from night to night.
  - It closes itself when no findings are left.
- **Notifying the owners:** the issue is assigned to the owners listed in `velocirepo.toml` (`[settings] owners`), and mentions them. Being assigned notifies a person whatever their watch settings are, and watchers are notified as usual.
  - `install-ci` fills in `owners` with the GitHub login of whoever runs it, and asks whether to add others. Owners must have access to the repository; `install-ci` checks that.
  - Teams can be listed too (`@posit-dev/devrel`). They're mentioned, since issues can't be assigned to teams.
  - Without owners, the issue only notifies watchers, and `check` reports a warning about it.
- **Escalation:** if the issue has been open for three days, the heartbeat (measure 6) adds one comment that mentions the owners again. It does that once per issue. With the optional Healthchecks heartbeat, the escalation can also go to ntfy or email.
- **Workflow failure emails:** GitHub's own emails for failed scheduled workflows depend on each person's settings. The four failed nights show they can't be relied on alone.

### Tokens

The workflow has two tokens, used for different things:

| Token | How velocirepo gets it | Used for | Why |
|---|---|---|---|
| A **personal access token**, the secret `GH_TOKEN` | the environment variable `GITHUB_TOKEN`, as today: the action exports its `github-token` input under that name, and `fetch`, `check` and `sync-secrets` read it | fetching from GitHub's APIs, and reconciliation's reference requests | It has the 5,000-an-hour quota and access to the tracked repositories. |
| The **workflow token** (`github.token`) | `gate --repo-token`, or the environment variable `VELOCIREPO_REPO_TOKEN`; the action's new `repo-token` input defaults to `github.token` | pushing to `main` and the checked ref, the tracking issue, the archive's release assets, and reading the repository's workflow runs | Its actions appear as `github-actions[bot]`. GitHub doesn't notify people about their own actions, so an issue opened with an owner's personal token wouldn't notify that owner. It needs no setup, and it can't trigger the workflow again. |

Nothing changes for local use: `GITHUB_TOKEN` keeps meaning the personal token. The gate never uses it for issues, pushes or releases, and a test runs the gate with both tokens set to different values and checks which one each call uses. When an issue, push or release call fails, for example with 403 because the workflow lacks `issues: write`, the gate reports a finding that names the missing permission.

### Concurrency and Git

- **The nightly workflow has its own concurrency group,** so no other workflow can cancel a pending nightly run. The heartbeat and the digest only add comments to the tracking issue and never rewrite its body, so they can't conflict with the gate.
- **History:** `actions/checkout` fetches a single commit by default. The nightly workflow fetches the history back to `refs/velocirepo/checked`, to check the commits since then.

### Job summary

Every run writes a summary to `$GITHUB_STEP_SUMMARY`:
- records per source;
- the requests made and the quota left;
- restored records, kept values and held-back projects, with their findings;
- warnings.

Errors and warnings also become `::error::` and `::warning::` annotations, so they're visible on the run's page.

### Where it lives

- **Gate logic:** in velocirepo. `velocirepo gate` does every step of the flow, running the fetch and `check` in-process, so that it can overlay held-back data before the fetch. The workflow only runs `velocirepo gate`. The gate calls GitHub's REST API directly, as `sync-secrets` does, so the workflow doesn't depend on `gh`.
- **Workflow:** the action runs them, and `install-ci` generates the new workflows, so every user gets the gate, not just devrel-io.
- **Reporting or enforcing:** the gate only enforces when `[settings] gate = "enforce"`. Without that setting it reports what it would do and commits everything, as today. There's no command-line flag for it, so the config is the only switch. So a workflow generated for report-only mode keeps reporting when the binary is upgraded, and enforcing is a deliberate change to the config.
- **Opt-in for existing workflows:** existing workflows get the action's and the binary's changes automatically, since the action is referenced by a floating tag such as `v1` and installs the latest release. So the action's new steps are opt-in: its `mode` input defaults to `fetch`, which runs `velocirepo fetch` exactly as today, and only the workflows `install-ci` generates set `mode: gate`. A test renders the old workflow template against the new `action.yml`, to check that it still fetches, concatenates and commits as before.
- **Permissions:** the nightly workflow needs `contents: write` (commits, the checked ref and the archive releases), `issues: write`, and `actions: read` (pending archive artifacts). The heartbeat and digest workflows need `issues: write` and `actions: read`. They're granted in the workflow files. No repository setting needs to change.
- **Action modes:** besides `fetch` and `gate`, the action's `mode` input has `check-heartbeat` and `digest`, so every workflow installs and runs velocirepo the same way.
- **Manual runs:** the nightly workflow has the `release`, `discard` and `repair` inputs from the first version that generates the gate, so a user who switches to enforcing always has them.

## For other velocirepo users

Everything in this design ships with velocirepo, so it works for any project that uses it, not just devrel-io. What a user has to do:

| Measure | Setup |
|---|---|
| Raw archive | None. Capture is part of `fetch`, and the archive is stored as release assets in the user's own data repository. |
| Data gate, concatenation and tracking issue | Regenerate the workflows with `install-ci` once. Optionally, list owners in `velocirepo.toml` to be assigned to the tracking issue. |
| Reconciliation and other checks | None. They're part of `velocirepo check`. |
| Day ends per time zone, quota budget | None. `install-ci` schedules the nightly run after the latest time zone of the configured sources. Accounts without access to Plausible's Sites API can set the time zones in the config. |
| Live API tests, changelog monitoring | None. They run in velocirepo's own CI, and protect users through releases. |
| Heartbeat workflow | None; `install-ci` generates it. The external heartbeat is optional and needs a Healthchecks instance. |
| Weekly digest | None; `install-ci` generates it. |

Until the workflows are regenerated, they keep working as before, without the gate.

## Further measures

### 3. Reconciliation

Each source can provide reference totals that the API computes independently of the data velocirepo stores.

**Who makes the requests:** `velocirepo check`, after `fetch`. It reads the quota left per API from `fetch --results`, and keeps tracking it from the response headers of its own requests. Reference totals are compared and then discarded; they aren't archived.

Two levels:

- **Every night, all-time totals.** For GitHub, one GraphQL query returns a repository's numbers of issues, pull requests, merged pull requests, forks and stars, and many repositories fit in one query. Two repositories cost 1 point of the 5,000 an hour, so all 465 of devrel-io's take roughly ten requests. Compared with the stored events, this finds missing or duplicated history right away. It would have shown 8,172 positron issues on GitHub against none stored.
- **Every week, per-month totals.** The search API's counts of issues and pull requests opened, closed and merged in the last complete month find gaps that all-time totals don't, such as a missing week that happens to balance out. Older months are covered by the all-time totals. That's four searches per repository, about 1,860 for devrel-io.

**Quota:**
- **Shared quotas** (GitHub's REST and GraphQL limits of 5,000 an hour, Plausible's): reconciliation may use at most 10% per run, and gives way first when the quota runs low (see the quota budget in measure 4).
- **The search API** has its own limit of 30 requests a minute, and only reconciliation uses it: `fetch` makes no search requests. So its budget is stated separately, as at most 1,500 searches a night at no more than 25 a minute, leaving a margin below GitHub's 30 (270 until velocirepo 1.4.0, which devrel-io's 465 repositories had all but used up). The search API has no daily limit, so the budget only bounds the time a night searches: up to an hour, enough to check about 2,600 repositories a week; devrel-io's 1,860 weekly searches take about 11 minutes a night. A search that GitHub still refuses for its rate limit waits until the limit resets and tries again, as every GitHub request does.

| Source | Reference total | Verified |
|---|---|---|
| GitHub events, all time | `issues`, `pullRequests` and `pullRequests(states: MERGED)` total counts | yes |
| GitHub events, per month | search API counts of issues and PRs opened, closed and merged | yes, in the October repair |
| GitHub stars | `stargazerCount` against summed `daily_stars` | yes |
| GitHub forks | `forkCount` against fork events | yes; deleted forks make the stored count slightly higher |
| Plausible | aggregate query over a date range, without the day dimension | to verify |
| CRAN | cranlogs totals over a range | to verify |
| PyPI | pypistats recent totals (day, week, month) | to verify |
| Open VSX, YouTube, Homebrew | snapshot totals are the data itself | none |

Tolerances belong to each source: stars, for example, differ by the stars given since the end date and the unstars of old stars, and fork events by the forks that were deleted since.

Every source either provides reference totals or declares that it has none, with a reason. A test enforces that, like the test that keeps badge presets in line with the metrics sources emit, so a new source can't skip reconciliation by accident.

### 4. Fetch each day once it has ended

An earlier version of this design re-read every source's last days on each run. That's dropped: stored values don't change once their day is complete, and re-reading would cost API quota and rewrite concatenated files. In the October repair, re-fetched CRAN month ends were identical to the stored ones, and 206,477 of about 210,000 re-fetched Plausible records were. The rest were partial days and sampled values.

**Days end in different time zones.** Most sources count days in UTC, but not all:

| Source | Days end in | Yesterday is over at |
|---|---|---|
| CRAN, PyPI, GitHub events and traffic, Open VSX, YouTube | UTC | 00:00 UTC |
| GitHub star history | US Pacific time | 07:00 UTC in summer, 08:00 UTC in winter |
| Plausible | each site's own time zone, from the Sites API (`GET /api/v1/sites/{site}`) | depends on the site; for devrel-io's 15 sites, the latest is `plotnine.org` on `America/Los_Angeles` |

A fetch only requests a day once it has ended in the target's time zone, plus a margin of an hour. This replaces `settleDays` (`internal/fetch/fetch.go`), which delays Plausible by a whole day regardless of the time of the run, and the star history's 7-day re-read. When a site's time zone can't be read, for example because the account has no access to the Sites API, the fetch assumes UTC−12, the latest time zone there is, unless the time zone is set in the config (`[settings.timezones]`, mapping a Plausible site to a time zone such as `"plotnine.org" = "America/Los_Angeles"`). An explicit `--end-date` that hasn't ended yet is moved back to the latest day that has, with a warning.

**Resume points per target and series.** Today a job has one resume point for all its targets: the latest day stored for the source and project. That breaks as soon as targets end their days at different times: a run before about 08:00 UTC would move a project's resume point past a day that hadn't ended yet for one of its targets, and that day would never be fetched. That affects the star history, which ends its days in US Pacific time, and projects with several Plausible sites in different time zones.

So every target, and for GitHub the star history separately, gets its own resume point: a watermark of the day it was fetched through, in the existing per-target watermarks (`store.MarkFetched`, `_watermark.json`), with a series key added. A watermark rather than the latest stored record, because a quiet repository may not get a star for months, and days without stars aren't stored.

**A watermark only moves as far as the source has published.** CRAN publishes two days late: the nightly run of 2026-10-21 could only store CRAN through 10-19, because cranlogs' `last-day` endpoint reported 10-19. If the watermark moved to the end date anyway, 10-20 would never be fetched. So each source tells how far its data is published. That's asked once per source and run, not per target:

| Source | Published through |
|---|---|
| CRAN | cranlogs' `last-day` endpoint |
| PyPI | the latest day pypistats reports for a reference package (`pip`), one request per run, so it doesn't depend on how quiet the run's own packages are |
| everything else | the end date: the data is real time, or a snapshot |

A source that can't tell moves the watermark only to the latest day it returned, which is always safe: at worst, a day without data is requested again the next night.

**Upgrading.** No target has a watermark of this kind yet. There's no migration for that: `fetch` refuses to run while the data schema is older than the binary, and migrations never run on their own, so raising the schema version would stop every existing nightly workflow on the first night. Instead, a target without a watermark gets one from its stored data the first time it's fetched: the star history from its latest stored `daily_stars` day, the others from today's resume point for their source and project. A target without any stored data, such as one added to the config later, starts from the beginning of its history. The watermark file only gains a field, so the schema version stays the same, and a test runs the old workflow template against the new binary on today's data.

**The nightly schedule.** `install-ci` schedules the nightly run after the latest time zone among the configured sources, plus the margin: 09:30 UTC for devrel-io, which covers US Pacific time in winter. At that time every real-time source's yesterday has ended, so the nightly run fetches them all through the same day. devrel-io's schedule moves from 06:00 to 09:30 UTC, so its data arrives three and a half hours later than now.

**Accepted drift.** GitHub lowers a past day of the star history when a star given that day is removed. Those corrections aren't fetched, so summed `daily_stars` slowly drifts above `total_stars`, by a few stars a year for a large repository. `total_stars` stays exact, and `reconcile.stars` allows for the drift.

**Quota.** The fetch reads rate-limit headers and keeps a budget per API. When a run would exceed it, reconciliation (measure 3) gives way first, and the run reports a `quota.low` warning.

**GitHub traffic.** GitHub's traffic API only keeps the last 14 days of views and clones. A missed day is fetched by the resume point as long as it's within those 14 days, and lost for good after that. That's why its freshness limit is the strictest, one day, and why its responses are archived.

### 5. Live API tests and upstream changes

- **Live tests:** a scheduled workflow in the velocirepo repository fetches known targets from every API each day. It asserts the response shape, the types, plausible ranges and known limits (the 300-page comment cap, the 30-week star history pages, the Sites API). Sources without a public target or a secret in velocirepo's repository are skipped with the reason, never counted as passing.
- **Deprecation headers:** the capture records `Deprecation` and `Sunset` headers for every request, archived or not, and `check` reports them as warnings.
- **Changelogs:** a configuration in the velocirepo repository fetches the GitHub and Plausible changelogs with `fetch-rss`, and flags entries that mention an endpoint velocirepo uses. GitHub announced both the stargazer restriction and the star history endpoint in its changelog.

### 6. Heartbeat

The nightly job can't report that it didn't run at all, for example because GitHub disabled the schedule or a token expired. Two levels:

- **Heartbeat workflow (default).** `install-ci` also generates a second scheduled workflow, `velocirepo-heartbeat.yml`, scheduled three hours after the nightly run (12:30 UTC for devrel-io). `velocirepo check-heartbeat` asks GitHub's API whether the nightly workflow has started a scheduled run (`event=schedule`) since its scheduled time that day, using the workflow token with `actions: read`. A run that's still in progress counts; manual runs for releases or repairs don't, so they can't hide a missed night. If there's none, it fails and comments on the tracking issue, or opens it. So a missed night is reported the same day. An open issue doesn't make it fail; it only adds the one escalation comment described under [Notifications](#notifications). It needs no setup. It does share GitHub Actions with the nightly job, so it can't report an Actions outage.
- **External heartbeat (optional).** For independence from GitHub, a self-hosted [Healthchecks](https://github.com/healthchecks/healthchecks) (BSD-3) instance on other infrastructure. The workflow pings it when it starts and when it finishes, with success or failure. Healthchecks alerts on a missing ping and escalates to ntfy or email. It's enabled by setting a ping URL as a secret.

### 7. Weekly digest

A scheduled workflow renders a view with one sparkline per source and project over the last 8 weeks, plus the week's findings, and posts it as a comment on a long-lived issue of its own, "Weekly digest", with the label `velocirepo-digest`. It never opens or closes the tracking issue. It's for the problems no check was written for. Three months of zero positron events would have stood out in the first week.

### 8. Testing the checks

Each incident in [Why](#why) becomes a test. It replays recorded or synthetic responses, or applies a recorded commit, and asserts that the gate reports it and keeps the data:

| Incident | Test |
|---|---|
| Hidden stargazers | Star sources return an empty list for a repository with stars, and `reconcile.stars` reports it. |
| Deleted history in a run | A job's files are deleted; `diff.records-removed` reports it, and the records are restored. |
| Deleted history in a commit made by hand | The gate checks a commit that removes a project's events, reports it, and keeps the finding open until the records are back. |
| CRAN month ends | The days were never fetched. Watermarks now prevent that: a test in 1a fails if a watermark moves past a day that wasn't fetched or published. Once 4c verifies CRAN's reference totals, `reconcile.cran` also reports a missing day. |
| Plausible pages | Pages overlap; `data.integrity` reports the duplicates and holds the new records back. |
| Replaced daily files | A write drops records; `diff.records-removed` reports it, and the records are restored. |
| Failed nights | A job fails; `fetch.failed` reports it, and the tracking issue is opened. |

Each check also gets a mutation test: the check is disabled, and its incident test must fail. That's done in plain Go tests, without a mutation tool.

#### Reconciliation tests

Reconciliation is the check that catches what nobody anticipated, so it gets its own tests. They use httptest servers with canned reference totals, like the source tests, and stored data written with the store functions.

| What | Test |
|---|---|
| Reading reference totals | Each source's reference totals are parsed from canned responses: GitHub's batched counts, including a batch with a repository that no longer exists, and the totals of Plausible, CRAN and PyPI where 4c verifies them. |
| Matching totals | Stored data that matches the reference totals reports nothing. |
| Within tolerance | Differences up to the source's tolerance report nothing: new items since the end date, deleted forks, removed stars. |
| Beyond tolerance, both ways | Missing data (fewer stored than the API reports) and duplicated data (more stored) are both reported, with both numbers in the message. |
| Missing history | A project with no stored events for a repository that has thousands is reported. That's the positron case. |
| No reference total | When the reference request fails, `reconcile.unavailable` is a warning, never a silent pass, and never a match. |
| A reference total that's suddenly zero | A repository that reported thousands of issues yesterday and 0 today is reported as a suspicious reference total, not as stored data that's now too high. |
| Per-month windows | Month boundaries are compared in the same time zone as the stored days, so items near midnight don't cause false differences. Stars use US Pacific time. |
| Weekly rotation | Over seven simulated nights, every repository's last complete month is checked exactly once, and no night exceeds the search API's budget. |
| Quota | When the budget is used up, the remaining checks are skipped with a `quota.low` warning that names them, and they're picked up on a later night. |
| Every source accounted for | Every registered source provides reference totals or declares that it has none. |

The live API tests (measure 5) also cover the reference endpoints, so a change in, for example, the shape of GitHub's counts is noticed as a broken reference, not as a mismatch in the data.

## Rollout

The [plan](2026-10-reliability-plan.md) splits this design into phases, each ending in a minor release that devrel-io adopts before the next phase builds on it: days fetched once they've ended, the archive and `check` first; then replay, the diff checks, concatenation and GitHub reconciliation; then the gate in report-only mode for two weeks; then the enforcing gate; and finally the digest, the external heartbeat and the documentation.

## Decisions

The questions this design left open, each with the default it uses, an alternative, and why the default was chosen.

### What's archived

- **Default:** only the responses of data that can't be re-fetched, in full for public targets, and only the read fields for LinkedIn and for targets that aren't public.
- **Alternative:** every response, in full.
- **Why the default:** for data that can be re-fetched, a re-fetch is the better repair: it also fixes bugs in how requests are built, which replay can't. Archiving only the rest keeps the archive small enough for release assets, avoids the artifact storage limits of private repositories, and keeps full comment bodies and other unstored fields of private repositories out of a possibly public archive. Fields left out of LinkedIn's archive are lost for good, since its statistics can't be re-fetched. That's accepted, because an accidentally published draft or non-public post can't be taken back.

### How data is held back

- **Default:** a nightly run never removes or changes stored data. New records that fail a check are held back per source and project, with their watermarks and content changes, in `velocirepo/held-back/` on `main`, outside the data, until they pass, are released or are discarded.
- **Alternative:** hold back all of a project's changes, removals included, on a separate branch with a commit per night.
- **Why the default:** most incidents removed or changed data, and undoing those is always right for a nightly run, so there's nothing to decide about them. That leaves only additions to hold back, and additions can be kept as plain record sets and merged through the store, without branches, rebases, or rules for which data can be re-fetched. A separate branch also turned out to be fragile: daily files mix data that can and can't be re-fetched, and rebasing per-night commits over them conflicts.

### Hold-back granularity

- **Default:** hold back per source and project. The rest of the night lands.
- **Alternative:** hold back the whole night when any project has a blocking finding.
- **Why the default:** with 465 projects, one broken repository or one false positive would otherwise stop all data, every night until it's resolved. That would also put pressure on accepting findings quickly instead of fixing them. Error findings that don't belong to a project still hold back everything.

### Commits made by hand

- **Default:** the nightly gate checks every commit since the last one it checked, so a bad commit is reported within a day.
- **Alternative:** a separate workflow that checks every push immediately.
- **Why the default:** a separate workflow needs its own concurrency rules, can have its pending runs cancelled by GitHub, and shares the tracking issue with the gate, which needs rules for who owns which finding. A day's delay is acceptable when the worst incident went unnoticed for three months, and the gate already has everything it needs to check those commits.

### Who gets notified

- **Default:** the owners in `[settings] owners`, assigned to the tracking issue. `install-ci` fills it in with the login of whoever runs it; teams can be mentioned.
- **Alternative:** only the repository's watchers, with nothing to configure.
- **Why the default:** whether watchers get notified depends on each person's settings, and the four failed nights showed that GitHub's default notifications can't be relied on. Assignment always notifies. Watchers still get notified too, so the default adds to the alternative rather than replacing it.

### Quota for reconciliation

- **Default:** at most 10% of each shared quota per run, and a separate budget of 270 searches a night for the search API. All-time totals every night, and per-month search counts spread over the week.
- **Alternative:** a separate token for reconciliation, or reconciliation only once a week.
- **Why the default:** the nightly totals cost almost nothing and catch most problems on the first night, as missing history did. The per-month counts are what's expensive, and spreading them over the week keeps them within the search API's limit without delaying the fetch. A second token is more setup for every user, and weekly-only checks would let problems go unnoticed for up to a week.

### Held-back data without pull requests

- **Default:** held-back records stay in `velocirepo/held-back/`, the tracking issue links to them, and they're released by the next passing run or by a manual run.
- **Alternative:** a pull request with the held-back data, created with the personal access token.
- **Why the default:** creating pull requests with the workflow token needs a repository setting that's off by default and that not every user is allowed to change. A pull request created with a personal token appears as that person's, so it doesn't notify them, and it ties the workflow to one person's token.
