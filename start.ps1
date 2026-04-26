$ErrorActionPreference = 'Stop'

# Anchor everything to the script's own directory so the script works
# wherever the project gets cloned. The previous version hard-coded
# D:\claude-code\coai\... (wrong dir name) and pointed at bin/server.exe
# which doesn't exist — `go build ./cmd/server` writes server.exe at
# the package root.
$root = $PSScriptRoot
$backendPath  = Join-Path $root 'platform\backend'
$frontendPath = Join-Path $root 'platform\frontend'
$serverExe    = Join-Path $backendPath 'server.exe'

# Kill anything still listening on the backend port (3003).
netstat -ano | findstr ":3003" | ForEach-Object {
    $procPid = ($_ -split '\s+')[-1]
    if ($procPid -match '^\d+$') {
        Stop-Process -Id ([int]$procPid) -Force -ErrorAction SilentlyContinue
    }
}

Start-Sleep -Seconds 2

if (-not (Test-Path $serverExe)) {
    Write-Error "Backend binary not found at $serverExe. Run ``go build ./cmd/server`` inside $backendPath first."
    exit 1
}

# Start backend.
Start-Process -FilePath $serverExe -WorkingDirectory $backendPath -WindowStyle Hidden
Write-Host "Backend started on port 3003"

Start-Sleep -Seconds 3

# Start frontend (expects ``npm install`` already done in $frontendPath).
Start-Process -FilePath 'cmd' -ArgumentList '/c', "cd /d $frontendPath && npm run dev" -WindowStyle Hidden
Write-Host "Frontend starting..."

Start-Sleep -Seconds 5
netstat -ano | findstr ":3003"

Write-Host "Done. Backend: http://localhost:3003"