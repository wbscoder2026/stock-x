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

export type FuturesLocalPeriod = {
  period: string
  bars: number
  first: string
  last: string
  days?: number // 覆盖了多少天（判断同步到什么程度比「根数」直观）
}

export type FuturesLocalVariety = {
  prefix: string
  name: string
  symbol: string
  periods: FuturesLocalPeriod[]
  contracts?: FuturesContractSpan[] // 该品种已同步的月份合约（不含主连）
}

export type FuturesContractSpan = {
  symbol: string // JM2601
  label: string // 2601
  periods: number
  days: number
}

export type FuturesMonthGroup = {
  month: string // 2026-09
  days: number
  bars: number
  missing?: string[]
}

export type FuturesLocalDetail = {
  prefix: string
  name: string
  symbol: string
  minute_synced: boolean // 有没有 1 分钟级别的数据
  minute_days: number // 1 分钟覆盖了多少天
  minute_first: string
  minute_last: string
  periods: FuturesPeriodDetail[]
}

export type FuturesPeriodDetail = {
  period: string
  is_minute: boolean
  bars: number
  first: string
  last: string
  days: FuturesDayDetail[]
  months: FuturesMonthGroup[] // 二级分类：月份 → 日期
  missing?: string[] // 首末之间工作日却没数据的日期（可能是节假日）
}

export type FuturesDayDetail = {
  day: string
  bars: number
  weekday: string
}

export type FuturesBackfillStatus = {
  paused: boolean
  running: boolean
  mode: string
  prefix: string
  name: string
  period: string
  from: string
  to: string
  oldest: string
  saved: number
  message: string
  queued: number
  done?: number // 当前品种已翻页数
  total?: number // 当前品种计划翻页数
  round_idx?: number // 本轮第几个品种
  round_all?: number // 本轮共几个品种
  percent?: number // 0~100
  started_at?: number
  elapsed_sec?: number
}

export type FuturesMemoryView = {
  used_bytes: number
  budget_bytes: number
  free_bytes: number
  fraction: number
}

export type FuturesLocalReport = {
  items: FuturesLocalVariety[]
  backfill: FuturesBackfillStatus
  memory: FuturesMemoryView
}

export type FuturesVarietyContracts = {
  prefix: string
  name: string
  exchange: string
  main_symbol: string // 主连代码（RB0）
  contracts: FuturesContract[] // 主连 + 各月份合约
  error?: string // 这个品种取合约失败（页面仍显示主连）
}

export type FuturesQuote = {
  symbol: string
  name: string
  price: number
  hold: number // 持仓量
  volume: number
  bid?: number // 买一价
  ask?: number // 卖一价
  bid_vol?: number // 买一量
  ask_vol?: number // 卖一量
  source?: string // hq（实时口，有盘口）| kline（退回分钟线，无盘口）
  time: string
  prev_close: number
  change_pct: number
  stale?: boolean // true = 这次没取到，用的是上一次的价
  error?: string
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
  stop_atr?: number
  stop_mode?: string // atr | prev_low
  stop_points?: number
  no_overnight?: boolean
  from?: string
  to?: string
  rr?: number
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
  alert_ttl_min?: number // 提醒保留时长（分钟），1~1440
  enabled?: boolean // 是否开着监控（服务端按它自动恢复，保存时由服务端回填）
  interval?: number
  prefixes?: string[]
  alert?: FuturesWatchAlert
}

export type FuturesWatchEvent = {
  seq: number
  fresh: boolean
  day: string
  time: string
  time_ms: number
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
  stop_price: number
  tp_price: number
  rr: number
  stop_atr: number
  stop_mode?: string
  stop_points?: number
  tick_size: number
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
  alert_ttl_sec?: number
  latest_seq: number
  backoff: number
  alert_note?: string
}

export type FuturesOutcome = FuturesEvent & {
  exit_time: string
  exit_price: number
  return: number
  correct: boolean
  exit_reason: string
  stop_price: number
  tp_price: number
  r_multiple: number
  stop_atr: number
  stop_mode?: string
  stop_points?: number
  tick_size: number
}

