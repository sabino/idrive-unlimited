#!/usr/bin/env sh
set -eu

remote_name="idrive-local"
url="http://127.0.0.1:8787"
user="idrive"
password=""

usage() {
    echo "Usage: $0 --password PASS [--remote-name NAME] [--url URL] [--user USER]" >&2
    exit 1
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --remote-name)
            [ "$#" -ge 2 ] || usage
            remote_name="$2"
            shift 2
            ;;
        --url)
            [ "$#" -ge 2 ] || usage
            url="$2"
            shift 2
            ;;
        --user)
            [ "$#" -ge 2 ] || usage
            user="$2"
            shift 2
            ;;
        --password)
            [ "$#" -ge 2 ] || usage
            password="$2"
            shift 2
            ;;
        *)
            usage
            ;;
    esac
done

[ -n "$password" ] || usage
command -v rclone >/dev/null 2>&1 || {
    echo "rclone was not found on PATH." >&2
    exit 1
}

obscured="$(rclone obscure "$password" | tr -d '\r\n')"
[ -n "$obscured" ] || {
    echo "rclone obscure returned an empty password." >&2
    exit 1
}

rclone config create "$remote_name" webdav \
    url "$url" \
    vendor other \
    user "$user" \
    pass "$obscured"

echo
echo "Configured rclone remote '$remote_name' for $url"
echo "Next commands:"
echo "  rclone lsd ${remote_name}:"
echo "  rclone mount ${remote_name}: /mnt/idrive --vfs-cache-mode full --dir-cache-time 30m --attr-timeout 1m --poll-interval 0 --no-modtime"
