while ($true) {
  Clear-Host
  try {
    $s = Invoke-RestMethod -Uri "http://localhost:8400/api/stats" -ErrorAction Stop
    $f = Invoke-RestMethod -Uri "http://localhost:8400/api/fills" -ErrorAction Stop
    
    $runStatus = if($s.running){"RUNNING"}else{"STOPPED"}
    $bal = [string]::Format("{0:N2}", ($s.pnl.bankroll + $s.pnl.realized))
    $tot = [string]::Format("{0:N2}", ($s.pnl.bankroll + $s.pnl.total))
    $rpnl = [string]::Format("{0:+0.00;-0.00}", $s.pnl.realized)
    $upnl = [string]::Format("{0:+0.00;-0.00}", $s.pnl.unrealized)
    $tpnl = [string]::Format("{0:+0.00;-0.00}", $s.pnl.total)
    
    Write-Output "============================================================"
    Write-Output "  SCALP5 TERMINAL  |  $(Get-Date -f 'HH:mm:ss')  |  $runStatus"
    Write-Output "============================================================"
    Write-Output ("  Available=$" + $bal + "  Total=$" + $tot)
    Write-Output ("  PnL  Real=" + $rpnl + "  Unreal=" + $upnl + "  TOTAL=" + $tpnl)
    Write-Output ("  Strat=" + $s.strategy + "  Fills=" + $s.pnl.fill_count + "  Fee=" + $s.pnl.fee_rate + "%")
    Write-Output ("  Book: Bid=" + $s.best_bid + "  Ask=" + $s.best_ask + "  Spread=" + [math]::Round($s.spread,4))
    Write-Output ("")
    Write-Output ("--- ORDERS (" + $s.open_orders.Length + ") ---")
    Write-Output ("  ID   Side  Price     Filled/Size      PnL     Model  Status")
    Write-Output ("  " + ("-" * 60))
    foreach($o in $s.open_orders) {
      $mid = $s.midpoints.($o.token_id)
      $pnl = 0
      if($mid) {
        if($o.side -eq 'BUY'){ $pnl = ($mid - $o.price) * $o.filled }
        else { $pnl = ($o.price - $mid) * $o.filled }
      }
      $pStr = [string]::Format("{0:+0.00;-0.00}", $pnl)
      $padded = $pStr.PadLeft(7)
      Write-Output ("  " + $o.id.PadRight(3) + " " + $o.side.PadRight(4) + " " + [string]::Format("{0:F4}", $o.price).PadRight(9) + " " + [string]::Format("{0:F2}", $o.filled).PadLeft(6) + "/" + [string]::Format("{0:F2}", $o.size).PadRight(5) + " $" + $padded + "  " + $o.model_tag.PadRight(5) + " " + $o.status)
    }
    Write-Output ("")
    Write-Output ("--- FILLS (last 5) ---")
    $f | Select-Object -Last 5 | ForEach-Object {
      $t = if($_.time -and $_.time.Length -ge 19){$_.time.Substring(11,8)}else{""}
      Write-Output ("  " + $_.order_id.PadRight(3) + " " + $_.side.PadRight(4) + " @" + [string]::Format("{0:F4}", $_.price) + "  x" + [string]::Format("{0:F2}", $_.size).PadLeft(6) + "  " + $t)
    }
    Write-Output ""
    Write-Output "  Press Ctrl+C to exit"
    
  } catch {
    Write-Output "API error: $_"
  }
  Start-Sleep -Seconds 1
}
