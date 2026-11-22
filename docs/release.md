---
title: Release
description: "How the hikecli website, PyPI package, GitHub releases and ClawHub skill are published."
---

# Release

This page is for maintainers. Publishing is automated with GitHub Actions in
`.github/workflows/`:

| Workflow | Runs on | What it does |
| --- | --- | --- |
| `ci.yml` | Every push and pull request | Tests on Linux, macOS and Windows; builds the website and checks every link; builds the package |
| `pages.yml` | Pushes to `main` that change the docs | Builds the website and publishes it to GitHub Pages |
| `release.yml` | Tags like `v0.2.0` | Tests, builds, publishes to PyPI, creates the GitHub release and publishes the OpenClaw skill to ClawHub |

## One-time setup

1. **GitHub.** Create the public repository `vinaycodeaxe/hikecli` and push `main`.
2. **Website.** In the repository, open *Settings > Pages* and set *Source* to
   **GitHub Actions**. The next docs change (or a manual run of `pages.yml`) publishes
   https://vinaycodeaxe.github.io/hikecli/.
3. **PyPI.** Sign in at pypi.org, open *Your account > Publishing*, and add a
   **pending trusted publisher**: project `hikecli`, owner `vinaycodeaxe`, repository
   `hikecli`, workflow `release.yml`, environment `pypi`. No API token is needed.
4. **ClawHub.** Install the CLI with `npm i -g clawhub`, run `clawhub login` (it signs in
   with GitHub), then print a token with `clawhub token`. In the repository's
   *Settings > Secrets and variables > Actions*, add the secret `CLAWHUB_TOKEN` with that
   token and the variable `CLAWHUB_PUBLISH` set to `true`. Without them, releases only do a
   dry run of the ClawHub step.

## Releasing a version

1. Set the version in `src/hikecli/__init__.py`, for example `0.2.0`.
2. In `CHANGELOG.md`, move the `[Unreleased]` notes under a new `## [0.2.0] - YYYY-MM-DD` heading.
3. Commit, tag and push:

```bash
git commit -am "Release 0.2.0"
git tag v0.2.0
git push origin main v0.2.0
```

The release workflow refuses a tag that does not match the version in the code, and copies
the changelog section into the GitHub release notes. ClawHub numbers skill versions itself
(1.0.0, then the next patch version for each change) and skips the upload when the skill
has not changed.

## Publishing the skill by hand

```bash
clawhub skill publish ./skills/hikecli --slug hikecli --name hikecli
```

Skills on ClawHub are distributed under MIT-0, whatever license the repository uses.

## Website

```bash
python -m pip install "markdown>=3.5"
python scripts/build_site.py              # writes dist/site and checks every link
python -m http.server -d dist/site 8000   # preview at http://localhost:8000/
```

The pages are the Markdown files in `docs/`; the sidebar order is set in
`scripts/build_site.py`. To use your own domain later, put the domain (for example
`hikecli.dev`) in a file named `docs/CNAME`, point the domain's DNS at GitHub Pages, and add
the domain under *Settings > Pages*.
