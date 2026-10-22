# Plan: deploy devrel-agent nightly with fresh data

Status: proposed, 2026-10-22. Builds on [the data dictionary plan](2026-10-data-dict.md); for the next agent session, or for Jeroen.

## In plain language

The devrel agent answers questions about the metrics that devrel-io collects every night. Today someone has to rebuild its database by hand and redeploy it to Posit Connect Cloud, so the agent's answers are as old as the last time someone did that.

After this plan, a scheduled job in the devrel-agent repository does it every morning:

1. At noon UTC, it takes the latest data from the devrel-io repository, which collects it at 09:30 UTC.
2. velocirepo turns that data into one database file and a data dictionary: the description of every table and column, and of what to watch out for, that the agent reads before it answers.
3. It checks that the dictionary is valid and that the data isn't old.
4. It deploys the app, with the new database, to Connect Cloud, where it replaces the version that was running.

If any step fails, nothing is deployed: the agent keeps running on yesterday's data, and GitHub emails whoever is set up to get workflow failures. Anyone can also start the job by hand, to deploy right away or to deploy an older version of the data.

The OpenAI key the agent needs stays in Connect Cloud's settings and never passes through GitHub. The job needs only one new secret: a Connect Cloud login for automated deploys.

## How it works

```
09:30 UTC  devrel-io's nightly workflow fetches the metrics and pushes them (7 to 13 minutes)
12:00 UTC  devrel-agent's deploy workflow:
           ├─ check out devrel-agent (the app)
           ├─ check out devrel-io: the latest commit only, velocirepo.toml and velocirepo/data only
           ├─ velocirepo export data --data-dict   → data/velocirepo.duckdb, data/data-dict.yaml
           ├─ data-dict validate-spec data         → stop on errors
           ├─ Rscript deploy.R
           │    ├─ stop if meta.as_of is older than 3 days
           │    ├─ prewarm commons' context index   → commons-cache/
           │    ├─ log in with the Connect Cloud client credentials
           │    └─ rsconnect::deployApp(appId = ...) → replaces the running version
           └─ on failure: the previous version keeps running; GitHub notifies
```

Decisions, with what was considered:

- **Redeploy the app with its data**, rather than have the app download the data at startup or read it from pins. A deploy is one snapshot of database, dictionary and context index, which belong together; a failed run changes nothing; the database is read from disk and shared by every session. Downloading at startup would leave running processes on old data and make cold starts download 450 MB; pins would read the tables into memory per session (`metrics_filled` alone has 10 million rows) and need a board and its credentials. Reconsider if nightly uploads of 450 MB become slow or flaky.
- **A schedule at noon UTC, two and a half hours after devrel-io's at 09:30 UTC**, rather than a `repository_dispatch` from devrel-io: no token that may trigger workflows in another repository, and devrel-io stays unaware of its consumers. The margin leaves room for a slow fetch, and for the gate when devrel-io adopts it ([its rollout](2026-10-devrel-io-rollout.md)). devrel-io's workflow runs at 06:00 UTC on 2026-10-22 and moves to 09:30 UTC; until it has, a noon deploy simply comes later than needed.
- **No OpenAI key in CI.** `prewarm()` builds a local search index and calls no model: it ran in 1.6 seconds with `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` unset (2026-10-22). The key was in `deploy.R` only because `deployApp(envVars = "OPENAI_API_KEY")` copies the deploying machine's value to Connect Cloud; it's set in the app's Content Settings instead.
- **Deploy every night**, also when the data didn't change: it's simpler than remembering what was deployed, and a deploy of the same data is harmless.

## Prerequisites

In this order. Steps marked **(go-ahead)** need Jeroen's explicit approval; everything outward-facing does.

