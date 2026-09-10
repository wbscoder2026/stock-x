import type { Time } from 'lightweight-charts'
import type { KlineBar } from './types'

export type OverlayLine = {
  color: string
  title: string
  dashed?: boolean
  points: { time: Time; value: number }[]
}

export const STRATEGY_COLOR: Record<string, string> = {
  turtle: '#e67e22',
  ma_volume: '#2980b9',
  high_tight_flag: '#8e44ad',
  limit_up_shakeout: '#c0392b',
  uptrend_limit_down: '#16a085',
  rps_breakout: '#2c3e50',
}

export const STRATEGY_NAME: Record<string, string> = {
  turtle: '海龟突破',
  ma_volume: '均线放量',
  high_tight_flag: '高窄旗形',
  limit_up_shakeout: '涨停洗盘',
  uptrend_limit_down: '上升跌停',
  rps_breakout: 'RPS突破',
}

export function strategyLabel(id: string, names?: Record<string, string>) {
  return names?.[id] || STRATEGY_NAME[id] || id
}

export function patternCaption(asOf: string, label: string, lines: OverlayLine[]): string {
  const bits = [...new Set(lines.map((l) => l.title).filter(Boolean))]
  const extra = bits.length ? ` · ${bits.join('、')}` : ''
  return `${asOf} ${label}${extra}`
}

function toTime(date: string): Time {
  return date.slice(0, 10) as Time
}

function idxOf(bars: KlineBar[], asOf: string): number {
  const d = asOf.slice(0, 10)
  for (let i = bars.length - 1; i >= 0; i--) {
    if (bars[i].date.slice(0, 10) <= d) return i
  }
  return -1
}

function sma(xs: number[], n: number): number[] {
  const out = new Array<number>(xs.length).fill(NaN)
  if (n <= 0) return out
  let sum = 0
  for (let i = 0; i < xs.length; i++) {
    sum += xs[i]
    if (i >= n) sum -= xs[i - n]
    if (i >= n - 1) out[i] = sum / n
  }
  return out
}

function maLine(bars: KlineBar[], period: number, color: string, title: string): OverlayLine | null {
  const arr = sma(
    bars.map((b) => Number(b.close)),
    period,
  )
  const points: OverlayLine['points'] = []
  for (let i = 0; i < bars.length; i++) {
    if (Number.isFinite(arr[i])) points.push({ time: toTime(bars[i].date), value: arr[i] })
  }
  if (points.length < 2) return null
  return { color, title, points }
}

function segment(
  bars: KlineBar[],
  from: number,
  to: number,
  value: number,
  color: string,
  title: string,
  dashed = false,
): OverlayLine | null {
  if (from < 0 || to < from || to >= bars.length || !Number.isFinite(value)) return null
  return {
    color,
    title,
    dashed,
    points: [
      { time: toTime(bars[from].date), value },
      { time: toTime(bars[to].date), value },
    ],
  }
}

export function strategyOverlay(
  strategy: string,
  bars: KlineBar[],
  asOf: string,
  params: Record<string, number>,
): OverlayLine[] {
  const i = idxOf(bars, asOf)
  if (i < 0) return []
  const color = STRATEGY_COLOR[strategy] || '#e67e22'
  const out: OverlayLine[] = []
  const push = (l: OverlayLine | null) => {
    if (l) out.push(l)
  }
  switch (strategy) {
    case 'turtle': {
      const window = Math.max(1, Math.round(params.window ?? 20))
      const from = Math.max(0, i - window)
      const to = Math.max(from, i - 1)
      let mx = -Infinity
      for (let k = from; k <= to; k++) mx = Math.max(mx, Number(bars[k].high))
      push(segment(bars, from, i, mx, color, `${window}日高`, true))
      break
    }
    case 'ma_volume': {
      push(maLine(bars, Math.max(1, Math.round(params.maFast ?? 5)), '#e74c3c', 'MA快'))
      push(maLine(bars, Math.max(1, Math.round(params.maSlow ?? 20)), '#3498db', 'MA慢'))
      break
    }
    case 'high_tight_flag': {
      const lookback = Math.max(1, Math.round(params.lookback ?? 40))
      const consDays = Math.max(1, Math.round(params.consDays ?? 10))
      const consFrom = Math.max(0, i - consDays + 1)
      const lbFrom = Math.max(0, i - lookback + 1)
      let hh = -Infinity
      let ll = Infinity
      let ch = -Infinity
      let cl = Infinity
      for (let k = lbFrom; k <= i; k++) {
        hh = Math.max(hh, Number(bars[k].high))
        ll = Math.min(ll, Number(bars[k].low))
      }
      for (let k = consFrom; k <= i; k++) {
        ch = Math.max(ch, Number(bars[k].high))
        cl = Math.min(cl, Number(bars[k].low))
      }
      push(segment(bars, lbFrom, i, hh, '#e74c3c', '动量高', true))
      push(segment(bars, lbFrom, i, ll, '#27ae60', '动量低', true))
      push(segment(bars, consFrom, i, ch, color, '旗形高'))
      push(segment(bars, consFrom, i, cl, color, '旗形低'))
      break
    }
    case 'rps_breakout': {
      const period = Math.max(1, Math.round(params.period ?? 120))
      const from = Math.max(0, i - period + 1)
      let mx = -Infinity
      for (let k = from; k <= i; k++) mx = Math.max(mx, Number(bars[k].high))
      push(segment(bars, from, i, mx, color, `${period}日高`, true))
      break
    }
    case 'limit_up_shakeout': {
      const a = Math.max(0, i - 2)
      const b = Math.max(0, i - 1)
      push(segment(bars, a, b, Number(bars[b].close), color, '涨停日'))
      push(segment(bars, b, i, Number(bars[i].low), color, '洗盘低', true))
      break
    }
    case 'uptrend_limit_down': {
      push(maLine(bars, Math.max(1, Math.round(params.maFast ?? 20)), '#e74c3c', 'MA快'))
      push(maLine(bars, Math.max(1, Math.round(params.maSlow ?? 60)), '#3498db', 'MA慢'))
      break
    }
    default:
      break
  }
  return out
}

export function barIndex(bars: KlineBar[], asOf: string): number {
  return idxOf(bars, asOf)
}
