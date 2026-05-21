param(
  [string]$BinSrc = ".\hostd.exe",
  [string]$InstallRoot = "$env:ProgramFiles\Sunvisai\hostd",
  [string]$ConfigDst = "$env:ProgramData\hostd\config.json",
  [string]$StatePath = "$env:ProgramData\hostd\state.json",
  [string]$ServiceName = "SunvisaiHostd",
  [switch]$DryRun
)

$ErrorActionPreference = "Stop"

function Invoke-Step {
  param(
    [string]$Summary,
    [scriptblock]$Action
  )
  if ($DryRun) {
    Write-Host "+ $Summary"
    return
  }
  & $Action
}

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$BinDst = Join-Path $InstallRoot "hostd.exe"
$ConfigDir = Split-Path -Parent $ConfigDst
$StateDir = Split-Path -Parent $StatePath
$ServiceCommand = "`"$BinDst`" service run --config `"$ConfigDst`" --state `"$StatePath`""

Invoke-Step "Create install directory $InstallRoot" {
  New-Item -ItemType Directory -Force -Path $InstallRoot | Out-Null
}
Invoke-Step "Create config directory $ConfigDir" {
  New-Item -ItemType Directory -Force -Path $ConfigDir | Out-Null
}
Invoke-Step "Create state directory $StateDir" {
  New-Item -ItemType Directory -Force -Path $StateDir | Out-Null
}
Invoke-Step "Copy hostd binary to $BinDst" {
  Copy-Item -Force -Path $BinSrc -Destination $BinDst
}
if (-not (Test-Path $ConfigDst)) {
  Invoke-Step "Install default config to $ConfigDst" {
    Copy-Item -Force -Path (Join-Path $ScriptDir "config.example.json") -Destination $ConfigDst
  }
}

Invoke-Step "Create or update Windows Service $ServiceName" {
  $existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
  if ($null -eq $existing) {
    & sc.exe create $ServiceName binPath= $ServiceCommand start= auto | Out-Null
  } else {
    & sc.exe config $ServiceName binPath= $ServiceCommand start= auto | Out-Null
  }
  & sc.exe description $ServiceName "Sunvisai host runtime daemon" | Out-Null
  & sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/15000/restart/30000 | Out-Null
}

Invoke-Step "Restart Windows Service $ServiceName" {
  $existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
  if ($null -ne $existing -and $existing.Status -eq "Running") {
    Stop-Service -Name $ServiceName -Force
  }
  Start-Service -Name $ServiceName
}
