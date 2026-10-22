# Plan: a standalone database and a data dictionary from `velocirepo export`

Status: implemented on the branch `data-dict`, 2026-10-22 (steps 1 to 6 below), with steps 8 and 9 prepared on local `data-dict` branches of devrel-io and devrel-agent; nothing committed, pushed or released. Target release: v1.2.0. See [What data-dict does](#what-data-dict-does) and [Where the implementation deviates](#where-the-implementation-deviates) at the end.

## Goal

Anyone who uses velocirepo can run a [commons](https://github.com/posit-dev/commons) agent on their own data. velocirepo writes everything the agent needs except its instructions:

- a standalone DuckDB database, with plain tables and no paths to the machine that built it;
- a [data-dict](https://data-dict.tidyverse.org) YAML file that describes that database, generated from the schema, the source metadata, the config and the data.

Today devrel-agent does this by hand for devrel-io: `etl/build-db.R` materializes the database and `dictionaries/devrel.data-dict.yaml` (744 lines) is written by hand. After this plan, devrel-agent runs one velocirepo command instead. Its `instructions.md` is generic already and stays in the agent.

## Where things stand

Three different things, which are easy to confuse:

| | What it writes | Standalone? |
|---|---|---|
| `velocirepo build-db` | `data/velocirepo.duckdb`: views that `read_json()` the JSONL, a `projects` table | No. The views hold absolute paths of the machine that built it, and every query parses the JSONL again |
| `velocirepo export <dir>` | One parquet or CSV file per table: `metrics`, `events`, `content`, `indicators`, `projects` | Yes, but no DuckDB format, and no `metrics_filled` or `meta` |
| devrel-agent's `etl/build-db.R` | `data/devrel.duckdb`, materialized from the views of `velocirepo build-db` | Yes |

`etl/build-db.R` reads the view definitions from `velocirepo.duckdb`, rewrites the path prefix, and materializes `events`, `metrics`, `content`, `metrics_filled`, `indicators` and `projects` as tables. On the way it:

- extracts `page`, `video_id` and `ref` from `extra` in `metrics`, `metrics_filled` and `indicators`, and drops `extra` there;
- renames `events.user` to `username` and drops `events.extra`;
- casts `content.tags` and `content.extra` to text, and extracts `author` and `state`;
- adds a one-row `meta` table (`as_of`, `built_at`);
- refuses JSON-typed columns, because the database must be readable without DuckDB's json extension when deployed to Connect, and data-dict's expression grammar has no `->>`;
- refuses empty tables, and warns when `as_of` is more than 30 days old.

`build-db` itself stays as it is: it's the live view for local tools, rebuilt after every fetch.

The guide's `user_guide/exporting.qmd` says "The DuckDB file uses relative paths". That's wrong: `data/velocirepo.duckdb` in this repository has `read_json('/Users/jeroen/repos/posit-dev/velocirepo/data/...')`. Fix it as part of this plan.

## The command

```
velocirepo export <dir> [--format parquet|csv|duckdb] [--data-dict] [--source S] [--project P]
```

```
velocirepo export data/agent --data-dict
#   data/agent/velocirepo.duckdb
#   data/agent/data-dict.yaml
```

- `export` always materializes: every format holds plain data, never views.
- `--data-dict` writes `data-dict.yaml` into `<dir>`, describing exactly what was just written: the same snapshot, the same `--source`/`--project` filter. Examples, ranges and counts come from the exported data.
- The dictionary describes the duckdb format only: `--data-dict` implies `--format duckdb` and refuses `--format parquet` and `--format csv`. Tables have no `source` (data-dict only supports parquet there); commons gets a connection and doesn't need it. (Decided after implementing: the parquet path only served `data-dict validate-data`, and hit two data-dict bugs; see below.)
- There's no separate `data-dict` command. Changing a note in `velocirepo.toml` means exporting again. A separate command can come later, on the same code.

## The exported tables

Every format gets the same tables and columns:

| Table | Change |
|---|---|
| `metrics`, `metrics_filled`, `indicators` | `metrics_filled` is new in export. Keys from `extra` become columns (`page`, `video_id`, `ref`, ...) |
| `events` | Unchanged; `user` keeps its name (neither DuckDB nor data-dict reserves it) |
| `content` | `author`, `state` as columns; `tags` as a list, `extra` as text |
| `projects` | Unchanged |
| `targets` | New: `project`, `source`, `target`, `canonical`, one row per configured target, from the config |
| `meta` | New: one row, `as_of` and `built_at` |

- **Which keys come out of `extra`** is declared per source (see the metadata below), not hard-coded for devrel-io. Homebrew, LinkedIn and GitHub traffic declare their own keys.
- **`extra` stays, as text**, in every format. Current parquet users only gain columns, and the DuckDB file has no JSON-typed column.
- **Empty tables are fine.** An instance that tracks only CRAN packages gets typed, empty `events` and `content` tables. The dictionary leaves out what doesn't apply, such as YouTube metrics without YouTube targets.
- **`targets`** also covers projects that are configured but have no data yet, which `SELECT DISTINCT source FROM metrics` can't. `canonical` says which project keeps a target that more than one project tracks.
- **Checks** from `etl/build-db.R` become tested guarantees of `export`: no JSON-typed columns in any format, and a warning when `meta.as_of` is older than the source freshness limits allow. `export` reports row counts per table.

## Source metadata

`source.Meta` gets a `Metrics` field: for every metric a source writes, its name, a description, whether it's `daily` (summable) or `total` (a snapshot), and the `extra` keys of its rows. The registry test that already requires freshness, archive mode and reference totals of every source requires this too. Metric names are string literals across `internal/source/*.go` today; the test should fail on a written metric that isn't declared.

The same applies to event types and content types, so `events.type` and `content.type` get their values and descriptions.

## What the generated dictionary contains

Generated, for every instance:

- every table and column, with types, constraints, `examples` and `range` (required by the spec for `string`, `date`, `datetime` and `number(quantity)` columns);
- the values of `source`, `metric`, `events.type`, `content.type`, `content.state` and `indicator`, each with a description, only for configured sources;
- relationships: everything to `projects.id`, `targets`, per-video metrics to `content.id`, `events.ref` and `metrics.ref` to `content.ref`;
- the dataset description: sources, coverage per source (number of targets, first date), the `daily_*` versus `total_*` rule, which table answers what;
- the `deduplicated` filter of every table, from the `targets` table;
- the definitions that belong to a source and are emitted when it's configured: `site_traffic`, `page_traffic` and `traffic_metric` for Plausible, `cran_downloads` and `pypi_downloads`, `net_increase` on `metrics_filled`;
- generic glossary entries: "project", "source vs target", "daily_* vs total_*", and per configured source entries such as "downloads" or "reactions";
- `version: {date: <as_of>}`.

From `velocirepo.toml`: everything below.

## The config

### `[data_dict]`

```toml
[data_dict]
name = "Posit open source devrel metrics"
description = """
Daily adoption and engagement metrics for Posit's open source projects. \
This is public data about public projects."""
```

Prepended to the generated dataset description. `details` is accepted too.

### Project notes

```toml
[projects.tidyverse]
notes = """
The tidyverse meta-package and the tidyverse/tidyverse repo, NOT a rollup of \
all tidyverse packages. Downloads of the tidyverse as a whole means summing \
the individual packages."""
```

Each becomes a glossary entry named after the project id.

### Duplicates

```toml
[projects.shiny]
duplicate_of = "shiny-r"          # every target is also tracked by shiny-r

[data_dict.keep.github]
"posit-dev/orbital" = "orbital-r"

[data_dict.keep.plausible]
"shiny.posit.co" = "shiny-python"
```

`duplicate_of` marks a project whose targets are all copies; `[data_dict.keep.<source>]` names the project that keeps a shared target. They set `targets.canonical` and generate `deduplicated`. `project validate` and `export --data-dict` fail when a target that more than one project tracks has no keeper, or when a `duplicate_of` project has a target the other project doesn't track.

### Notes on tables

```toml
# Star events were dropped in the 2026-07-28 refetch; remove once refetched.
[[data_dict.notes]]
tables = ["metrics", "events"]
source = "github"
text = """
Every star event was dropped in a 2026-07-28 refetch, so there are no \
daily_stars rows. Say so rather than substituting forks or another metric."""
```

`tables` is required; `source` and `column` are optional. The text is appended to the matching `details`, grouped by source. A note about data that velocirepo can repair is deleted once it's repaired.

### Definitions and glossary

```toml
[[data_dict.definitions]]
table = "metrics"
name = "pre_seam"
label = "before the 2026-06-23 collection seam"
description = "Rows collected before the collection-system migration."
expr = "date <= '2026-06-22'"

[data_dict.glossary]
"package authorship" = """
Formal package authorship and maintainer roles are not collected. GitHub \
event usernames describe contribution activity, not authorship."""
```

A definition must not use the name of a column; one with the name of a generated definition replaces it.

## Documentation

- **New guide page `user_guide/data-dict.qmd`**, under "Working with Data" after `exporting.qmd`:
  - what data-dict is, with a link to <https://data-dict.tidyverse.org>, and how to install the CLI;
  - exporting the database and its dictionary with `velocirepo export --format duckdb --data-dict`, and what each table holds;
  - what's generated and what comes from `velocirepo.toml`, with the config keys above;
  - checking the result with `data-dict validate-spec`;
  - using it: a commons agent with the exported database and dictionary, and opening the database from DuckDB, R and Python.
- **`user_guide/exporting.qmd`**: the DuckDB format, the new tables, and the difference between `build-db` (live views, absolute paths, local use) and `export` (standalone copies). Remove the claim about relative paths.
- **`user_guide/configuration.qmd`**: the `[data_dict]` keys, `notes` and `duplicate_of`.
- `great-docs.yml`: add the page.

## Tests

- **Unit tests, offline as always:** source metadata completeness; `extra` key extraction; empty tables; no JSON-typed columns in any format; `targets.canonical` and the keeper validation; merging the config into the dictionary; `--source`/`--project` filters; golden files of the generated YAML for a fixture data directory.
- **Validation with the data-dict CLI:** a test runs `data-dict validate-spec` on the dictionary of the fixture. data-dict checks data only in parquet files, so the values are checked by Go tests against the database, which also check that the dictionary describes every table and column of it. The test is skipped when `data-dict` isn't on the path, so `go test ./...` still runs anywhere.
- **CI installs a pinned version** in `.github/workflows/ci.yml` (`uv tool install data-dict-yaml==0.0.3`, after `astral-sh/setup-uv`), so the test runs there. Warnings don't change the exit code, so the test reads the `status` of `--json` and fails on warnings too (see below).

## Order of work

1. `source.Meta.Metrics`, event and content types, with the registry test. Done.
2. The `targets` table, in `build-db` and `export`. Done.
3. `export`: `--format duckdb`, `metrics_filled`, `meta`, the `extra` columns, the checks. Done.
4. The config keys and their validation. Done.
5. `export --data-dict`, with golden tests and the data-dict CLI test in CI. Done.
6. The guide pages. Done.
7. Release v1.2.0.
8. devrel-io: move the notes from `devrel.data-dict.yaml` into `velocirepo.toml` (go-ahead needed to push). Prepared: only the notes that still hold for the data of 2026-10-20, checked against it (see below).
9. devrel-agent: replace `etl/build-db.R` and the hand-written YAML with `velocirepo export data --format duckdb --data-dict`, and compare its evals before and after. Prepared: `etl/build-db.R` calls velocirepo; 54 of the 55 eval targets compute the same on both databases, and the other differs only in the order of a tie. The evals themselves (LLM runs) haven't been run.

## What data-dict does

Checked with data-dict 0.0.3 (`uv tool install data-dict-yaml`), the latest release on 2026-10-22:

- **Exit codes.** `validate-spec`, `validate-meta` and `validate-data` exit 1 on errors and 0 otherwise, also with warnings, such as an unresolved `todo` (S31) or a column in the data that the dictionary doesn't describe (M03). `--json` gives `status` (`ok`, `warning` or `error`), `problems` and, for `validate-data`, the `steps` it checked. The tests read `status`, so warnings fail them too.
- **Keys and joins.** A composite `primary_key` works, and a relationship can join on several columns (`a.x = b.x AND a.y = b.y`). A `many-to-one` relationship needs a `primary_key` or `unique` on its right side (S06), so `events.ref` and `metrics.ref` can't be relationships to `content.ref`, which is unique only within a project, source and target and isn't the key; the table details say how to join them instead.
- **Empty columns.** A string column without values passes with `examples: []`; a numeric or temporal one with `range: [-.inf, .inf]`.
- **Examples** don't have to occur in the data, so long values are cut short.
- **Lists and dates in parquet.** `validate-meta` and `validate-data` read a parquet list column as `struct`, and a `DATE` column of a parquet file written by DuckDB as a number (M01): DuckDB marks dates with parquet's converted type, and data-dict only reads the logical type, which pyarrow writes. Since the dictionary describes the duckdb format only, neither affects velocirepo; both are worth reporting to data-dict. `tags` stays a list (`list(string)`), since devrel-agent's evals use `list_contains` on it.
- **The hand-written devrel-agent dictionary** doesn't pass `validate-spec`: `meta` has no `columns`, and once that's fixed, 39 errors remain (string columns without `examples`, numeric and temporal columns without `range`, datetime ranges without a time zone, `foreign_key` columns without a relationship, and the `many-to-one` joins on `content.ref`).

## Where the implementation deviates

- **`targets.canonical`, not `primary`**, which is a keyword in SQL.
- **Columns follow the data.** An `extra` key becomes a column only when the exported rows of the table have it, so the export never has a column that's always empty, and the indicators have no `ref`. `extra` stays, as text, with all the keys.
- **`tags` is a list** in `projects` and now also in `content` (JSON before), in every format. Empty project fields are NULL.
- **`[data_dict.values.<table>.<column>]`** describes enum values velocirepo can't, such as the content types an RSS feed sets with `vr:type`. Values nothing describes are labeled "undocumented", with a `todo` and a warning.
- **A shared target without a keeper** doesn't fail the export: velocirepo guesses the project with the most targets (the curated one, usually), warns, and the `deduplicated` definitions get a `todo`. `validate-projects` warns about them too. References to unknown projects, sources and tables in `[data_dict]` and `duplicate_of` fail when the config loads.
- **A definition of the config replaces a generated one** with the same name, such as devrel-io's `pageviews` over both collection eras.
- **Source notes** (`source.Meta.Notes`) go in the dataset's details, once, rather than in every table.
- **`--source` exports every table**, filtered to the source, where the old export wrote only the tables of the source's category (only `events` for `github`); `projects` holds the projects with a target of the source.
- **The MCP `export` tool** has `format: duckdb` and `data_dict` too.
- **`metrics.extra` makes the standalone database larger**: 442 MB for devrel-io, against 400 MB for devrel-agent's `devrel.duckdb`, with the same row counts.

## devrel-io's notes, checked

The hand-written dictionary was last updated for the data through 2026-10-07; the data repair of 2026-10-20 made several of its notes wrong. Against the export of 2026-10-20:

- **Stars are there**: 71,944 `daily_stars` rows and 472 `total_stars` rows, in devrel-agent's current database too. The dictionary says there is no star data and tells the agent to refuse star questions.
- **posit-dev/positron, quarto-dev/quarto-cli and rstudio/rstudio have their GitHub history** (62,656, 47,739 and 93,684 events). The dictionary says they lost it.
- **CRAN misses 17 days**, none since 2025-09-02: the month-end gaps the dictionary describes are repaired.
- **The Plausible collection eras overlap on 2026-06-22** for most sites: the old site-level rows run through 2026-06-22, and `daily_site_*` starts on 2026-06-22, not 2026-06-23. The old `site_traffic` filter counts that day twice; the one in devrel-io's config takes the old rows through 2026-06-21 only. devrel-agent's eval target `dplyr_docs` (June 2026 visitors of dplyr.tidyverse.org) sums both eras with the same overlap.
- Not carried over, as they can't be checked from the data or no longer hold: the July 2026 RSS and GitHub gaps (GitHub events since 2026-06-23 were fetched again), and the PyPI retention gaps.
