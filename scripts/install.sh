#!/bin/sh
# deslop-hook installer (macOS, Linux).
#
#   curl -fsSL https://github.com/productmind-code/deslop-hook/releases/latest/download/install.sh | sh
#
# Environment:
#   DESLOP_HOOK_VERSION      install this version (e.g. 0.1.0) instead of the latest
#   DESLOP_HOOK_INSTALL_DIR  install here instead of /usr/local/bin or ~/.local/bin
#
# Other ways to install: brew install productmind-code/tap/deslop-hook,
# npm/bun add -D deslop-hook, go install github.com/productmind-code/deslop-hook/cmd/deslop-hook@latest.
# On Windows use install.ps1.
#
# POSIX sh on purpose: this runs on whatever /bin/sh the machine has.
set -eu

REPO="productmind-code/deslop-hook"
BASE_URL="https://github.com/$REPO/releases"

TMPDIR_INSTALL=""
INSTALL_DIR=""
cleanup() {
  [ -n "$TMPDIR_INSTALL" ] && rm -rf "$TMPDIR_INSTALL"
  [ -n "$INSTALL_DIR" ] && rm -f "$INSTALL_DIR/.deslop-hook.new"
  return 0
}
trap cleanup EXIT INT TERM

die() { printf 'deslop-hook install: %s\n' "$1" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

need uname
need tar
need mkdir
command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 ||
  die "need either curl or wget"

# fetch_to <url> <dest>: fails on a non-2xx.
fetch_to() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$1"
  else
    wget -qO "$2" "$1"
  fi
}

# latest_version: follow the releases/latest redirect instead of calling the
# API, which allows only 60 unauthenticated requests an hour per IP.
latest_version() {
  if command -v curl >/dev/null 2>&1; then
    url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$BASE_URL/latest")
  else
    url=$(wget -S --spider "$BASE_URL/latest" 2>&1 | sed -n 's/^ *Location: *//p' | tail -n1)
  fi
  printf '%s' "${url##*/tag/v}"
}

# ---------------------------------------------------------------- platform ---

os=$(uname -s)
case "$os" in
  Darwin) OS=darwin ;;
  Linux) OS=linux ;;
  MINGW* | MSYS* | CYGWIN*)
    die "on Windows, run in PowerShell: irm $BASE_URL/latest/download/install.ps1 | iex" ;;
  *) die "unsupported operating system: $os" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) ARCH=amd64 ;;
  arm64 | aarch64) ARCH=arm64 ;;
  *) die "unsupported architecture: $arch (deslop-hook supports amd64 and arm64)" ;;
esac

# ----------------------------------------------------------------- version ---

VERSION="${DESLOP_HOOK_VERSION:-}"
VERSION="${VERSION#v}"
if [ -z "$VERSION" ]; then
  printf 'Resolving the latest release...\n'
  VERSION=$(latest_version) || die "could not reach $BASE_URL/latest"
fi
case "$VERSION" in
  "" | */*) die "could not work out the latest version (got '$VERSION'); set DESLOP_HOOK_VERSION" ;;
esac

ARCHIVE="deslop-hook_${VERSION}_${OS}_${ARCH}.tar.gz"
DIR_URL="$BASE_URL/download/v$VERSION"

# ---------------------------------------------------------------- download ---

TMPDIR_INSTALL=$(mktemp -d 2>/dev/null) || TMPDIR_INSTALL=$(mktemp -d -t deslop-hook) ||
  die "could not create a temporary directory"

printf 'Downloading deslop-hook %s (%s/%s)...\n' "$VERSION" "$OS" "$ARCH"
fetch_to "$DIR_URL/$ARCHIVE" "$TMPDIR_INSTALL/$ARCHIVE" ||
  die "download failed: $DIR_URL/$ARCHIVE"
fetch_to "$DIR_URL/checksums.txt" "$TMPDIR_INSTALL/checksums.txt" ||
  die "could not download checksums.txt"

# The checksum is mandatory: a curl | sh installer must not trust the
# transport alone.
if command -v sha256sum >/dev/null 2>&1; then
  sha_cmd="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  sha_cmd="shasum -a 256"
else
  die "no sha256 tool found (need sha256sum or shasum); refusing to install unverified"
fi
expected=$(grep " $ARCHIVE\$" "$TMPDIR_INSTALL/checksums.txt" | awk '{print $1}' | head -n1)
[ -n "$expected" ] || die "$ARCHIVE is not listed in checksums.txt"
actual=$(cd "$TMPDIR_INSTALL" && $sha_cmd "$ARCHIVE" | awk '{print $1}')
[ "$expected" = "$actual" ] ||
  die "checksum mismatch for $ARCHIVE (expected $expected, got $actual)"
printf 'Checksum verified.\n'

# Build provenance, when the GitHub CLI is available. The checksum above
# proves the download matches the release; this proves the release was built
# by this repository's release workflow.
if [ "${DESLOP_HOOK_SKIP_ATTESTATION:-0}" != "1" ] && command -v gh >/dev/null 2>&1 &&
  gh auth status >/dev/null 2>&1; then
  if gh attestation verify "$TMPDIR_INSTALL/$ARCHIVE" -R "$REPO" >/dev/null 2>&1; then
    printf 'Build provenance verified.\n'
  else
    die "build provenance verification FAILED for $ARCHIVE; refusing to install"
  fi
fi

tar -xzf "$TMPDIR_INSTALL/$ARCHIVE" -C "$TMPDIR_INSTALL" deslop-hook ||
  die "could not extract $ARCHIVE"

# ----------------------------------------------------------------- install ---

if [ -n "${DESLOP_HOOK_INSTALL_DIR:-}" ]; then
  INSTALL_DIR="$DESLOP_HOOK_INSTALL_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null; then
  INSTALL_DIR=/usr/local/bin
else
  INSTALL_DIR="$HOME/.local/bin"
fi
mkdir -p "$INSTALL_DIR" || die "could not create $INSTALL_DIR"

# Stage next to the destination, then rename: the swap is atomic.
cp "$TMPDIR_INSTALL/deslop-hook" "$INSTALL_DIR/.deslop-hook.new" ||
  die "could not write to $INSTALL_DIR (try sudo, or set DESLOP_HOOK_INSTALL_DIR)"
chmod 0755 "$INSTALL_DIR/.deslop-hook.new"
mv -f "$INSTALL_DIR/.deslop-hook.new" "$INSTALL_DIR/deslop-hook" ||
  die "could not install into $INSTALL_DIR"

printf '\ndeslop-hook %s installed to %s\n' "$VERSION" "$INSTALL_DIR"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) printf '\nWARNING: %s is not on your PATH. Add it:\n  export PATH="%s:$PATH"\n' \
       "$INSTALL_DIR" "$INSTALL_DIR" ;;
esac

printf '\nNext, in each repository: deslop-hook install\n'
