# Local dev quick reference (Windows, this machine)

> Notes for spinning up A3C on the current dev box. Paths are specific
> to the machine that committed this file; adjust for yours.

## Binary locations

| Tool                  | Path                                    |
|-----------------------|-----------------------------------------|
| MySQL 8.0.39 server   | `D:\mysql\bin\mysqld.exe`               |
| MySQL client          | `D:\mysql\bin\mysql.exe`                |
| MySQL data dir        | `D:\mysql\data`                         |
| Redis server          | `D:\redis\redis-server.exe`             |
| Redis CLI             | `D:\redis\redis-cli.exe`                |
| Go backend entrypoint | `platform/backend/cmd/server`           |
| Loopcheck CLI         | `platform/backend/experiments/loopcheck`|
| Loopseed CLI          | `platform/backend/experiments/loopseed` |
| Evobench CLI          | `platform/backend/cmd/evobench`         |

## Start MySQL

MySQL on this box is **not installed as a Windows service** — start it
manually and leave the process running in the background:

```powershell
Start-Process -FilePath D:\mysql\bin\mysqld.exe `
  -ArgumentList '--console' `
  -RedirectStandardError D:\claude-code\coai2\mysql.log `
  -WindowStyle Hidden
```

Sanity-check port 3306:

```powershell
Get-NetTCPConnection -LocalPort 3306 -State Listen
```

## Start Redis

Backend also requires Redis (broadcast buffer, rate limits):

```powershell
Start-Process -FilePath D:\redis\redis-server.exe `
  -WorkingDirectory D:\redis `
  -RedirectStandardOutput D:\claude-code\coai2\redis.log `
  -WindowStyle Hidden
```

Sanity-check:

```powershell
Get-NetTCPConnection -LocalPort 6379 -State Listen
```

## Reset the `a3c` database from scratch

```powershell
& D:\mysql\bin\mysql.exe -u root -e "DROP DATABASE IF EXISTS a3c; CREATE DATABASE a3c CHARACTER SET utf8mb4;"
```

The backend's AutoMigrate recreates every table on first boot; no
manual schema import needed.

## Run the backend

The server reads DB name from `A3C_DB_NAME` (default `a3c`). Launch
non-blocking and tail logs:

```powershell
$env:A3C_DB_NAME = 'a3c'
Start-Process -FilePath go `
  -ArgumentList 'run','./cmd/server' `
  -WorkingDirectory D:\claude-code\coai2\platform\backend `
  -RedirectStandardError D:\claude-code\coai2\server.log `
  -WindowStyle Hidden
```

Health check once the port is live:

```powershell
(Invoke-WebRequest http://localhost:3003/health).Content
```

## Stop everything cleanly

```powershell
Get-NetTCPConnection -LocalPort 3003 -State Listen -ErrorAction SilentlyContinue |
  ForEach-Object { Stop-Process -Id $_.OwningProcess -Force }
Get-NetTCPConnection -LocalPort 3306 -State Listen -ErrorAction SilentlyContinue |
  ForEach-Object { Stop-Process -Id $_.OwningProcess -Force }
```

## First-run auth bootstrap

After the tightening in `auth: guard /project/* + record creator` and
`auth: gate is_human=true on /agent/register`, the flow is:

1. `POST /api/v1/agent/register` with `{ "name":"you", "is_human": true }`
   — allowed only when the DB has zero humans yet.
2. Capture `access_key` from the response.
3. `POST /api/v1/auth/login` with `{ "key": "<access_key>" }`.
4. Use `Authorization: Bearer <access_key>` for everything else —
   `/project/*`, `/task/*`, `/chief/*`, `/internal/*`, `/events` (key
   goes in the query string for SSE since EventSource can't set
   headers).
