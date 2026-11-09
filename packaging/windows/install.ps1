<#
.SYNOPSIS
  Installs foxcloud and foxcloudd for the current user and starts the daemon at logon.

.DESCRIPTION
  The daemon runs as a scheduled task at logon, not as a Windows service. The
  crypt key lives in this user's Windows credential store, which a service
  account cannot read; a task in the user's own session can. The task waits
  before starting (see [daemon] startup_delay for the hold before the first
  sync), restarts on failure and logs to %LOCALAPPDATA%\FoxCloud\foxcloudd.log.

  Run with -Uninstall to remove the task and the installed binaries.

.PARAMETER BinDir
  Directory holding foxcloud.exe and foxcloudd.exe. Built from source when missing.
#>
[CmdletBinding()]
param(
    [string]$BinDir = (Join-Path $PSScriptRoot '..\..\bin'),
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'FoxCloud\bin'),
    [TimeSpan]$LogonDelay = (New-TimeSpan -Seconds 60),
    [switch]$Uninstall
)
$ErrorActionPreference = 'Stop'
$taskName = 'FoxCloud daemon'
$logFile = Join-Path $env:LOCALAPPDATA 'FoxCloud\foxcloudd.log'

if ($Uninstall) {
    if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
    }
    $p = @(Get-Process foxcloudd, foxcloud, foxcloud-tray -ErrorAction SilentlyContinue |
        Where-Object { -not $_.Path -or $_.Path -like (Join-Path $InstallDir '*') })
    $p | Stop-Process -Force -ErrorAction SilentlyContinue
    $p | Wait-Process -Timeout 10 -ErrorAction SilentlyContinue
    Remove-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'FoxCloud tray' -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force $InstallDir -ErrorAction SilentlyContinue
    Write-Host 'Removed. State, config and the log were left in place.'
    return
}

$root = Resolve-Path (Join-Path $PSScriptRoot '..\..')
# Build when the binaries are missing, and always when this is a source checkout
# with Go available, so an old bin\ never hides new code. A release archive has
# no source and installs the binaries it ships.
$haveBins = (Test-Path (Join-Path $BinDir 'foxcloud.exe')) -and (Test-Path (Join-Path $BinDir 'foxcloudd.exe'))
$canBuild = (Test-Path (Join-Path $root 'cmd/foxcloud')) -and (Get-Command go -ErrorAction SilentlyContinue)
if (-not $haveBins -or $canBuild) {
    Write-Host "building into $BinDir"
    New-Item -ItemType Directory -Force $BinDir | Out-Null
    $env:CGO_ENABLED = '0'
    Push-Location $root
    try {
        go build -o (Join-Path $BinDir 'foxcloud.exe') ./cmd/foxcloud
        # No console window for the background daemon; it logs to a file.
        go build -ldflags '-H=windowsgui' -o (Join-Path $BinDir 'foxcloudd.exe') ./cmd/foxcloudd
        go build -ldflags '-H=windowsgui' -o (Join-Path $BinDir 'foxcloud-tray.exe') ./cmd/foxcloud-tray
    } finally { Pop-Location }
}

# Stop a running copy first: Windows cannot overwrite a running executable.
if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
}
# The task may not own the daemon (started by hand, or still shutting down), and
# the tray's popup keeps foxcloud.exe running, so stop every copy from the
# install directory and wait for it to let go of its file.
$running = @(Get-Process foxcloudd, foxcloud, foxcloud-tray -ErrorAction SilentlyContinue |
    Where-Object { -not $_.Path -or $_.Path -like (Join-Path $InstallDir '*') })
$running | Stop-Process -Force -ErrorAction SilentlyContinue
$running | Wait-Process -Timeout 10 -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $InstallDir | Out-Null
Get-ChildItem $InstallDir -Filter '*.old' -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue

# Copies over an installed file even if something still holds it open (the
# process that was just stopped, or a virus scan of it): retry briefly, then move
# the old file aside, which Windows allows for a file in use, and copy again.
function Install-File([string]$Source, [string]$Dest) {
    for ($i = 0; $i -lt 5; $i++) {
        try { Copy-Item $Source $Dest -Force -ErrorAction Stop; return } catch { Start-Sleep -Milliseconds 400 }
    }
    if (Test-Path $Dest) { Move-Item $Dest "$Dest.old" -Force }
    Copy-Item $Source $Dest -Force
}
foreach ($f in 'foxcloud.exe', 'foxcloudd.exe', 'foxcloud-tray.exe') {
    $src = Join-Path $BinDir $f
    if (Test-Path $src) { Install-File $src (Join-Path $InstallDir $f) }
}

# The tray's click-to-open status popup and the status icons it shows.
Copy-Item (Join-Path $PSScriptRoot 'foxcloud-popup.ps1') $InstallDir -Force
$iconDir = Join-Path $InstallDir 'icons'
New-Item -ItemType Directory -Force $iconDir | Out-Null
Copy-Item (Join-Path $root 'internal/tray/icons/*.png') $iconDir -Force

$exe = Join-Path $InstallDir 'foxcloudd.exe'
$action = New-ScheduledTaskAction -Execute $exe -Argument "-logfile `"$logFile`""
$trigger = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"
$trigger.Delay = [Xml.XmlConvert]::ToString($LogonDelay)
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -StartWhenAvailable -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -MultipleInstances IgnoreNew
# Interactive: runs in the user's session, with access to their credential store.
$principal = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger `
    -Settings $settings -Principal $principal -Force | Out-Null

# The tray starts with the user's session through the Run key (no admin needed).
$installedTray = Join-Path $InstallDir 'foxcloud-tray.exe'
if (Test-Path $installedTray) {
    Set-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'FoxCloud tray' -Value "`"$installedTray`""
}

Write-Host "installed to $InstallDir"
if (Test-Path $installedTray) {
    Start-Process $installedTray
    Write-Host 'Tray:       started now (click its icon for the status popup); it also starts at every logon.'
    Write-Host '            If the icon is hidden, drag it out of the taskbar''s overflow (^) menu.'
}
Write-Host 'Daemon:     starts at logon after a delay. Start it now:  Start-ScheduledTask -TaskName "FoxCloud daemon"'
Write-Host "Tray later: & `"$installedTray`""
Write-Host "Log:        $logFile"
Write-Host 'Add the install directory to PATH to use `foxcloud` from a terminal.'
