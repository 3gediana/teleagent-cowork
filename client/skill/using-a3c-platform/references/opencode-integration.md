# OpenCode integration

How to wire the A3C MCP server into [OpenCode](https://opencode.ai) so that
each workdir gets its own A3C configuration, active project, and staging
snapshots — without cross-pollution between workdirs on the same machine,
and without anyone having to hard-code their machine's filesystem layout into
shared config.

This is a deployment-side document. Agents do not read it; humans setting up
A3C for their team do.

## Install the client

The A3C MCP server is shipped as an npm package whose `bin` exposes the
`a3c-mcp` command. Pick whichever install flow matches how you got the code.

### From a checkout (recommended for now)

If you cloned this repo:

```sh
cd client/mcp
npm install        # installs deps; the prepare hook also runs `npm run build`
npm link           # registers `a3c-mcp` on this machine's PATH
```

Verify:

```sh
a3c-mcp --version  # any output (or stdio idle, which is normal for MCP)
which a3c-mcp      # POSIX
Get-Command a3c-mcp # PowerShell
```

`npm link` is idempotent — re-run it after pulling new commits to refresh the
linked dist. To remove: `npm unlink -g @a3c/mcp-server`.

### From an npm-published tarball (future)

```sh
npm install -g @a3c/mcp-server
```

This is what your `opencode.json` should target once the package is published.
Until then, use `npm link` from a checkout — the resulting `a3c-mcp` command
behaves identically.

## Wire it into OpenCode

A3C tracks the active project and access key **per workdir**: two workdirs
on the same machine can hold two different projects without overwriting each
other's `project_id`. To make this work, OpenCode just needs to launch the
a3c MCP server in the right cwd; the bin shim handles the rest.

### Option A — cwd-based (simplest, recommended)

OpenCode launches MCP child processes in its own `cwd` by default, so:

1. `cd <workdir>`
2. Run `opencode`

…then the a3c MCP server's `process.cwd()` is `<workdir>`, and a3c reads/writes
config and staging inside `<workdir>/.a3c/…` and `<workdir>/.a3c_staging/…`.

Place this `opencode.json` at the root of `<workdir>`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "a3c": {
      "type": "local",
      "command": ["a3c-mcp"],
      "enabled": true,
      "environment": {
        "A3C_PLATFORM_URL": "http://localhost:3003"
      }
    }
  }
}
```

OpenCode looks for `opencode.json` starting from the directory you launched
it in and walks up to the nearest git repo. Putting the file in the workdir
root (or repo root) means it's picked up automatically and can be checked
into git for the team.

No paths to the MCP install location anywhere in this config — `a3c-mcp`
is resolved through PATH, so this same `opencode.json` works on every
teammate's machine regardless of where they cloned the A3C repo.

### Option B — explicit `A3C_HOME` (most robust)

If your launcher does anything non-trivial with `cwd` (CI, IDE plugins,
`opencode run` from a different directory), pin a3c's workdir explicitly:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "a3c": {
      "type": "local",
      "command": ["a3c-mcp"],
      "enabled": true,
      "environment": {
        "A3C_HOME": "{env:A3C_WORKDIR}",
        "A3C_PLATFORM_URL": "http://localhost:3003"
      }
    }
  }
}
```

`A3C_HOME` overrides `process.cwd()` and locks all per-workdir state to that
absolute path, regardless of how OpenCode was started. Source whatever shell
profile sets `A3C_WORKDIR` before launching opencode.

Avoid baking an absolute path into `A3C_HOME` directly — the whole point of
this document is that `opencode.json` should travel between machines.

## Public deployment via a tunnel

When the A3C platform is exposed publicly through a tunnel (ngrok,
Cloudflared, frp, vite-allowed-hosts entry, etc.), MCP clients and browser
users hit the same URL.

The frontend currently owns the public hostname (vite dev server, port
3303 by default), and vite's built-in proxy forwards every `/api/*` request
to the local backend on port 3003. The MCP server speaks HTTP over
`A3C_PLATFORM_URL` — it does not route through React, but it does land on
the frontend's host because that's where the proxy lives. So MCP and
browser users share one tunnel; the proxy multiplexes them onto the
backend.

