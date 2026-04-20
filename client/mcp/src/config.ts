import fs from 'fs'
import path from 'path'
import os from 'os'

const CONFIG_DIR = path.join(os.homedir(), '.a3c')
const CONFIG_FILE = path.join(CONFIG_DIR, 'config.json')

interface A3CConfig {
  access_key?: string
  project_id?: string
}

export function loadConfig(): A3CConfig {
  try {
    if (!fs.existsSync(CONFIG_FILE)) {
      console.error('[Config] No config file at', CONFIG_FILE)
      return {}
    }
    const content = fs.readFileSync(CONFIG_FILE, 'utf-8')
    console.error('[Config] Loaded from', CONFIG_FILE, ':', content)
    return JSON.parse(content)
  } catch (e) {
    console.error('[Config] Error loading:', e)
    return {}
  }
}

export function saveConfig(config: Partial<A3CConfig>) {
  try {
    if (!fs.existsSync(CONFIG_DIR)) {
      fs.mkdirSync(CONFIG_DIR, { recursive: true })
    }
    const existing = loadConfig()
    const merged = { ...existing, ...config }
    fs.writeFileSync(CONFIG_FILE, JSON.stringify(merged, null, 2))
    console.error('[Config] Saved to', CONFIG_FILE, ':', JSON.stringify(merged))
  } catch (e) {
    console.error('[Config] Error saving:', e)
  }
}
