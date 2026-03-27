#!/usr/bin/env bash
set -euo pipefail

version="0.0.0-dev"
commit="none"
build_date=""
artifact_dir="./dist-native"
os_label=""
arch_label=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      version="$2"
      shift 2
      ;;
    --commit)
      commit="$2"
      shift 2
      ;;
    --build-date)
      build_date="$2"
      shift 2
      ;;
    --artifact-dir)
      artifact_dir="$2"
      shift 2
      ;;
    --os-label)
      os_label="$2"
      shift 2
      ;;
    --arch-label)
      arch_label="$2"
      shift 2
      ;;
    *)
      echo "unknown flag: $1" >&2
      exit 1
      ;;
  esac
done

if [[ -z "$build_date" ]]; then
  build_date="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
fi

normalized_version="${version#v}"
if [[ -z "$os_label" ]]; then
  case "$(uname -s)" in
    Linux) os_label="linux" ;;
    Darwin) os_label="macOS" ;;
    *)
      echo "unsupported platform for package-native-posix.sh" >&2
      exit 1
      ;;
  esac
fi

if [[ -z "$arch_label" ]]; then
  case "$(uname -m)" in
    x86_64) arch_label="amd64" ;;
    arm64|aarch64) arch_label="arm64" ;;
    *)
      echo "unsupported architecture for package-native-posix.sh" >&2
      exit 1
      ;;
  esac
fi

artifact_base="idrive-gateway_${normalized_version}_${os_label}_${arch_label}_native"
mkdir -p "$artifact_dir"
artifact_dir="$(cd "$artifact_dir" && pwd -P)"
stage_dir="${artifact_dir}/${artifact_base}"
archive_path="${artifact_dir}/${artifact_base}.tar.gz"

rm -rf "$stage_dir"
mkdir -p "$stage_dir"

if [[ "$os_label" == "linux" ]] && pkg-config --exists webkit2gtk-4.1 && ! pkg-config --exists webkit2gtk-4.0; then
  tmp_pkgconfig="$(mktemp -d)"
  cp "$(pkg-config --variable=pcfiledir webkit2gtk-4.1)/webkit2gtk-4.1.pc" "${tmp_pkgconfig}/webkit2gtk-4.0.pc"
  export PKG_CONFIG_PATH="${tmp_pkgconfig}${PKG_CONFIG_PATH:+:${PKG_CONFIG_PATH}}"
fi

ldflags="-s -w -X main.version=${normalized_version} -X main.commit=${commit} -X main.date=${build_date}"
CGO_ENABLED=1 go build -ldflags "$ldflags" -o "${stage_dir}/idrive-gateway" ./cmd/idrive-gateway
cp LICENSE "${stage_dir}/LICENSE"
cp README.md "${stage_dir}/README.md"

rm -f "$archive_path"
tar -C "$artifact_dir" -czf "$archive_path" "$artifact_base"

echo "Built native POSIX package:"
echo "  $archive_path"
