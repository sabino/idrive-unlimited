param(
    [string]$Output = ".\idrive-gateway.exe",
    [switch]$PersistGoEnv,
    [switch]$VerboseEnv
)

$ErrorActionPreference = "Stop"

function Resolve-MsysToolchainBin {
    $candidates = @(
        "C:\msys64\ucrt64\bin",
        "C:\msys64\mingw64\bin"
    )

    foreach ($candidate in $candidates) {
        $gcc = Join-Path $candidate "gcc.exe"
        $gxx = Join-Path $candidate "g++.exe"
        if ((Test-Path $gcc) -and (Test-Path $gxx)) {
            return $candidate
        }
    }

    throw "Could not find an MSYS2 GCC toolchain. Expected gcc.exe and g++.exe under C:\msys64\ucrt64\bin or C:\msys64\mingw64\bin."
}

function Ensure-PathContains([string]$dir) {
    $parts = $env:PATH -split ';' | Where-Object { $_ -ne "" }
    if ($parts -notcontains $dir) {
        $env:PATH = "$dir;$env:PATH"
    }
}

function Copy-RuntimeDlls([string]$toolchainBin, [string]$outputPath) {
    $outputDir = Split-Path -Parent $outputPath
    if ([string]::IsNullOrWhiteSpace($outputDir)) {
        $outputDir = (Get-Location).Path
    }

    $dlls = @(
        "libstdc++-6.dll",
        "libgcc_s_seh-1.dll",
        "libwinpthread-1.dll"
    )

    foreach ($dll in $dlls) {
        $source = Join-Path $toolchainBin $dll
        if (Test-Path $source) {
            Copy-Item $source -Destination (Join-Path $outputDir $dll) -Force
        }
    }
}

$toolchainBin = Resolve-MsysToolchainBin
Ensure-PathContains $toolchainBin

$env:CC = "gcc"
$env:CXX = "g++"
$env:CGO_ENABLED = "1"

if ($PersistGoEnv) {
    go env -w CC=gcc
    go env -w CXX=g++
    go env -w CGO_ENABLED=1
}

if ($VerboseEnv) {
    Write-Host "Toolchain bin: $toolchainBin"
    Write-Host "gcc: $((Get-Command gcc).Source)"
    Write-Host "g++: $((Get-Command g++).Source)"
    Write-Host "go env CC: $(go env CC)"
    Write-Host "go env CXX: $(go env CXX)"
    Write-Host "go env CGO_ENABLED: $(go env CGO_ENABLED)"
}

$resolvedOutput = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Output)
go build -o $resolvedOutput .\cmd\idrive-gateway
Copy-RuntimeDlls -toolchainBin $toolchainBin -outputPath $resolvedOutput

Write-Host "Built $resolvedOutput"
