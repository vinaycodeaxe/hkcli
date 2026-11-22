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
| `release.yml` | Tags like `v0.2.0` | Tests, builds hkcli for 6 platforms, creates the GitHub release with downloads and checksums, and publishes the OpenClaw skill to ClawHub |

`go install github.com/vinaycodeaxe/hkcli/cmd/hkcli@latest` needs no publishing step: the Go
module proxy picks up each new tag from GitHub.

## One-time setup

1. **Website.** *Settings > Pages* must have *Source* set to **GitHub Actions** (already done).
2. **ClawHub (optional).** Install the CLI with `npm i -g clawhub`, run `clawhub login` (it
   signs in with GitHub), then print a token with `clawhub token`. In the repository's
   *Settings > Secrets and variables > Actions*, add the secret `CLAWHUB_TOKEN` with that
   token and the variable `CLAWHUB_PUBLISH` set to `true`. Without them, releases only do a
   dry run of the ClawHub step.

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
ClawHub numbers skill versions itself (1.0.0, then the next patch version for each change) and
skips the upload when the skill has not changed.

To try a release build locally, install GoReleaser and run `goreleaser release --snapshot --clean`;
the archives land in `dist/`.

## Publishing the skill by hand

```bash
clawhub skill publish ./skills/hkcli --slug hkcli --name hkcli
```

Skills on ClawHub are distributed under MIT-0, whatever license the repository uses.

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
