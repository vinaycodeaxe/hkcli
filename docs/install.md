---
title: Install
description: "Install hikecli with uv, pipx or pip, or from source."
---

# Install

hikecli needs Python 3.10 or newer with SQLite FTS5, which python.org, Homebrew and most
Linux builds include. `hikecli doctor` checks it.

## uv (recommended)

```bash
uv tool install hikecli
```

[uv](https://docs.astral.sh/uv/) installs hikecli in its own environment and puts the
`hikecli` command on your `PATH` (usually `~/.local/bin`). This is also how the
[OpenClaw skill](openclaw.md) installs it.

## pipx

```bash
pipx install hikecli
```

## pip

```bash
python -m pip install hikecli
```

## From source

```bash
uv tool install git+https://github.com/vinaycodeaxe/hikecli
```

To work on the code:

```bash
git clone https://github.com/vinaycodeaxe/hikecli
cd hikecli
python -m pip install -e ".[test,docs]"
```

## Check the install

```bash
hikecli --version
hikecli doctor
```

`doctor` fails its `login` check until you run [`hikecli auth`](auth.md); that is expected.

## Upgrade and uninstall

```bash
uv tool upgrade hikecli
uv tool uninstall hikecli
```

Uninstalling keeps your data in `~/.hikecli`. To remove it too, run `hikecli auth logout`
first, then delete that folder. See [Store](store.md).
