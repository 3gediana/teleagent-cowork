# OpenCode integration

How to wire the A3C MCP server into [OpenCode](https://opencode.ai) so that
each workdir gets its own A3C configuration, active project, and staging
snapshots — without cross-pollution between workdirs on the same machine.

This is a deployment-side document. Agents do not read it; humans setting up
A3C for their team do.

## Why workdir-level

A3C tracks the active project and access key **per workdir**. Two workdirs
on the same machine can hold two different projects, and the MCP client
will not let them overwrite each other's `project_id`. For this to work,
OpenCode must launch the a3c MCP server with one of:

- Its `cwd` set to the workdir (the default behaviour, see Option A), **or**
- An explicit `A3C_HOME` env var pointing at the workdir (Option B)

Internally, a3c resolves the workdir via `workdirRoot()` in
`client/mcp/src/config.ts`: `A3C_HOME` wins; otherwise `process.cwd()`.

## Option A — cwd-based (simplest)

OpenCode launches MCP child processes in its own `cwd` by default. So if you:

1. `cd <workdir>`
2. Run `opencode`

…then the a3c MCP server's `process.cwd()` is `<workdir>`, and a3c reads/writes
config and staging inside `<workdir>/.a3c/…`.

Place this `opencode.json` at the root of `<workdir>`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "a3c": {
      "type": "local",
      "command": ["node", "/absolute/path/to/a3c/client/mcp/dist/index.js"],
      "enabled": true,
      "environment": {
        "A3C_PLATFORM_URL": "http://localhost:8080"
      }
    }
  }
}
```

OpenCode looks for `opencode.json` starting from the directory you launched
it in and walks up to the nearest git repo. Putting the file in your workdir
root means it is picked up automatically and can be checked into git.

The path inside `command` is the absolute filesystem path of the built MCP
server (`client/mcp/dist/index.js`). If you bundle a3c via `npm pack` or
publish it, swap it for `["npx", "-y", "@a3c/mcp-server"]` or whatever your
distribution uses.

## Option B — explicit `A3C_HOME` (most robust)

If your launcher does anything non-trivial with `cwd` (CI, IDE plugins,
`opencode run` from a different directory, multiplexed terminals), pin a3c's
workdir explicitly via env:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "a3c": {
      "type": "local",
      "command": ["node", "/absolute/path/to/a3c/client/mcp/dist/index.js"],
      "enabled": true,
      "environment": {
        "A3C_HOME": "/absolute/path/to/this/workdir",
        "A3C_PLATFORM_URL": "http://localhost:8080"
      }
    }
  }
}
```

`A3C_HOME` overrides `process.cwd()` and locks all per-workdir state to that
absolute path regardless of how OpenCode was started.

## Other useful env vars

| Var | Default | What it does |
|---|---|---|
| `A3C_HOME` | `process.cwd()` | Overrides workdir resolution (Option B) |
| `A3C_PLATFORM_URL` | `http://localhost:3003` | Where the A3C backend is reachable |
| `A3C_ACCESS_KEY` | (config file) | Bypass the per-workdir config; useful in CI |
| `A3C_PROJECT_ID` | (config file) | Pre-select a project on startup |
| `A3C_STAGING_DIR` | `workdirRoot()` | Override staging root only (rare; tests / CI) |
| `OPENCODE_SERVE_URL` | `http://127.0.0.1:4096` | Where this client's local opencode serve listens (for broadcast injection) |

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

If you instead see `~/.a3c/config.json` getting *modified* (rather than just
read), the launcher is starting the MCP in the wrong cwd and not passing
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
    "a3c": { "enabled": false, ... }
  }
}
```

## Sandboxing reminder

The MCP client treats the workdir as the agent's sandbox. Project files live
under `<workdir>/.a3c_staging/<project_id>/full/…`; agents read and write
only inside that subtree. The other `.a3c*` paths are MCP-managed metadata
and should be left to the client.
