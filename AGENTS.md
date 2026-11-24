# AGENTS.md

Notes for people and coding agents working on this repository.

## Layout

- `cmd/hkcli/`: the `main` package.
- `internal/`: the CLI, one package per concern: `cli` (commands and output), `syncer`
  (catch-up and live loop), `api` (REST and token refresh), `mqtt` (minimal MQTT over
  WebSocket), `protocol` (Hike packet builders and parsers), `db` (SQLite and FTS5), `auth`
  (QR and SMS login), `media`, `store`, `phone`, `config`, `version`.
- `tests/`: `fakehike.py` fakes Hike's REST API, QR socket and relay; `test_cli_e2e.py` runs
  the built binary against it for every command.
- `docs/`: website pages (Markdown with a small frontmatter). `scripts/build_site.py` builds them into `dist/site`.
- `skills/hkcli/SKILL.md`: the OpenClaw skill published to ClawHub.
- `openclaw-plugin/`: the OpenClaw Hike channel plugin (`@vinaycodeaxe/hkcli-channel`,
  TypeScript). It runs hkcli as child processes; `tests/openclaw_channel_e2e.py` drives it
  through a real OpenClaw Gateway against the fake Hike servers (in CI).
- `.github/workflows/`: `ci.yml` (tests, docs, release build), `pages.yml` (website),
  `release.yml` (GoReleaser release, then ClawHub), `clawhub.yml` (publishes the skill to
  ClawHub; also runs by hand), `plugin.yml` (plugin build, tests, packaged install, end-to-end,
  ClawHub dry run), `plugin-publish.yml` (publishes the plugin to ClawHub; run by hand),
  `plugin-verify.yml` (installs the published plugin from ClawHub; run by hand).
  `.goreleaser.yaml` defines the release builds.

## Commands

```bash
go test ./...                              # unit tests, offline
go vet ./... && gofmt -l .                 # gofmt must print nothing
go build -o dist/ ./cmd/hkcli              # writes dist/hkcli (dist/hkcli.exe on Windows)
python -m pip install -r requirements-dev.txt
python -m pytest -q tests                  # end-to-end tests against the fake server; set HKCLI_BIN to test another binary
python scripts/build_site.py               # build the website and check every link
python -m http.server -d dist/site 8000    # preview at http://localhost:8000/

cd openclaw-plugin                         # the channel plugin (Node 24)
npm ci && npm run typecheck && npm run build && npm run manifest:check && npm test
```

`npm run manifest:write` regenerates `openclaw.plugin.json` from `src/config-schema.ts`; CI
fails when they differ.

## Rules

- Never use a real Hike account in tests. Extend `tests/fakehike.py` instead.
- Never commit a store: `session.json` holds login tokens and `hike.db` holds private messages.
- Builds must stay pure Go (`CGO_ENABLED=0`) so releases and `go install` work everywhere.
- Keep `--json` output, exit codes, flags, environment variables and the store format stable;
  scripts, agents and older stores depend on them.
- User-facing changes need a docs update in `docs/`, a `CHANGELOG.md` entry, and (if commands
  change) `skills/hkcli/SKILL.md`.
- Protocol facts come from the public Hike Web bundle; record anything new in `docs/how-it-works.md`.
- Releases: follow `docs/release.md` (bump `internal/version/version.go`, update the changelog,
  push a `vX.Y.Z` tag).

## Gotchas

Hike accounts (test only against `tests/fakehike.py`):

- Logging an account out of the Hike app ends its web logins, hkcli's included. When a login
  suddenly stops working, check for that first before suspecting Hike or hkcli.
- The Hike app holds one number at a time. A second number (for example an agent's) logs in with
  an SMS code and stays out of the app.
- SMS codes are rate limited per number (`OTP send limit reached`); more attempts do not help.
- Keep one relay connection per account. A second web connection can make Hike stop delivering to
  the first one without closing it, so while `sync` runs, other commands publish through it
  (`internal/syncer/control.go`); never open another relay connection next to a running sync.
- hkcli identifies itself honestly (`User-Agent: hkcli/<version>`). Never disguise it to get
  around Hike's limits.

OpenClaw channel plugin:

- Read the Gateway's config per message with `core.config.current({ clone: true })`, never the
  `ctx.cfg` snapshot from `startAccount`: `bindings`, `agents`, `session` and `messages` change
  without restarting channels. Only `channels.hike` changes restart the channel
  (`reload.configPrefixes`).
- `groups` keys are Hike group ids, never names, and `requireMention` defaults to `true`.
- Gateway services often lack `~/go/bin` on their PATH; the docs tell users to set
  `channels.hike.cliPath`.
- OpenClaw warns `provenance-invalid` for community plugins installed from ClawHub;
  `plugin-verify.yml` fails only on errors.
