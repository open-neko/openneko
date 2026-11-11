#!/usr/bin/env bash
# Floors from the eMudhra intermediate CA compromise (October 2026). Go, Node.js
# and Python's certifi ignore browser revocation lists, so only these releases
# distrust the eight keys.
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
at_least() {
  if [ "$(printf '%s\n%s\n' "$3" "$2" | sort -V | head -n1)" != "$3" ]; then
    echo "$1 is $2; the eMudhra fix needs $3 or later" >&2
    fail=1
  fi
}
pinned() {
  if [ -z "$2" ]; then
    echo "$1 is not pinned to a version" >&2
    fail=1
    return 1
  fi
}

instructions() {
  sed -e ':a' -e '/\\$/N; s/\\\n//; ta' "$1"
}

dockerfiles=(Dockerfile recipes/*/Dockerfile)

for f in "${dockerfiles[@]}"; do
  while read -r tag; do
    at_least "$f node image" "${tag%%-*}" 24.22.1
  done < <(sed -n 's/^FROM node:\([0-9.]*\)-.*/\1/p' "$f")
  if grep -Eq '^FROM node:([0-9]+|[0-9]+\.[0-9]+)(-| |$)' "$f"; then
    echo "$f: a node image uses a floating tag; pin the patch release" >&2
    fail=1
  fi

  while read -r tag; do
    at_least "$f golang image" "$tag" 1.26.10
  done < <(sed -n 's/^FROM golang:\([0-9.]*\)-.*/\1/p' "$f")

  while read -r tag; do
    at_least "$f python image" "$tag" 3.13.17
  done < <(sed -n 's/^FROM python:\([0-9.]*\)-.*/\1/p' "$f")

  if grep -q '^FROM python:' "$f"; then
    pip=$(sed -n 's/.*pip install --no-cache-dir pip==\([0-9.]*\).*/\1/p' "$f" | head -n1)
    pinned "$f pip" "$pip" && at_least "$f pip" "$pip" 26.2.2
  fi

  while read -r run; do
    case "$run" in
      *"ge 20250419~deb12u2"*) ;;
      *) echo "$f: a Debian stage installs ca-certificates without the 20250419~deb12u2 check: ${run:0:100}" >&2; fail=1 ;;
    esac
  done < <(instructions "$f" | grep 'apt-get install.*ca-certificates' || true)

  while read -r line; do
    case "$line" in
      *--upgrade*ca-certificates-bundle*) ;;
      *) echo "$f: an Alpine stage does not upgrade ca-certificates-bundle: $line" >&2; fail=1 ;;
    esac
  done < <(instructions "$f" | grep 'apk add' || true)
done

for req in recipes/*/requirements.txt; do
  certifi=$(sed -n 's/^certifi==\([0-9.]*\).*/\1/p' "$req")
  pinned "$req certifi" "$certifi" && at_least "$req certifi" "$certifi" 2026.10.19
done

for mod in apps/*/go.mod recipes/*/go.mod; do
  version=$(sed -n 's/^go \([0-9.]*\)$/\1/p' "$mod")
  pinned "$mod go directive" "$version" && at_least "$mod go directive" "$version" 1.26.10
done

while read -r version; do
  at_least "workflow go-version" "$version" 1.26.10
done < <(grep -ho 'go-version: "[0-9.]*"' .github/workflows/*.yml | sed 's/.*"\(.*\)"/\1/')
while read -r version; do
  at_least "workflow node-version" "$version" 24.22.1
done < <(grep -ho 'node-version: [0-9.]*' .github/workflows/*.yml | sed 's/node-version: //')

exit "$fail"
