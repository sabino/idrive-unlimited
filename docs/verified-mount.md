# Verified replacement renames

Use `idrive-rclone` for writable mounts. It is a companion built from rclone
1.75.2 with a small reviewed patch, current reachable dependencies and Go 1.27.3.
It supports local and WebDAV remotes and the usual mount/file commands. Keep
stock rclone for other backends. This companion is maintained by this repository;
it is not an upstream rclone release.

## Build and mount on Linux

Install Go 1.27.3+, Python 3.12+, curl and patch, then run:

```sh
bash scripts/build-idrive-rclone.sh "$HOME/.local/bin/idrive-rclone"
```

The script verifies the pinned source archive's SHA-256 before extraction, applies
the patch without fuzz, and uses Go module checksum verification. It also writes
the upstream license next to the binary. The companion reports
`v1.75.2-idrive.2`. With `govulncheck` on PATH, set `IDRIVE_RCLONE_VERIFY=1` to
scan its reachable source before building. Do not disable Go's checksum database.

Reuse the existing gateway session and WebDAV remote configuration. Start the
gateway using a private password file containing the existing local bridge
password, followed by a single optional newline:

```sh
idrive-gateway serve --no-login --dav-user idrive \
  --dav-pass-file "$HOME/.config/idrive-gateway/local-webdav-password"

idrive-rclone mount idrive-local: "$HOME/mnt/idrive" \
  --webdav-idrive-safe-move --webdav-encoding Slash,Dot,Ctl,Del,RightSpace,RightCrLfHtVt,InvalidUtf8 \
  --vfs-cache-mode full --vfs-cache-max-size 4G \
  --dir-cache-time 30s --attr-timeout 1s --poll-interval 0 --no-modtime
```

Keep the server running in a separate terminal. Match the URL, user and password
in your existing remote configuration. On Unix, make the password file 0600 and
its directory 0700. Obscured rclone passwords are reversible; protect its config
file too. The companion requires safe MOVE negotiation by default and refuses
servers that do not advertise this protocol before doing file operations.

The patch adds a backend capability allowing MOVE to replace its target without
rclone first deleting it. The WebDAV backend sets that capability only after an
authenticated OPTIONS negotiation. MOVE carries the versioned header and the
gateway retains x/net/webdav's path validation and lock checks. A failed immediate
rename restores the VFS file's previous directory and name.

## Behavior and limits

PUT replacement uses private local staging and verified upload. File MOVE instead preserves the target backup, uses a native full-path rename, verifies the destination, and cleans up only the backup. It never separately deletes the canonical source path. Both files and directories can move between parents without colliding with an unrelated entry sharing the old basename. Reads and other gateway mutations wait for replacement to finish.

The web upload limit remains 2,000,000,000 bytes. Native remote renames do not upload file data and therefore do not use that limit, although large-file checksums consume bandwidth and are bounded by transfer timeouts. Existing directory targets are rejected. Plain COPY/MOVE requests still reject collisions.

EVS does not supply an atomic replacement or conditional delete. Persistent transaction recovery reconciles interrupted replacements on restart. An uncertain native rename can retain recovery files; ambiguous promotion results
can leave the original target at a `.idrive-gateway-backup-*` recovery path.
Inspect the error and recovery paths before retrying. Concurrent modifications
from the website or another gateway are outside this process's lock. See
[write safety](safety.md) for recovery details.

## Verification

CI builds the pinned patch and exercises real `moveto` commands against a local
EVS fixture, including native rename failure, refusal of an incompatible server and
absence of destination DELETE. On Linux with `/dev/fuse`, run:

```sh
IDRIVE_RCLONE_TEST_BINARY="$HOME/.local/bin/idrive-rclone" \
IDRIVE_RCLONE_FUSE_TEST=1 go test -race ./internal/webdav -run 'TestCompanion|TestNegotiatedMove' -count=1
```

These tests mount a disposable local fixture and verify successful and failed
replacement, including the VFS view after failure. They require no IDrive login.

See the [reliability review](reliability-review-2026-10-27.md) for journal recovery, request preconditions and private setup-helper configuration.

The supported profile uses rclone's reversible encoding for trailing whitespace,
control characters and invalid UTF-8. Local names retain their spelling; IDrive's
website displays encoding symbols in the stored names. Use the same encoding for
all commands against that remote. The companion refuses listings with names
that cannot round-trip, preventing an existing raw name from silently selecting
another file. A separate `--webdav-encoding None --read-only` profile can inspect
legacy raw names. Raw mutations containing trailing ASCII whitespace are refused
because the provider trims those paths. The private setup helper writes the
supported encoding into its dedicated config automatically.
