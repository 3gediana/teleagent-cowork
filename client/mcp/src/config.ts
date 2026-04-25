import fs from 'fs'
import path from 'path'
import os from 'os'

// workdirRoot is the single source of truth for "where does this MCP
// process think its workdir is". Used for per-workdir config, file_sync
// staging, and the .a3c_version pointer.
//
// Resolution order:
//   1. A3C_HOME       — explicit env override; absolute path. Useful
//                       when the launcher (e.g. OpenCode) cannot or
//                       does not want to set the child's cwd.
//   2. process.cwd()  — whatever the launcher started us in.
//
// We intentionally do NOT fall back to the MCP install dir or the
// user's home: both led to cross-workdir state leaks before.
export function workdirRoot(): string {
  if (process.env.A3C_HOME && process.env.A3C_HOME.trim() !== '') {
    return path.resolve(process.env.A3C_HOME)
  }
  return process.cwd()
}

interface A3CConfig {
  access_key?: string
  project_id?: string
}

function workdirConfigDir(): string {
  return path.join(workdirRoot(), '.a3c')
}

function workdirConfigFile(): string {
  return path.join(workdirConfigDir(), 'config.json')
}

function homeConfigFile(): string {
  return path.join(os.homedir(), '.a3c', 'config.json')
}

// loadConfig reads the per-workdir config first; if absent, falls back
// to the legacy ~/.a3c/config.json so existing users still log in
// cleanly the first time they enter a fresh workdir. Once they call
// select_project, saveConfig writes the per-workdir file and the home
// config is no longer consulted from this workdir.
export function loadConfig(): A3CConfig {
  try {
    const p = workdirConfigFile()
    if (fs.existsSync(p)) {
      return JSON.parse(fs.readFileSync(p, 'utf-8')) as A3CConfig
    }
  } catch (_) {
    // fall through to the home fallback rather than crashing the MCP
  }

  try {
    const p = homeConfigFile()
    if (fs.existsSync(p)) {
      return JSON.parse(fs.readFileSync(p, 'utf-8')) as A3CConfig
    }
  } catch (_) {
    // ignore — empty config is a legitimate first-run state
  }

  return {}
}

// saveConfig always writes the per-workdir file. We never write back to
// ~/.a3c from here: that file is treated as a read-only seed so two
// workdirs cannot stomp on each other's project_id.
export function saveConfig(config: Partial<A3CConfig>) {
  try {
    const dir = workdirConfigDir()
    if (!fs.existsSync(dir)) {
      fs.mkdirSync(dir, { recursive: true })
    }
    const merged = { ...loadConfig(), ...config }
    fs.writeFileSync(workdirConfigFile(), JSON.stringify(merged, null, 2))
  } catch (e) {
    console.error('[Config] Error saving:', e)
  }
}
