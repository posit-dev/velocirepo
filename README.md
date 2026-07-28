# velocirepo

Track your open-source project's pulse across package registries, GitHub, and the web.

## Overview

velocirepo collects metrics from multiple sources, stores them as JSONL files, and exposes them via SQL (powered by DuckDB). It's designed to run on a schedule (e.g., nightly via GitHub Actions) and commit the results to git, giving you a permanent record of your project's growth.

Supported sources:

| Source | What it tracks |
|--------|----------------|
| **GitHub Events** | Individual events (stars, forks, issues, PRs) with user and timestamp |
| **GitHub Traffic** | Daily page views and git clones (requires admin access) |
| **PyPI** | Daily download counts |
| **CRAN** | Daily download counts |
| **Homebrew** | Install counts (30-day, 90-day, 365-day, lifetime) |
| **Plausible** | Daily pageviews, visitors, visits |
| **OpenVSX** | Total downloads, reviews, ratings |
| **YouTube** | Views, likes, comments, subscribers (channel and per-video) |

## Highlights

- **Git-native storage** — JSONL files committed to your repo as a permanent, versioned history
- **SQL-powered queries** — DuckDB under the hood with built-in views for metrics, events, content, and indicators
- **Growth indicators** — automatic 28-day growth rate and trend computation
- **Dashboards & reports** — pluggable Views system supporting Quarto, Marimo, Jupyter, R, and ggsql
- **AI-ready** — built-in MCP server for conversational access from Claude and other assistants
- **Zero-config CI** — runs as a GitHub Action with one-command workflow generation
- **Badges** — auto-generated shields.io-style SVG badges from your live data
- **Export anywhere** — Parquet, CSV, DuckDB file for use with pandas, Polars, R, Observable, and more

## Quick start

```bash
# Install
brew install posit-dev/tap/velocirepo

# Initialize config (auto-detects sources)
velocirepo init

# Fetch metrics
velocirepo fetch

# Query with SQL
velocirepo query "SELECT project, SUM(value) AS stars FROM metrics WHERE metric = 'daily_stars' GROUP BY project ORDER BY stars DESC LIMIT 5"
```

## Documentation

Full documentation is available at **[posit-dev.github.io/velocirepo](https://posit-dev.github.io/velocirepo)**:

- [Installation](https://posit-dev.github.io/velocirepo/user-guide/installation.html) — GitHub Actions, Homebrew, uv, Go, and more
- [Configuration](https://posit-dev.github.io/velocirepo/user-guide/configuration.html) — set up `velocirepo.toml` and authentication
- [Data Storage](https://posit-dev.github.io/velocirepo/user-guide/data-storage.html) — how metrics, events, and content are stored
- [Querying Data](https://posit-dev.github.io/velocirepo/user-guide/querying.html) — SQL queries with examples
- [Exporting](https://posit-dev.github.io/velocirepo/user-guide/exporting.html) — Parquet/CSV export and tool integrations
- [Indicators](https://posit-dev.github.io/velocirepo/user-guide/indicators.html) — growth rate and trend computation
- [Badges](https://posit-dev.github.io/velocirepo/user-guide/badges.html) — generate SVG badges
- [Views](https://posit-dev.github.io/velocirepo/user-guide/views.html) — dashboards and reports
- [MCP Server](https://posit-dev.github.io/velocirepo/user-guide/mcp-server.html) — AI assistant integration
- [CLI Reference](https://posit-dev.github.io/velocirepo/reference/cli/) — all commands

## License

MIT
