import { ApiClient } from './api-client.js'

export class Poller {
  private api: ApiClient
  private pollInterval: NodeJS.Timeout | null = null
  private heartbeatInterval: NodeJS.Timeout | null = null
  private aliveCheckInterval: NodeJS.Timeout | null = null
  private onBroadcast: ((messages: any[]) => void) | null = null
  // ackProvider lets the broadcast handler hand the poller a list of
  // directed-message IDs that have been fully processed. The poller
  // forwards them on the next /poll call so the platform LREMs them
  // from the per-agent queue. See client/mcp/src/index.ts for the
  // wiring; see platform/backend/internal/handler/sync.go::Poll for
  // the server-side LREM path.
  private ackProvider: (() => string[]) | null = null
  private onAcksConfirmed: ((acked: string[]) => void) | null = null
  private running = false
  private parentPid: number | null = null

  constructor(api: ApiClient) {
    this.api = api
  }

  setBroadcastHandler(handler: (messages: any[]) => void) {
    this.onBroadcast = handler
  }

  // setAckProvider wires in a function the poller calls just before
  // each /poll request. The returned IDs are sent to the server and,
  // on a successful poll response, passed to onAcksConfirmed (set via
  // setAckConfirmedHandler) so the broadcast handler can drop them
  // from its in-memory pending map. If poll throws, acks are NOT
  // confirmed and stay queued in the provider for the next tick.
  setAckProvider(fn: () => string[]) {
    this.ackProvider = fn
  }

  setAckConfirmedHandler(fn: (acked: string[]) => void) {
    this.onAcksConfirmed = fn
  }

  async start() {
    if (this.running) return
    this.running = true
    this.parentPid = process.ppid

    this.pollInterval = setInterval(async () => {
      const acks = this.ackProvider ? this.ackProvider() : []
      try {
        const data = await this.api.poll(acks)
        // Acks were durably accepted by the server: notify the
        // broadcast handler so it clears matching entries from its
        // pending map. Done before broadcasting so the handler sees
        // the cleanest state when processing fresh inbound messages.
        if (acks.length > 0 && this.onAcksConfirmed) {
          this.onAcksConfirmed(acks)
        }
        if (data?.data?.messages?.length > 0 && this.onBroadcast) {
          this.onBroadcast(data.data.messages)
        }
      } catch (e) {
        console.error('[Poller] Poll error:', e)
        // acks remain in the provider's queue and will be retried
        // on the next tick; no manual rollback needed here.
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