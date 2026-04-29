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
  private trackerStarted = false
  private trackerStop = false
  private trackerReq: http.ClientRequest | null = null
  private waiters: Array<(sid: string) => void> = []
  // Last time the tracker observed an event whose payload carried a
  // sessionID. /event is instance-scoped so any such event implies the
  // attach client / embedded TUI on this server is the one driving
  // activity. Initialised to startup time so the first 30 min counts
  // as a grace period (gives attach time to make its first noise).
  private lastAttachActivityAt: number
  private idleSuicideTimer: NodeJS.Timeout | null = null

  constructor(serveURL: string, startupTime?: number) {
    this.serveURL = serveURL
    this.startupTime = startupTime || Date.now()
    this.lastAttachActivityAt = this.startupTime
  }

  // lockSession is the primary entry point used by select_project. It
  // returns the session ID currently focused by this server's attach
  // client (or embedded TUI). On every subsequent broadcast we use
  // getLatestSession() which returns the *live-tracked* session ID,
  // automatically following the user across session switches.
  //
  // Strategy:
  //   1. Spawn a long-running SSE tracker on /event (idempotent).
  //      /event is instance-scoped — it ONLY receives events generated
  //      by this server's own attach client / embedded TUI. Every
  //      message.* / session.* event payload carries the sessionID
  //      that the user is currently interacting with.
  //   2. Wait up to ~3 s for the tracker to observe the first event
  //      (when select_project runs, the user has just submitted the
  //      LLM prompt that triggered this tool, so the stream is hot).
  //   3. Fall back to /session latest-updated heuristic if the tracker
  //      sees nothing in time (rare: requires the attach client to be
  //      completely idle and a stale storage list).
  async lockSession(): Promise<string | null> {
    this.startSessionTracker()
    if (this.cachedSessionId) {
      return this.cachedSessionId
    }
    const fromTracker = await this.waitForFirstSession(2500)
    if (fromTracker) {
      console.error('[OpenCode] Locked session via SSE tracker:', fromTracker)
      return fromTracker
    }
    const fromList = await this.findLatestSession()
    if (fromList) {
      this.cachedSessionId = fromList
      console.error(
        '[OpenCode] Locked session via latest-updated fallback:',
        fromList,
      )
    }
    return fromList
  }

  // Background SSE listener that keeps cachedSessionId in sync with the
  // attach client's currently-focused session. Reconnects automatically
  // on stream end / error. Idempotent: calling repeatedly is safe.
  startSessionTracker(): void {
    if (this.trackerStarted) return
    this.trackerStarted = true
    this.trackerStop = false
    this.connectTracker()
  }

  // Stop the background tracker. Call on shutdown.
  stopSessionTracker(): void {
    this.trackerStop = true
    this.trackerStarted = false
    if (this.trackerReq) {
      try {
        this.trackerReq.destroy()
      } catch {
        // ignore
      }
      this.trackerReq = null
    }
  }

  private connectTracker(): void {
    if (this.trackerStop) return
    const url = new URL('/event', this.serveURL)
    const req = http.get(
      url,
      { headers: { Accept: 'text/event-stream' } },
      (res) => {
        if (res.statusCode && res.statusCode >= 400) {
          res.resume()
          this.scheduleReconnect()
          return
        }
        res.setEncoding('utf8')
        let buf = ''
        res.on('data', (chunk: string) => {
          buf += chunk
          let idx
          while ((idx = buf.indexOf('\n\n')) >= 0) {
            const block = buf.slice(0, idx)
            buf = buf.slice(idx + 2)
            const m = /^data:\s*(\{.*\})\s*$/m.exec(block)
            if (!m) continue
            let ev: any
            try {
              ev = JSON.parse(m[1])
            } catch {
              continue
            }
            const sid = ev?.properties?.sessionID
            if (sid && typeof sid === 'string') {
              this.updateCachedSession(sid)
            }
          }
        })
        res.on('end', () => this.scheduleReconnect())
        res.on('error', () => this.scheduleReconnect())
        res.on('close', () => this.scheduleReconnect())
      },
    )
    req.on('error', () => this.scheduleReconnect())
    req.on('timeout', () => {
      req.destroy()
      this.scheduleReconnect()
    })
    this.trackerReq = req
  }

  private scheduleReconnect(): void {
    this.trackerReq = null
    if (this.trackerStop) return
    setTimeout(() => this.connectTracker(), 1000)
  }

  private updateCachedSession(sid: string): void {
    // Any sessionID-bearing event coming through /event proves the
    // attach client is alive and triggering server activity — even if
    // the same session id repeats. Always bump the activity stamp.
    this.lastAttachActivityAt = Date.now()
    if (sid === this.cachedSessionId) return
    const prev = this.cachedSessionId
    this.cachedSessionId = sid
    if (prev === null) {
      console.error('[OpenCode] Tracker locked initial session:', sid)
    } else {
      console.error(
        `[OpenCode] Tracker followed session switch: ${prev} -> ${sid}`,
      )
    }
    // Wake everyone waiting for the first sessionID.
    const pending = this.waiters
    this.waiters = []
    for (const w of pending) w(sid)
  }

  // Heuristic liveness check based on the last SSE event whose payload
  // carried a sessionID. Such events only fire when the attach client
  // (or embedded TUI) is doing something — user input, prompt submit,
  // session switch, LLM streaming, etc. A long quiet stretch strongly
  // suggests the attach client is gone, even though the OpenCode
  // server itself is still running.
  isAttachLikelyAlive(graceMs: number = 30 * 60 * 1000): boolean {
    return Date.now() - this.lastAttachActivityAt < graceMs
  }

  // The age, in ms, of the most recent attach-driven event. Useful for
  // logging and threshold checks.
  attachIdleMs(): number {
    return Date.now() - this.lastAttachActivityAt
  }

  // Idempotent. Starts a periodic check that calls process.exit(0)
  // when no attach activity has been seen for graceMs. The default
  // (30 min) is well above any plausible "user thinking" gap and well
  // below the platform 7 min heartbeat sweep — so once we exit, the
  // backend marks the agent offline within ~7 min, releasing tasks
  // and locks. checkIntervalMs controls how often we recheck.
  startIdleSuicide(
    graceMs: number = 30 * 60 * 1000,
    checkIntervalMs: number = 60 * 1000,
  ): void {
    if (this.idleSuicideTimer) return
    this.idleSuicideTimer = setInterval(() => {
      const idleMs = this.attachIdleMs()
      if (idleMs > graceMs) {
        console.error(
          `[OpenCode] Attach idle for ${Math.round(idleMs / 60000)}min ` +
            `(threshold ${Math.round(graceMs / 60000)}min) — mcp self-exiting`,
        )
        // Tear down the tracker socket cleanly so we don't leave a
        // half-open SSE connection lingering on the server.
        this.stopSessionTracker()
        process.exit(0)
      }
    }, checkIntervalMs)
    if (this.idleSuicideTimer.unref) {
      // Don't keep the event loop alive purely on this timer; mcp
      // also has stdio + the SSE socket as live handles.
      this.idleSuicideTimer.unref()
    }
  }

  stopIdleSuicide(): void {
    if (this.idleSuicideTimer) {
      clearInterval(this.idleSuicideTimer)
      this.idleSuicideTimer = null
    }
  }

  // Used by lockSession() to await the very first observation. Waiters
  // are also resolved on subsequent switches but lockSession only
  // consumes the first.
  private waitForFirstSession(timeoutMs: number): Promise<string | null> {
    return new Promise((resolve) => {
      if (this.cachedSessionId) {
        resolve(this.cachedSessionId)
        return
      }
      let settled = false
      const settle = (val: string | null) => {
        if (settled) return
        settled = true
        resolve(val)
      }
      const timer = setTimeout(() => settle(null), timeoutMs)
      this.waiters.push((sid) => {
        clearTimeout(timer)
        settle(sid)
      })
    })
  }

  get lockedSessionId(): string | null {
    return this.cachedSessionId
  }

  // Returns the live-tracked session id when available, otherwise falls
  // back to a fresh /session lookup. Broadcast injection should always
  // call this so it follows the attach client across session switches.
  async getLatestSession(): Promise<string | null> {
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

  // injectMessage POSTs a single text part into the OpenCode session
  // identified by sessionId. Returns true iff opencode accepted the
  // POST (HTTP 2xx). Returns false on HTTP error, network error, or
  // timeout — callers must check and decide whether to retry / buffer.
  //
  // Earlier this was fire-and-forget (`req.on('error', () => {})`),
  // which combined with the directed-broadcast queue's consume-on-read
  // semantics on the platform side caused silent data loss every time
  // OpenCode was restarting or the session id had changed since the
  // poll fetched the message. The Promise<boolean> shape now lets the
  // MCP poller buffer + retry instead.
  injectMessage(sessionId: string, message: string): Promise<boolean> {
    return new Promise((resolve) => {
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
          const ok = typeof res.statusCode === 'number' && res.statusCode >= 200 && res.statusCode < 300
          // Drain the body so the socket is reusable; we don't care
          // about the parsed response — opencode just echoes the part.
          res.resume()
          res.on('end', () => resolve(ok))
          res.on('error', () => resolve(false))
        },
      )
      req.on('error', (err) => {
        console.error('[OpenCodeClient] injectMessage error session=%s: %s', sessionId, (err as Error).message)
        resolve(false)
      })
      req.on('timeout', () => {
        console.error('[OpenCodeClient] injectMessage timeout session=%s', sessionId)
        req.destroy()
        resolve(false)
      })
      req.write(data)
      req.end()
    })
  }
}