1. **velocirepo 1.2.0.** Commit the `data-dict` branch, open a PR, merge, and release v1.2.0 (GitHub release and PyPI) **(go-ahead)**. devrel-io's nightly workflow installs the latest release, so it runs 1.2.0 from then on; 1.2.0 changes nothing about fetching.
2. **devrel-io's `[data_dict]` settings.** Commit the `data-dict` branch (worktree `/tmp/devrel-io-data-dict`, 153 lines added to `velocirepo.toml`), open a PR, merge **(go-ahead)**. This can go before step 1: velocirepo 1.1.0 ignores config keys it doesn't know. Only change devrel-io after that day's nightly run has finished.
3. **devrel-agent's `data-dict` branch** (two commits: velocirepo export instead of `etl/build-db.R`, the README's "Refresh the data"). Add `inst/devrel-io/` to `.gitignore`. Delete the hand-written `dictionaries/devrel.data-dict.yaml`, which is now unused and wrong (it says there are no stars) **(go-ahead)**. Open a PR, merge **(go-ahead)**.
4. **The OpenAI key in Connect Cloud.** In the app's Content Settings → Variables, add `OPENAI_API_KEY` **(Jeroen)**. Then deploy once from a laptop with the new `deploy.R` (without `envVars`), and check that the agent still answers: the docs don't say whether variables persist across republishes.
5. **Connect Cloud client credentials.** Create a credential at <https://login.posit.cloud/identity/credentials> (the secret is shown once) **(Jeroen)**, and add `CONNECT_CLOUD_CLIENT_ID` and `CONNECT_CLOUD_CLIENT_SECRET` as repository secrets of devrel-agent **(Jeroen)**. The account is `posit` (see <https://connect.posit.cloud/whoami>).
6. **The plan's bundle limit.** The bundle is about 445 MB: within the 1 GiB of the Free plan, but with little room to grow; paid plans allow 5 GiB. Check which plan the `posit` account is on.

## The changes

### `deploy.R`

Works the same locally and in CI. Draft:

```r
withr::local_options(rsconnect.python.enabled = FALSE)

source("agent.R")

# A stale database means devrel-io's nightly fetch stopped: don't deploy, so
# the failure gets noticed. The running version has the same data anyway.
as_of <- DBI::dbGetQuery(devrel_con, "SELECT as_of FROM meta")$as_of
if (as_of < Sys.Date() - 3) {
  cli::cli_abort("The data is stale: the latest metric date is {as_of}.")
}

# Builds the context search index in commons-cache/, which ships with the
# app; it calls no model, so no API key is needed.
build_devrel_agent(
  con = devrel_con,
  client = ellmer::chat_openai(model = "gpt-5.6-terra")
)$prewarm()

# In CI, log in with the client credentials of the repository secrets;
# locally, rsconnect uses the account it already has.
if (nzchar(Sys.getenv("CONNECT_CLOUD_CLIENT_ID"))) {
  rsconnect::connectCloudClientCredentials(
    clientId = Sys.getenv("CONNECT_CLOUD_CLIENT_ID"),
    clientSecret = Sys.getenv("CONNECT_CLOUD_CLIENT_SECRET"),
    account = "posit"
  )
}

# OPENAI_API_KEY is set in the app's Content Settings on Connect Cloud.
rsconnect::deployApp(
  appPrimaryDoc = "app.R",
  appFiles = c(
    "DESCRIPTION",
    "app.R",
    "agent.R",
    "agent-builder.R",
    "instructions.md",
    "_brand.yml",
    "data/velocirepo.duckdb",
    "data/data-dict.yaml",
    list.files(
      c("_assets", "_fonts", "commons-cache"),
      recursive = TRUE,
      full.names = TRUE
    )
  ),
  appId = "01a043bd-5dee-7f82-917c-531eec503648",
  account = "posit",
  server = "connect.posit.cloud",
  forceUpdate = TRUE
)
```

To check while implementing:

- `connectCloudClientCredentials()` is in rsconnect 1.10.0 and later, per its NEWS; its arguments are `clientId`, `clientSecret` and `account`, per Connect Cloud's docs. That the account it registers is found by `account = "posit", server = "connect.posit.cloud"` isn't documented: check `rsconnect::accounts()` after logging in, in a first manual run.
- `forceUpdate = TRUE` keeps `deployApp()` from asking to confirm the update; in a non-interactive session it shouldn't ask anyway.
- `withr` isn't in `DESCRIPTION`; CI installs it as an extra package.

### `.github/workflows/deploy.yml`

Draft:

