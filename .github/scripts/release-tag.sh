#!/usr/bin/env bash
# The release, in two steps, so that what is published is what was compared.
#
#   release-tag.sh build [goreleaser arguments]
#     Builds twice, each from an empty cache, without publishing. The second
#     build signs and writes the Homebrew cask. Fails when the checksums differ.
#
#   release-tag.sh publish <tag>
#     Uploads the files of that second build, unchanged, then pushes the cask.
#     It never builds; GoReleaser's own publishing would build a third time.
#
# RELEASE_STATE (default RUNNER_TEMP) holds the caches and the first checksums.
set -euo pipefail

usage="usage: release-tag.sh build [goreleaser arguments] | publish <tag>"
goreleaser=${GORELEASER:-goreleaser}
state=${RELEASE_STATE:-${RUNNER_TEMP:-}}
first_sums="${state:?set RELEASE_STATE or RUNNER_TEMP}/checksums-first.txt"
cask=dist/homebrew/Casks/herdsman.rb
tap=git@github.com:arafathusayn/homebrew-tap.git

build() {
  rm -f "$first_sums"
  GOCACHE="$state/cache-first" "$goreleaser" release --clean --skip=publish,sign,homebrew "$@"
  cp dist/checksums.txt "$state/checksums-candidate.txt"
  GOCACHE="$state/cache-second" "$goreleaser" release --clean --skip=publish "$@"
  if ! cmp "$state/checksums-candidate.txt" dist/checksums.txt; then
    echo "release-tag: the two builds differ" >&2
    exit 1
  fi
  mv "$state/checksums-candidate.txt" "$first_sums"
  echo "release-tag: the two builds are identical"
}

publish() {
  local tag=$1
  if [[ ! -f $first_sums ]]; then
    echo "release-tag: no compared build to publish" >&2
    exit 1
  fi
  cmp "$first_sums" dist/checksums.txt
  (cd dist && shasum -a 256 -c checksums.txt)
  for needed in dist/checksums.txt.sig "$cask"; do
    if [[ ! -f $needed ]]; then
      echo "release-tag: $needed is missing" >&2
      exit 1
    fi
  done

  local assets=() name notes=(--generate-notes) pre=()
  while read -r _ name; do
    assets+=("dist/$name")
  done <dist/checksums.txt
  assets+=(dist/checksums.txt dist/checksums.txt.sig)
  [[ -f dist/CHANGELOG.md ]] && notes=(--notes-file dist/CHANGELOG.md)
  [[ $tag == *-* ]] && pre=(--prerelease)
  gh release create "$tag" "${assets[@]}" --verify-tag --title "$tag" "${notes[@]}" ${pre[@]+"${pre[@]}"}

  key="$state/tap-key"
  local clone="$state/tap"
  (umask 077 && printf '%s\n' "${HOMEBREW_TAP_SSH_KEY:?HOMEBREW_TAP_SSH_KEY is not set}" >"$key")
  trap 'rm -f "$key"' EXIT
  export GIT_SSH_COMMAND="ssh -i $key -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -F /dev/null"
  git clone --depth 1 --branch main "$tap" "$clone"
  mkdir -p "$clone/Casks"
  cp "$cask" "$clone/Casks/herdsman.rb"
  git -C "$clone" add Casks/herdsman.rb
  git -C "$clone" -c user.name=herdsman-release -c user.email=release@users.noreply.github.com \
    commit -m "herdsman $tag"
  git -C "$clone" push origin main
}

case ${1:-} in
build)
  shift
  build "$@"
  ;;
publish)
  [[ $# -eq 2 ]] || { echo "$usage" >&2; exit 2; }
  publish "$2"
  ;;
*)
  echo "$usage" >&2
  exit 2
  ;;
esac
