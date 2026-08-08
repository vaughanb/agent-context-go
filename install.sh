#!/bin/sh
# agent-context-go installer (macOS / Linux).
#
# Downloads the latest prebuilt binary and registers it into every detected AI
# coding agent.
#
#   curl -fsSL https://raw.githubusercontent.com/vaughanb/agent-context-go/main/install.sh | sh
#
# Environment overrides:
#   VERSION       release tag to install (default: latest)
#   INSTALL_DIR   where to place the binary (default: $HOME/.local/bin)
#   NO_CONFIGURE  set to any value to skip auto-configuring agents
set -eu

REPO="vaughanb/agent-context-go"
BIN="agent-context-go"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

err() { printf 'error: %s\n' "$1" >&2; exit 1; }
info() { printf '%s\n' "$1" >&2; }

# Detect OS.
os=$(uname -s)
case "$os" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) err "unsupported OS: $os (Windows: use install.ps1)" ;;
esac

# Detect architecture.
arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) err "unsupported architecture: $arch" ;;
esac

asset="${BIN}_${os}_${arch}"
version="${VERSION:-latest}"
if [ "$version" = "latest" ]; then
	base="https://github.com/$REPO/releases/latest/download"
else
	base="https://github.com/$REPO/releases/download/$version"
fi

# Pick a downloader.
if command -v curl >/dev/null 2>&1; then
	dl() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
	dl() { wget -qO "$2" "$1"; }
else
	err "need curl or wget to download"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

info "Downloading $asset ($version)…"
dl "$base/$asset" "$tmp/$BIN" || err "download failed: $base/$asset
The release may still be building, or no binary exists for $os/$arch yet.
Check https://github.com/$REPO/releases and try again in a few minutes."

# Verify checksum when the release publishes one.
if dl "$base/SHA256SUMS" "$tmp/SHA256SUMS" 2>/dev/null; then
	want=$(grep " $asset\$" "$tmp/SHA256SUMS" | awk '{print $1}')
	if [ -n "$want" ]; then
		if command -v sha256sum >/dev/null 2>&1; then
			got=$(sha256sum "$tmp/$BIN" | awk '{print $1}')
		else
			got=$(shasum -a 256 "$tmp/$BIN" | awk '{print $1}')
		fi
		[ "$want" = "$got" ] || err "checksum mismatch for $asset"
		info "Checksum verified."
	fi
fi

mkdir -p "$INSTALL_DIR"
chmod +x "$tmp/$BIN"
mv "$tmp/$BIN" "$INSTALL_DIR/$BIN"
info "Installed to $INSTALL_DIR/$BIN"

case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*) info "Note: $INSTALL_DIR is not on your PATH. Add it, e.g.:"
	   info "  echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.profile" ;;
esac

if [ -z "${NO_CONFIGURE:-}" ]; then
	info ""
	"$INSTALL_DIR/$BIN" install
else
	info "Skipping agent configuration (NO_CONFIGURE set). Run: $BIN install"
fi
