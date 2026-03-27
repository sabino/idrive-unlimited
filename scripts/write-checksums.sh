#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 OUTPUT FILE [FILE...]" >&2
  exit 1
fi

output="$1"
shift

sha256sum "$@" >"$output"
echo "Wrote $output"
