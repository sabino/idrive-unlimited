# Security and dependency review, 2026-10-27

The review covered gateway authentication, HTTP error handling, host validation,
write verification, WebDAV replacement and the mounted rclone client. It is a
code review and targeted runtime validation, not a penetration test of IDrive.

| Finding | Change |
| --- | --- |
| rclone deletes an existing rename target before MOVE | Pinned companion negotiates verified replacement and skips pre-delete; gateway retains the source until target readback succeeds. |
| Supplied local WebDAV password appears in argv and startup output | `--dav-pass-file` reads a bounded private file; supplied passwords are not logged. |
| Authentication compares variable-length strings directly | Compare SHA-256 digests with constant-time comparisons. |
| Metadata/error bodies can consume unbounded memory or disclose raw response documents | 64 MiB response limit; omit raw bodies and unknown API error fields. File downloads remain streamed. |
| Server host can contain URL components | Require a plain HTTPS authority, rejecting user information, paths, queries and fragments. |
| Device root rename guard misses moves into nested directories | Protect the device root regardless of the target depth. |
| Readback checks use MD5 | Use SHA-256 for gateway upload and move verification. EVS's required upload-chunk MD5 remains protocol-specific. |
| CI action tags can change | Upgrade every used action to the latest release and pin its exact commit in CI and release workflows. |

Go is upgraded to **1.27.3**, x/net to **0.61.0**, x/sys to **0.49.0**, and
x/sync to **0.24.0**. webview_go's latest available version remains
`v0.0.0-20240831120633-6173450d4dd6`. The latest stable rclone is **1.75.2**;
the companion identifies itself as **1.75.2-idrive.1** and upgrades its reachable
library dependencies. Its pinned module changes are included in the patch.
Versions were checked against upstream releases and the Go module proxy.

Native gateway race tests, vet and govulncheck 1.9.0 pass. The companion's
reachable-source scan reports zero affected vulnerabilities. Its module graph
also contains x/crypto's deprecated OpenPGP advisory GO-2026-5932; those packages
are not imported by the companion. A stripped-binary scan conservatively reports
the module advisory, so it must not be described as an entirely clean binary scan.
The companion integration tests exercise real rclone operations and optional
Linux FUSE mounts against a local fixture, including a failed replacement.

Keep session files, bridge passwords, rclone configuration and caches private.
The existing saved login is reused; a dependency upgrade does not require
collecting the website password. Neither rclone's password obscuring nor the
local cache provides encryption at rest. The gateway intentionally listens only
on loopback. See [verified mounts](verified-mount.md) and [write safety](safety.md)
for transfer costs, recovery paths, and the remaining external-writer and
non-atomic upstream limitations.
