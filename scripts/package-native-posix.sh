#!/usr/bin/env bash
set -euo pipefail

version="0.0.0-dev"
commit="none"
build_date=""
artifact_dir="./dist-native"
os_label=""
arch_label=""

while [[ $# -gt 0 ]]; do
  if [[ $# -lt 2 ]]; then echo "missing value for $1" >&2; exit 1; fi
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
if [[ ! "$normalized_version" =~ ^[0-9][0-9A-Za-z.+_-]*$ || ! "$commit" =~ ^[0-9A-Za-z._-]+$ || ! "$build_date" =~ ^[0-9A-Za-z.:+_-]+$ ]]; then
  echo "invalid version, commit or build date" >&2; exit 1
fi
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

# Verify the compiler target rather than trusting the archive label or a
# runner alias. GitHub's unqualified macOS labels can change CPU family.
build_arch="$(go env GOARCH)"
build_os="$(go env GOOS)"
case "$build_os" in
  darwin) expected_os="macOS" ;;
  linux) expected_os="linux" ;;
  *) echo "unsupported native POSIX target: $build_os" >&2; exit 1 ;;
esac
if [[ "$arch_label" != "$build_arch" || "$os_label" != "$expected_os" ]]; then
  echo "native build target is $build_os/$build_arch; artifact label is $os_label/$arch_label" >&2
  exit 1
fi

artifact_base="idrive-gateway_${normalized_version}_${os_label}_${arch_label}_native"
mkdir -p "$artifact_dir"
artifact_dir="$(cd "$artifact_dir" && pwd -P)"
stage_dir="${artifact_dir}/${artifact_base}"
archive_path="${artifact_dir}/${artifact_base}.tar.gz"

rm -rf "$stage_dir"
mkdir -p "$stage_dir"

ldflags="-s -w -X main.version=${normalized_version} -X main.commit=${commit} -X main.date=${build_date}"
bash "$(dirname "$0")/go-native.sh" build -ldflags "$ldflags" -o "${stage_dir}/idrive-gateway" ./cmd/idrive-gateway
cp LICENSE "${stage_dir}/LICENSE"
cp README.md "${stage_dir}/README.md"
mkdir -p "${stage_dir}/docs" "${stage_dir}/scripts" "${stage_dir}/patches"
cp docs/*.md "${stage_dir}/docs/"
cp scripts/setup-rclone.sh scripts/configure-rclone.py scripts/build-idrive-rclone.sh "${stage_dir}/scripts/"
cp patches/* "${stage_dir}/patches/"

rm -f "$archive_path"
tar -C "$artifact_dir" -czf "$archive_path" "$artifact_base"

echo "Built native POSIX package:"
echo "  $archive_path"
