#!/usr/bin/env bash
# Reuse successful native artifacts from the same version only when their
# complete build inputs match. Go/base artifacts are always rebuilt.
set -euo pipefail
[[ "${GITHUB_ACTIONS:-}" == true && $# == 4 ]] || exit 2
arch=$1; component=$2; version=$3; output=$4
[[ "$arch" == amd64 || "$arch" == arm64 ]] || exit 2
case "$component" in cuda12|cuda13|jetpack5|jetpack6) ;; *) exit 2;; esac
repo=${GITHUB_REPOSITORY:?}
work=$(mktemp -d "${RUNNER_TEMP:?}/native-reuse.XXXXXX")
trap 'rm -rf "$work"' EXIT
if ! gh run list --repo "$repo" --workflow ictrek-release.yaml --branch "$version" --limit 10 \
  --json databaseId,headSha --jq '.[] | [.databaseId,.headSha] | @tsv' >"$work/runs"; then
  exit 0
fi
while IFS=$'\t' read -r run sha; do
  [[ "$run" != "${GITHUB_RUN_ID:?}" ]] || continue
  conclusion=$(gh api "repos/$repo/actions/runs/$run/jobs" \
    --jq ".jobs[] | select(.name | startswith(\"build ($arch, $component,\")) | .conclusion") || continue
  [[ "$conclusion" == success ]] || continue
  git fetch --quiet --depth=1 origin "$sha" || continue
  if ! git diff --quiet "$sha" HEAD -- Dockerfile LLAMA_CPP_VERSION llama/server llama/compat \
      cmake CMakeLists.txt CMakePresets.json scripts/ictrek_build.sh; then
    continue
  fi
  mkdir -p "$work/$run"
  if gh run download "$run" --repo "$repo" --name "$arch-$component" --dir "$work/$run" \
    && [[ -s "$work/$run/component.tar" ]]; then
    mkdir -p "$output"
    mv "$work/$run/component.tar" "$output/component.tar"
    echo 'reused=true' >>"${GITHUB_OUTPUT:?}"
    echo "Reused unchanged $arch/$component native artifact from CI $run ($sha)"
    exit 0
  fi
done <"$work/runs"
