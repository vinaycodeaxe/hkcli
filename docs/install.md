---
title: Install
description: "Install hkcli from a release download or with go install. One file, nothing else needed."
---

# Install

hkcli is a single program file with no runtime to install. It runs on macOS, Linux and
Windows, on both Intel/AMD (`amd64`) and ARM (`arm64`) machines.

## Download a release

Download the archive for your system from the
[latest release](https://github.com/vinaycodeaxe/hkcli/releases/latest), unpack it, and put
`hkcli` in a folder on your `PATH`.

**macOS and Linux:**

```bash
os=$(uname -s | tr '[:upper:]' '[:lower:]')                 # darwin or linux
arch=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')  # amd64 or arm64
curl -fsSL "https://github.com/vinaycodeaxe/hkcli/releases/latest/download/hkcli_${os}_${arch}.tar.gz" | tar -xz hkcli
sudo mv hkcli /usr/local/bin/
```

**Windows (PowerShell):**

```powershell
$dir = "$env:LOCALAPPDATA\Programs\hkcli"
Invoke-WebRequest "https://github.com/vinaycodeaxe/hkcli/releases/latest/download/hkcli_windows_amd64.zip" -OutFile hkcli.zip
Expand-Archive hkcli.zip -DestinationPath $dir -Force
# Add the folder to your PATH once, then open a new terminal:
[Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$dir", "User")
```

Use `hkcli_windows_arm64.zip` on Windows on ARM. Every release has a `checksums.txt` file
with SHA-256 checksums of the downloads.

## go install

With Go installed:

```bash
go install github.com/vinaycodeaxe/hkcli/cmd/hkcli@latest
```

This builds hkcli and puts it in `$(go env GOPATH)/bin`, usually `~/go/bin`. It is also how
the [OpenClaw skill](openclaw.md) installs it.

## From source

```bash
git clone https://github.com/vinaycodeaxe/hkcli
cd hkcli
go build -o hkcli ./cmd/hkcli
```

## Check the install

```bash
hkcli --version
hkcli doctor
```

`doctor` fails its `login` check until you run [`hkcli auth`](auth.md); that is expected.

## Upgrade and uninstall

To upgrade, download the latest release again or rerun the `go install` command. To
uninstall, delete the `hkcli` file. Your data stays in `~/.hkcli`; to remove it too, run
`hkcli auth logout` first, then delete that folder. See [Store](store.md).
