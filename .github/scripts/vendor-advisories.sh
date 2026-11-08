#!/usr/bin/env bash
# Asks OSV about the version every VENDOR.md pins and fails when a pin has a
# known advisory, or when a pin cannot be read or checked: an unanswered query
# is not a clean result.
# Usage: vendor-advisories.sh <repository root>
set -euo pipefail

root=${1:-.}
api=${OSV_API:-https://api.osv.dev/v1/query}

# vendor/ repeats the VENDOR.md of a replaced module, so pins are deduplicated.
pins=$({ find "$root/third_party" "$root/vendor" -name VENDOR.md 2>/dev/null || true; } |
  while read -r file; do
    awk -v file="$file" '
      /^- Module: / { module = $3 }
      /^- Version: / { version = $3 }
      END {
        if (module == "" || version == "") { print "unreadable " file; exit }
        print module, version
      }' "$file"
  done | sort -u)

if [[ -z $pins ]]; then
  echo "vendor-advisories: no VENDOR.md under $root" >&2
  exit 2
fi

status=0
while read -r module version; do
  if [[ $module == unreadable ]]; then
    echo "vendor-advisories: no Module or Version in $version" >&2
    exit 2
  fi
  query=$(jq -cn --arg name "$module" --arg version "$version" \
    '{package: {name: $name, ecosystem: "Go"}, version: $version}')
  if ! answer=$(curl -fsS --max-time 60 -d "$query" "$api"); then
    echo "vendor-advisories: OSV did not answer for $module" >&2
    exit 2
  fi
  ids=$(jq -r '[.vulns[]?.id] | join(" ")' <<<"$answer")
  if [[ -n $ids ]]; then
    echo "$module $version: $ids"
    status=1
  else
    echo "$module $version: no advisories"
  fi
done <<<"$pins"
exit $status
