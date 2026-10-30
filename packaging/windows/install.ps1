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
    Remove-Item -Recurse -Force $InstallDir -ErrorAction SilentlyContinue
    Write-Host 'Removed. State, config and the log were left in place.'
    return
}

$root = Resolve-Path (Join-Path $PSScriptRoot '..\..')
if (-not ((Test-Path (Join-Path $BinDir 'foxcloud.exe')) -and (Test-Path (Join-Path $BinDir 'foxcloudd.exe')))) {
    Write-Host "building into $BinDir"
    New-Item -ItemType Directory -Force $BinDir | Out-Null
    $env:CGO_ENABLED = '0'
    Push-Location $root
    try {
        go build -o (Join-Path $BinDir 'foxcloud.exe') ./cmd/foxcloud
        # No console window for the background daemon; it logs to a file.
        go build -ldflags '-H=windowsgui' -o (Join-Path $BinDir 'foxcloudd.exe') ./cmd/foxcloudd
    } finally { Pop-Location }
}

# Stop a running copy first: Windows cannot overwrite a running executable.
if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
}
New-Item -ItemType Directory -Force $InstallDir | Out-Null
Copy-Item (Join-Path $BinDir 'foxcloud.exe'), (Join-Path $BinDir 'foxcloudd.exe') $InstallDir -Force

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

Write-Host "installed to $InstallDir"
Write-Host 'Start now:  Start-ScheduledTask -TaskName "FoxCloud daemon"'
Write-Host "Log:        $logFile"
Write-Host 'Add the install directory to PATH to use `foxcloud` from a terminal.'
