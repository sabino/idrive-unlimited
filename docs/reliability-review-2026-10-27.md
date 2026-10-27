# Recovery and reliability review, 2026-10-27

This pass extends the earlier write-safety and dependency reviews. Its scope is
this gateway, the companion patch, setup and packaging scripts, CI, and the
installed Linux mount. Passing these checks is evidence for the exercised
behavior; it is not a claim that arbitrary bugs or IDrive service limits vanish.

| Reproduced or inspected issue | Resulting behavior |
| --- | --- |
| Cross-parent renames rely on an unnecessary intermediate basename and MOVE separately deletes the source | Use the live-verified native full-path rename, preserving the target backup through checksum validation. Journal cleanup never deletes a canonical source, including a file recreated by an external writer. Native renames do not upload bytes. |
| Process exit between backup and promotion leaves replacement state unresolved | Private, synced transaction journals are reconciled before serving. A real uploader-process kill after backup is covered by a regression test. |
| Two servers can recover or mutate the same saved session concurrently | An OS process lock protects the session's transaction namespace and is released by process exit. |
| Special-folder name checks block ordinary user folders named Contacts/SMS | Classify the actual device root before applying special-folder restrictions. Unknown root containers cannot be deleted without classification. |
| Refreshed cookies are lost on restart; an older server can overwrite a newer login | Persist changed cookies every 30 seconds and on shutdown, using stable session identity and a locked revision check. |
| File replacement with identical size, time and upstream version returns cached old bytes | Invalidate cached versions before mutations and bound metadata-identical cache reuse to five minutes. |
| Cache pruning races opening a downloaded file | Pin paths until the file descriptor is acquired; open readers retain their descriptor. |
| A seek during a pending download is lost | Apply the latest pending offset when installing the downloaded descriptor. |
| Cache/recovery directory overrides let pruning touch unrelated files | Store managed data in dedicated subdirectories of the override location. |
| Eviction leaves empty directories, orphan metadata or exceeds recovery quota under concurrent saves | Remove empty unpinned directories, group recovery payloads with their metadata, account for both, and serialize retention. |
| Device containers can be deleted or moved via raw paths; aliases can escape or shadow another device | Protect device roots in EVS operations and accept only unambiguous, single-component friendly names. |
| Trimming paths or rewriting backslashes silently selects another filename | Preserve literal read paths, encode problematic names through the supported rclone profile, and refuse raw mutations that the provider trims. |
| Malformed browse paths, duplicate names or invalid sizes can become filesystem entries | Reject inconsistent upstream metadata before exposing or mutating paths. Bound retained metadata listings. |
| Recursive copy/move into a directory's own descendants can produce partial trees or unbounded recursion | Reject these destinations before mutation. |
| Native login callbacks are callable from untrusted pages | Require a per-window capability, initialized only on the top-level HTTPS IDrive page; bound callback payloads and avoid logging token response details. |
| Stale WebView2 registry names or a zero version count as an installed runtime | Require a usable nonzero `pv` version; align installer detection. |
| HTTP write preconditions are ignored and metadata ETags claim strong byte identity | Evaluate write preconditions under the mutation guard, emit weak metadata validators, and reject weak tags for strong If-Match. Use WebDAV locks for gateway-local coordination. |
| Non-upload XML bodies and stalled sockets can hold resources indefinitely | Cap other request bodies at 1 MiB, apply two-minute idle I/O deadlines, and cancel queued mutations with their request context. |
| Shutdown returns before recovery finishes | Drain active handler work after closing network connections; retain the recovery lock until draining succeeds or the process exits. |
| MKCOL can collide with an existing entry or ignore a chunked request body | Reject existing entries and unsupported bodies before directory creation. |
| In-place writes are rejected using current length plus write size | Enforce the resulting offset/length instead. Honor exclusive-create and missing-file open flags. |
| Setup/smoke processes expose local passwords in arguments | Read passwords through private files/stdin; write a dedicated private rclone config. Helpers refuse configs belonging to other remotes. Windows helper output receives a restricted user ACL. |
| Package names can traverse staging paths; PowerShell ignores native exit codes | Validate labels before staging changes and fail on native build/installer errors. |
| A downloaded Windows bootstrapper is packaged without publisher verification | Require a valid Microsoft Authenticode signature. |
| Packaged README links refer to omitted docs/helpers | Include the referenced documentation and setup helpers in archives/installers. |
| Hosted Ubuntu mirror failures stall CI | Use the official HTTPS mirror with retries, I/O timeouts and explicit update failure detection. No package-signature verification is disabled. |

