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

  // Backoff state. The poller used to fire every 5s no matter what,
  // which meant a platform outage caused 12 errors per minute in the
  // operator's stderr — drowning out anything actionable. Now: stay
  // at the configured base interval while polls succeed; on each
  // consecutive failure double the delay (capped at 60s); reset to
  // base on the next success. Heartbeat and alive-check intervals
  // run independently and are NOT subject to backoff (they're cheap
  // and matter for liveness detection on the platform side).
  private static readonly BASE_POLL_MS = 5_000
  private static readonly MAX_POLL_MS = 60_000
  private currentPollDelay = Poller.BASE_POLL_MS
  private consecutivePollFailures = 0

  private scheduleNextPoll() {
    if (!this.running) return
    this.pollInterval = setTimeout(async () => {
      const acks = this.ackProvider ? this.ackProvider() : []
      try {
        const data = await this.api.poll(acks)
        // Success path: acks were durably accepted by the server,
        // notify broadcast handler so it clears matching entries.
        // Done before broadcasting so the handler sees the cleanest
        // state when processing fresh inbound messages.
        if (acks.length > 0 && this.onAcksConfirmed) {
          this.onAcksConfirmed(acks)
        }
        if (data?.data?.messages?.length > 0 && this.onBroadcast) {
          this.onBroadcast(data.data.messages)
        }
        if (this.consecutivePollFailures > 0) {
          console.error('[Poller] Recovered after %d failures; resetting interval to %dms',
            this.consecutivePollFailures, Poller.BASE_POLL_MS)
        }
        this.consecutivePollFailures = 0
        this.currentPollDelay = Poller.BASE_POLL_MS
      } catch (e) {
        this.consecutivePollFailures++
        // Exponential backoff: 5s → 10s → 20s → 40s → 60s (capped).
        // Acks remain queued in the provider for the next attempt.
        this.currentPollDelay = Math.min(this.currentPollDelay * 2, Poller.MAX_POLL_MS)
        // Throttle the noise: log full error every 10 consecutive
        // failures, otherwise just emit a brief notice.
        if (this.consecutivePollFailures === 1 || this.consecutivePollFailures % 10 === 0) {
          console.error('[Poller] Poll error (failure #%d, next retry in %ds):',
            this.consecutivePollFailures, Math.floor(this.currentPollDelay / 1000), e)
        }
      } finally {
        this.scheduleNextPoll()
      }
    }, this.currentPollDelay) as unknown as NodeJS.Timeout
  }

  async start() {
    if (this.running) return
    this.running = true
    this.parentPid = process.ppid

    this.scheduleNextPoll()

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