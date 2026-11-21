#!/bin/sh
# Installs the native OpenNeko release on Linux or macOS, with no Docker: for
# one user, or on Linux with --server as two systemd services (needs root).
# --upgrade upgrades an install with the new release's own executable.
set -eu

fail() { echo "openneko install: $*" >&2; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "this script is for Linux and macOS; on Windows, run install-native.ps1" ;;
esac
machine="$(uname -m)"
if [ "$os" = darwin ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then
  machine=arm64
fi
case "$machine" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "no native release for $machine" ;;
esac
server=
upgrade=
if [ "${1:-}" = --server ]; then
  server=1
  shift
  [ "$os" = linux ] || fail "--server is for Linux only"
  [ "$(id -u)" = 0 ] || fail "--server needs root: run it with sudo"
elif [ "${1:-}" = --upgrade ]; then
  upgrade=1
  shift
  if [ -e /etc/openneko/server.json ]; then
    server=1
    [ "$(id -u)" = 0 ] || fail "this host has a server install, so --upgrade needs root: run it with sudo"
  fi
elif [ "$#" -gt 0 ]; then
  fail "unknown option $1; the options go after --server or --upgrade"
fi
command -v curl >/dev/null || fail "curl is needed"
if command -v sha256sum >/dev/null; then
  sha256() { sha256sum "$1"; }
elif command -v shasum >/dev/null; then
  sha256() { shasum -a 256 "$1"; }
else
  fail "sha256sum or shasum is needed"
fi

if [ -n "$server" ]; then
  prefix="${OPENNEKO_PREFIX:-/opt/openneko}"
  bindir="${OPENNEKO_BIN_DIR:-/usr/local/bin}"
else
  prefix="${OPENNEKO_PREFIX:-$HOME/.local/opt/openneko}"
  bindir="${OPENNEKO_BIN_DIR:-$HOME/.local/bin}"
fi
base="${OPENNEKO_RELEASE_URL:-https://github.com/open-neko/openneko/releases/download}"
tag="${OPENNEKO_VERSION:-}"
if [ -z "$tag" ]; then
  [ -z "${OPENNEKO_RELEASE_URL:-}" ] || fail "OPENNEKO_RELEASE_URL is set, so set OPENNEKO_VERSION too"
  url="$(curl --proto '=https' --proto-redir '=https' -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/open-neko/openneko/releases/latest)"
  tag="${url##*/}"
fi
case "$base" in
  https://*) proto='=https' ;;
  http://127.0.0.1:* | http://127.0.0.1/* | http://localhost:* | http://localhost/* | "http://[::1]:"* | "http://[::1]/"*) proto='=http' ;;
  *) fail "OPENNEKO_RELEASE_URL must be an https URL: $base" ;;
esac
case "$tag" in v*) ;; *) tag="v$tag" ;; esac
printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$' || fail "not a release tag: $tag"

if [ -n "$upgrade" ]; then
  [ -x "$prefix/bin/openneko" ] || fail "no OpenNeko in $prefix; set OPENNEKO_PREFIX to the install folder"
else
  [ ! -e "$prefix/bin/openneko" ] || fail "$prefix already holds OpenNeko; run \`openneko upgrade\` or this script with --upgrade"
fi

name="openneko-native_${tag#v}_${os}_${arch}.tar.gz"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM
echo "Downloading OpenNeko $tag for $os/$arch..."
curl --proto "$proto" --proto-redir '=https' -fsSL -o "$tmp/checksums-native.txt" "$base/$tag/checksums-native.txt"
curl --proto "$proto" --proto-redir '=https' -fsSL -o "$tmp/$name" "$base/$tag/$name"
want="$(grep -E "^[0-9a-f]{64}  \*?$name\$" "$tmp/checksums-native.txt" | cut -c1-64 || true)"
[ -n "$want" ] || fail "checksums-native.txt does not list $name"
[ "$(sha256 "$tmp/$name" | cut -c1-64)" = "$want" ] || fail "checksum mismatch for $name"

if [ -n "$upgrade" ]; then
  mkdir "$tmp/release"
  tar -xzf "$tmp/$name" -C "$tmp/release" --no-same-owner bin/openneko share/openneko
  chmod -R go-w,a+rX "$tmp/release"
  echo "Upgrading the OpenNeko in $prefix to $tag..."
  status=0
  "$tmp/release/bin/openneko" upgrade --from "$tmp/release" --exe "$prefix/bin/openneko" "$@" || status=$?
  exit "$status"
fi

mkdir -p "$prefix" "$bindir"
tar -xzf "$tmp/$name" -C "$prefix" --no-same-owner bin/openneko share/openneko
ln -sf "$prefix/bin/openneko" "$bindir/openneko"
"$bindir/openneko" version --short >/dev/null || fail "the installed executable does not run"
"$prefix/bin/openneko" release-check --skills --bundled-packs >/dev/null ||
  fail "the release in $prefix lacks its bundled skills or packs; remove $prefix and run this script again"

if [ -n "$server" ]; then
  chmod -R go-w "$prefix"
  chmod -R a+rX "$prefix"
  echo "OpenNeko $tag is in $prefix. Setting up the server install..."
  exec "$prefix/bin/openneko" server-install "$@"
fi
echo "OpenNeko $tag is in $prefix; $bindir/openneko runs it."
case ":$PATH:" in *":$bindir:"*) ;; *) echo "Add $bindir to your PATH." ;; esac
echo "Start it with your Postgres database:"
echo "  openneko start --native -d --database-url 'postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=verify-full'"
