param(
    [string]$BotDir = ".",
    [int]$PollSeconds = 3
)

$botExe = Join-Path $BotDir "bot.exe"
$botName = "bot"
$outFile = Join-Path $BotDir "bot_out.log"
$errFile = Join-Path $BotDir "bot_err.log"

Write-Host "=== Bot Watchdog ===" -ForegroundColor Cyan
Write-Host "Binary: $botExe"
Write-Host "Poll interval: ${PollSeconds}s"
Write-Host "Logs: $outFile"
Write-Host "Press Ctrl+C to stop watchdog`n"

function Start-Bot {
    $ts = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    Add-Content -Path $outFile -Value "`n=== Watchdog restart at $ts ==="
    Add-Content -Path $errFile -Value "`n=== Watchdog restart at $ts ==="
    $p = Start-Process -FilePath $botExe -WorkingDirectory $BotDir -WindowStyle Hidden -RedirectStandardOutput $outFile -RedirectStandardError $errFile -PassThru
    Write-Host "$(Get-Date -Format 'HH:mm:ss') Started bot (PID: $($p.Id))" -ForegroundColor Green
    return $p
}

$proc = $null
while ($true) {
    $current = Get-Process -Name $botName -ErrorAction SilentlyContinue
    if (-not $current) {
        Write-Host "$(Get-Date -Format 'HH:mm:ss') Bot not running" -ForegroundColor Yellow
        $proc = Start-Bot
    } elseif ($proc -and $current.Id -ne $proc.Id) {
        Write-Host "$(Get-Date -Format 'HH:mm:ss') Bot PID changed ($($current.Id)), tracking new" -ForegroundColor Yellow
        $proc = $current
    } elseif (-not $proc) {
        $proc = $current
    }
    Start-Sleep -Seconds $PollSeconds
}
