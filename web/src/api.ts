import type { ApiEnvelope, BacktestResult, CoveragePage, Health, Job, KlineBar, PickRow, Schedule, StockHit, Strategy } from './types'

async function parseEnvelope<T>(res: Response): Promise<T> {
  const text = await res.text()
  let json: ApiEnvelope<T>
  try {
    json = JSON.parse(text) as ApiEnvelope<T>
  } catch {
    throw new Error(text || `HTTP ${res.status}`)
  }
  if (!json.ok) {
    throw new Error(json.error || `请求失败 (${res.status})`)
  }
  return json.data
}

export async function apiGet<T>(path: string): Promise<T> {
  const res = await fetch(path)
  return parseEnvelope<T>(res)
}

export async function apiSend<T>(path: string, method: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  return parseEnvelope<T>(res)
}

export function parseParams(s: Pick<Strategy, 'params' | 'params_json'>): Record<string, number> {
  if (s.params && typeof s.params === 'object' && !Array.isArray(s.params)) {
    const out: Record<string, number> = {}
    for (const [k, v] of Object.entries(s.params)) {
      const n = Number(v)
      out[k] = Number.isFinite(n) ? n : 0
    }
    return out
  }
  const raw = typeof s.params === 'string' ? s.params : s.params_json
  if (typeof raw === 'string' && raw.trim()) {
    try {
      const obj = JSON.parse(raw) as Record<string, unknown>
      const out: Record<string, number> = {}
      for (const [k, v] of Object.entries(obj)) {
        const n = Number(v)
        out[k] = Number.isFinite(n) ? n : 0
      }
      return out
    } catch {
      return {}
    }
  }
  return {}
}

export function parseExtra(extra: unknown): Record<string, unknown> {
  if (extra == null || extra === '') return {}
  if (typeof extra === 'string') {
    try {
      const v = JSON.parse(extra) as unknown
      if (v && typeof v === 'object' && !Array.isArray(v)) return v as Record<string, unknown>
      return { extra: v }
    } catch {
      return { extra }
    }
  }
  if (typeof extra === 'object' && !Array.isArray(extra)) return extra as Record<string, unknown>
  return { extra }
}

export const fetchHealth = () => apiGet<Health>('/api/health')
export const fetchPicks = (q?: { from?: string; to?: string; date?: string; strategy?: string; symbol?: string }) => {
  const p = new URLSearchParams()
  if (q?.from) p.set('from', q.from)
  if (q?.to) p.set('to', q.to)
  if (q?.date) p.set('date', q.date)
  if (q?.strategy) p.set('strategy', q.strategy)
  if (q?.symbol) p.set('symbol', q.symbol)
  const qs = p.toString()
  return apiGet<PickRow[]>(qs ? `/api/picks?${qs}` : '/api/picks')
}
export const fetchStrategies = () => apiGet<Strategy[]>('/api/strategies')
export const fetchStocks = (q: string) => apiGet<StockHit[]>(`/api/stocks?q=${encodeURIComponent(q)}`)
export const fetchKline = (code: string) =>
  apiGet<KlineBar[]>(`/api/stocks/${encodeURIComponent(code)}/kline`)
export const fetchKlineCoverage = (q?: { q?: string; offset?: number; limit?: number }) => {
  const p = new URLSearchParams()
  if (q?.q) p.set('q', q.q)
  if (q?.offset != null) p.set('offset', String(q.offset))
  if (q?.limit != null) p.set('limit', String(q.limit))
  const qs = p.toString()
  return apiGet<CoveragePage>(qs ? `/api/kline-coverage?${qs}` : '/api/kline-coverage')
}
export const fetchJobs = () => apiGet<Job[]>('/api/jobs')
export const fetchJob = (id: string | number) => apiGet<Job>(`/api/jobs/${encodeURIComponent(String(id))}`)
export const fetchSchedule = () => apiGet<Schedule | string>('/api/schedule')

export function putStrategy(
  id: string | number,
  body: { enabled?: boolean; params?: Record<string, number>; webhook_url?: string },
) {
  return apiSend<Strategy>(`/api/strategies/${encodeURIComponent(String(id))}`, 'PUT', body)
}

export function postJob(type: string, extra?: { from?: string; to?: string; symbols?: string[] }) {
  return apiSend<Job>('/api/jobs', 'POST', { type, ...extra })
}

export function pauseJob(type?: string) {
  return apiSend<{ status?: string }>('/api/jobs/pause', 'POST', type ? { type } : {})
}

export function runningTypes(h?: Health): Set<string> {
  const s = new Set<string>()
  for (const j of h?.running_jobs ?? []) {
    if (j.type) s.add(j.type)
  }
  if (h?.running?.type) s.add(h.running.type)
  return s
}

export function resumeBackfill() {
  return apiSend<Job>('/api/jobs/resume', 'POST', {})
}

export function putSchedule(cron: string) {
  return apiSend<Schedule>('/api/schedule', 'PUT', { cron })
}

export function postBacktest(body: { strategy: string; from: string; to: string; holdDays: number }) {
  return apiSend<BacktestResult>('/api/backtest', 'POST', body)
}

export function scheduleCron(data: Schedule | string | null | undefined): string {
  if (typeof data === 'string') return data
  return data?.cron ?? ''
}

export function stockCode(s: StockHit): string {
  return s.symbol || s.code || ''
}
