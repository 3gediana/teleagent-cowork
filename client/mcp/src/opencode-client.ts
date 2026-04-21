import http from 'http'

interface Session {
  id: string
  title: string
  time: { updated: number }
}

export class OpenCodeClient {
  private serveURL: string
  private startupTime: number
  private cachedSessionId: string | null = null

  constructor(serveURL: string, startupTime?: number) {
    this.serveURL = serveURL
    this.startupTime = startupTime || Date.now()
  }

  // Lock the session ID for this platform connection. Called once at select_project.
  // After locking, all broadcasts inject into this specific session.
  async lockSession(): Promise<string | null> {
    const sessionId = await this.findLatestSession()
    if (sessionId) {
      this.cachedSessionId = sessionId
      console.error('[OpenCode] Locked session ID:', sessionId)
    }
    return sessionId
  }

  get lockedSessionId(): string | null {
    return this.cachedSessionId
  }

  async getLatestSession(): Promise<string | null> {
    // If we have a locked session, use it directly
    if (this.cachedSessionId) {
      return this.cachedSessionId
    }
    return this.findLatestSession()
  }

  async findLatestSession(): Promise<string | null> {
    return new Promise((resolve) => {
      const url = new URL('/session', this.serveURL)
      const req = http.get(url, { timeout: 5000 }, (res) => {
        let body = ''
        res.on('data', (chunk) => (body += chunk))
        res.on('end', () => {
          try {
            const sessions: Session[] = JSON.parse(body)
            if (!sessions || sessions.length === 0) {
              resolve(null)
              return
            }
            // Filter to sessions updated after MCP startup (belongs to this instance)
            const current = sessions.filter((s) => s.time.updated >= this.startupTime - 5000)
            if (current.length === 0) {
              // Fallback: return absolute latest
              const sorted = sessions.sort((a, b) => b.time.updated - a.time.updated)
              resolve(sorted[0].id)
              return
            }
            const sorted = current.sort((a, b) => b.time.updated - a.time.updated)
            resolve(sorted[0].id)
          } catch {
            resolve(null)
          }
        })
      })
      req.on('error', () => resolve(null))
      req.on('timeout', () => {
        req.destroy()
        resolve(null)
      })
    })
  }

  injectMessage(sessionId: string, message: string): void {
    const data = JSON.stringify({
      parts: [{ type: 'text', text: message }],
    })
    const url = new URL(`/session/${sessionId}/message`, this.serveURL)
    const req = http.request(
      url,
      {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Content-Length': Buffer.byteLength(data),
        },
        timeout: 3000,
      },
      (res) => {
        res.resume()
        res.on('end', () => {})
      },
    )
    req.on('error', () => {})
    req.on('timeout', () => req.destroy())
    req.write(data)
    req.end()
  }
}
