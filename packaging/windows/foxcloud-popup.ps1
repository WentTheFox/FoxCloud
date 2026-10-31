<#
.SYNOPSIS
  The status popup that opens when the FoxCloud tray icon is clicked.

.DESCRIPTION
  Ported from the old RcloneProtonDrive tray popup (borderless WinForms window
  that follows the Windows light/dark setting and accent colour, with rounded
  corners and a shadow from DWM). It shows the Windows counterpart of the Plasma
  widget: a summary header, a card per job with Sync now / Stop, the conflict
  count, the update notice and the recent changes.

  It is event-driven like the widget: `foxcloud status-json -wait REV` and
  `foxcloud history-json -wait REV` block inside foxcloud until something
  changes, so there is no timer. The window closes when it loses focus, and the
  waiting commands are killed with it.
#>
[CmdletBinding()]
param(
    [string]$Bin,
    [string]$IconDir
)
$ErrorActionPreference = 'Stop'
# $PSScriptRoot can be empty in a param() default, so work the folder out here.
$here = if ($PSScriptRoot) { $PSScriptRoot } else { Split-Path -Parent $MyInvocation.MyCommand.Path }
if (-not $Bin) { $Bin = Join-Path $here 'foxcloud.exe' }
if (-not $IconDir) { $IconDir = Join-Path $here 'icons' }
Add-Type -AssemblyName System.Windows.Forms, System.Drawing

