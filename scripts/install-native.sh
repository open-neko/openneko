#!/bin/sh
# Installs the native OpenNeko release on Linux, with no Docker: for one
# user, or with --server as two systemd services (needs root).
set -eu

fail() { echo "openneko install: $*" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || fail "the native release is for Linux only"
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "no native release for $(uname -m)" ;;
esac
server=
if [ "${1:-}" = --server ]; then
  server=1
  shift
  [ "$(id -u)" = 0 ] || fail "--server needs root: run it with sudo"
elif [ "$#" -gt 0 ]; then
  fail "unknown option $1; the options go after --server"
fi
command -v curl >/dev/null || fail "curl is needed"
command -v sha256sum >/dev/null || fail "sha256sum is needed"

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
  url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/open-neko/openneko/releases/latest)"
  tag="${url##*/}"
fi
case "$tag" in v*) ;; *) tag="v$tag" ;; esac
printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$' || fail "not a release tag: $tag"

[ ! -e "$prefix/bin/openneko" ] || fail "$prefix already holds OpenNeko; run \`openneko upgrade\`"

name="openneko-native_${tag#v}_linux_${arch}.tar.gz"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM
echo "Downloading OpenNeko $tag for linux/$arch..."
curl -fsSL -o "$tmp/checksums-native.txt" "$base/$tag/checksums-native.txt"
curl -fsSL -o "$tmp/$name" "$base/$tag/$name"
grep -E "^[0-9a-f]{64}  \*?$name\$" "$tmp/checksums-native.txt" | sed 's/  \*/  /' > "$tmp/want.txt"
[ -s "$tmp/want.txt" ] || fail "checksums-native.txt does not list $name"
(cd "$tmp" && sha256sum -c --quiet want.txt) || fail "checksum mismatch for $name"

mkdir -p "$prefix" "$bindir"
tar -xzf "$tmp/$name" -C "$prefix" --no-same-owner bin/openneko share/openneko/web
ln -sf "$prefix/bin/openneko" "$bindir/openneko"
"$bindir/openneko" version --short >/dev/null || fail "the installed executable does not run"

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
