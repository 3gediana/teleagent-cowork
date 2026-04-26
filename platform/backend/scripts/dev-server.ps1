# Reliable dev-server lifecycle for local smoke testing.
#
# Pattern:
#   start | stop | restart | status | tail
#
# The critical difference from `cmd /c ".\server.exe"` is that we use
# Start-Process -PassThru, which hands back a real PID immediately. The
# PID is stashed in .server.pid so every other subshell invocation can
# Stop-Process / query status without re-scanning port 3003.

param(
    [Parameter(Position=0)][ValidateSet('start','stop','restart','status','tail')]
    [string]$Action = 'status',

    [string]$Exe     = '',
    [string]$Log     = '',
    [string]$PidFile = '',
    [hashtable]$Env  = @{ A3C_DB_NAME='a3c'; A3C_OPENCODE_CMD='D:\openclaw\npm\opencode.cmd'; A3C_OPENCODE_ARGS='serve' },
    [int]$Port       = 3003,
    [int]$BootTimeout = 15
)

# $PSScriptRoot is only populated inside the script body, not in the param
# block default expressions. Defer defaulting until here.
if (-not $Exe)     { $Exe     = Join-Path $PSScriptRoot '..\server.exe' }
if (-not $Log)     { $Log     = Join-Path $PSScriptRoot '..\..\..\server.log' }
if (-not $PidFile) { $PidFile = Join-Path $PSScriptRoot '..\.server.pid' }

$Exe     = [IO.Path]::GetFullPath($Exe)
$Log     = [IO.Path]::GetFullPath($Log)
$PidFile = [IO.Path]::GetFullPath($PidFile)

function Read-Pid {
    if (-not (Test-Path $PidFile)) { return $null }
    $raw = (Get-Content $PidFile -ErrorAction SilentlyContinue | Select-Object -First 1).Trim()
    if ([int]::TryParse($raw, [ref]$null)) { return [int]$raw }
    return $null
}

function Is-Alive($procId) {
    if (-not $procId) { return $false }
    return [bool](Get-Process -Id $procId -ErrorAction SilentlyContinue)
}

function Port-Listening($port) {
    [bool](Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
}

switch ($Action) {
    'start' {
        $existing = Read-Pid
        if ($existing -and (Is-Alive $existing)) { Write-Output "already running pid=$existing"; return }
        if (-not $Exe -or -not (Test-Path $Exe)) {
            Write-Error "server.exe not found at $Exe (build first: go build ./cmd/server)"; return
        }
        foreach ($k in $Env.Keys) { [Environment]::SetEnvironmentVariable($k, $Env[$k], 'Process') }
        if (Test-Path $Log) { Remove-Item $Log -Force -ErrorAction SilentlyContinue }
        $p = Start-Process -FilePath $Exe `
            -WorkingDirectory (Split-Path $Exe) `
            -RedirectStandardOutput $Log `
            -RedirectStandardError "$Log.err" `
            -NoNewWindow -PassThru
        $p.Id | Set-Content $PidFile
        Write-Output "started pid=$($p.Id) log=$Log"
        for ($i=0; $i -lt $BootTimeout; $i++) {
            if (Port-Listening $Port) { Write-Output "listening on :$Port after ${i}s"; return }
            Start-Sleep 1
        }
        Write-Warning "pid=$($p.Id) did not start listening within ${BootTimeout}s"
    }
    'stop' {
        $procId = Read-Pid
        if ($procId -and (Is-Alive $procId)) {
            Stop-Process -Id $procId -Force
            Write-Output "stopped pid=$procId"
        } else {
            Write-Output "no running pid in $PidFile"
        }
        Remove-Item $PidFile -Force -ErrorAction SilentlyContinue
        # Belt-and-suspenders: also kill anything listening on the port
        $p = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
        if ($p) { Stop-Process -Id $p.OwningProcess -Force -ErrorAction SilentlyContinue; Write-Output "also killed port-holder pid=$($p.OwningProcess)" }
    }
    'restart' {
        & $PSCommandPath -Action stop  -Exe $Exe -Log $Log -PidFile $PidFile -Port $Port -Env $Env
        Start-Sleep 1
        & $PSCommandPath -Action start -Exe $Exe -Log $Log -PidFile $PidFile -Port $Port -Env $Env -BootTimeout $BootTimeout
    }
    'status' {
        $procId = Read-Pid
        $alive = Is-Alive $procId
        $port  = Port-Listening $Port
        Write-Output "pid_file=$procId alive=$alive port_listening=$port log=$Log"
    }
    'tail' {
        if (Test-Path $Log) { Get-Content $Log -Tail 30 }
        if (Test-Path "$Log.err") { "---STDERR---"; Get-Content "$Log.err" -Tail 15 }
    }
}
