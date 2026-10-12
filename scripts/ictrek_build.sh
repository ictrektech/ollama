#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
usage() { echo 'Usage: ictrek_build.sh build <amd64|arm64> <base|cuda12|cuda13|jetpack5|jetpack6> <vX.Y.Z_YYYYMMDD> <output-dir>'; echo '       ictrek_build.sh package <version> <component-dir> <output-dir>'; }
validate_version() { [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+_[0-9]{8}$ ]] || { echo 'Expected vX.Y.Z_YYYYMMDD' >&2; exit 2; }; }
absolute_dir() { mkdir -p "$1"; (cd "$1" && pwd); }

case "${1:-}" in
build)
  [[ $# == 5 ]] || { usage; exit 2; }
  arch=$2; component=$3; version=$4; validate_version "$version"
  [[ "$arch" == amd64 || "$arch" == arm64 ]] || exit 2
  output=$(absolute_dir "$5")
  case "$component" in
    base) targets=(publish-llama-server-cpu publish-go);;
    cuda12) targets=(publish-llama-server-cuda_v12);;
    cuda13) targets=(publish-llama-server-cuda_v13);;
    jetpack5|jetpack6)
      [[ "$arch" == arm64 ]] || { echo 'JetPack requires arm64' >&2; exit 2; }
      targets=("publish-llama-server-cuda_${component}");;
    *) usage; exit 2;;
  esac
  # CPU/CUDA/JetPack do not need the upstream ROCm development image.
  # Retain the upstream targets, presets, dependency bundling and license generation.
  work=$(mktemp -d "${TMPDIR:-/tmp}/ictrek-ollama-build.XXXXXX")
  trap 'rm -rf "$work"' EXIT
  sed -e "s|rocm/dev-almalinux-8:\${ROCMVERSION}-complete AS base-amd64|almalinux:8 AS base-amd64|" \
      -e 's|RUN dnf install -y yum-utils ccache|RUN dnf install -y epel-release \&\& dnf install -y git yum-utils ccache|' \
      Dockerfile >"$work/Dockerfile"
  for target in "${targets[@]}"; do
    args=(--platform "linux/$arch" --file "$work/Dockerfile" --target "$target"
      --build-arg "GOFLAGS='-ldflags=-w -s -X=github.com/ollama/ollama/version.Version=${version#v} -X=github.com/ollama/ollama/server.mode=release'"
      --provenance=false --sbom=false --output "type=local,dest=$work/$target")
    if [[ "${ICTREK_BUILD_CACHE:-}" == gha ]]; then
      args+=(--cache-from "type=gha,scope=ollama-$arch-$component-$target" --cache-to "type=gha,scope=ollama-$arch-$component-$target,mode=max")
    fi
    docker buildx build "${args[@]}" .
    cp -a "$work/$target/." "$output/"
  done
  ;;
package)
  [[ $# == 4 ]] || { usage; exit 2; }
  version=$2; validate_version "$version"
  components=$(absolute_dir "$3"); output=$(absolute_dir "$4")
  work=$(mktemp -d "${TMPDIR:-/tmp}/ictrek-ollama-package.XXXXXX")
  trap 'rm -rf "$work"' EXIT
  for arch in amd64 arm64; do
    root="$work/$arch"; mkdir -p "$root"
    for component in base cuda12 cuda13; do
      test -d "$components/$arch-$component/lib/ollama"
      cp -a "$components/$arch-$component/." "$root/"
    done
    test -x "$root/bin/ollama"
    test -x "$root/lib/ollama/llama-server"
    test -f "$root/lib/ollama/cuda_v12/libggml-cuda.so"
    test -f "$root/lib/ollama/cuda_v13/libggml-cuda.so"
    ./scripts/deduplicate_cuda_libs.sh "$root"
    tar -C "$root" --owner=0 --group=0 -cf - bin lib | zstd -T0 -19 -o "$output/ollama-linux-$arch.tar.zst"
  done
  for component in jetpack5 jetpack6; do
    root="$components/arm64-$component"
    test -f "$root/lib/ollama/cuda_$component/libggml-cuda.so"
    tar -C "$root" --owner=0 --group=0 -cf - lib | zstd -T0 -19 -o "$output/ollama-linux-arm64-$component.tar.zst"
  done
  (cd "$output" && sha256sum ollama-*.tar.zst >sha256sum.txt)
  ;;
*) usage; exit 2;;
esac
