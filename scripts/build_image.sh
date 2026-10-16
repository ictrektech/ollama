#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)
source "$SCRIPT_DIR/feishu_release.sh"
log() { echo "[INFO] $*"; }
err() { echo "[ERROR] $*" >&2; }
usage() { echo 'Usage: build_image.sh --target <amd|arm|l4t|jetpack5|thor> --version vX.Y.Z_YYYYMMDD --package-dir DIR [--sheet-title TITLE] [--no-publish] [--dry-run]'; }
target=;version=;packages=;publish=1;dry=0;sheets=()
FEISHU_CONFIG_FILE=${FEISHU_CONFIG_FILE:-$HOME/.feishu.json}
FEISHU_SPREADSHEET_TOKEN=Htotsn3oahO1zxt73YMcaB1zn8e
while [[ $# -gt 0 ]]; do
  case "$1" in
    --target) target=$2;shift 2;;
    --version) version=$2;shift 2;;
    --package-dir) packages=$2;shift 2;;
    --sheet-title) sheets+=("$2");shift 2;;
    --no-publish) publish=0;shift;;
    --dry-run) dry=1;shift;;
    -h|--help) usage;exit 0;;
    *) usage;exit 2;;
  esac
done
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+_[0-9]{8}$ && -n "$packages" ]] || { usage;exit 2; }
overlay=
case "$target" in
  amd) arch=amd64;prefix=amd;base=ubuntu:24.04;defaults=(AMD_with_cuda);;
  arm) arch=arm64;prefix=arm;base=ubuntu:24.04;defaults=(ARM_with_cuda ARM_without_cuda SOPHON_bm1688);;
  l4t) arch=arm64;prefix=l4t;base=nvcr.io/nvidia/l4t-jetpack:r36.4.0;overlay=jetpack6;defaults=(l4t);;
  jetpack5) arch=arm64;prefix=l4t5;base=nvcr.io/nvidia/l4t-jetpack:r35.4.1;overlay=jetpack5;defaults=();;
  thor|thor_spark) arch=arm64;prefix=thor;base=nvcr.io/nvidia/cuda:13.0.2-runtime-ubuntu24.04;defaults=(thor_spark);;
  *) usage;exit 2;;
esac
if [[ ${#sheets[@]} == 0 ]]; then sheets=("${defaults[@]}");fi
# Component remains ollama_server so existing Model Hub/VOS packages find the images.
COMPONENT_NAME=ollama_server
IMAGE_REPO=swr.cn-southwest-2.myhuaweicloud.com/ictrek/ollama_server
# VOS image tags remain platform_YYYYMMDD; the embedded binary/release retains X.Y.Z_YYYYMMDD.
DATE=${version##*_};TAG=${prefix}_${DATE};IMAGE_URI=$IMAGE_REPO:$TAG
log "target=$target platform=linux/$arch version=$version image=$IMAGE_URI sheets=${sheets[*]}"
if [[ "$dry" == 1 ]];then exit 0;fi
packages=$(cd "$packages" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/ictrek-ollama-image.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/payload"
(cd "$packages" && sha256sum -c sha256sum.txt)
zstd -dc "$packages/ollama-linux-$arch.tar.zst" | tar -xf - -C "$work/payload"
if [[ -n "$overlay" ]];then zstd -dc "$packages/ollama-linux-arm64-$overlay.tar.zst" | tar -xf - -C "$work/payload";fi
# Validate the binary inside the target image below: a compatible runtime can
# be newer than the build host's glibc, so executing it on that host is invalid.
cp "$REPO_ROOT/docker/Dockerfile.runtime" "$work/Dockerfile"
docker buildx build --platform "linux/$arch" --load --provenance=false --sbom=false --build-arg "BASE_IMAGE=$base" \
  --label "org.opencontainers.image.version=${version#v}" -t "$IMAGE_URI" "$work"
actual_arch=$(docker image inspect --format '{{.Architecture}}' "$IMAGE_URI")
[[ "$actual_arch" == "$arch" ]] || { err 'Image architecture mismatch';exit 1; }
# Both listeners must expose the same API before any publication/table update.
container="ictrek-ollama-verify-$$"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
docker run -d --name "$container" -e NVIDIA_VISIBLE_DEVICES=void "$IMAGE_URI" >/dev/null
for attempt in {1..60}; do
  if docker exec "$container" curl -fsS http://127.0.0.1:11434/api/version >"$work/version11434" \
    && docker exec "$container" curl -fsS http://127.0.0.1:11535/api/version >"$work/version11535";then break;fi
  sleep 1
done
cmp "$work/version11434" "$work/version11535"
python3 - "$work/version11434" "${version#v}" <<'PYVERSION'
import json,sys
assert json.load(open(sys.argv[1]))["version"]==sys.argv[2], "Container version mismatch"
PYVERSION
docker exec "$container" curl -fsS http://127.0.0.1:11535/metrics >/dev/null
docker rm -f "$container" >/dev/null
if [[ "$publish" == 0 ]];then exit 0;fi
docker push "$IMAGE_URI"
docker buildx imagetools inspect "$IMAGE_URI" >/dev/null
if [[ ${#sheets[@]} == 0 ]];then log 'No default sheet for this target; use --sheet-title to record it';exit 0;fi
if [[ -n "${FEISHU_APP_ID:-}" && -n "${FEISHU_APP_SECRET:-}" ]];then :;else
  FEISHU_APP_ID=$(read_feishu_field feishu_app_id)
  FEISHU_APP_SECRET=$(read_feishu_field feishu_app_secret)
fi
[[ -n "$FEISHU_APP_ID" && -n "$FEISHU_APP_SECRET" ]] || { err 'Missing Feishu credentials';exit 1; }
for sheet in "${sheets[@]}";do
  token=$(get_feishu_token "$FEISHU_APP_ID" "$FEISHU_APP_SECRET")
  sheet_id=$(get_sheet_id_by_title "$token" "$FEISHU_SPREADSHEET_TOKEN" "$sheet")
  column=$(find_or_create_component_column_letter "$token" "$FEISHU_SPREADSHEET_TOKEN" "$sheet_id" "$COMPONENT_NAME" "$IMAGE_REPO")
  row=$(find_date_row "$token" "$FEISHU_SPREADSHEET_TOKEN" "$sheet_id" "$DATE")
  if [[ -z "$row" ]];then prepend_date_row "$token" "$FEISHU_SPREADSHEET_TOKEN" "$sheet_id" "$DATE" >/dev/null;row=4;fi
  write_cell "$token" "$FEISHU_SPREADSHEET_TOKEN" "$sheet_id" "$column$row" "$TAG" >/dev/null
  values=$(get_range_values "$token" "$FEISHU_SPREADSHEET_TOKEN" "${sheet_id}!$column$row:$column$row")
  python3 - "$values" "$TAG" <<'PYVERIFY'
import json,sys
response=json.loads(sys.argv[1])
assert response.get("code")==0 and response["data"]["valueRange"]["values"]==[[sys.argv[2]]], "Feishu readback failed"
PYVERIFY
  log "Feishu verified: $sheet!$column$row = $TAG"
done
