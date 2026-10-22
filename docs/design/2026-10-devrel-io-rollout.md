# Handoff: rolling out the gate on devrel-io

For the next agent session. velocirepo v1.1.0 (released 2026-10-22, PR #6) contains everything in [the reliability plan](2026-10-reliability-plan.md); this is what's left to do on devrel-io (`../devrel-io`, `posit-dev/devrel-io`). Read the user guide chapter `user_guide/reliability.qmd` first.

## Ground rules

- **Ask Jeroen before anything outward-facing:** pushing to devrel-io, merging its PRs, starting or disabling workflows, creating issues or releases. Each step below says where his go-ahead is needed.
- **Only change devrel-io after that day's nightly run has finished.** Check with `gh run list --repo posit-dev/devrel-io --workflow velocirepo.yml --limit 1`. Changing the workflow while a run is pending or running gives odd states.
- **Never run `velocirepo gate` locally in a clone of devrel-io that has the real remote:** it commits and pushes. Rehearse in a clone of a local bare copy (`git clone --bare ../devrel-io /tmp/x/remote.git`), as was done on 2026-10-21.
- **Commits:** Jeroen Janssens is the only author; no co-author lines.
- Tokens are in `../devrel-io/.env` (`GITHUB_TOKEN`, `PLAUSIBLE_TOKEN`, `YOUTUBE_TOKEN`). Load them with `set -a; . ../devrel-io/.env; set +a`, and never print them. The `gh` CLI's own login isn't authorised for the `rstudio` organisation: its searches of `rstudio/*` fail with a 422. Use the `.env` token for anything about those repositories.

## Where things stand (2026-10-22)

- devrel-io's workflow `.github/workflows/velocirepo.yml` runs at 06:00 UTC, uses `posit-dev/velocirepo@main` with the default `version: latest`, and commits in a shell step. Since the release it already runs v1.1.0 in `fetch` mode:
  - days are fetched once they've ended in their time zone, with watermarks;
  - months are concatenated on or after the 8th.
  
  The gate, the archive, the tracking issue, the heartbeat and the digest aren't on yet.
- Secrets: `GH_TOKEN`, `PLAUSIBLE_TOKEN` and `YOUTUBE_TOKEN`. The workflow token's default permission is write. "Allow GitHub Actions to create and approve pull requests" is off, and nothing needs it.
- `velocirepo.toml` has no `[settings]` section.
- The local clone `../devrel-io` was at `bc6b5d81b` on 2026-10-22 and is behind the remote. The untracked `velocirepo/views/positron/` is Jeroen's; leave it alone.
- `velocirepo check` on 2026-10-21's data reported one known false positive: `data.integrity` `952675f07b21`, two bot comments in the same second in `events/github/positron/2026-10-02.jsonl`. Reconciliation reported nothing.

## Step 1: prepare (no go-ahead needed)

1. `cd ../devrel-io && git pull --ff-only`.
2. Make sure the `velocirepo` on the path is 1.1.0 (`velocirepo version`), or build it: `go build -o bin/velocirepo ./cmd/velocirepo` in the velocirepo repository.
3. With the `.env` tokens loaded, run `velocirepo check` in `../devrel-io`. It reconciles with GitHub, so it takes about a minute. Expect only `952675f07b21`. Anything else: report it to Jeroen before going on.

## Step 2: the change, on a branch (go-ahead needed to push and merge)

In `../devrel-io`, on a new branch `gate` (`git switch -c gate`):

1. **Accept the known false positive.** Create `velocirepo/checks.toml`:

   ```toml
   [[accept]]
   id = "952675f07b21"
   reason = "Two bot comments posted in the same second"
   ```

2. **Settings.** Add to the top of `velocirepo.toml`, before the first `[projects.…]`:

   ```toml
   [settings]
   owners = ["jeroenjanssens"]
   ```

   Leave `gate` unset, so the gate only reports for now.

3. **Workflows.** `install-ci` needs an interactive terminal, so either ask Jeroen to run `! velocirepo install-ci` himself, or copy the three golden files from velocirepo's `cmd/velocirepo/cmd/testdata/`. Their values are exactly devrel-io's: nightly `30 9 * * *`, heartbeat `30 12 * * *`, digest `30 13 * * 1`, action `@v1`, Plausible and YouTube tokens, workflow file `velocirepo.yml`.

   ```bash
   cp ../velocirepo/cmd/velocirepo/cmd/testdata/velocirepo.yml .github/workflows/velocirepo.yml
   cp ../velocirepo/cmd/velocirepo/cmd/testdata/velocirepo-heartbeat.yml .github/workflows/
   cp ../velocirepo/cmd/velocirepo/cmd/testdata/velocirepo-digest.yml .github/workflows/
   ```

   This:
   - moves the nightly run from 06:00 to 09:30 UTC, an hour after yesterday ends in US Pacific time (the star history, and plotnine.org's Plausible site);
   - pins the action to `@v1` instead of `@main`;
   - replaces the shell commit step with the gate.
   
   Keep `actions/checkout@v4` as the template has it, or match devrel-io's `@v6`; either works.

4. Check the workflows with `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7` in `../devrel-io`.
5. Commit, for example "Run the nightly fetch through velocirepo's gate", push the branch, and open a PR. **Ask Jeroen before pushing, and again before merging.** Merge only after that day's nightly run has finished.

## Step 3: the first gate run (go-ahead needed)

Right after the merge, with Jeroen's go-ahead, start the nightly workflow once by hand: `gh workflow run velocirepo.yml --repo posit-dev/devrel-io`. Then check:

- The run succeeds, or fails only because of a real finding.
- Its log says "First run: checking from … on".
- `main` has a commit "Fetch metrics on <date>" with a "Stored through:" body, and `velocirepo/gate-state.json`.
- `git ls-remote origin refs/velocirepo/checked` points at that commit.
- The job summary ends with "What enforcing would have done", listing nothing.
- An issue labelled `velocirepo-health` exists only if there are open findings.
- The archive: if anything was fetched, a pre-release `raw-<YYYY-MM>` with `<run-id>-<source>.jsonl.zst` and `<run-id>-manifest.json` assets (`gh release view raw-2026-10 --repo posit-dev/devrel-io`). On the day of the switch most sources are up to date, so the archive can be empty; then check after the next scheduled run.

Then run the heartbeat by hand: `gh workflow run velocirepo-heartbeat.yml`. It should pass, since the 06:00 scheduled run of that day counts. Manual runs never count.

## Step 4: acceptance checks of the plan (on later days)

1. **Archive and replay** (plan 2e), after the first scheduled gate run: pick its run ID from the release's manifest asset, then in a scratch clone of devrel-io (not the real working copy):

   ```bash
   velocirepo download-archive <run-id> --archive /tmp/raw
   velocirepo replay <run-id> --archive /tmp/raw
   git status   # expect no changes: the replay writes the same records
   ```

2. **A commit made by hand is reported** (plan 3a), optional and only with Jeroen's go-ahead. It's a real commit on `main`, so it's simpler to keep this as rehearsed locally on 2026-10-21.
3. **Heartbeat** (plan 3b), with Jeroen's go-ahead: on one day, after 06:00 and before 09:30 UTC, `gh workflow disable velocirepo.yml`. The heartbeat at 12:30 should fail and comment on, or open, the tracking issue. Then `gh workflow enable velocirepo.yml` the same day.
4. **The digest:** after its first Monday, an issue labelled `velocirepo-digest` has a comment with a sparkline per source and project. Projects that stopped are marked; on 2026-10-21 those were github/leaflet and github/ptd, which were just quiet.

## Step 5: two weeks of reporting (until about 2026-11-05)

Every day, look at the run's job summary and the tracking issue:

- **False positives:** add their IDs to `velocirepo/checks.toml`, with a reason (and an `expires` date where it fits), through a PR.
- **Real findings:** fix them, with a repair run where needed. Run the nightly workflow by hand with `repair: <source>/<project> <from>..<to>`; it commits "Repair …" with a trailer.
- **Thresholds that need tuning** (reconciliation tolerances, the volume check, freshness limits): change them in velocirepo, not in devrel-io.
- **Reconciliation:** each night checks a seventh of the repositories' last month with GitHub's search. A `reconcile.unavailable` for a repository can mean the token can't search it.

**The first concatenation** is on or after 2026-11-08: a separate commit "Concatenate October 2026". Check it by hand: `velocirepo check --base <commit>^ --head <commit> --no-reconcile` should report no `diff.*` errors, and the gate itself runs `concatenate.records-moved` before committing it.

## Step 6: enforce (go-ahead needed)

After two quiet weeks, add `gate = "enforce"` to `[settings]` through a PR. From then on:
- removed records are restored;
- changed values keep their stored value;
- new records with an error are held back in `velocirepo/held-back/`.

Check the first concatenation commit after the switch by hand, as above.

## If something goes wrong

- **Back to the old behaviour:** revert the workflow PR. The old shell-commit workflow works with v1.1.0, since the action's `mode` defaults to `fetch`.
- **Held-back data:** `release: <source>/<project>` lands it, `discard: <source>/<project>` deletes it so it's fetched again. If `gate = "enforce"` is removed, the next run lands everything that's held back.
- **A force-push to `main`** leaves `refs/velocirepo/checked` outside the history; the gate reports `gate.checked-ref` and starts over from `HEAD`.
