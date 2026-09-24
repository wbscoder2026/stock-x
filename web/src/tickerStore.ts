// 期货浮窗的配置放在模块级：浮窗本体与「期货总览」页共用一份，
// 总览页点「加入浮窗」浮窗立刻出现该品种，点「移出」立刻消失。
// （沿用 sweepStore 的写法：模块状态 + useSyncExternalStore + localStorage 持久化。）
import { useSyncExternalStore } from 'react'

export const MAX_TICKER_ROWS = 5
const STORE_KEY = 'stockx.futures-ticker'

export type TickerSettings = {
  symbols: string[] // 最多 5 个（浮窗就 5 行）
  count: number // 显示数量 1~5
  opacity: number // 0.3~1
  intervalMs: number // 刷新间隔
  x: number // -1 = 还没拖过（默认贴右边）
  y: number
  collapsed: boolean
  hidden: boolean
  showBidAsk: boolean // 是否显示买一/卖一（多一行）
}

/** 刷新频率可选值（0 = 不自动刷新，只手动点）。 */
export const TICKER_INTERVALS = [
  { value: 1000, label: '1 秒' },
  { value: 2000, label: '2 秒' },
  { value: 3000, label: '3 秒' },
  { value: 5000, label: '5 秒' },
  { value: 10000, label: '10 秒' },
  { value: 15000, label: '15 秒' },
  { value: 30000, label: '30 秒' },
  { value: 60000, label: '60 秒' },
  { value: 0, label: '不自动刷新（手动）' },
]

const DEFAULTS: TickerSettings = {
  symbols: [],
  count: MAX_TICKER_ROWS,
  opacity: 0.92,
  intervalMs: 3000,
  x: -1,
  y: 96,
  collapsed: false,
  hidden: false,
  showBidAsk: false,
}

function clampNum(v: number, lo: number, hi: number) {
  if (!Number.isFinite(v)) return lo
  return Math.min(hi, Math.max(lo, v))
}

function normalize(parsed: Partial<TickerSettings>): TickerSettings {
  const symbols = Array.isArray(parsed.symbols)
    ? parsed.symbols.filter((s) => typeof s === 'string' && s.trim() !== '').slice(0, MAX_TICKER_ROWS)
    : []
  const countRaw = parsed.count ?? DEFAULTS.count
  return {
    ...DEFAULTS,
    ...parsed,
    symbols: [...new Set(symbols.map((s) => s.toUpperCase()))],
    count: clampNum(countRaw, 1, MAX_TICKER_ROWS),
    opacity: clampNum(parsed.opacity ?? DEFAULTS.opacity, 0.3, 1),
    intervalMs: TICKER_INTERVALS.some((o) => o.value === Number(parsed.intervalMs))
      ? Number(parsed.intervalMs)
      : DEFAULTS.intervalMs,
  }
}

function load(): TickerSettings {
  try {
    const raw = localStorage.getItem(STORE_KEY)
    if (!raw) return DEFAULTS
    return normalize(JSON.parse(raw) as Partial<TickerSettings>)
  } catch {
    return DEFAULTS
  }
}

let state: TickerSettings = load()
const listeners = new Set<() => void>()

function persist(next: TickerSettings) {
  try {
    localStorage.setItem(STORE_KEY, JSON.stringify(next))
  } catch {
    // 存不上不影响使用
  }
}

/** patchTicker 改配置（会立即写 localStorage 并通知所有订阅者）。 */
export function patchTicker(p: Partial<TickerSettings>) {
  state = normalize({ ...state, ...p })
  persist(state)
  for (const l of listeners) l()
}

export function resetTicker() {
  patchTicker({ ...DEFAULTS })
}

/** addToTicker 加入浮窗；超出 5 个的部分会被拒绝并回报，页面好提示用户。 */
export function addToTicker(symbols: string[]): { added: string[]; skipped: string[] } {
  const want = symbols.map((s) => s.toUpperCase().trim()).filter(Boolean)
  const added: string[] = []
  const skipped: string[] = []
  const next = [...state.symbols]
  for (const s of want) {
    if (next.includes(s)) continue
    if (next.length >= MAX_TICKER_ROWS) {
      skipped.push(s)
      continue
    }
    next.push(s)
    added.push(s)
  }
  if (added.length > 0) patchTicker({ symbols: next, hidden: false })
  return { added, skipped }
}

export function removeFromTicker(symbols: string[]) {
  const drop = new Set(symbols.map((s) => s.toUpperCase().trim()))
  const next = state.symbols.filter((s) => !drop.has(s))
  if (next.length !== state.symbols.length) patchTicker({ symbols: next })
}

const subscribe = (cb: () => void) => {
  listeners.add(cb)
  return () => {
    listeners.delete(cb)
  }
}

export function useTicker(): TickerSettings {
  return useSyncExternalStore(subscribe, () => state)
}
