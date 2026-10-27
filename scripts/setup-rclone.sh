#!/usr/bin/env sh
set -eu
remote_name="idrive-local"
url="http://127.0.0.1:8787"
user="idrive"
password=""
password_file=""
config=""
usage() { echo "Usage: $0 --password-file PATH [--remote-name NAME] [--url URL] [--user USER]" >&2; exit 1; }
while [ "$#" -gt 0 ]; do
    [ "$#" -ge 2 ] || usage
    case "$1" in
        --remote-name) remote_name="$2" ;;
        --url) url="$2" ;;
        --user) user="$2" ;;
        --password-file) password_file="$2" ;;
        --config) config="$2" ;;
        --password) password="$2" ;;
        *) usage ;;
    esac
    shift 2
done
command -v "${IDRIVE_RCLONE_BINARY:-rclone}" >/dev/null 2>&1 || { echo 'rclone was not found on PATH.' >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo 'Python 3.9+ is required for private configuration input.' >&2; exit 1; }
helper_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
set --
if [ -n "$config" ]; then set -- --config "$config"; fi
if [ -n "$password_file" ]; then
    [ -z "$password" ] || usage
    python3 "$helper_dir/configure-rclone.py" --password-file "$password_file" --remote-name "$remote_name" --url "$url" --user "$user" "$@"
else
    [ -n "$password" ] || usage
    printf '%s\n' "$password" | python3 "$helper_dir/configure-rclone.py" --remote-name "$remote_name" --url "$url" --user "$user" "$@"
fi
