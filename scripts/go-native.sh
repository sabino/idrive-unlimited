#!/usr/bin/env bash
# Run any Go command with the same native dependencies as release packaging.
set -euo pipefail

tmp_pkgconfig=""
trap 'if [[ -n "$tmp_pkgconfig" ]]; then rm -rf "$tmp_pkgconfig"; fi' EXIT
if [[ "$(uname -s)" == Linux ]] && pkg-config --exists webkit2gtk-4.1 && ! pkg-config --exists webkit2gtk-4.0; then
  tmp_pkgconfig="$(mktemp -d)"
  cp "$(pkg-config --variable=pcfiledir webkit2gtk-4.1)/webkit2gtk-4.1.pc" "${tmp_pkgconfig}/webkit2gtk-4.0.pc"
  export PKG_CONFIG_PATH="${tmp_pkgconfig}${PKG_CONFIG_PATH:+:${PKG_CONFIG_PATH}}"
fi
CGO_ENABLED=1 go "$@"
