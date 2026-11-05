<#
.SYNOPSIS
  AxonRouter-Go one-line installer for Windows: downloads the latest release
  .exe from GitHub, installs it, and prints next steps.

.DESCRIPTION
  Run in PowerShell (no admin needed):

    irm https://raw.githubusercontent.com/RMz17X/AxonRouter-Go/main/install.ps1 | iex

  Optional overrides (set before piping, or pass when saved locally):

    $env:VERSION = "v1.9.2"   # default: latest
    $env:BINDIR  = "C:\Tools" # default: $env:LOCALAPPDATA\axonrouter-go
#>
[CmdletBinding()]
param(
  [string]$Version = $env:VERSION,
  [string]$InstallDir = $env:BINDIR
)

$ErrorActionPreference = 'Stop'

$Repo = 'RMz17X/AxonRouter-Go'
if ([string]::IsNullOrWhiteSpace($Version)) { $Version = 'latest' }
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
  $InstallDir = Join-Path $env:LOCALAPPDATA 'axonrouter-go'
}

function Write-Info($msg) { Write-Host "[AxonRouter-Go] $msg" -ForegroundColor Green }

# Windows release asset is amd64-only; refuse ARM with a clear message.
$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -ne 'AMD64') {
  throw "Unsupported architecture: $arch (Windows asset is amd64-only; see https://github.com/$Repo/releases/latest)"
}

$Asset = 'axonrouter-go-windows-amd64.exe'
if ($Version -eq 'latest') {
  $Url = "https://github.com/$Repo/releases/latest/download/$Asset"
} else {
  $Url = "https://github.com/$Repo/releases/download/$Version/$Asset"
}

if (Get-Process -Name 'axonrouter-go' -ErrorAction SilentlyContinue) {
  throw 'AxonRouter-Go is currently running — stop it first (Ctrl+C in its terminal), then re-run the installer.'
}

if (-not (Test-Path $InstallDir)) {
  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}
$Dest = Join-Path $InstallDir 'axonrouter-go.exe'
$Tmp = Join-Path $env:TEMP 'axonrouter-go-download.exe'

Write-Info "downloading $Url ..."
Invoke-WebRequest -Uri $Url -OutFile $Tmp -UseBasicParsing
Move-Item -Path $Tmp -Destination $Dest -Force

# Add install dir to the *user* PATH so `AxonRouter-Go` works in new terminals.
$currentUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$newPath = $null
if ([string]::IsNullOrWhiteSpace($currentUserPath)) {
  $newPath = $InstallDir
} elseif ((($currentUserPath -split ';')) -notcontains $InstallDir) {
  $newPath = $currentUserPath.TrimEnd(';') + ';' + $InstallDir
}
if ($newPath) {
  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
  Write-Info "added $InstallDir to your user PATH (restart the terminal to use it)"
}