// 参数扫描（网格搜索）
export type FuturesSweepRequest = {
  symbol?: string
  period?: string
  periods?: string[]
  orb?: number[]
  donchian?: number[]
  atr_period?: number[]
  atr_k?: number[]
  vol_ratio?: number[]
  hold_bars?: number[]
  stop_atr?: number[]
  stop_modes?: string[]
  stop_points?: number[]
  no_overnight?: number[]
  from?: string
  to?: string
  rr?: number[]
  objective?: FuturesSweepObjective
  min_trades?: number
  limit?: number
  workers?: number
  // 进度令牌：带上它服务端才会把进度登记下来，前端就能轮询画进度条
  token?: string
}

export type FuturesSweepProgress = {
  running: boolean
  done: number
  total: number
  workers: number // 当前实际并发（中途改过就是改后的值）
}

export type FuturesSweepWorkersResult = {
  running: boolean
  workers: number
}

export type FuturesSweepObjective = 'win_rate' | 'avg_return' | 'avg_r' | 'profit_factor'

export type FuturesSweepRow = {
  params: FuturesParams
  trades: number
  win_rate: number
  avg_return: number
  avg_r: number
  profit_factor: number
  stop_exits: number
  tp_exits: number
  hold_exits: number
  reliable: boolean
}

export type FuturesSweepResult = {
  symbol: string
  periods: string[]
  objective: FuturesSweepObjective
  min_trades: number
  combos: number
  workers: number
  best?: FuturesSweepRow
  rows: FuturesSweepRow[]
  skipped?: string[]
  period_bars?: Record<string, number>
  elapsed_ms: number
}

export type FuturesBacktestResult = {
  symbol: string
  period: string
  win_rate: number
  avg_return: number
  avg_win?: number
  avg_loss?: number
  profit_factor?: number
  avg_r?: number
  trades: number
  correct: number
  stop_exits?: number
  tp_exits?: number
  hold_exits?: number
  eod_exits?: number
  skipped_eod?: number
  from?: string
  to?: string
  items: FuturesOutcome[]
  bars?: FuturesKlineBar[]
}

export type FuturesFavorite = {
  id: number
  name: string
  note: string
  params: FuturesParams
  origin_symbol: string
  origin_win_rate: number
  origin_avg_return: number
  origin_avg_r: number
  origin_profit_factor: number
  origin_trades: number
  created_at: string
  updated_at: string
}

export type FuturesFavoriteInput = {
  name: string
  note?: string
  params: FuturesParams
  origin_symbol?: string
  origin_win_rate?: number
  origin_avg_return?: number
  origin_avg_r?: number
  origin_profit_factor?: number
  origin_trades?: number
}

export type FuturesNewsItem = {
  id: string
  title: string
  summary: string
  url: string
  media: string
  provider: string
  published: string
  ts: number
  tags?: string[]
}

export type FuturesNewsSourceStatus = {
  name: string
  ok: boolean
  error?: string
  count: number
}

export type FuturesNewsReport = {
  items: FuturesNewsItem[]
  sources: FuturesNewsSourceStatus[]
  updated: string
  count: number
  interval?: string
}

export type FuturesSymbolStat = {
  symbol: string
  name: string
  trades: number
  correct: number
  win_rate: number
  avg_return: number
  avg_r: number
  profit_factor: number
  error?: string
}

export type FuturesConfigScan = {
  id: number
  name: string
  params: FuturesParams
  symbols: number
  covered: number
  reliable: number
  no_sample: number
  failed: number
  total_trades: number
  total_correct: number
  avg_win_rate: number
  avg_return: number
  avg_r: number
  avg_profit_factor: number
  pooled_win_rate: number
  pooled_avg_return: number
  details: FuturesSymbolStat[]
}

export type FuturesAcrossScan = {
  symbols: number
  min_trades: number
  configs: FuturesConfigScan[]
  overall: {
    configs: number
    avg_win_rate: number
    avg_return: number
    avg_r: number
    avg_profit_factor: number
    pooled_win_rate: number
    pooled_avg_return: number
    total_trades: number
  }
  skipped?: string[]
  elapsed_ms: number
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
