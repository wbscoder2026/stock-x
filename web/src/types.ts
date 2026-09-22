export type ApiEnvelope<T> = {
  ok: boolean
  data: T
  error?: string
}

export type Strategy = {
  id: string | number
  name: string
  description?: string
  enabled?: boolean
  params?: Record<string, number> | string
  params_json?: string
  webhook_url?: string
}

export type PickRow = {
  as_of?: string
  strategy: string
  symbol: string
  name: string
  extra?: unknown
}

export type KlineBar = {
  date: string
  open: number
  high: number
  low: number
  close: number
  volume: number
  turnover?: number
}

export type StockHit = {
  code?: string
  symbol?: string
  name?: string
}

export type KlineCoverage = {
  symbol: string
  name: string
  market?: string
  start_date?: string
  end_date?: string
  bars?: number
}

export type CoveragePage = {
  total: number
  items: KlineCoverage[]
}

export type Job = {
  id: string | number
  type?: string
  status?: string
  progress?: number | string
  log?: string
  error?: string
  message?: string
  created_at?: string
  updated_at?: string
  started_at?: string
  finished_at?: string
}

export type Schedule = {
  cron?: string
}

export type RunningJob = { id?: string; type?: string; status?: string }

export type Health = {
  status?: string
  version?: string
  stocks?: number
  bars?: number
  max_date?: string
  running?: RunningJob
  running_jobs?: RunningJob[]
}

export type FuturesVariety = {
  name: string
  prefix: string
  exchange: string
}

export type FuturesContract = {
  symbol: string
  name: string
  label: string
  variety: string
  exchange: string
  kind: string
  position?: number
}

export type FuturesLevel = {
  name: string
  kind: string
  value: number
}

export type FuturesBarView = {
  time: string
  close: number
  volume: number
  hold: number
}

export type FuturesEvent = {
  time: string
  direction: string
  level: string
  close: number
  volume: number
  level_price: number
}

export type FuturesKlineBar = {
  time: string
  open: number
  high: number
  low: number
  close: number
  volume: number
}

export type FuturesSnapshot = {
  symbol: string
  day: string
  upcoming: boolean
  trend: string
  last_5?: FuturesBarView
  last_15?: FuturesBarView
  levels: FuturesLevel[]
  vwap?: number
  position: string
  events_5: FuturesEvent[]
  events_15: FuturesEvent[]
  resonance: string[]
  bars_5?: FuturesKlineBar[]
  bars_15?: FuturesKlineBar[]
  bars_period?: FuturesKlineBar[]
}

export type FuturesParams = {
  symbol?: string
  period?: string
  orb?: number
  donchian?: number
  atr_period?: number
  atr_k?: number
  vol_ratio?: number
  hold_bars?: number
}

export type FuturesWatchAlert = {
  feishu?: boolean
  desktop?: boolean
}

export type FuturesBlacklistScope = 'variety' | 'contract'

export type FuturesBlacklistEntry = {
  scope: FuturesBlacklistScope
  value: string
  note?: string
  created_at?: string
}

export type FuturesWatchConfig = FuturesParams & {
  interval?: number
  prefixes?: string[]
  alert?: FuturesWatchAlert
}

export type FuturesWatchEvent = {
  seq: number
  fresh: boolean
  day: string
  time: string
  symbol: string
  prefix: string
  name: string
  contract?: string
  contract_label?: string
  direction: string
  level: string
  close: number
  level_price: number
  volume: number
}

export type FuturesWatchSource = {
  name: string
  calls: number
  ok: number
  fails: number
  cooldown: number
}

export type FuturesWatchStatus = {
  running: boolean
  config: FuturesWatchConfig
  source?: string
  sources?: FuturesWatchSource[]
  varieties: number
  started_at: string
  last_tick: string
  ticks: number
  scanned: number
  failures: number
  last_error: string
  last_ms: number
  events: number
  latest_seq: number
  backoff: number
  alert_note?: string
}

export type FuturesOutcome = FuturesEvent & {
  exit_time: string
  exit_price: number
  return: number
  correct: boolean
}

export type FuturesBacktestResult = {
  symbol: string
  period: string
  win_rate: number
  avg_return: number
  trades: number
  correct: number
  items: FuturesOutcome[]
  bars?: FuturesKlineBar[]
}

export type BacktestResult = {
  winRate?: number
  win_rate?: number
  avgReturn?: number
  avg_return?: number
  trades?: Record<string, unknown>[]
  items?: Record<string, unknown>[]
  rows?: Record<string, unknown>[]
}
