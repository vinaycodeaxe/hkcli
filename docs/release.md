---
title: Release
description: "How the hkcli website, release downloads and ClawHub skill are published."
---

# Release

This page is for maintainers. Publishing is automated with GitHub Actions in
`.github/workflows/` and [GoReleaser](https://goreleaser.com) (`.goreleaser.yaml`):

| Workflow | Runs on | What it does |
| --- | --- | --- |
| `ci.yml` | Every push and pull request | Go tests and end-to-end tests on Linux, macOS and Windows; checks formatting; builds the website and checks every link; builds every release target |
| `pages.yml` | Pushes to `main` that change the docs | Builds the website and publishes it to GitHub Pages |
| `release.yml` | Tags like `v0.2.0` | Tests, builds hkcli for 6 platforms, creates the GitHub release with downloads and checksums, and publishes the OpenClaw skill to ClawHub (through `clawhub.yml`) |
| `clawhub.yml` | Each release, or by hand | Publishes the OpenClaw skill in `skills/hkcli` to ClawHub |
| `plugin.yml` | Pushes and pull requests that touch the channel plugin | Builds and tests `openclaw-plugin/`, installs the packed plugin into OpenClaw, runs the end-to-end test against the fake Hike servers, and does a ClawHub dry run |
| `plugin-publish.yml` | By hand | Publishes the channel plugin `@vinaycodeaxe/hkcli-channel` to ClawHub |
| `plugin-verify.yml` | By hand, after a publish | Installs the published plugin from ClawHub into a clean OpenClaw and checks that it loads |

`go install github.com/vinaycodeaxe/hkcli/cmd/hkcli@latest` needs no publishing step: the Go
module proxy picks up each new tag from GitHub.

## One-time setup

1. **Website.** *Settings > Pages* must have *Source* set to **GitHub Actions** (already done).
2. **ClawHub.** The repository has the secret `CLAWHUB_TOKEN` (a ClawHub API token) and the
   variable `CLAWHUB_PUBLISH` set to `true`, so releases publish the skill. To replace the
   token, create a new one on [clawhub.ai](https://clawhub.ai) under *Settings > API tokens*
   and save it as the `CLAWHUB_TOKEN` secret in *Settings > Secrets and variables > Actions*.
   Set `CLAWHUB_PUBLISH` to `false` to make releases do only a dry run of the ClawHub step.

## Releasing a version

1. Set the version in `internal/version/version.go`, for example `0.3.0`.
2. In `CHANGELOG.md`, move the `[Unreleased]` notes under a new `## [0.3.0] - YYYY-MM-DD` heading.
3. Commit, tag and push:

```bash
git commit -am "Release 0.3.0"
git tag v0.3.0
git push origin main v0.3.0
```

The release workflow refuses a tag that does not match the version in the code, and uses the
changelog section as the release notes. Downloads are named `hkcli_<os>_<arch>.tar.gz` (`.zip`
on Windows), so links to `releases/latest/download/...` always point at the newest version.
ClawHub numbers skill versions itself: 1.0.0 first, then the next patch version for every
publish. The workflow always sends the skill's category and topics, so each release adds a
skill version even when `SKILL.md` did not change.

To try a release build locally, install GoReleaser and run `goreleaser release --snapshot --clean`;
the archives land in `dist/`.

## Publishing the skill by hand

To publish a change to `skills/hkcli` without a new hkcli release, open *Actions > clawhub >
Run workflow* on GitHub and run it on `main`. Leave *Preview only* ticked to see what would be
published, untick it to publish.

With the ClawHub CLI (`npm i -g clawhub`, then `clawhub login`) instead:

```bash
clawhub skill publish ./skills/hkcli --slug hkcli --name hkcli --categories communication --topics hike,messaging,chat,cli
```

Skills on ClawHub are distributed under MIT-0, whatever license the repository uses.

## Publishing the channel plugin

The OpenClaw channel plugin in `openclaw-plugin/` has its own version (in its `package.json`).
To release it:

1. Bump `version` in `openclaw-plugin/package.json` (and in `package-lock.json`), and update
   `openclaw.build.openclawVersion` if you built against a newer OpenClaw.
2. Push to `main` and wait for `plugin.yml` to pass.
3. Open *Actions > plugin-publish > Run workflow* on `main`, untick *Preview only* and add a
   short changelog.
4. When ClawHub has finished its checks, run *Actions > plugin-verify* to install the published
   plugin into a clean OpenClaw.

ClawHub checks every new version before it can be installed. The plugin is MIT licensed.

## Website

```bash
python -m pip install -r requirements-dev.txt
python scripts/build_site.py              # writes dist/site and checks every link
python -m http.server -d dist/site 8000   # preview at http://localhost:8000/
```

The pages are the Markdown files in `docs/`; the sidebar order is set in
`scripts/build_site.py`. To use your own domain later, put the domain (for example
`hkcli.dev`) in a file named `docs/CNAME`, point the domain's DNS at GitHub Pages, and add the
domain under *Settings > Pages*.
