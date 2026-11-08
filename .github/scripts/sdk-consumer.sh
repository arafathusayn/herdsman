#!/usr/bin/env bash
# The external-consumer smoke test: a throwaway module outside the repository
# requires the SDK at one commit and builds, the way a third party would.
# Usage: sdk-consumer.sh <full commit id>
set -euo pipefail

commit=${1:-}
if [[ ! $commit =~ ^[0-9a-f]{40}$ ]]; then
  echo "sdk-consumer: need a full commit id, got '$commit'" >&2
  exit 2
fi

module=github.com/arafathusayn/herdsman/sdk

# Refuse, never clear: an inherited -mod=vendor would turn this independence
# test into a vendored build, so its arrival is the failure to report.
inherited=$(go env GOFLAGS)
if [[ -n $inherited ]]; then
  echo "sdk-consumer: GOFLAGS must be empty, got '$inherited'" >&2
  exit 1
fi

# GOSUMDB is off because a commit under test is not a release: looking it up
# would wait on, and permanently record it in, the public checksum database.
export GOWORK=off GOPROXY=direct GOSUMDB=off

work=$(mktemp -d)
trap 'chmod -R u+w "$work"; rm -rf "$work"' EXIT
cd "$work"

go mod init example.com/sdk-consumer
cat >main.go <<EOF
package main

import (
	"fmt"

	"$module/version"
)

func main() { fmt.Println(version.Version) }
EOF

go get "$module@$commit"
go build -o consumer .

resolved=$(go list -m -f '{{.Version}}' "$module")
if ! go mod download -json "$module@$resolved" | grep -q "\"Hash\": \"$commit\""; then
  echo "sdk-consumer: $resolved is not commit $commit" >&2
  exit 1
fi

printed=$(./consumer)
if [[ -z $printed ]]; then
  echo "sdk-consumer: the consumer printed no version" >&2
  exit 1
fi
echo "sdk consumer ok at $commit (sdk version $printed)"
