$backendPath = "D:\claude-code\coai\platform\backend"
$frontendPath = "D:\claude-code\coai\frontend"

# Kill existing processes on backend/frontend ports
netstat -ano | findstr ":3003" | ForEach-Object {
    $pid = ($_ -split '\s+')[-1]
    if ($pid -match '^\d+$') { Stop-Process -Id ([int]$pid) -Force -ErrorAction SilentlyContinue }
}

Start-Sleep -Seconds 2

# Start backend
Start-Process -FilePath "$backendPath\bin\server.exe" -WorkingDirectory $backendPath -WindowStyle Hidden
Write-Host "Backend started on port 3003"

Start-Sleep -Seconds 3

# Start frontend
Start-Process -FilePath "cmd" -ArgumentList "/c", "cd /d $frontendPath && npm run dev" -WindowStyle Hidden
Write-Host "Frontend starting..."

Start-Sleep -Seconds 5
netstat -ano | findstr ":3003"

Write-Host "Done. Backend: http://localhost:3003"