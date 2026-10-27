#!/usr/bin/env bash
# Build the narrowly scoped IDrive mount companion from pinned upstream source.
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output="${1:?usage: build-idrive-rclone.sh OUTPUT}"
python3 -c 'import sys; sys.exit("Python 3.12+ is required" if sys.version_info < (3,12) else 0)'
output="$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.argv[1]).resolve())' "$output")"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/idrive-rclone-build.XXXXXXXX")"
trap 'rm -rf "$work_dir"' EXIT
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --connect-timeout 15 --max-time 300 --retry 3 \
  https://github.com/rclone/rclone/archive/refs/tags/v1.75.2.tar.gz -o "$work_dir/source.tar.gz"
python3 - "$work_dir" <<'PY'
import hashlib, pathlib, sys, tarfile
root = pathlib.Path(sys.argv[1])
archive = root / 'source.tar.gz'
expected = '68afd7f68c84bd978966feae2116339aa7bf454b39c573910c462e87c3774d5a'
if hashlib.sha256(archive.read_bytes()).hexdigest() != expected:
    raise SystemExit('rclone source archive checksum mismatch')
with tarfile.open(archive) as source:
    source.extractall(root, filter='data')
PY
cd "$work_dir/rclone-1.75.2"
patch --batch --fuzz=0 -p1 < "$repo_dir/patches/rclone-v1.75.2-idrive.patch"
if [[ "${IDRIVE_RCLONE_VERIFY:-0}" == 1 ]]; then
  govulncheck .
fi
go build -trimpath -ldflags '-s -w -X github.com/rclone/rclone/fs.Version=v1.75.2-idrive.2' -o "$output" .
cp COPYING "$output.license"
"$output" version
