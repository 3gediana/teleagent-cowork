import { ApiClient } from './api-client.js'

export class Poller {
  private api: ApiClient
  private pollInterval: NodeJS.Timeout | null = null
  private heartbeatInterval: NodeJS.Timeout | null = null
  private aliveCheckInterval: NodeJS.Timeout | null = null
  private onBroadcast: ((messages: any[]) => void) | null = null
  private running = false
  private parentPid: number | null = null

  constructor(api: ApiClient) {
    this.api = api
  }

  setBroadcastHandler(handler: (messages: any[]) => void) {
    this.onBroadcast = handler
  }

  async start() {
    if (this.running) return
    this.running = true
    this.parentPid = process.ppid

    this.pollInterval = setInterval(async () => {
      try {
        const data = await this.api.poll()
        if (data?.data?.messages?.length > 0 && this.onBroadcast) {
          this.onBroadcast(data.data.messages)
        }
      } catch (e) {
        console.error('[Poller] Poll error:', e)
      }
    }, 5000)

    // Heartbeat slightly before the server's 5-minute timeout window to
    // tolerate network jitter. Also renews active filelocks so long-running
    // tasks don't lose their locks mid-work (server lock TTL = 5 min).
    this.heartbeatInterval = setInterval(async () => {
      try {
        await this.api.heartbeat()
      } catch (e) {
        console.error('[Poller] Heartbeat error:', e)
      }
      try {
        await this.api.renewLocks()
      } catch (e) {
        // Most likely: no project selected yet, or no active locks. Benign.
      }
    }, 3 * 60 * 1000)

    this.aliveCheckInterval = setInterval(() => {
      try {
        if (this.parentPid !== null) {
          try {
            process.kill(this.parentPid, 0)
          } catch {
            console.error('[Poller] Parent process exited, stopping poller')
            this.stop()
            return
          }
        }
      } catch {
        console.error('[Poller] Alive check error')
      }
    }, 5000)

    console.error('[Poller] Started: poll=5s, heartbeat=5min, alive=5s')
  }

  async stop() {
    this.running = false
    if (this.pollInterval) clearInterval(this.pollInterval)
    if (this.heartbeatInterval) clearInterval(this.heartbeatInterval)
    if (this.aliveCheckInterval) clearInterval(this.aliveCheckInterval)
    this.pollInterval = null
    this.heartbeatInterval = null
    this.aliveCheckInterval = null
    console.error('[Poller] Stopped')
  }
}