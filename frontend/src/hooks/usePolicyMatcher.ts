import { useEffect, useState } from 'react'
import { policyApi } from '../api/endpoints'

/**
 * usePolicyMatcher is the client-side mirror of Chief's server-side policy
 * evaluation loop. Chief itself matches on the backend at decision time,
 * but the UI needs to *preview* which policy would apply to a PR so the
 * human can see "this is why Chief will (or won't) auto-approve".
 *
 * v1 caveat: this runs entirely in the browser. It can drift from the
 * backend's matching logic if the policy DSL grows. The backend is the
 * single source of truth at actual decision time — this hook exists only
 * to tell the UI what *should* happen. If a policy ships that uses a
 * field we don't know about, we just won't match it (safe fallback), and
 * the human will see "no matching policy" instead of a misleading match.
 *
 * Known match keys (mirror of create_policy schema in agent/tools.go):
 *   - scope         : "pr_review" | "pr_merge" | "milestone_switch" | "task_create"
 *   - file_count_gt : number
 *   - file_count_lt : number
 *   - file_pattern  : glob ("*schema*", "*.tsx")
 *   - merge_cost_in : array of "low"/"medium"/"high"
 *   - submitter     : agent id
 */

export type Policy = {
  id: string
  name: string
  match_condition: string
  actions: string
  priority: number
  status: string
  source: string
}

// Input context the UI builds from a PR before asking "which policy fits?".
// Keep the shape flat and JSON-ish so adding fields is cheap.
export type PolicyMatchContext = {
  scope: 'pr_review' | 'pr_merge'
  file_count?: number
  file_paths?: string[]
  merge_cost?: 'low' | 'medium' | 'high'
  submitter?: string
}

export type MatchedPolicy = {
  policy: Policy
  matchCondition: Record<string, unknown>
  actions: Record<string, unknown>
}

// Tiny glob → regex. Enough for "*.tsx", "*schema*", "src/**/*.ts".
// Not trying to be full Ant/Claude-grade; the backend still owns truth.
function globToRegex(glob: string): RegExp {
  const escaped = glob
    .replace(/[.+^${}()|[\]\\]/g, '\\$&')
    .replace(/\*\*/g, '__DOUBLESTAR__')
    .replace(/\*/g, '[^/]*')
    .replace(/__DOUBLESTAR__/g, '.*')
    .replace(/\?/g, '.')
  return new RegExp('^' + escaped + '$')
}

function matchesPath(pattern: string, paths: string[]): boolean {
  const re = globToRegex(pattern)
  return paths.some((p) => re.test(p) || re.test(p.split('/').pop() || ''))
}

export function evaluatePolicy(
  match: Record<string, unknown>,
  ctx: PolicyMatchContext,
): boolean {
  // scope is a hard filter — a pr_review policy never applies to pr_merge.
  if (match.scope && match.scope !== ctx.scope) return false

  if (typeof match.file_count_gt === 'number' && typeof ctx.file_count === 'number') {
    if (!(ctx.file_count > (match.file_count_gt as number))) return false
  }
  if (typeof match.file_count_lt === 'number' && typeof ctx.file_count === 'number') {
    if (!(ctx.file_count < (match.file_count_lt as number))) return false
  }

  if (typeof match.file_pattern === 'string' && ctx.file_paths && ctx.file_paths.length > 0) {
    if (!matchesPath(match.file_pattern as string, ctx.file_paths)) return false
  }

  if (Array.isArray(match.merge_cost_in) && ctx.merge_cost) {
    if (!(match.merge_cost_in as string[]).includes(ctx.merge_cost)) return false
  }

  if (typeof match.submitter === 'string' && ctx.submitter) {
    if (match.submitter !== ctx.submitter) return false
  }

  return true
}

// Returns highest-priority matching active policy, or null.
export function pickBestMatch(policies: Policy[], ctx: PolicyMatchContext): MatchedPolicy | null {
  const active = policies.filter((p) => p.status === 'active')
  const matches: MatchedPolicy[] = []
  for (const p of active) {
    let match: Record<string, unknown> = {}
    let actions: Record<string, unknown> = {}
    try { match = JSON.parse(p.match_condition) } catch { continue }
    try { actions = JSON.parse(p.actions) } catch { continue }
    if (evaluatePolicy(match, ctx)) {
      matches.push({ policy: p, matchCondition: match, actions })
    }
  }
  if (matches.length === 0) return null
  // Highest priority wins; ties broken by name for deterministic output.
  matches.sort((a, b) => {
    if (b.policy.priority !== a.policy.priority) return b.policy.priority - a.policy.priority
    return a.policy.name.localeCompare(b.policy.name)
  })
  return matches[0]
}

// usePolicyMatcher loads active policies once and exposes a matcher fn
// consumers can call repeatedly (e.g. once per PR in the queue).
export function usePolicyMatcher() {
  const [policies, setPolicies] = useState<Policy[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    policyApi.list().then((res) => {
      if (cancelled) return
      if (res.success) setPolicies(res.data.policies || [])
      setLoading(false)
    }).catch(() => {
      if (!cancelled) setLoading(false)
    })
    return () => { cancelled = true }
  }, [])

  const match = (ctx: PolicyMatchContext) => pickBestMatch(policies, ctx)

  return { policies, match, loading }
}