Add-Type -ReferencedAssemblies System.Windows.Forms -TypeDefinition @'
using System;
using System.Diagnostics;
using System.Threading;
using System.Windows.Forms;
public static class Cli {
    // Runs a command off the UI thread and hands its output back on it. The
    // process is returned so the caller can kill a command that is still waiting.
    public static Process Run(Control ui, string exe, string args, Action<string, int> done) {
        var psi = new ProcessStartInfo(exe, args) {
            UseShellExecute = false, CreateNoWindow = true,
            RedirectStandardOutput = true, RedirectStandardError = true,
            StandardOutputEncoding = System.Text.Encoding.UTF8,
        };
        var p = Process.Start(psi);
        ThreadPool.QueueUserWorkItem(_ => {
            string o = p.StandardOutput.ReadToEnd();
            p.StandardError.ReadToEnd();
            p.WaitForExit();
            int code = p.ExitCode;
            try { if (!ui.IsDisposed) ui.BeginInvoke(done, o, code); } catch { }
        });
        return p;
    }
}
public static class Win {
    [System.Runtime.InteropServices.DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
    [System.Runtime.InteropServices.DllImport("dwmapi.dll")] public static extern int DwmSetWindowAttribute(IntPtr h, int attr, ref int v, int size);
    [System.Runtime.InteropServices.StructLayout(System.Runtime.InteropServices.LayoutKind.Sequential)] public struct MARGINS { public int l, r, t, b; }
    [System.Runtime.InteropServices.DllImport("dwmapi.dll")] public static extern int DwmExtendFrameIntoClientArea(IntPtr h, ref MARGINS m);
}
'@

# --- Theme: read once when the popup opens -----------------------------------
function Get-Reg($path, $name, $default) {
    try { $v = (Get-ItemProperty -Path $path -Name $name -ErrorAction Stop).$name; if ($null -ne $v) { return $v } } catch { }
    $default
}
$light = (Get-Reg 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Themes\Personalize' 'AppsUseLightTheme' 0) -eq 1
$abgr = [uint32](Get-Reg 'HKCU:\Software\Microsoft\Windows\DWM' 'ColorizationColor' 0xFFD47800)
$c = { param($r, $g, $b) [Drawing.Color]::FromArgb($r, $g, $b) }
$accent = & $c (($abgr -shr 16) -band 0xFF) (($abgr -shr 8) -band 0xFF) ($abgr -band 0xFF)
if ($light) {
    $cBg = & $c 249 249 249; $cFg = & $c 26 26 26; $cDim = & $c 96 96 96; $cCard = & $c 238 238 238
    $cBorder = & $c 205 205 205; $cErr = & $c 196 43 28; $cWarn = & $c 157 93 0
} else {
    $cBg = & $c 43 43 43; $cFg = & $c 255 255 255; $cDim = & $c 171 171 171; $cCard = & $c 56 56 56
    $cBorder = & $c 70 70 70; $cErr = & $c 255 153 164; $cWarn = & $c 252 225 0
}
$theme = if ($light) { 'light' } else { 'dark' }
$fBody = New-Object Drawing.Font 'Segoe UI Variable Text', 9.5
$fSmall = New-Object Drawing.Font 'Segoe UI Variable Small', 9
$fTitle = New-Object Drawing.Font 'Segoe UI Variable Display Semibold', 13
$fBold = New-Object Drawing.Font 'Segoe UI Variable Text Semibold', 9.5
$width = 360

# --- Form --------------------------------------------------------------------
$form = New-Object Windows.Forms.Form
$form.FormBorderStyle = 'None'; $form.ShowInTaskbar = $false; $form.TopMost = $true
$form.StartPosition = 'Manual'; $form.BackColor = $cBg; $form.ForeColor = $cFg; $form.Font = $fBody
$form.AutoSize = $true; $form.AutoSizeMode = 'GrowAndShrink'
$form.Padding = New-Object Windows.Forms.Padding 20, 18, 20, 20
$form.Add_HandleCreated({
    $h = $this.Handle
    $v = if ($light) { 0 } else { 1 }; [Win]::DwmSetWindowAttribute($h, 20, [ref]$v, 4) | Out-Null   # dark mode
    $v = 2; [Win]::DwmSetWindowAttribute($h, 33, [ref]$v, 4) | Out-Null                              # rounded corners
    $v = $cBorder.B * 65536 + $cBorder.G * 256 + $cBorder.R; [Win]::DwmSetWindowAttribute($h, 34, [ref]$v, 4) | Out-Null
    $m = New-Object Win+MARGINS; $m.l = 1; $m.r = 1; $m.t = 1; $m.b = 1
    [Win]::DwmExtendFrameIntoClientArea($h, [ref]$m) | Out-Null
})
$panel = New-Object Windows.Forms.FlowLayoutPanel
$panel.FlowDirection = 'TopDown'; $panel.WrapContents = $false; $panel.AutoSize = $true; $panel.BackColor = $cBg
$panel.Location = New-Object Drawing.Point $form.Padding.Left, $form.Padding.Top
$form.Controls.Add($panel)

# --- Helpers -----------------------------------------------------------------
function Format-Ago($d) {
    $s = ((Get-Date) - $d).TotalSeconds
    if ($s -lt 60) { 'just now' } elseif ($s -lt 3600) { "$([int]($s / 60)) min ago" }
    elseif ($s -lt 86400) { "$([int]($s / 3600)) h ago" } else { "$([int]($s / 86400)) d ago" }
}
# The wrap width is explicit: a label's parent is an auto-sized flow panel whose
# own width is not known yet, and using it made short text wrap one letter early.
function New-Label($text, $font = $fBody, $color = $cFg, $top = 0, $bottom = 0, $parent = $panel, $bg = $cBg, $maxW = $width) {
    $l = New-Object Windows.Forms.Label
    $l.Text = $text; $l.Font = $font; $l.ForeColor = $color; $l.BackColor = $bg; $l.AutoSize = $true
    $l.MaximumSize = New-Object Drawing.Size $maxW, 0
    $l.Margin = New-Object Windows.Forms.Padding 0, $top, 0, $bottom
    $parent.Controls.Add($l)
    $l
}
function New-Button($text, $w, $onClick) {
    $b = New-Object Windows.Forms.Button
    $b.Text = $text; $b.FlatStyle = 'Flat'; $b.FlatAppearance.BorderSize = 0; $b.Font = $fSmall
    $b.Size = New-Object Drawing.Size $w, 28; $b.Cursor = 'Hand'; $b.UseVisualStyleBackColor = $false
    $b.BackColor = $cBorder; $b.ForeColor = $cFg
    $b.Margin = New-Object Windows.Forms.Padding 0, 0, 8, 0
    $b.Add_Click($onClick)
    $b
}
function Get-Icon($name) {
    $f = Join-Path $IconDir "$theme-$name.png"
    if (Test-Path $f) { [Drawing.Image]::FromFile($f) }
}
# Mirrors tray.Model.Icon: trouble outranks activity, which outranks rest.
function Get-Summary($view) {
    if (-not $view.running) { return @{ Icon = 'offline'; Text = 'Service not running' } }
    $s = @($view.jobs | ForEach-Object { $_.state })
    $has = { param($n) $s -contains $n }
    if (& $has 'failed') { return @{ Icon = 'failed'; Text = 'Sync failed' } }
    if (& $has 'paused') { return @{ Icon = 'paused'; Text = 'Sync paused' } }
    $up = & $has 'uploading'; $down = & $has 'downloading'
    if ((& $has 'syncing') -or ($up -and $down)) { return @{ Icon = 'syncing'; Text = 'Syncing' } }
    if ($up) { return @{ Icon = 'uploading'; Text = 'Uploading' } }
    if ($down) { return @{ Icon = 'downloading'; Text = 'Downloading' } }
    if (& $has 'checking') { return @{ Icon = 'checking'; Text = 'Checking for changes' } }
    if (& $has 'waiting') { return @{ Icon = 'idle'; Text = 'Waiting' } }
    if ((& $has 'synced') -or (& $has 'mounted')) { return @{ Icon = 'synced'; Text = 'Up to date' } }
    @{ Icon = 'idle'; Text = 'Idle' }
}
function Get-JobText($j) {
    switch ($j.state) {
        'waiting' {
            $t = 'Waiting for the system to settle'
            if ($j.until) { $t += " (until $(([datetime]$j.until).ToString('HH:mm:ss')))" }
            $t
        }
        'checking' { 'Checking for changes' }
        { $_ -in 'failed', 'paused' } { if ($j.message) { "$($j.state): $($j.message)" } else { $j.state } }
        default { if ($j.message) { "$($j.state): $($j.message)" } else { $j.state } }
    }
}
$opText = @{ new = 'New'; changed = 'Changed'; deleted = 'Deleted'; moved = 'Moved'; renamed = 'Renamed' }
$busy = 'uploading', 'downloading', 'syncing', 'checking'

# --- Rendering ---------------------------------------------------------------
$script:view = $null
$script:hist = @()
$script:err = $null

function Update-Popup {
    $panel.SuspendLayout()
    foreach ($ctl in @($panel.Controls)) { $panel.Controls.Remove($ctl); $ctl.Dispose() }
    $panel.Width = $width

    if ($script:err) {
        New-Label 'FoxCloud' $fSmall $cDim 0 0 | Out-Null
        New-Label "Can't run foxcloud" $fTitle $cFg 0 6 | Out-Null
        New-Label $script:err $fSmall $cErr 0 0 | Out-Null
        $panel.ResumeLayout(); return
    }
    $view = $script:view
    if (-not $view) { New-Label 'Loading…' $fBody $cDim | Out-Null; $panel.ResumeLayout(); return }
    $sum = Get-Summary $view

    # Header
    $hdr = New-Object Windows.Forms.FlowLayoutPanel
    $hdr.FlowDirection = 'LeftToRight'; $hdr.WrapContents = $false; $hdr.AutoSize = $true; $hdr.BackColor = $cBg
    $hdr.Margin = New-Object Windows.Forms.Padding 0, 0, 0, 12
    $panel.Controls.Add($hdr)
    $pic = New-Object Windows.Forms.PictureBox
    $pic.Size = New-Object Drawing.Size 36, 36; $pic.SizeMode = 'Zoom'; $pic.BackColor = $cBg
    $pic.Margin = New-Object Windows.Forms.Padding 0, 0, 12, 0
    $pic.Image = Get-Icon $sum.Icon
    $hdr.Controls.Add($pic)
    $col = New-Object Windows.Forms.FlowLayoutPanel
    $col.FlowDirection = 'TopDown'; $col.WrapContents = $false; $col.AutoSize = $true; $col.BackColor = $cBg
    $col.Margin = New-Object Windows.Forms.Padding 0
    $hdr.Controls.Add($col)
    New-Label 'FoxCloud' $fSmall $cDim 0 0 $col $cBg ($width - 48) | Out-Null
    New-Label $sum.Text $fTitle $cFg 0 0 $col $cBg ($width - 48) | Out-Null

    if ($view.update) {
        New-Label "Update available: $($view.update) (run foxcloud update)" $fSmall $cWarn 0 10 | Out-Null
    }
    if (-not $view.running) {
        New-Label 'Start it with: Start-ScheduledTask -TaskName "FoxCloud daemon"' $fSmall $cDim 0 10 | Out-Null
    }

    # A card per job
    foreach ($j in $view.jobs) {
        $card = New-Object Windows.Forms.FlowLayoutPanel
        $card.FlowDirection = 'TopDown'; $card.WrapContents = $false; $card.AutoSize = $true; $card.BackColor = $cCard
        $card.Padding = New-Object Windows.Forms.Padding 12, 10, 12, 10
        $card.Width = $width; $card.Margin = New-Object Windows.Forms.Padding 0, 0, 0, 8
        $inner = $width - 24
        $card.Tag = $inner
        $panel.Controls.Add($card)
        $holder = New-Object Windows.Forms.Panel
        $holder.Width = $inner; $holder.AutoSize = $true; $holder.BackColor = $cCard
        $holder.Margin = New-Object Windows.Forms.Padding 0
        $card.Controls.Add($holder)
        $flow = New-Object Windows.Forms.FlowLayoutPanel
        $flow.FlowDirection = 'TopDown'; $flow.WrapContents = $false; $flow.AutoSize = $true; $flow.BackColor = $cCard
        $flow.Width = $inner; $flow.Margin = New-Object Windows.Forms.Padding 0
        $holder.Controls.Add($flow)
        $flow.MaximumSize = New-Object Drawing.Size $inner, 0

        $name = $j.job; if ($j.role) { $name += "  ·  $($j.role)" }
        New-Label $name $fBold $cFg 0 2 $flow $cCard $inner | Out-Null
        $col = if ($j.state -eq 'failed') { $cErr } elseif ($j.state -eq 'paused') { $cWarn } else { $cDim }
        New-Label (Get-JobText $j) $fSmall $col 0 4 $flow $cCard $inner | Out-Null
        if ($j.conflicts -gt 0) {
            New-Label "$($j.conflicts) file(s) have conflict copies" $fSmall $cWarn 0 4 $flow $cCard $inner | Out-Null
        }
        $row = New-Object Windows.Forms.FlowLayoutPanel
        $row.FlowDirection = 'LeftToRight'; $row.WrapContents = $false; $row.AutoSize = $true; $row.BackColor = $cCard
        $row.Margin = New-Object Windows.Forms.Padding 0, 4, 0, 0
        $flow.Controls.Add($row)
        $jobName = $j.job
        if ($j.state -in $busy) {
            $row.Controls.Add((New-Button 'Stop' 70 { [Cli]::Run($form, $Bin, "stop `"$jobName`"", [Action[string, int]]{ param($o, $x) }) | Out-Null }.GetNewClosure()))
        } else {
            $row.Controls.Add((New-Button 'Sync now' 84 { [Cli]::Run($form, $Bin, "sync-now `"$jobName`"", [Action[string, int]]{ param($o, $x) }) | Out-Null }.GetNewClosure()))
        }
        if ($j.state -eq 'paused') {
            $row.Controls.Add((New-Button 'Resume' 74 { [Cli]::Run($form, $Bin, "resume `"$jobName`"", [Action[string, int]]{ param($o, $x) }) | Out-Null }.GetNewClosure()))
        }
        if ($j.conflicts -gt 0) {
            $row.Controls.Add((New-Button 'Resolve…' 80 {
                # The resolver is interactive, so it gets a terminal of its own.
                Start-Process cmd.exe -ArgumentList '/k', "`"$Bin`" conflicts list"
                $form.Close()
            }.GetNewClosure()))
        }
    }

    # Recent changes
    if ($script:hist.Count -gt 0) {
        New-Label 'Recent changes' $fBold $cFg 8 6 | Out-Null
        foreach ($e in ($script:hist | Select-Object -First 12)) {
            $verb = $opText[$e.op]; if (-not $verb) { $verb = $e.op }
            $path = $e.p; if ($e.to) { $path += " → $($e.to)" }
            $arrow = switch ($e.way) { 'Up' { '↑ ' } 'Down' { '↓ ' } default { '' } }
            $line = New-Object Windows.Forms.FlowLayoutPanel
            $line.FlowDirection = 'TopDown'; $line.WrapContents = $false; $line.AutoSize = $true; $line.BackColor = $cBg
            $line.Margin = New-Object Windows.Forms.Padding 0, 0, 0, 5
            $panel.Controls.Add($line)
            $l = New-Label "$arrow$verb  $path" $fSmall $cFg 0 0 $line
            $l.AutoEllipsis = $true
            $w = $e.way; if ($e.m) { $w = "from $($e.m)" }
            New-Label "$(Format-Ago ([datetime]$e.t))  ·  $($e.job)$(if ($w) { "  ·  $w" })" $fSmall $cDim 0 0 $line | Out-Null
        }
    }
    $panel.ResumeLayout()
    Move-ToCorner
}

function Move-ToCorner {
    $wa = [Windows.Forms.Screen]::PrimaryScreen.WorkingArea
    $form.PerformLayout()
    $form.Location = New-Object Drawing.Point ($wa.Right - $form.Width - 12), ($wa.Bottom - $form.Height - 12)
}

# --- Event-driven feeds: each run blocks until the state differs -------------
$script:procs = @{}
function Watch-Status([string]$rev) {
    $a = if ($rev) { "status-json -wait $rev" } else { 'status-json' }
    $script:procs.status = [Cli]::Run($form, $Bin, $a, [Action[string, int]]{
        param($out, $code)
        if ($form.IsDisposed -or $script:closing) { return }
        try { $v = $out | ConvertFrom-Json } catch { $v = $null }
        if (-not $v) { $script:err = "status-json exited with $code"; Update-Popup; return }
        $script:err = $null; $script:view = $v; Update-Popup
        Watch-Status $v.rev
    })
}
function Watch-History([string]$rev) {
    $a = if ($rev) { "history-json -n 12 -wait $rev" } else { 'history-json -n 12' }
    $script:procs.hist = [Cli]::Run($form, $Bin, $a, [Action[string, int]]{
        param($out, $code)
        if ($form.IsDisposed -or $script:closing) { return }
        try { $h = $out | ConvertFrom-Json } catch { $h = $null }
        if (-not $h) { return }
        $script:hist = @($h.entries); Update-Popup
        Watch-History $h.rev
    })
}

$script:closing = $false
$form.Add_FormClosing({
    $script:closing = $true
    foreach ($p in $script:procs.Values) { try { if (-not $p.HasExited) { $p.Kill() } } catch { } }
})
$form.Add_Deactivate({ $form.Close() })
$form.Add_KeyDown({ if ($_.KeyCode -eq 'Escape') { $form.Close() } })
$form.KeyPreview = $true
$form.Add_Shown({ $form.Activate(); [Win]::SetForegroundWindow($form.Handle) | Out-Null })

Update-Popup
Watch-Status ''
Watch-History ''
[Windows.Forms.Application]::Run($form)
