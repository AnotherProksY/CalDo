#!/bin/sh
# Installs the latest CalDo release into ~/.local/bin (or $CALDO_BIN_DIR).
#   curl -fsSL https://raw.githubusercontent.com/AnotherProksY/CalDo/main/install.sh | sh
set -eu

repo=AnotherProksY/CalDo
bin_dir=${CALDO_BIN_DIR:-$HOME/.local/bin}

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) echo "caldo: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) echo "caldo: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
url="https://github.com/$repo/releases/latest/download/caldo_${os}_${arch}.tar.gz"
echo "Downloading $url"
curl -fsSL "$url" -o "$tmp/caldo.tar.gz"
tar -xzf "$tmp/caldo.tar.gz" -C "$tmp" caldo
mkdir -p "$bin_dir"
install -m 0755 "$tmp/caldo" "$bin_dir/caldo"
echo "Installed $("$bin_dir/caldo" version) to $bin_dir/caldo"

case ":$PATH:" in
*":$bin_dir:"*) ;;
*) echo "Add $bin_dir to your PATH, then run: caldo" ;;
esac
