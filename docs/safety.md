# Write safety and local operation

The gateway can mount IDrive read/write. Run `login` once, then reuse the saved session with `serve --no-login`; this fails with a useful error instead of opening a browser when authentication is unavailable. `probe` and `smoke` also support `--no-login` for unattended checks.

## Uploads and recovery

The web upload limit is enforced conservatively at 2,000,000,000 bytes, before remote changes. Oversized WebDAV PUT requests return HTTP 413. A failed or incomplete incoming request is discarded without committing its partial body.

Each complete upload is staged at a unique remote name and downloaded for a size/SHA-256 check. For replacements, the existing file is then renamed to a unique backup, the staged file is promoted, and the final path is checked again before the backup is removed. This adds transfer overhead to writes. Upload commits within one gateway client are serialized.

EVS does not provide a demonstrated atomic replacement operation. Gateway requests are serialized around mutations so its readers do not see the backup/promotion window. External IDrive clients can briefly see the destination absent. A promotion failure triggers rollback when the destination is confirmed missing. If the server's outcome cannot be established, the gateway preserves recovery files and reports their paths rather than deleting uncertain bytes. Concurrent modifications from another gateway or the IDrive website cannot be made transactional by this local process.

Complete local uploads that fail to save are retained under the user cache directory in `idrive-gateway/recovery`. The error includes the recovery filename; its adjacent JSON file records the intended remote path. These files contain private uploaded content. The default retention budget is 4 GiB and seven days, pruned when recovery storage is used. Override the location with `IDRIVE_GATEWAY_RECOVERY_DIR` and the byte budget with `IDRIVE_GATEWAY_RECOVERY_MAX_BYTES`. If retention itself fails, the error identifies the staging/recovery file that remains.

Ordinary WebDAV COPY/MOVE operations reject an existing target with HTTP 412. Stock rclone 1.75.2 can separately DELETE the target before MOVE, bypassing that guard. For mounting, use the [verified companion](verified-mount.md): it negotiates `X-IDrive-Safe-Move: verified-copy-v1`, skips rclone's pre-delete and leaves replacement to the gateway. Explicit DELETE still deletes data.

A negotiated file MOVE downloads the source to a private temporary file, uses the staged SHA-256-verified upload transaction for the target, then rechecks the source before removing it. Both same-directory and cross-directory replacement are supported up to the upload limit. Existing directory targets are rejected. A failed upload retains the source; uncertain upstream outcomes retain remote recovery files. A crash or failed source removal can leave duplicates, and an error after promotion can mean the destination was saved while the source remains. Inspect the reported paths before retrying. EVS has no conditional deletion or cross-client lock, so concurrent external edits and fully atomic remote POSIX rename semantics remain unsupported.

## Download cache

The persistent cache is separated by a SHA-256 session identity and mounted root. Reusing the same session reuses downloads; a different authenticated session cannot reuse another account's cached bytes.

The default cache target is 4 GiB with seven days of inactivity, pruned on access. Set `IDRIVE_GATEWAY_CACHE_DIR` for the location and `IDRIVE_GATEWAY_CACHE_MAX_BYTES` for the budget. The currently requested file is protected from eviction; a file larger than the budget can temporarily exceed it, and open files can delay eviction on Windows. Limit rclone's VFS cache separately, for example `--vfs-cache-max-size 4G`, because the two caches store separate copies.

Reads are still downloaded completely before local seeking. Request cancellation now reaches downloads, waiting callers can cancel independently, and download transfers have a 30-minute limit instead of the metadata client's two-minute limit. Range streaming requires separate validation against EVS and is not claimed by this change.

## Authentication and listening address

The session file is replaced using a new private temporary file, preserving the previous file if replacement fails. New sessions retain cookie paths, domains, host-only scope, expiry, deletion, Secure, and HttpOnly attributes. Existing session files remain loadable. Cookie attributes that an older gateway already discarded cannot be reconstructed without a new login, but existing valid sessions can still be reused.

Use `--dav-pass-file /private/path/password` with a regular 0600 file on Unix to keep the bridge password out of process arguments. Supplied passwords are not printed. Windows requires an appropriately restricted file ACL. Basic authentication comparisons use fixed-length constant-time hashes. Metadata responses are limited to 64 MiB, and errors omit raw upstream documents and unknown API message fields.

The local server accepts only explicit loopback IP addresses or `localhost`. Wildcard and LAN binds are rejected before authentication starts. The native token exchange accepts HTTPS IDrive URLs, supports the API's `redirect_url` fallback, and rejects redirects to unrelated origins.

On Linux, use `bash scripts/go-native.sh build -o idrive-gateway ./cmd/idrive-gateway` to support either WebKitGTK pkg-config family. Native tests use `bash scripts/go-native.sh test -race ./...`. Release packaging uses the same helper and removes its temporary compatibility alias afterward.

## Writable mount example

After starting the gateway and configuring a local rclone WebDAV remote as described in the README:

```sh
mkdir -p "$HOME/mnt/idrive"
idrive-rclone mount idrive-local: "$HOME/mnt/idrive" \
  --vfs-cache-mode full --vfs-cache-max-size 4G \
  --dir-cache-time 30s --attr-timeout 1s --poll-interval 0 --no-modtime
```

Full VFS caching can upload writes after a file closes. Check rclone's logs and allow pending uploads to finish before unmounting. A mount is writable unless `--read-only` is specified.
