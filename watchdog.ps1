# watchdog.ps1 — keep bot.exe alive forever. Polls every 5s.
param([string]$BotPath = ".\bot.exe", [int]$PollSec = 5)

$BotDir = Split-Path -Parent $BotPath
if (-not $BotDir) { $BotDir = "." }
$ExeName = (Split-Path -Leaf $BotPath) -replace '\.exe$',''

Write-Host "=== Scalp5 Watchdog ==="
Write-Host "Bot: $BotPath | Poll: ${PollSec}s | Press Ctrl+C to stop"

while ($true) {
    $p = Get-Process -Name $ExeName -ErrorAction SilentlyContinue
    if (-not $p) {
        Write-Host "$(Get-Date -Format HH:mm:ss) Bot dead — restarting..."
        try {
            Start-Process -FilePath $BotPath -WorkingDirectory $BotDir -WindowStyle Hidden
        } catch {
            Write-Host "Start failed: $_"
        }
    }
    Start-Sleep -Seconds $PollSec
}