```yaml
name: Deploy

on:
  schedule:
    # Two and a half hours after devrel-io's nightly fetch (09:30 UTC, 7 to 13 minutes).
    - cron: '0 12 * * *'
  workflow_dispatch:
    inputs:
      devrel-io-ref:
        description: The devrel-io commit, branch or tag to export
        default: main
      deploy:
        description: Deploy to Connect Cloud (otherwise only export and check)
        type: boolean
        default: true

concurrency:
  group: deploy
  cancel-in-progress: false

permissions:
  contents: read

jobs:
  deploy:
    runs-on: ubuntu-latest
    timeout-minutes: 60
    steps:
      - uses: actions/checkout@v5

      # The latest commit only (no history), and only what velocirepo reads.
      - uses: actions/checkout@v5
        with:
          repository: posit-dev/devrel-io
          ref: ${{ inputs.devrel-io-ref || 'main' }}
          path: inst/devrel-io
          sparse-checkout: |
            velocirepo.toml
            velocirepo/data
          sparse-checkout-cone-mode: false

      - uses: astral-sh/setup-uv@v6

      - name: Install velocirepo and data-dict
        run: |
          uv tool install velocirepo==1.2.0
          uv tool install data-dict-yaml==0.0.3

      - name: Export the data and its dictionary
        run: velocirepo --config inst/devrel-io/velocirepo.toml export data --data-dict

      # Errors stop the deploy; warnings, such as a todo for a new shared
      # target, are shown but don't.
      - name: Validate the dictionary
        run: |
          data-dict validate-spec data
          data-dict validate-spec data --json | jq -e '.status != "error"'

      - uses: r-lib/actions/setup-r@v2
        with:
          use-public-rspm: true

      - uses: r-lib/actions/setup-r-dependencies@v2
        with:
          extra-packages: any::rsconnect, any::withr

      - name: Deploy
        if: ${{ github.event_name == 'schedule' || inputs.deploy }}
        env:
          CONNECT_CLOUD_CLIENT_ID: ${{ secrets.CONNECT_CLOUD_CLIENT_ID }}
          CONNECT_CLOUD_CLIENT_SECRET: ${{ secrets.CONNECT_CLOUD_CLIENT_SECRET }}
        run: Rscript deploy.R
```

Notes:

- Every repository involved is public, so the checkouts and the R packages from GitHub (`posit-dev/commons/pkg-r`, `posit-dev/brand-yml/pkg-r`) need no token.
- Pinning `velocirepo==1.2.0` and `data-dict-yaml==0.0.3` keeps a release from changing the nightly deploy unannounced; bump them on purpose. velocirepo also installs with its `install.sh`.
- The freshness check is in `deploy.R`, where the database is open, so it holds for local deploys too.
- With `deploy: false`, the run exports and validates only: the way to test the workflow without touching the app.

## Rollout

1. Prerequisites 1 to 6.
2. Add `deploy.R` and the workflow on a branch of devrel-agent; run the workflow by hand on that branch with `deploy: false`, and check its log: the export's row counts, a dictionary with status `ok`, the R dependencies installed **(go-ahead to push the branch)**.
3. Run it by hand with `deploy: true`, and check the deployed app: it answers, and `SELECT * FROM meta` gives yesterday's date **(go-ahead)**.
4. Merge **(go-ahead)**. Watch the scheduled runs for a week: duration, bundle upload, and that the app keeps its `OPENAI_API_KEY`.

## Running it

- **Deploy now:** Actions → Deploy → Run workflow.
- **Go back to older data:** run the workflow with `devrel-io-ref` set to an earlier devrel-io commit.
- **A run failed:** the app runs the previous version. Read the failing step: a stale `meta.as_of` means devrel-io's nightly run failed (look there first); a dictionary error means devrel-io's `[data_dict]` settings or velocirepo; a deploy error means Connect Cloud or its credentials.
- **Rotate the credentials:** create a new credential, replace the two secrets, delete the old credential.
- **Change a caveat the agent should know:** edit `[data_dict]` in devrel-io's `velocirepo.toml`; the next nightly deploy carries it.

## Open questions

- Whether Connect Cloud keeps `OPENAI_API_KEY` across republishes from rsconnect (rollout step 4 checks).
- Which plan the `posit` account is on, and so how much room the bundle has (prerequisite 6).
- Whether the context index needs rebuilding every night: commons caches it once per version of the context documents, and the dictionary changes daily (its examples, ranges and coverage). It takes seconds, so the plan rebuilds it every time.
- Who gets the failure emails: GitHub sends them to whoever last changed the schedule, by default; a team may want a Slack notification instead.
