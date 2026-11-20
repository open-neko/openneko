#!/usr/bin/env bash
# Usage: scripts/write-brew-cask.sh <tag> <checksums-native.txt> [url-base]
# Prints the Homebrew cask that installs the native archive (bin/openneko and share/openneko),
# so that `openneko start --native` finds the web app beside the executable.
set -euo pipefail

tag="${1:?release tag, for example v6.0.0}"
sums="${2:?checksums-native.txt}"
base="${3:-https://github.com/open-neko/openneko/releases/download}"
version="${tag#v}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "write-brew-cask: $tag is not a stable release tag" >&2; exit 1; }

sha() {
  local name="openneko-native_${version}_$1.tar.gz" sum
  sum="$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1 }' "$sums")"
  [[ "$sum" =~ ^[0-9a-f]{64}$ ]] || { echo "write-brew-cask: $sums holds no checksum for $name" >&2; exit 1; }
  printf '%s' "$sum"
}

mac_arm="$(sha darwin_arm64)"
mac_intel="$(sha darwin_amd64)"
linux_arm="$(sha linux_arm64)"
linux_intel="$(sha linux_amd64)"

cat <<EOF
cask "openneko" do
  version "$version"

  on_macos do
    on_arm do
      sha256 "$mac_arm"
      url "$base/v#{version}/openneko-native_#{version}_darwin_arm64.tar.gz"
    end
    on_intel do
      sha256 "$mac_intel"
      url "$base/v#{version}/openneko-native_#{version}_darwin_amd64.tar.gz"
    end
  end
  on_linux do
    on_arm do
      sha256 "$linux_arm"
      url "$base/v#{version}/openneko-native_#{version}_linux_arm64.tar.gz"
    end
    on_intel do
      sha256 "$linux_intel"
      url "$base/v#{version}/openneko-native_#{version}_linux_amd64.tar.gz"
    end
  end

  name "OpenNeko"
  desc "Operator CLI and runtime for OpenNeko"
  homepage "https://openneko.app"

  livecheck do
    skip "Written by the OpenNeko release workflow."
  end

  binary "bin/openneko"

  postflight do
    if OS.mac?
      system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", staged_path.to_s]
    end
  end

  caveats <<~EOS
    Run OpenNeko with Docker: \`openneko setup\`.
    Run it without Docker, against your PostgreSQL database:
      openneko start --native -d --database-url 'postgres://USER:PASSWORD@HOST:5432/DATABASE'
  EOS
end
EOF
