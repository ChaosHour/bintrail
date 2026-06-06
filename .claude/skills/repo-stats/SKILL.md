---
name: repo-stats
description: Fetch and present bintrail distribution/adoption statistics — GitHub release download counts per version/platform/format with deltas since the last check, stars/forks/watchers, and repo traffic (clones/views) when a token is available. Use whenever the user asks for repository statistics in ANY phrasing — "estadísticas del repositorio", "cuántas descargas", "download stats", "how many downloads/installs", "adoption numbers", "how is the release doing", "stars" — even if they don't say "stats" explicitly.
---

Run the bundled script and present its output:

```bash
python3 .claude/skills/repo-stats/scripts/repo_stats.py
```

The script (stdlib-only, no dependencies) fetches from the GitHub API and prints a markdown report: repo metadata, total artifact downloads, and breakdowns by version / platform (os/arch) / format (tar.gz vs deb vs rpm). `--json` dumps the raw data instead; `--no-snapshot` skips persisting.

**Snapshots & deltas**: `download_count` is a cumulative counter — GitHub keeps no history. The script saves each run to `~/.local/state/bintrail-repo-stats/snapshots/` and shows `(+N)` deltas against the previous snapshot. The first run has no deltas; that's expected.

**Traffic** (clones/views, last 14 days) needs a token with push access — resolved from `GITHUB_TOKEN`/`GH_TOKEN` or `gh auth token`. If absent, the report says so and continues; don't treat it as an error.

When presenting, keep the interpretation honest:

- **Download ≠ install**: CI re-downloads, proxies and multi-version testing inflate counts. Trends and relative mix (arm64 vs amd64, deb vs tarball) are the signal, not absolutes.
- **GHCR pulls are invisible** (no public API) until Scarf Gateway is set up — see `drafts/telemetry-design.md` Phase 0. Mention this only if the user asks about Docker numbers.
- Releases published the same day will show zeros — the counter starts at asset publication.

If the user asks for something the script doesn't cover (issues/PR breakdowns, contributor stats), answer with the GitHub MCP tools on top of the script's output.
