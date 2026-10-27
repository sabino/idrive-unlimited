param(
    [string]$RemoteName = "idrive-local",
    [string]$Url = "http://127.0.0.1:8787",
    [string]$User = "idrive",
    [string]$PasswordFile,
    [string]$Password,
    [string]$Config
)
$ErrorActionPreference = "Stop"
$rcloneBinary = if ($env:IDRIVE_RCLONE_BINARY) { $env:IDRIVE_RCLONE_BINARY } else { "rclone" }
if (-not (Get-Command $rcloneBinary -ErrorAction SilentlyContinue)) { throw "rclone was not found on PATH." }
$python = Get-Command python3 -ErrorAction SilentlyContinue
if (-not $python) { $python = Get-Command python -ErrorAction SilentlyContinue }
if (-not $python) { throw "Python 3.9+ is required for private configuration input." }
$arguments = @((Join-Path $PSScriptRoot 'configure-rclone.py'), '--remote-name', $RemoteName, '--url', $Url, '--user', $User)
if ($Config) { $arguments += @('--config', $Config) }
if ($PasswordFile) {
    if ($Password) { throw "Use only one password option." }
    & $python.Source @arguments --password-file $PasswordFile
} else {
    if (-not $Password) { throw "Supply -PasswordFile with the private bridge password file." }
    $previousEncoding = $OutputEncoding
    try {
        $OutputEncoding = New-Object Text.UTF8Encoding($false)
        $Password | & $python.Source @arguments
    } finally { $OutputEncoding = $previousEncoding }
}
if ($LASTEXITCODE -ne 0) { throw "rclone configuration failed." }
