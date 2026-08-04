# CLAUDE.md

This file guides Claude Code (claude.com/code) when working in this repository.
The canonical, tool-agnostic guidance lives in **AGENTS.md** and is imported
below so there is a single source of truth.

@AGENTS.md

## Claude-specific notes

- Before committing non-trivial changes, exercise the change end-to-end (run the
  affected `koc` command against a mock or the real cloud), not just tests.
- **Any new, renamed, or removed `koc` command means `docs/coverage.md` changes in
  the same commit** — counts, gap tiers, and the snapshot line. The rule and the
  re-derivation recipe live in AGENTS.md → "Coverage tracking"; treat it as part
  of the definition of done for a feature, not a follow-up.
- Prefer the dedicated file/search tools over shell `grep`/`find`.
- This repo is developed on a feature branch — never push to `main` without
  explicit permission. Commit and push only when asked.
