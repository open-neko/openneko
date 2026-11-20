#!/usr/bin/env bash
# Usage: scripts/build-native-release.sh <tag> <out-dir> [os/arch ...]
# Writes openneko-native_<version>_<os>_<arch>.tar.gz, or .zip with bin/openneko.exe for windows
# (bin/openneko, share/openneko/web, share/openneko/packs, share/openneko/skills and
# share/openneko/sandbox), and checksums-native.txt. A bare arch means linux/<arch>.
set -euo pipefail

tag="${1:?release tag, for example v5.1.0}"
out="${2:?output folder}"
shift 2
targets=("$@")
[ "${#targets[@]}" -gt 0 ] || targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)
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
rm -f "${out:?}/checksums-native.txt"
for target in "${targets[@]}"; do
  [[ "$target" == */* ]] || target="linux/$target"
  os="${target%/*}"
  arch="${target#*/}"
  case "$os" in
    linux | darwin) exe=openneko ext=tar.gz ;;
    windows) exe=openneko.exe ext=zip ;;
    *) echo "build-native-release: no native archive for $os" >&2; exit 1 ;;
  esac
  name="openneko-native_${version}_${os}_${arch}.${ext}"
  tree="$stage/${os}_${arch}"
  mkdir -p "$tree/bin" "$tree/share/openneko"
  (cd "$root/apps/openneko" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X $pkg.Version=$version -X $pkg.Commit=$commit -X $pkg.CommitTimestamp=$date" \
    -o "$tree/bin/$exe" ./cmd/openneko)
  cp -R "$OPENNEKO_WEB_DIST" "$tree/share/openneko/web"
  cp -R "$root/packs" "$tree/share/openneko/packs"
  mkdir -p "$tree/share/openneko/skills/sandbox"
  cp -R "$root/packages/llm/assets/builtin-skills" "$tree/share/openneko/skills/builtin"
  for skill in "$root"/recipes/sandbox/skills/*/SKILL.md; do
    cp -R "$(dirname "$skill")" "$tree/share/openneko/skills/sandbox/"
  done
  mkdir -p "$tree/share/openneko/sandbox"
  cp "$root/recipes/sandbox/compose.native.yml" "$tree/share/openneko/sandbox/"
  if [ "$ext" = zip ]; then
    python3 -I "$root/scripts/native-zip.py" "$tree" "$out/$name"
    listing="$(python3 -I "$root/scripts/native-zip.py" --list "$out/$name")"
  else
    tar -C "$tree" --owner=0 --group=0 --numeric-owner --sort=name -czf "$out/$name" bin share
    listing="$(tar -tzf "$out/$name")"
  fi
  for want in "bin/${exe/./\\.}" 'share/openneko/web/index\.html' 'share/openneko/packs/[^/]+/pack\.yaml' \
    'share/openneko/skills/builtin/[^/]+/SKILL\.md' \
    share/openneko/skills/sandbox/{docx,pdf,pptx,skill-creator,xlsx}/SKILL\\.md 'share/openneko/sandbox/compose\.native\.yml'; do
    if ! grep -E "^$want\$" <<<"$listing" >/dev/null; then
      echo "build-native-release: $name holds no $want" >&2
      rm -f "${out:?}/${name:?}"
      exit 1
    fi
  done
  (cd "$out" && sha256sum "$name" >> checksums-native.txt)
done
cat "$out/checksums-native.txt"
