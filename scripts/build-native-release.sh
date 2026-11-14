#!/usr/bin/env bash
# Usage: scripts/build-native-release.sh <tag> <out-dir> [arch ...]
# Writes openneko-native_<version>_linux_<arch>.tar.gz and checksums-native.txt.
set -euo pipefail

tag="${1:?release tag, for example v5.1.0}"
out="${2:?output folder}"
shift 2
arches=("$@")
[ "${#arches[@]}" -gt 0 ] || arches=(amd64 arm64)
version="${tag#v}"
root="$(cd "$(dirname "$0")/.." && pwd)"
commit="$(git -C "$root" rev-parse HEAD)"
date="$(git -C "$root" show -s --format=%cI HEAD)"
mkdir -p "$out"
out="$(cd "$out" && pwd)"

if [ -z "${OPENNEKO_WEB_DIST:-}" ]; then
  pnpm --dir "$root" --filter @neko/web build:spa
  OPENNEKO_WEB_DIST="$root/apps/web/dist"
fi
[ -f "$OPENNEKO_WEB_DIST/index.html" ] || { echo "no index.html in $OPENNEKO_WEB_DIST" >&2; exit 1; }

pkg=github.com/open-neko/neko/apps/openneko/internal/version
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
rm -f "$out/checksums-native.txt"
for arch in "${arches[@]}"; do
  name="openneko-native_${version}_linux_${arch}.tar.gz"
  tree="$stage/$arch"
  mkdir -p "$tree/bin" "$tree/share/openneko"
  (cd "$root/apps/openneko" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X $pkg.Version=$version -X $pkg.Commit=$commit -X $pkg.CommitTimestamp=$date" \
    -o "$tree/bin/openneko" ./cmd/openneko)
  cp -R "$OPENNEKO_WEB_DIST" "$tree/share/openneko/web"
  tar -C "$tree" --owner=0 --group=0 --numeric-owner --sort=name -czf "$out/$name" bin share
  (cd "$out" && sha256sum "$name" >> checksums-native.txt)
done
cat "$out/checksums-native.txt"
