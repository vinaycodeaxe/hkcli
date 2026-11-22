# AGENTS.md

Notes for people and coding agents working on this repository.

## Layout

- `src/hikecli/`: the CLI. `cli.py` (commands), `sync.py` (catch-up and live loop),
  `api.py` (REST + token refresh), `mqtt.py` (minimal MQTT over WebSocket),
  `protocol.py` (Hike packet builders/parsers), `db.py` (SQLite + FTS5),
  `auth.py` (QR/OTP login), `media.py`, `store.py`, `phone.py`, `config.py`.
- `tests/`: `fakehike.py` fakes Hike's REST API, QR socket and relay;
  `test_cli_e2e.py` drives every command against it; `test_units.py` covers codecs and storage.
- `docs/`: website pages (Markdown + small frontmatter). `scripts/build_site.py` builds them into `dist/site`.
- `skills/hikecli/SKILL.md`: the OpenClaw skill published to ClawHub.
- `.github/workflows/`: `ci.yml` (tests, docs, package), `pages.yml` (website), `release.yml` (PyPI, GitHub release, ClawHub).

## Commands

```bash
python -m pip install -e ".[test,docs]"
python -m pytest -q                     # all tests; never needs a real Hike account
python scripts/build_site.py            # build the website and check every link
python -m http.server -d dist/site 8000 # preview at http://localhost:8000/
```

## Rules

- Never use a real Hike account in tests. Extend `tests/fakehike.py` instead.
- Never commit a store: `session.json` holds login tokens and `hike.db` holds private messages.
- Keep Python 3.10 compatibility and the three runtime dependencies.
- Keep `--json` output, exit codes, flags and environment variables stable; they are used by scripts and agents.
- User-facing changes need a docs update in `docs/`, a `CHANGELOG.md` entry, and (if commands change) `skills/hikecli/SKILL.md`.
- Protocol facts come from the public Hike Web bundle; record anything new in `docs/how-it-works.md`.
- Releases: follow `docs/release.md` (bump `src/hikecli/__init__.py`, update the changelog, push a `vX.Y.Z` tag).
