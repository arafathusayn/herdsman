#!/usr/bin/env bash
# Runs every native fuzz target of the given modules for a fixed time each and
# fails when any target fails, or when no target was found at all (a broken
# search must not look like a clean night). New failing inputs are left under
# each package's testdata/fuzz/.
# Usage: fuzz-nightly.sh <time per target> <module directory>...
set -uo pipefail

fuzztime=${1:?usage: fuzz-nightly.sh <time per target> <module directory>...}
shift

start=$PWD
ran=0
failed=()

for module in "$@"; do
  cd "$start/$module" || exit 1
  while read -r dir; do
    while read -r target; do
      echo "== $module ${dir#./} $target"
      ran=$((ran + 1))
      if ! go test -run '^$' -fuzz "^$target\$" -fuzztime "$fuzztime" "$dir"; then
        failed+=("$module ${dir#./} $target")
      fi
    done < <(grep -hoE '^func Fuzz[A-Za-z0-9_]*\(' "$dir"/*_test.go | sed -E 's/^func //; s/\($//')
  done < <(find . -type f -name '*_test.go' -not -path './vendor/*' -not -path './third_party/*' -not -path '*/testdata/*' \
    -exec grep -lE '^func Fuzz' {} + | xargs -n1 dirname | sort -u)
done

if ((ran == 0)); then
  echo "fuzz-nightly: no fuzz target found" >&2
  exit 1
fi
if ((${#failed[@]} > 0)); then
  printf 'fuzz-nightly: failed: %s\n' "${failed[@]}" >&2
  exit 1
fi
echo "fuzz-nightly: $ran targets passed"