Set `A3C_PLATFORM_URL` to the tunnel URL in `opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "a3c": {
      "type": "local",
      "command": ["a3c-mcp"],
      "environment": {
        "A3C_PLATFORM_URL": "https://your-tunnel.example.com"
      }
    }
  }
}
```

Only one tunnel is needed: the one that fronts the React UI. Both browser
and MCP call `https://your-tunnel.example.com/api/v1/...`; vite (or your
reverse proxy in production) routes those calls to the local backend.

Things to double-check:

- **SSE compatibility.** The platform uses Server-Sent Events for real-time
  broadcasts (`/api/v1/.../events`). Some tunneling services buffer or
  close long-lived connections — verify your provider keeps SSE open before
  relying on it.
- **Allowed hosts.** vite refuses unknown hostnames by default. Add your
  tunnel domain to `frontend/vite.config.ts::server.allowedHosts`.
- **Production hosting.** vite's dev server is fine for personal or
  small-team tunnels. For production, build the frontend (`vite build`)
  and serve `dist/` from nginx / Caddy / Traefik, with `/api/v1/*` proxied
  to the backend port (default 3003). The MCP-side config does not change.
- **The frontend does not handle MCP.** MCP is a stdio protocol that runs
  locally inside each user's OpenCode process and dials the backend
  directly. The React UI and the MCP server are independent clients of the
  same backend — neither knows about the other.

## Useful env vars

| Var | Default | What it does |
|---|---|---|
| `A3C_HOME` | `process.cwd()` | Overrides workdir resolution (Option B) |
| `A3C_PLATFORM_URL` | `http://localhost:3003` | Where the A3C backend is reachable |
| `A3C_ACCESS_KEY` | (config file) | Bypass the per-workdir config; useful in CI |
| `A3C_PROJECT_ID` | (config file) | Pre-select a project on startup |
| `A3C_STAGING_DIR` | `workdirRoot()` | Override staging root only (rare; tests / CI) |
| `OPENCODE_SERVE_URL` | `http://127.0.0.1:4096` | Where this client's local opencode serve listens (for broadcast injection) |

All of these can be set in the `environment` block of the MCP entry.

## Verifying the integration

After your first `select_project` call from inside OpenCode, the workdir
should look like:

```
<workdir>/
├── opencode.json                # the config you just wrote
├── .a3c/
│   └── config.json              # written by select_project
├── .a3c_staging/                # appears after the first file_sync
│   └── <project_id>/
│       └── full/
└── .a3c_version                 # appears after the first file_sync
```

If you instead see `~/.a3c/config.json` getting *modified* (not just read),
the launcher is starting the MCP in the wrong cwd and not passing
`A3C_HOME` either — switch to Option B.

## Migrating from the old global config

Before per-workdir support, the MCP client only stored config at
`~/.a3c/config.json`. Two workdirs would constantly overwrite each other's
`project_id`.

The new client reads `~/.a3c/config.json` as a **read-only fallback** the
first time it boots in a fresh workdir, so existing users still log in
cleanly. As soon as you call `select_project` from a workdir, the result is
written to `<workdir>/.a3c/config.json` and the home file is no longer
consulted from that workdir.

To make a clean break: delete `~/.a3c/config.json` after migrating every
workdir you care about. Until then, leaving it in place costs nothing.

## Disabling the a3c MCP server temporarily

`opencode.json` supports `"enabled": false` per server. Use that to suspend
a3c in one workdir without ripping out the config:

```json
{
  "mcp": {
    "a3c": { "enabled": false }
  }
}
```

## Sandboxing reminder

The MCP client treats the workdir as the agent's sandbox. Project files live
under `<workdir>/.a3c_staging/<project_id>/full/…`; agents read and write
only inside that subtree. The other `.a3c*` paths are MCP-managed metadata
and should be left to the client.
