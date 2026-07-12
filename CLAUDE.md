# CLAUDE.md

This file guides Claude Code (claude.com/code) when working in this repository.
The canonical, tool-agnostic guidance lives in **AGENTS.md** and is imported
below so there is a single source of truth.

@AGENTS.md

## Claude-specific notes

- Before committing non-trivial changes, exercise the change end-to-end (run the
  affected `koc` command against a mock or the real cloud), not just tests.
- Prefer the dedicated file/search tools over shell `grep`/`find`.
- This repo is developed on a feature branch — never push to `main` without
  explicit permission. Commit and push only when asked.
