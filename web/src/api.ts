import type {
  ApiEnvelope,
  BacktestResult,
  CoveragePage,
  FuturesBacktestResult,
  FuturesBlacklistEntry,
  FuturesBlacklistScope,
  FuturesContract,
  FuturesParams,
  FuturesSnapshot,
  FuturesSweepProgress,
  FuturesSweepRequest,
  FuturesSweepResult,
  FuturesSweepWorkersResult,
  FuturesAcrossScan,
  FuturesBackfillStatus,
  FuturesLocalDetail,
  FuturesQuote,
  FuturesVarietyContracts,
  FuturesFavorite,
  FuturesFavoriteInput,
  FuturesLocalReport,
  FuturesVariety,
  FuturesWatchAlert,
  FuturesWatchConfig,
  FuturesWatchEvent,
  FuturesWatchStatus,
  Health,
  Job,
  KlineBar,
  PickRow,
  Schedule,
  StockHit,
  Strategy,
} from './types'

export type { FuturesParams }

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

export function fetchFuturesVarieties() {
  return apiGet<FuturesVariety[]>('/api/futures/varieties')
}

export function fetchFuturesLocalDetail(prefix: string, symbol?: string) {
  const q = symbol ? `&symbol=${encodeURIComponent(symbol)}` : ''
  return apiGet<FuturesLocalDetail>(`/api/futures/local/detail?prefix=${encodeURIComponent(prefix)}${q}`)
}

export function fetchFuturesLocal() {
  return apiGet<FuturesLocalReport>('/api/futures/local')
}

export function postFuturesLocalBackfill(body: { prefix: string; symbol?: string; from?: string; to?: string }) {
  return apiSend<FuturesBackfillStatus>('/api/futures/local/backfill', 'POST', body)
}

export function pauseFuturesLocal() {
  return apiSend<FuturesBackfillStatus>('/api/futures/local/pause', 'POST', {})
}

export function resumeFuturesLocal() {
  return apiSend<FuturesBackfillStatus>('/api/futures/local/resume', 'POST', {})
}

export function fetchFuturesFavorites() {
  return apiGet<FuturesFavorite[]>('/api/futures/favorites')
}

export function postFuturesFavorites(items: FuturesFavoriteInput[]) {
  return apiSend<FuturesFavorite[]>('/api/futures/favorites', 'POST', { items })
}

export function putFuturesFavorite(id: number, body: FuturesFavoriteInput) {
  return apiSend<FuturesFavorite>(`/api/futures/favorites/${id}`, 'PUT', body)
}

export function deleteFuturesFavorite(id: number) {
  return apiSend<{ removed: number }>(`/api/futures/favorites/${id}`, 'DELETE')
}

export function deleteFuturesFavorites(ids: number[]) {
  return apiSend<{ removed: number }>('/api/futures/favorites/delete', 'POST', { ids })
}

export function scanFuturesFavorites(body: { ids: number[]; workers?: number; min_trades?: number }) {
  return apiSend<FuturesAcrossScan>('/api/futures/favorites/scan', 'POST', body)
}

export function postFuturesSweep(body: FuturesSweepRequest) {
  return apiSend<FuturesSweepResult>('/api/futures/sweep', 'POST', body)
}

export function fetchFuturesSweepProgress(token: string) {
  return apiGet<FuturesSweepProgress>(`/api/futures/sweep/progress?token=${encodeURIComponent(token)}`)
}

// 扫描途中改并发（服务端会立刻 Tune 协程池）
export function updateFuturesSweepWorkers(token: string, workers: number) {
  return apiSend<FuturesSweepWorkersResult>('/api/futures/sweep/workers', 'POST', { token, workers })
}

export function fetchFuturesOverview() {
  return apiGet<FuturesVarietyContracts[]>('/api/futures/overview')
}

export function fetchFuturesQuotes(symbols: string[]) {
  const q = encodeURIComponent(symbols.slice(0, 5).join(','))
  return apiGet<FuturesQuote[]>(`/api/futures/quotes?symbols=${q}`)
}

export function fetchFuturesContracts(prefix: string) {
  return apiGet<FuturesContract[]>(`/api/futures/contracts?prefix=${encodeURIComponent(prefix)}`)
}

export function fetchFuturesScan(p: FuturesParams) {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(p)) {
    if (v == null || v === '') continue
    q.set(k, String(v))
  }
  const qs = q.toString()
  return apiGet<FuturesSnapshot>(qs ? `/api/futures/scan?${qs}` : '/api/futures/scan')
}

export function postFuturesBacktest(body: FuturesParams) {
  return apiSend<FuturesBacktestResult>('/api/futures/backtest', 'POST', body)
}

export function startFuturesWatch(cfg: FuturesWatchConfig) {
  return apiSend<FuturesWatchStatus>('/api/futures/watch/start', 'POST', cfg)
}

export function updateFuturesWatch(cfg: FuturesWatchConfig) {
  return apiSend<FuturesWatchStatus>('/api/futures/watch/config', 'POST', cfg)
}

export function stopFuturesWatch() {
  return apiSend<FuturesWatchStatus>('/api/futures/watch/stop', 'POST', {})
}

export function fetchFuturesWatchConfig() {
  return apiGet<FuturesWatchConfig>('/api/futures/watch/config')
}

export function fetchFuturesWatchStatus() {
  return apiGet<FuturesWatchStatus>('/api/futures/watch/status')
}

export function fetchFuturesWatchEvents(since: number) {
  return apiGet<FuturesWatchEvent[]>(`/api/futures/watch/events?since=${since}`)
}

export function testFuturesWatchAlert(body: FuturesWatchAlert) {
  return apiSend<{ note: string }>('/api/futures/watch/alert/test', 'POST', body)
}

export function fetchFuturesBlacklist() {
  return apiGet<FuturesBlacklistEntry[]>('/api/futures/blacklist')
}

export function addFuturesBlacklist(body: { scope: FuturesBlacklistScope; value: string; note?: string }) {
  return apiSend<{ scope: string; value: string }>('/api/futures/blacklist', 'POST', body)
}

export function removeFuturesBlacklist(scope: FuturesBlacklistScope, value: string) {
  return apiSend<{ removed: number }>(
    `/api/futures/blacklist?scope=${encodeURIComponent(scope)}&value=${encodeURIComponent(value)}`,
    'DELETE',
  )
}

export function scheduleCron(data: Schedule | string | null | undefined): string {
  if (typeof data === 'string') return data
  return data?.cron ?? ''
}

export function stockCode(s: StockHit): string {
  return s.symbol || s.code || ''
}
