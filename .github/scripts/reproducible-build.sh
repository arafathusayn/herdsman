#!/usr/bin/env bash
# Runs the real release build (.goreleaser.yaml, which holds the repository
# mode) twice, each time with an empty build cache, and fails when any release
# binary differs. Extra arguments go to goreleaser build.
# Usage: reproducible-build.sh [goreleaser build arguments]
set -euo pipefail

goreleaser=${GORELEASER:-goreleaser}

work=$(mktemp -d)
trap 'chmod -R u+w "$work"; rm -rf "$work"' EXIT

for run in first second; do
  GOCACHE="$work/cache-$run" "$goreleaser" build --snapshot --clean "$@"
  mkdir -p "$work/$run"
  while read -r binary; do
    mkdir -p "$work/$run/$(dirname "$binary")"
    cp "$binary" "$work/$run/$binary"
  done < <(find dist -type f -name herdsman)
  if [[ -z $(find "$work/$run" -type f) ]]; then
    echo "reproducible-build: no release binaries in dist" >&2
    exit 1
  fi
done

if ! diff -r "$work/first" "$work/second"; then
  echo "reproducible-build: the two builds differ" >&2
  exit 1
fi
echo "reproducible-build: identical"
