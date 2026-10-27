#!/usr/bin/env bash
set -euo pipefail
# The hosted runner's Azure mirror repeatedly stalled before package installation.
# Keep signed Ubuntu repositories, using the official HTTPS mirror directly.
if [[ -f /etc/apt/apt-mirrors.txt ]]; then
  printf '%s\n' 'https://archive.ubuntu.com/ubuntu' | sudo tee /etc/apt/apt-mirrors.txt >/dev/null
fi
apt_options=(-o Acquire::Retries=3 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30)
sudo apt-get "${apt_options[@]}" -o APT::Update::Error-Mode=any update
sudo apt-get "${apt_options[@]}" install -y --no-install-recommends build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev
