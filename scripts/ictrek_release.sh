#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

usage() { echo 'Usage: ictrek_release.sh <check|build|release|retry|status> [vX.Y.Z_YYYYMMDD]'; }
action=${1:-}; version=${2:-}
repo=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
case "$action" in
check)
  bash -n scripts/ictrek_build.sh scripts/ictrek_release.sh scripts/reuse_native_component.sh scripts/build_image.sh scripts/feishu_release.sh
  git diff --check
  ;;
build|release|retry)
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+_[0-9]{8}$ ]] || { usage; exit 2; }
  [[ -z "$(git status --porcelain)" ]] || { echo 'Commit and push changes before CI build/release' >&2; exit 1; }
  branch=$(git symbolic-ref --short HEAD)
  [[ "$(git rev-parse HEAD)" == "$(git ls-remote origin "refs/heads/$branch" | cut -f1)" ]] || { echo 'Local HEAD must be pushed' >&2; exit 1; }
  if [[ "$action" == build ]]; then
    gh workflow run ictrek-release.yaml --repo "$repo" --ref "$branch" -f version="$version"
  elif [[ "$action" == retry ]]; then
    # Only replace an unpublished, terminal failed/cancelled trigger. Never overwrite a release.
    if gh release view "$version" --repo "$repo" >/dev/null 2>&1; then
      echo 'Published release exists; choose a new dated version' >&2; exit 1
    fi
    state=$(gh run list --repo "$repo" --workflow ictrek-release.yaml --branch "$version" --limit 1 --json status,conclusion --jq '.[0] | .status + ":" + .conclusion')
    [[ "$state" == completed:failure || "$state" == completed:cancelled ]] || { echo 'Retry requires a terminal failed/cancelled run' >&2;exit 1; }
    old_tag=$(git ls-remote origin "refs/tags/$version" | cut -f1)
    [[ -n "$old_tag" ]] || { echo 'No remote trigger tag to retry' >&2;exit 1; }
    git tag -f -a "$version" -m "ictrek Ollama $version (retry unpublished CI)"
    git push --force-with-lease="refs/tags/$version:$old_tag" origin "refs/tags/$version"
  else
    if git show-ref --verify --quiet "refs/tags/$version" || [[ -n "$(git ls-remote origin "refs/tags/$version")" ]]; then
      echo 'Version tag already exists; inspect CI before retrying' >&2; exit 1
    fi
    git tag -a "$version" -m "ictrek Ollama $version"
    git push origin "refs/tags/$version"
  fi
  echo "CI dispatched: $repo ($version); use scripts/ictrek_release.sh status"
  ;;
status)
  gh run list --repo "$repo" --workflow ictrek-release.yaml --limit 5
  ;;
*) usage; exit 2;;
esac