Live tests exposed trimming of trailing whitespace by the provider rename API. The supported profile uses standard rclone encoding and fails closed when existing names cannot round-trip. This representation is visible as encoding symbols in the IDrive website.

The companion is now `v1.75.2-idrive.2`; it also includes the local `obscure`
command needed by the private setup helper. It remains a repository-maintained
build, distinct from an upstream rclone release.

## Recovery operation

`serve` places transaction records under `transactions/<session-namespace>` next
to the selected session file. Records contain paths and checksums, not login
cookies. They are atomically written with mode 0600; the namespace is private.
An active server holds `server.lock`. Do not delete that lock to start another
server: stop the old process instead.

The journal distinguishes staging, prepared replacement and committed cleanup.
Restart restores an original when promotion did not finish, or finalizes a
checksum-verified promoted destination. A committed record never rolls back a
later edit of the canonical destination. Unexpected bytes or malformed records
are retained and startup fails with the relevant paths for inspection. Fresh
logins have separate namespaces; recovery is never silently applied across an
account change. Keep the original saved session when recovering pending work.

Refreshed cookies retain the saved session identity across restart. Persistence
uses a revision check so an external login/session update wins over a stale
writer. Session files and transaction contents are synced; Windows lacks the
Unix directory-fsync primitive, so this is not a claim about every filesystem's
power-loss guarantees.

## Configuration and validation

The setup helpers require Python 3.9+ and rclone (or `IDRIVE_RCLONE_BINARY`). They
create a dedicated config under the user configuration directory, or at
`--config` / `-Config`. Use the printed path with `--config` when listing or
mounting. They accept `--password-file` / `-PasswordFile`; protect the input file
as well. The legacy plaintext argument remains for compatibility and should be
avoided. Helpers do not edit encrypted or unrelated multi-remote configurations.

When `IDRIVE_GATEWAY_CACHE_DIR` is set, managed cache data resides in its
`idrive-gateway-files` child. `IDRIVE_GATEWAY_RECOVERY_DIR` similarly uses an
`idrive-gateway-recovery` child. Other files in either configured base are left
alone. Old override layouts are not automatically deleted or migrated.

The regression suite covers interrupted replacement phases, conflicting remote
bytes, process-lock exclusion, real process termination, session persistence and
revision conflicts, stale cache reads, safe directory overrides, alias/root
protection, HTTP preconditions, request limits and shutdown draining. JS tests
exercise the actual injected login bootstrap against trusted/untrusted origins
and frames. Setup tests use the actual rclone executable. CI also checks native
packaging and signing prerequisites on the target platforms.

## Provider and platform boundaries

The web upload limit remains 2,000,000,000 bytes. EVS supplies neither a verified
atomic replace nor a conditional delete, so external concurrent writers cannot
participate in this process's transaction lock. Full-file readback and original
verification add bandwidth; PUT staging also consumes local storage. Native renames use no web upload, but large live renames have not been exercised. Remote reads still download a
whole file before seeking. A fresh login whose account identity cannot be
verified is deliberately not allowed to recover a previous login's namespace.

The verified mount companion is exercised on Linux. Windows/macOS builds and
installers are checked in CI; this does not establish native GUI login or
physical FUSE/WinFsp behavior on those platforms. macOS release signing requires
a developer signing identity that this repository does not provide. These
constraints are documented rather than represented as solved by unit tests.

References: [HTTP conditional request semantics](https://www.rfc-editor.org/rfc/rfc9110.html#section-13),
[Microsoft WebView2 runtime detection](https://learn.microsoft.com/microsoft-edge/webview2/concepts/distribution#detect-if-a-webview2-runtime-is-already-installed).
