// 参数扫描的「跨页面」状态：表单 / 进度 / 结果 / 表格视图都放在模块级。
// 组件 state 会随路由卸载丢掉（切到监控页再回来就空了），而请求本身不会因为
// 切页被取消 —— 所以状态提到模块里，切回来能接着看进度和结果。
import { useSyncExternalStore } from 'react'
import { fetchFuturesSweepProgress, postFuturesSweep, updateFuturesSweepWorkers } from './api'
import type { FuturesSweepObjective, FuturesSweepRequest, FuturesSweepResult } from './types'

export type SweepSortKey =
  | 'win_rate' | 'avg_return' | 'avg_r' | 'profit_factor' | 'trades'
  | 'rr' | 'stop_atr' | 'hold_bars' | 'donchian' | 'orb'

export type SweepForm = {
  periods: string[]
  orb: string[]
  donchian: string[]
  atrPeriod: string[]
  atrK: string[]
  volRatio: string[]
  holdBars: string[]
  stopATR: string[]
  stopModes: string[]
  stopPoints: string[]
  overnight: string[]
  rr: string[]
  objective: FuturesSweepObjective
  minTrades: number
  workers: number
}

// 默认并发 64（后端上限也是 64；填 0 = 自动用满本机核数）
//
// 默认勾选：每根轴都给上值（不留空让页面去猜），关键轴给多档；级别 / ORB 只取 15 分钟。
// 这一套 = 1080 个组合（约 30 秒）。注意「全部预置都勾选」是 150 万组合，远超服务端 1 万上限，
// 想扩大范围就点各轴旁边的预置标签（止损方式有「两种都跑」一键）。
export const DEFAULT_SWEEP_FORM: SweepForm = {
  periods: ['15'],
  orb: ['15'],
  donchian: ['10', '20', '30'],
  atrPeriod: ['14'],
  atrK: ['0.25'],
  volRatio: ['1.5'],
  holdBars: ['4', '6', '8'],
  stopATR: ['0.5', '1', '1.5'],
  stopModes: ['atr', 'prev_low'], // 两种止损方式默认都跑 → 直接对照
  stopPoints: ['1', '2'],
  overnight: ['0', '1'], // 允许隔夜 + 日内 都跑
  rr: ['0.6', '1', '1.5', '2', '3'],
  objective: 'avg_return',
  minTrades: 30,
  workers: 64,
}

export type SweepState = {
  form: SweepForm
  running: boolean
  startedAt: number // 本次扫描起始时刻（毫秒）
  done: number
  total: number // 0 = 还没拿到组合数（服务端刚开始取数）
  result?: FuturesSweepResult
  error?: string
  msPerCombo?: number // 上一次实测的单组耗时，用来估时
  sortKey: SweepSortKey
  sortAsc: boolean
  onlyReliable: boolean
  token?: string // 正在跑的扫描的进度令牌（没在跑就没有）
  liveWorkers?: number // 服务端当前的并发（扫描中改过就是改后的值）
}

let state: SweepState = {
  form: { ...DEFAULT_SWEEP_FORM },
  running: false,
  startedAt: 0,
  done: 0,
  total: 0,
  sortKey: 'avg_return',
  sortAsc: false,
  onlyReliable: true,
}

const listeners = new Set<() => void>()

function patch(next: Partial<SweepState>) {
  state = { ...state, ...next }
  for (const l of listeners) l()
}

const subscribe = (cb: () => void) => {
  listeners.add(cb)
  return () => {
    listeners.delete(cb)
  }
}
const getSnapshot = () => state

export function useSweepState(): SweepState {
  return useSyncExternalStore(subscribe, getSnapshot)
}

export function setSweepForm(next: Partial<SweepForm>) {
  patch({ form: { ...state.form, ...next } })
}

export function setSweepView(next: Partial<Pick<SweepState, 'sortKey' | 'sortAsc' | 'onlyReliable'>>) {
  patch(next)
}

let workersTimer: number | undefined

// 改并发：表单立刻更新；**扫描中还要通知服务端**，让它当场 Tune 协程池
// （正在跑的任务不打断，之后提交的按新并发上）。点 InputNumber 会连点，防抖 300ms。
export function setSweepWorkers(workers: number) {
  setSweepForm({ workers })
  if (!state.running || !state.token) return
  if (workersTimer !== undefined) window.clearTimeout(workersTimer)
  workersTimer = window.setTimeout(() => {
    workersTimer = undefined
    const token = state.token
    if (!state.running || !token) return
    void updateFuturesSweepWorkers(token, state.form.workers)
      .then((res) => {
        if (res?.running) patch({ liveWorkers: res.workers })
      })
      .catch(() => {
        /* 改并发失败不影响扫描本身：下一拍轮询会把服务端实际并发带回来 */
      })
  }, 300)
}

let pollTimer: number | undefined

function stopPolling() {
  if (pollTimer !== undefined) {
    window.clearInterval(pollTimer)
    pollTimer = undefined
  }
}

// 每 500ms 问一次服务端「跑到第几组了」。轮询失败不打断主请求。
function startPolling(token: string) {
  stopPolling()
  pollTimer = window.setInterval(() => {
    void (async () => {
      try {
        const p = await fetchFuturesSweepProgress(token)
        // 主请求已经收工（running=false）就别再用轮询结果盖回去
        if (p?.running && state.running) patch({ done: p.done, total: p.total, liveWorkers: p.workers })
      } catch {
        /* 跳过这一拍 */
      }
    })()
  }, 500)
}

export function newSweepToken(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `sweep-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

// startSweep 发起一次扫描。请求挂在模块上，切页面不会中断它；
// 进度和结果都写回 store，切回来还在。
export async function startSweep(body: FuturesSweepRequest): Promise<FuturesSweepResult | undefined> {
  if (state.running) return undefined
  const token = newSweepToken()
  patch({ running: true, startedAt: Date.now(), done: 0, total: 0, error: undefined, token, liveWorkers: undefined })
  startPolling(token)
  try {
    const res = await postFuturesSweep({ ...body, token })
    patch({
      running: false,
      result: res,
      done: res.combos,
      total: res.combos,
      msPerCombo: res.combos > 0 && res.elapsed_ms > 0 ? res.elapsed_ms / res.combos : state.msPerCombo,
    })
    return res
  } catch (e) {
    patch({ running: false, error: e instanceof Error ? e.message : '参数扫描失败' })
    throw e
  } finally {
    stopPolling()
    patch({ token: undefined })
  }
}

// 已经跑了多久（秒）；没在跑就是 0
export function sweepElapsedSec(s: SweepState): number {
  return s.running && s.startedAt > 0 ? (Date.now() - s.startedAt) / 1000 : 0
}

// 进度百分比：总量还未知时给 0（进度条显示为「进行中」）
export function sweepPercent(s: SweepState): number {
  if (s.total <= 0) return 0
  return Math.min(99, Math.floor((s.done / s.total) * 100))
}

// 按「已跑完的实测速度」外推剩余秒数（比拿上一次的 msPerCombo 准）
export function sweepRemainSec(s: SweepState): number | undefined {
  const elapsed = sweepElapsedSec(s)
  if (!s.running || s.done <= 0 || elapsed <= 0 || s.total <= s.done) return undefined
  return ((s.total - s.done) * elapsed) / s.done
}
