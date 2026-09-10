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

export type BacktestResult = {
  winRate?: number
  win_rate?: number
  avgReturn?: number
  avg_return?: number
  trades?: Record<string, unknown>[]
  items?: Record<string, unknown>[]
  rows?: Record<string, unknown>[]
}
