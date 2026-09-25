// 期货回测页：单品种扫描（关键位 + 价格图）+ 回测（1×ATR 止损 + 盈亏比止盈）+ 参数扫描（网格搜索）。
// 与「监控突破」页拆开：这里不订阅任何行情，只在你点按钮时取一次数据。
import { useEffect, useMemo, useRef, useState, type Key } from 'react'
import { useSearchParams } from 'react-router-dom'
import dayjs from 'dayjs'
import type { Dayjs } from 'dayjs'
import { Alert, App, Button, Card, DatePicker, Form, Input, InputNumber, Modal, Progress, Select, Space, Statistic, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { fetchFuturesContracts, fetchFuturesScan, fetchFuturesVarieties, postFuturesBacktest, postFuturesFavorites } from '../api'
import {
  setSweepForm,
  setSweepView,
  setSweepWorkers,
  startSweep,
  sweepElapsedSec,
  sweepPercent,
  sweepRemainSec,
  useSweepState,
} from '../sweepStore'
import type { SweepSortKey } from '../sweepStore'
import type {
  FuturesBacktestResult,
  FuturesContract,
  FuturesEvent,
  FuturesOutcome,
  FuturesParams,
  FuturesSnapshot,
  FuturesSweepObjective,
  FuturesSweepRequest,
  FuturesSweepRow,
  FuturesVariety,
} from '../types'
import {
  DEFAULT_PARAMS,
  FuturesChart,
  LEVEL_OPTIONS,
  ParamLabel,
  TIPS,
  contractOptions,
  exitTag,
  STOP_MODE_ATR,
  STOP_MODE_OPTIONS,
  STOP_MODE_PREV_LOW,
  fmtPrice,
  pct,
  stopModeLabel,
  varietyOptions,
} from './FuturesShared'

type RangeValue = [Dayjs | null, Dayjs | null] | null

const eventCols: ColumnsType<FuturesEvent> = [
  { title: '时间', dataIndex: 'time', width: 150 },
  { title: '方向', dataIndex: 'direction', width: 100 },
  { title: '关键位', dataIndex: 'level', width: 160 },
  { title: '收盘', dataIndex: 'close', render: (v: number) => v?.toFixed(1) },
  { title: '关键位价', dataIndex: 'level_price', render: (v: number) => v?.toFixed(1) },
  { title: '量', dataIndex: 'volume' },
]

// 工厂函数：表头里的 TIPS 在模块加载后才可用（直接建数组会踩 TDZ）
const outcomeCols = (): ColumnsType<FuturesOutcome> => [
  { title: '时间', dataIndex: 'time', width: 150 },
  { title: '方向', dataIndex: 'direction', width: 100 },
  { title: '关键位', dataIndex: 'level', width: 160 },
  { title: '入场价', dataIndex: 'close', width: 100, render: (v: number, r) => fmtPrice(v, r.tick_size) },
  { title: '止损', dataIndex: 'stop_price', width: 100, render: (v: number, r) => (v ? fmtPrice(v, r.tick_size) : '-') },
  { title: '止盈', dataIndex: 'tp_price', width: 100, render: (v: number, r) => (v ? fmtPrice(v, r.tick_size) : '-') },
  { title: '平仓时间', dataIndex: 'exit_time', width: 150 },
  { title: '平仓价', dataIndex: 'exit_price', width: 100, render: (v: number, r) => fmtPrice(v, r.tick_size) },
  {
    title: <ParamLabel text="出场" hint={TIPS.exit_rule} />,
    dataIndex: 'exit_reason',
    width: 110,
    render: (v: string) => (v ? exitTag(v) : '-'),
  },
  {
    title: '收益',
    dataIndex: 'return',
    render: (v: number) =>
      v == null ? '' : <span style={{ color: v >= 0 ? '#389e0d' : '#cf1322' }}>{pct(v)}</span>,
  },
  {
    title: <ParamLabel text="R 倍数" hint={TIPS.r_multiple} />,
    dataIndex: 'r_multiple',
    width: 100,
    render: (v: number, r) => (v == null ? '' : <span style={{ color: r.return >= 0 ? '#389e0d' : '#cf1322' }}>{v.toFixed(2)}R</span>),
  },
]

const OBJECTIVES: { value: FuturesSweepObjective; label: string }[] = [
  { value: 'avg_return', label: '平均收益' },
  { value: 'avg_r', label: '期望 R' },
  { value: 'win_rate', label: '胜率' },
  { value: 'profit_factor', label: '盈利因子' },
]

const SWEEP_PRESETS = {
  orb: [15, 30, 45, 60],
  donchian: [10, 20, 30, 40, 60],
  atrPeriod: [7, 10, 14, 21],
  atrK: [0.1, 0.25, 0.5, 0.75],
  volRatio: [1.0, 1.2, 1.5, 2.0],
  holdBars: [3, 4, 6, 8, 12],
  stopATR: [0.5, 0.75, 1.0, 1.5, 2.0],
  rr: [0.6, 1.0, 1.5, 2.0, 2.5, 3.0],
}

function NumTags({
  value,
  onChange,
  presets,
  width = 160,
}: {
  value: string[]
  onChange: (v: string[]) => void
  presets: number[]
  width?: number
}) {
  return (
    <Select
      mode="tags"
      style={{ width }}
      value={value}
      onChange={onChange}
      placeholder="留空 = 用表单值"
      options={presets.map((p) => ({ value: String(p), label: String(p) }))}
    />
  )
}

// 候选值 → 数字数组；没填就用表单里的当前值（保证"留空"不是悄悄换成默认值）
function numAxis(vals: string[], fallback: number): number[] {
  const parsed = vals.map((v) => Number(v)).filter((v) => Number.isFinite(v) && v > 0)
  return parsed.length ? parsed : [fallback]
}

// 秒 → 人话（s / 分钟 / 小时）
function fmtDur(sec: number): string {
  if (sec < 60) return `${Math.max(1, Math.round(sec))}s`
  if (sec < 3600) return `${(sec / 60).toFixed(1)} 分钟`
  return `${(sec / 3600).toFixed(1)} 小时`
}

// 排序：不依赖 Table 内置排序（antd v6 行为与 v5 有差异，点了不动），
// 自己维护 sortKey/sortAsc + 点击表头切换，行为完全可预期。
// （SweepSortKey / 扫描状态都在 sweepStore，切页面再回来排序也还在。）
const SWEEP_SORT_LABEL: Record<SweepSortKey, string> = {
  win_rate: '胜率',
  avg_return: '平均收益',
  avg_r: '期望R',
  profit_factor: '盈利因子',
  trades: '样本',
  rr: '盈亏比',
  stop_atr: '止损ATR',
  hold_bars: '持有',
  donchian: 'Donchian',
  orb: 'ORB',
}

function sweepSortValue(r: FuturesSweepRow, key: SweepSortKey): number {
  switch (key) {
    case 'win_rate': return r.win_rate ?? 0
    case 'avg_return': return r.avg_return ?? 0
    case 'avg_r': return r.avg_r ?? 0
    case 'profit_factor': return r.profit_factor ?? 0
    case 'trades': return r.trades ?? 0
    case 'rr': return r.params.rr ?? 0
    case 'stop_atr': return r.params.stop_atr ?? 0
    case 'hold_bars': return r.params.hold_bars ?? 0
    case 'donchian': return r.params.donchian ?? 0
    case 'orb': return r.params.orb ?? 0
  }
}

// SortHeader 可点击表头：显示当前排序方向，点击切换 降序 → 升序 → 降序。
function SortHeader({
  text, hint, sortKey, activeKey, asc, onSort,
}: {
  text: string
  hint?: React.ReactNode
  sortKey: SweepSortKey
  activeKey?: SweepSortKey
  asc: boolean
  onSort: (key: SweepSortKey) => void
}) {
  const active = activeKey === sortKey
  return (
    <span
      role="button"
      tabIndex={0}
      onClick={() => onSort(sortKey)}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') onSort(sortKey)
      }}
      style={{ cursor: 'pointer', userSelect: 'none', whiteSpace: 'nowrap' }}
      title={`按${text}排序`}
    >
      {hint ? <ParamLabel text={text} hint={hint} /> : text}
      <span style={{ marginLeft: 4, color: active ? '#1677ff' : '#bbb', fontSize: 10 }}>
        {active ? (asc ? '▲' : '▼') : '↕'}
      </span>
    </span>
  )
}

function sweepRowKey(r: FuturesSweepRow) {
  return (
    `${r.params.period}-${r.params.rr}-${r.params.stop_atr}-${r.params.hold_bars}-${r.params.donchian}-` +
    `${r.params.orb}-${r.params.atr_period}-${r.params.atr_k}-${r.params.vol_ratio}-${r.params.no_overnight ? 1 : 0}-` +
    `${r.params.stop_mode ?? STOP_MODE_ATR}-${r.params.stop_points ?? 1}`
  )
}

function clipName(name: string) {
  return [...name].slice(0, 80).join('')
}

const sweepCols = (
  onApply: (row: FuturesSweepRow) => void,
  sort: { key?: SweepSortKey; asc: boolean; onSort: (key: SweepSortKey) => void },
): ColumnsType<FuturesSweepRow> => [
  { title: '#', key: 'idx', width: 46, render: (_, __, i) => i + 1 },
  {
    title: <ParamLabel text="级别" hint={TIPS.sweep_period} />,
    dataIndex: ['params', 'period'],
    width: 90,
    render: (v: string) => `${v}分钟`,
  },
  { ...{ title: <SortHeader text="盈亏比" sortKey="rr" activeKey={sort.key} asc={sort.asc} onSort={sort.onSort} /> }, dataIndex: ['params', 'rr'], width: 80 },
  { title: <SortHeader text="止损ATR" sortKey="stop_atr" activeKey={sort.key} asc={sort.asc} onSort={sort.onSort} />, dataIndex: ['params', 'stop_atr'], width: 88 },
  {
    title: <ParamLabel text="止损方式" hint={TIPS.stop_mode} />,
    dataIndex: ['params', 'stop_mode'],
    width: 100,
    render: (v: string, r) => (
      <Tag color={v === STOP_MODE_PREV_LOW ? 'purple' : 'blue'}>{stopModeLabel(v, r.params.stop_points)}</Tag>
    ),
  },
  {
    title: <ParamLabel text="隔夜" hint={TIPS.overnight} />,
    dataIndex: ['params', 'no_overnight'],
    width: 88,
    render: (v: boolean) => (v ? <Tag color="orange">日内</Tag> : <Tag>允许</Tag>),
  },
  { title: <SortHeader text="持有" sortKey="hold_bars" activeKey={sort.key} asc={sort.asc} onSort={sort.onSort} />, dataIndex: ['params', 'hold_bars'], width: 70 },
  { title: <SortHeader text="Donchian" sortKey="donchian" activeKey={sort.key} asc={sort.asc} onSort={sort.onSort} />, dataIndex: ['params', 'donchian'], width: 90 },
  { title: <SortHeader text="ORB" sortKey="orb" activeKey={sort.key} asc={sort.asc} onSort={sort.onSort} />, dataIndex: ['params', 'orb'], width: 70 },
  { title: 'ATR周期', dataIndex: ['params', 'atr_period'], width: 84 },
  { title: 'ATR缓冲', dataIndex: ['params', 'atr_k'], width: 84 },
  { title: '量能', dataIndex: ['params', 'vol_ratio'], width: 70 },
  { title: <SortHeader text="样本" sortKey="trades" activeKey={sort.key} asc={sort.asc} onSort={sort.onSort} />, dataIndex: 'trades', width: 80 },
  { title: <SortHeader text="胜率" sortKey="win_rate" activeKey={sort.key} asc={sort.asc} onSort={sort.onSort} />, dataIndex: 'win_rate', width: 96, render: (v: number) => pct(v) },
  {
    title: <SortHeader text="平均收益" sortKey="avg_return" activeKey={sort.key} asc={sort.asc} onSort={sort.onSort} />,
    dataIndex: 'avg_return',
    width: 110,
    render: (v: number) => <span style={{ color: v >= 0 ? '#389e0d' : '#cf1322' }}>{pct(v)}</span>,
  },
  {
    title: <SortHeader text="期望R" hint={TIPS.r_multiple} sortKey="avg_r" activeKey={sort.key} asc={sort.asc} onSort={sort.onSort} />,
    dataIndex: 'avg_r',
    width: 100,
    render: (v: number) => v.toFixed(2),
  },
  {
    title: <SortHeader text="盈利因子" hint={TIPS.profit_factor} sortKey="profit_factor" activeKey={sort.key} asc={sort.asc} onSort={sort.onSort} />,
    dataIndex: 'profit_factor',
    width: 110,
    render: (v: number) =>
      v ? v.toFixed(2) : <Tooltip title="没有亏损单（PF 理论上是无穷大）"><span>-</span></Tooltip>,
  },
  {
    title: '出场',
    key: 'exits',
    width: 130,
    render: (_, r) => `止 ${r.tp_exits} / 损 ${r.stop_exits} / 到 ${r.hold_exits}`,
  },
  {
    title: '可靠性',
    dataIndex: 'reliable',
    width: 96,
    render: (v: boolean) =>
      v ? <Tag color="green">样本足</Tag> : <Tooltip title="样本数少于「最少样本数」，指标容易被运气主导"><Tag color="orange">样本不足</Tag></Tooltip>,
  },
  {
    title: '操作',
    key: 'ops',
    width: 90,
    render: (_, row) => (
      <Button size="small" type="link" onClick={() => onApply(row)}>
        用这组
      </Button>
    ),
  },
]

export default function FuturesBacktestPage() {
  const { message } = App.useApp()
  const [search] = useSearchParams()
  const [prefix, setPrefix] = useState('JM')
  const [symbol, setSymbol] = useState('JM0')
  const [varieties, setVarieties] = useState<FuturesVariety[]>([])
  const [contracts, setContracts] = useState<FuturesContract[]>([])
  const [loadingContracts, setLoadingContracts] = useState(false)
  const [period, setPeriod] = useState(DEFAULT_PARAMS.period)
  const [orb, setOrb] = useState(DEFAULT_PARAMS.orb)
  const [donchian, setDonchian] = useState(DEFAULT_PARAMS.donchian)
  const [atrPeriod, setAtrPeriod] = useState(DEFAULT_PARAMS.atrPeriod)
  const [atrK, setAtrK] = useState(DEFAULT_PARAMS.atrK)
  const [volRatio, setVolRatio] = useState(DEFAULT_PARAMS.volRatio)
  const [holdBars, setHoldBars] = useState(DEFAULT_PARAMS.holdBars)
  const [stopATR, setStopATR] = useState(DEFAULT_PARAMS.stopATR)
  const [stopMode, setStopMode] = useState<string>(STOP_MODE_ATR)
  const [stopPoints, setStopPoints] = useState(1)
  const [allowOvernight, setAllowOvernight] = useState(true)
  const [rr, setRr] = useState(DEFAULT_PARAMS.rr)
  const [scanning, setScanning] = useState(false)
  const [running, setRunning] = useState(false)
  const [snap, setSnap] = useState<FuturesSnapshot>()
  const [result, setResult] = useState<FuturesBacktestResult>()
  const snapCardRef = useRef<HTMLDivElement>(null)

  // 参数扫描：表单 / 进度 / 结果都在 sweepStore（模块级），切页面回来还在
  const sweepState = useSweepState()
  const {
    periods: swPeriods,
    orb: swOrb,
    donchian: swDon,
    atrPeriod: swAtrP,
    atrK: swAtrK,
    volRatio: swVol,
    holdBars: swHold,
    stopATR: swStop,
    stopModes: swStopModes,
    stopPoints: swStopPoints,
    overnight: swOvernight,
    rr: swRr,
    objective,
    minTrades,
    workers,
  } = sweepState.form
  const setSwPeriods = (v: string[]) => setSweepForm({ periods: v })
  const setSwOrb = (v: string[]) => setSweepForm({ orb: v })
  const setSwDon = (v: string[]) => setSweepForm({ donchian: v })
  const setSwAtrP = (v: string[]) => setSweepForm({ atrPeriod: v })
  const setSwAtrK = (v: string[]) => setSweepForm({ atrK: v })
  const setSwVol = (v: string[]) => setSweepForm({ volRatio: v })
  const setSwHold = (v: string[]) => setSweepForm({ holdBars: v })
  const setSwStop = (v: string[]) => setSweepForm({ stopATR: v })
  const setSwStopModes = (v: string[]) => setSweepForm({ stopModes: v })
  const setSwStopPoints = (v: string[]) => setSweepForm({ stopPoints: v })
  const setSwOvernight = (v: string[]) => setSweepForm({ overnight: v })
  const setSwRr = (v: string[]) => setSweepForm({ rr: v })
  const setObjective = (v: FuturesSweepObjective) => setSweepForm({ objective: v })
  const setMinTrades = (v: number) => setSweepForm({ minTrades: v })
  const setWorkers = setSweepWorkers // 扫描中改会当场通知服务端 Tune 协程池
  const sweeping = sweepState.running
  const sweep = sweepState.result
  const msPerCombo = sweepState.msPerCombo
  const { sortKey, sortAsc, onlyReliable } = sweepState
  const [range, setRange] = useState<RangeValue>(null)
  const [applyFlash, setApplyFlash] = useState(false)
  const [focusIdx, setFocusIdx] = useState<number | undefined>(undefined)
  const flashTimerRef = useRef<number | undefined>(undefined)

  useEffect(() => {
    void (async () => {
      try {
        const list = (await fetchFuturesVarieties()) ?? []
        setVarieties(Array.isArray(list) ? list : [])
      } catch (e) {
        message.error(e instanceof Error ? e.message : '加载品种失败')
      }
    })()
  }, [message])

  useEffect(() => {
    if (!prefix) return
    let cancelled = false
    void (async () => {
      setLoadingContracts(true)
      try {
        const list = (await fetchFuturesContracts(prefix)) ?? []
        if (cancelled) return
        const rows = Array.isArray(list) ? list : []
        setContracts(rows)
        setSymbol((cur) => {
          if (rows.some((c) => c.symbol === cur)) return cur
          const main = rows.find((c) => c.kind === 'main')
          return main?.symbol || rows[0]?.symbol || `${prefix}0`
        })
      } catch (e) {
        if (!cancelled) {
          setContracts([])
          message.error(e instanceof Error ? e.message : '加载合约失败')
        }
      } finally {
        if (!cancelled) setLoadingContracts(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [prefix, message])

  const varietyOpts = useMemo(() => varietyOptions(varieties), [varieties])
  const contractOpts = useMemo(() => contractOptions(contracts), [contracts])

  const params: FuturesParams = {
    symbol,
    period,
    orb,
    donchian,
    atr_period: atrPeriod,
    atr_k: atrK,
    vol_ratio: volRatio,
    hold_bars: holdBars,
    stop_atr: stopATR,
    stop_mode: stopMode,
    stop_points: stopPoints,
    no_overnight: !allowOvernight,
    from: range?.[0]?.format('YYYY-MM-DD HH:mm') ?? '',
    to: range?.[1]?.format('YYYY-MM-DD HH:mm') ?? '',
    rr,
  }

  async function scan() {
    setScanning(true)
    try {
      setSnap(await fetchFuturesScan(params))
      message.success('扫描完成')
    } catch (e) {
      message.error(e instanceof Error ? e.message : '扫描失败')
    } finally {
      setScanning(false)
    }
  }

  async function backtest() {
    setRunning(true)
    try {
      setResult(await postFuturesBacktest(params))
      message.success('回测完成')
    } catch (e) {
      message.error(e instanceof Error ? e.message : '回测失败')
    } finally {
      setRunning(false)
    }
  }

  // 从监控页点提醒行跳过来：?symbol=JM2701&period=15 → 直接加载对应的价格图
  const jumpedRef = useRef(false)
  useEffect(() => {
    if (jumpedRef.current) return
    const target = search.get('symbol')
    if (!target) return
    jumpedRef.current = true
    const level = search.get('period') || period
    const pf = (target.match(/^[A-Za-z]+/) || [''])[0].toUpperCase()
    if (pf) setPrefix(pf)
    setSymbol(target)
    setPeriod(level)
    setScanning(true)
    void (async () => {
      try {
        setSnap(await fetchFuturesScan({ ...params, symbol: target, period: level }))
        message.success(`已从监控跳转加载 ${target}`)
      } catch (e) {
        message.error(e instanceof Error ? e.message : '加载价格图失败')
      } finally {
        setScanning(false)
      }
    })()
    // params 里的其余项在挂载时就是默认值，这里只需要跑一次
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search])

  const sweepBody: FuturesSweepRequest = {
    symbol,
    period,
    periods: swPeriods.length ? swPeriods : [period],
    orb: numAxis(swOrb, orb),
    donchian: numAxis(swDon, donchian),
    atr_period: numAxis(swAtrP, atrPeriod),
    atr_k: numAxis(swAtrK, atrK),
    vol_ratio: numAxis(swVol, volRatio),
    hold_bars: numAxis(swHold, holdBars),
    stop_atr: numAxis(swStop, stopATR),
    stop_modes: swStopModes.length ? swStopModes : [stopMode],
    // 只有选了「前低」方式，点数才有意义：否则点数轴会跑出一堆完全相同的组合
    stop_points: (swStopModes.length ? swStopModes : [stopMode]).includes(STOP_MODE_PREV_LOW)
      ? numAxis(swStopPoints, stopPoints)
      : [stopPoints],
    no_overnight: swOvernight.length ? swOvernight.map(Number) : [allowOvernight ? 0 : 1],
    rr: numAxis(swRr, rr),
    objective,
    min_trades: minTrades,
    workers,
  }
  const sweepRows = useMemo(() => {
    const rows = (sweep?.rows ?? []).filter((r) => !onlyReliable || r.reliable)
    return [...rows].sort((a, b) => {
      const d = sweepSortValue(a, sortKey) - sweepSortValue(b, sortKey)
      if (d !== 0) return sortAsc ? d : -d
      return (b.trades ?? 0) - (a.trades ?? 0) // 同值按样本多的在前
    })
  }, [sweep, sortKey, sortAsc, onlyReliable])
  const [pickedKeys, setPickedKeys] = useState<Key[]>([])
  const [favOpen, setFavOpen] = useState(false)
  const [favName, setFavName] = useState('')
  const [favNote, setFavNote] = useState('')
  const [savingFav, setSavingFav] = useState(false)
  const pickedRows = useMemo(() => {
    const keys = new Set(pickedKeys)
    return (sweep?.rows ?? []).filter((r) => keys.has(sweepRowKey(r)))
  }, [pickedKeys, sweep])

  useEffect(() => {
    setPickedKeys([])
  }, [sweep])

  function toggleSort(key: SweepSortKey) {
    if (key === sortKey) {
      setSweepView({ sortAsc: !sortAsc })
      return
    }
    setSweepView({ sortKey: key, sortAsc: false }) // 换列默认降序（找最优）
  }

  function openSaveFavorites() {
    if (pickedRows.length === 0) {
      message.warning('先勾选要收藏的扫描结果')
      return
    }
    setFavName(`${symbol} 收藏`)
    setFavNote('')
    setFavOpen(true)
  }

  async function saveFavorites() {
    const prefix = favName.trim()
    if (!prefix) {
      message.warning('填一个名称')
      return
    }
    setSavingFav(true)
    try {
      const items = pickedRows.map((row, i) => ({
        name: clipName(
          pickedRows.length === 1
            ? prefix
            : `${prefix} #${i + 1} · ${row.params.period}分钟 RR${row.params.rr} 止损${row.params.stop_atr}`,
        ),
        note: favNote.trim(),
        params: { ...row.params, symbol: sweep?.symbol || symbol },
        origin_symbol: sweep?.symbol || symbol,
        origin_win_rate: row.win_rate,
        origin_avg_return: row.avg_return,
        origin_avg_r: row.avg_r,
        origin_profit_factor: row.profit_factor,
        origin_trades: row.trades,
      }))
      await postFuturesFavorites(items)
      message.success(`已保存 ${items.length} 条，可到「期货收藏」里管理或扫全品种`)
      setFavOpen(false)
      setPickedKeys([])
    } catch (e) {
      message.error(e instanceof Error ? e.message : '保存失败')
    } finally {
      setSavingFav(false)
    }
  }

  useEffect(() => () => window.clearTimeout(flashTimerRef.current), [])

  // 换了回测结果（或改了参数重跑）→ 清掉聚焦，避免下标对不上另一批样本
  useEffect(() => {
    setFocusIdx(undefined)
  }, [result])

  const comboCount =
    (sweepBody.periods?.length ?? 1) *
    (sweepBody.orb?.length ?? 1) *
    (sweepBody.donchian?.length ?? 1) *
    (sweepBody.atr_period?.length ?? 1) *
    (sweepBody.atr_k?.length ?? 1) *
    (sweepBody.vol_ratio?.length ?? 1) *
    (sweepBody.hold_bars?.length ?? 1) *
    (sweepBody.stop_atr?.length ?? 1) *
    (sweepBody.no_overnight?.length ?? 1) *
    (sweepBody.rr?.length ?? 1)

  async function runSweep() {
    try {
      // 请求挂在 sweepStore 上：切页面也照跑，回来继续显示进度/结果
      const res = await startSweep(sweepBody)
      if (!res) return // 已经有一次在跑
      message.success(`扫完 ${res.combos} 个组合（并发 ${res.workers}），用时 ${(res.elapsed_ms / 1000).toFixed(1)}s`)
    } catch (e) {
      message.error(e instanceof Error ? e.message : '参数扫描失败')
    }
  }

  // 套用扫描结果到上面的回测表单。
  // 注意：级别（period）也是扫描轴，必须一起覆盖 —— 漏掉它就会出现「点了用该组但配置没变」。
  function applySweepRow(row: FuturesSweepRow) {
    const p = row.params
    const num = (v: number | undefined, fallback: number) =>
      typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : fallback

    const nextPeriod = p.period || period
    const nextOrb = num(p.orb, orb)
    const nextDonchian = num(p.donchian, donchian)
    const nextATRPeriod = num(p.atr_period, atrPeriod)
    const nextATRK = num(p.atr_k, atrK)
    const nextVol = num(p.vol_ratio, volRatio)
    const nextStop = num(p.stop_atr, stopATR)
    const nextStopMode = p.stop_mode || stopMode
    const nextStopPoints = num(p.stop_points, stopPoints)
    const nextHold = num(p.hold_bars, holdBars)
    const nextRR = num(p.rr, rr)
    const nextOvernight = !(p.no_overnight ?? false)

    setPeriod(nextPeriod)
    setOrb(nextOrb)
    setDonchian(nextDonchian)
    setAtrPeriod(nextATRPeriod)
    setAtrK(nextATRK)
    setVolRatio(nextVol)
    setStopATR(nextStop)
    setStopMode(nextStopMode)
    setStopPoints(nextStopPoints)
    setHoldBars(nextHold)
    setRr(nextRR)
    setAllowOvernight(nextOvernight)

    // 让「套用成功」看得见：滚回表单 + 高亮一下，并把套用的值念出来
    setApplyFlash(true)
    window.clearTimeout(flashTimerRef.current)
    flashTimerRef.current = window.setTimeout(() => setApplyFlash(false), 2600)
    document.querySelector('.page-wrap')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    message.success(
      `已套用：${nextPeriod}分钟 · ORB ${nextOrb} · Donchian ${nextDonchian} · ` +
        `止损 ${
          nextStopMode === STOP_MODE_PREV_LOW ? `前低/前高±${nextStopPoints}点` : `${nextStop}×ATR`
        } · 盈亏比 ${nextRR} · 持有 ${nextHold} 根${nextOvernight ? '' : ' · 日内'} → 点「回测突破」看逐笔明细`,
    )
  }

  const objectiveLabel = OBJECTIVES.find((o) => o.value === objective)?.label ?? objective
  const etaSec = msPerCombo ? Math.round((comboCount * msPerCombo) / 1000) : undefined
  // 扫描中的实时进度（进度条 + 已用/剩余时间）
  const sweepPct = sweepPercent(sweepState)
  const sweepElapsed = sweepElapsedSec(sweepState)
  const sweepLeft = sweepRemainSec(sweepState)

  return (
    <div className="page-wrap">
      <Typography.Title level={4} style={{ marginBottom: 16 }}>
        期货回测
      </Typography.Title>
      <Card
        size="small"
        style={{
          marginBottom: 16,
          boxShadow: applyFlash ? '0 0 0 3px #ffa940' : undefined,
          transition: 'box-shadow .25s',
        }}
      >
        <Form layout="inline">
          <Form.Item label="品种">
            <Select
              showSearch
              optionFilterProp="label"
              style={{ width: 200 }}
              value={prefix}
              onChange={setPrefix}
              options={varietyOpts}
              placeholder="焦煤"
            />
          </Form.Item>
          <Form.Item label="合约">
            <Select
              showSearch
              optionFilterProp="label"
              style={{ width: 140 }}
              value={symbol}
              onChange={setSymbol}
              options={contractOpts}
              loading={loadingContracts}
              placeholder="主连 / 2611"
            />
          </Form.Item>
          <Form.Item label="级别">
            <Select style={{ width: 100 }} value={period} onChange={setPeriod} options={LEVEL_OPTIONS} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="ORB分钟" hint={TIPS.orb} />}>
            <InputNumber min={5} value={orb} onChange={(v) => setOrb(Number(v ?? 30))} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="Donchian" hint={TIPS.donchian} />}>
            <InputNumber min={2} value={donchian} onChange={(v) => setDonchian(Number(v ?? 20))} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="ATR周期" hint={TIPS.atr_period} />}>
            <InputNumber min={2} value={atrPeriod} onChange={(v) => setAtrPeriod(Number(v ?? 14))} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="ATR缓冲" hint={TIPS.atr_k} />}>
            <InputNumber min={0.05} step={0.05} value={atrK} onChange={(v) => setAtrK(Number(v ?? 0.25))} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="量能倍数" hint={TIPS.vol_ratio} />}>
            <InputNumber min={0.5} step={0.1} value={volRatio} onChange={(v) => setVolRatio(Number(v ?? 1.5))} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="持有K线" hint={TIPS.hold_bars} />}>
            <InputNumber min={1} value={holdBars} onChange={(v) => setHoldBars(Number(v ?? 6))} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="时间范围" hint={TIPS.range} />}>
            <Space size={4}>
              <DatePicker.RangePicker
                showTime={{ format: 'HH:mm' }}
                format="YYYY-MM-DD HH:mm"
                value={range}
                onChange={setRange}
                allowClear
                placeholder={['不限', '不限']}
                style={{ width: 330 }}
              />
              <Button size="small" onClick={() => setRange([dayjs().subtract(1, 'month'), dayjs()])}>
                近1月
              </Button>
              <Button size="small" onClick={() => setRange([dayjs().subtract(3, 'month'), dayjs()])}>
                近3月
              </Button>
              <Button size="small" onClick={() => setRange(null)}>
                不限
              </Button>
            </Space>
          </Form.Item>
          <Form.Item label={<ParamLabel text="允许隔夜" hint={TIPS.overnight} />}>
            <Switch checked={allowOvernight} onChange={setAllowOvernight} checkedChildren="允许" unCheckedChildren="日内" />
          </Form.Item>
          <Form.Item label={<ParamLabel text="止损方式" hint={TIPS.stop_mode} />}>
            <Select style={{ width: 172 }} value={stopMode} onChange={setStopMode} options={STOP_MODE_OPTIONS} />
          </Form.Item>
          {stopMode === STOP_MODE_PREV_LOW ? (
            <Form.Item label={<ParamLabel text="止损点数" hint={TIPS.stop_points} />}>
              <InputNumber
                min={0.5}
                max={20}
                step={0.5}
                value={stopPoints}
                onChange={(v) => setStopPoints(Number(v ?? 1))}
              />
            </Form.Item>
          ) : (
            <Form.Item label={<ParamLabel text="止损ATR" hint={TIPS.stop_atr} />}>
              <InputNumber min={0.1} max={5} step={0.25} value={stopATR} onChange={(v) => setStopATR(Number(v ?? 1))} />
            </Form.Item>
          )}
          <Form.Item label={<ParamLabel text="盈亏比" hint={TIPS.rr} />}>
            <InputNumber min={0.1} step={0.1} value={rr} onChange={(v) => setRr(Number(v ?? 1.5))} />
          </Form.Item>
          <Form.Item>
            <Space>
              <Button onClick={() => void scan()} loading={scanning}>
                扫描关键位
              </Button>
              <Button type="primary" onClick={() => void backtest()} loading={running}>
                回测突破
              </Button>
            </Space>
          </Form.Item>
        </Form>
      </Card>

      <Card
        size="small"
        style={{ marginBottom: 16 }}
        title={<ParamLabel text="参数扫描" hint={TIPS.sweep} />}
        extra={
          <Space>
            <ParamLabel text="目标" hint={TIPS.objective} />
            <Select
              style={{ width: 120 }}
              value={objective}
              onChange={setObjective}
              options={OBJECTIVES}
            />
            <ParamLabel text="最少样本" hint={TIPS.min_trades} />
            <InputNumber min={1} max={500} value={minTrades} onChange={(v) => setMinTrades(Number(v ?? 30))} style={{ width: 90 }} />
            <Tooltip title="隐藏样本数少于「最少样本」的组合（避免被 3 笔 100% 胜率这种组合带偏）">
              <span className="param-label">
                只看样本足
                <Switch size="small" checked={onlyReliable} onChange={(v) => setSweepView({ onlyReliable: v })} />
              </span>
            </Tooltip>
            <ParamLabel text="并发" hint={TIPS.workers} />
            <InputNumber min={0} max={64} value={workers} onChange={(v) => setWorkers(Number(v ?? 0))} style={{ width: 80 }} />
            {etaSec && !sweeping ? (
              <Typography.Text type={etaSec > 30 ? 'danger' : 'secondary'} style={{ fontSize: 12 }}>
                预计 ~{etaSec >= 60 ? `${Math.round(etaSec / 60)} 分钟` : `${etaSec}s`}
                {etaSec > 30 ? '（很久，考虑缩小范围）' : ''}
              </Typography.Text>
            ) : null}
            <Button type="primary" loading={sweeping} onClick={() => void runSweep()}>
              开始扫描（{comboCount} 组）
            </Button>
          </Space>
        }
      >
        <Space wrap size={12}>
          <span>
            <ParamLabel text="级别" hint={TIPS.sweep_period} />{' '}
            <Select
              mode="multiple"
              allowClear
              style={{ minWidth: 200 }}
              placeholder={`留空 = 用表单级别（${period}分钟）`}
              value={swPeriods}
              onChange={setSwPeriods}
              options={LEVEL_OPTIONS}
            />
          </span>
          <span>
            <ParamLabel text="隔夜" hint={TIPS.overnight} />{' '}
            <Select
              mode="multiple"
              allowClear
              style={{ minWidth: 170 }}
              placeholder={allowOvernight ? '留空 = 允许隔夜' : '留空 = 日内'}
              value={swOvernight}
              onChange={setSwOvernight}
              options={[
                { value: '0', label: '允许隔夜' },
                { value: '1', label: '日内（禁隔夜）' },
              ]}
            />
          </span>
          <span>
            <ParamLabel text="止损方式" hint={TIPS.stop_mode} />{' '}
            <Select
              mode="multiple"
              allowClear
              style={{ minWidth: 226 }}
              placeholder="留空 = 只用当前方式"
              value={swStopModes}
              onChange={setSwStopModes}
              options={STOP_MODE_OPTIONS}
            />
            <Tooltip title="两种都选上 → 同一段行情一次跑出两套止损，直接对照">
              <Button
                size="small"
                style={{ marginLeft: 6 }}
                onClick={() => setSwStopModes([STOP_MODE_ATR, STOP_MODE_PREV_LOW])}
              >
                两种都跑
              </Button>
            </Tooltip>
          </span>
          <span>
            <ParamLabel text="止损点数" hint={TIPS.stop_points} />{' '}
            <NumTags value={swStopPoints} onChange={setSwStopPoints} presets={[1, 2, 3]} />
          </span>
          <span>
            <ParamLabel text="止损ATR" hint={TIPS.stop_atr} />{' '}
            <NumTags value={swStop} onChange={setSwStop} presets={SWEEP_PRESETS.stopATR} width={150} />
          </span>
          <span>
            <ParamLabel text="盈亏比" hint={TIPS.rr} />{' '}
            <NumTags value={swRr} onChange={setSwRr} presets={SWEEP_PRESETS.rr} />
          </span>
          <span>
            <ParamLabel text="持有K线" hint={TIPS.hold_bars} />{' '}
            <NumTags value={swHold} onChange={setSwHold} presets={SWEEP_PRESETS.holdBars} />
          </span>
          <span>
            <ParamLabel text="Donchian" hint={TIPS.donchian} />{' '}
            <NumTags value={swDon} onChange={setSwDon} presets={SWEEP_PRESETS.donchian} />
          </span>
          <span>
            <ParamLabel text="ORB分钟" hint={TIPS.orb} />{' '}
            <NumTags value={swOrb} onChange={setSwOrb} presets={SWEEP_PRESETS.orb} width={140} />
          </span>
          <span>
            <ParamLabel text="ATR周期" hint={TIPS.atr_period} />{' '}
            <NumTags value={swAtrP} onChange={setSwAtrP} presets={SWEEP_PRESETS.atrPeriod} width={140} />
          </span>
          <span>
            <ParamLabel text="ATR缓冲" hint={TIPS.atr_k} />{' '}
            <NumTags value={swAtrK} onChange={setSwAtrK} presets={SWEEP_PRESETS.atrK} width={140} />
          </span>
          <span>
            <ParamLabel text="量能倍数" hint={TIPS.vol_ratio} />{' '}
            <NumTags value={swVol} onChange={setSwVol} presets={SWEEP_PRESETS.volRatio} width={140} />
          </span>
        </Space>
        <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginTop: 8, marginBottom: 0 }}>
          每个框里填多个候选值（回车确认，或从下拉里挑）→ 跑所有组合；留空的用上面表单里的当前值。
          <b>级别也可以多选</b>（每个级别各自取数，所以级别越多越慢）；合约只影响显示标签。
          样本不足的组合会被标出来并排在后面。
        </Typography.Paragraph>

        {sweeping ? (
          <div style={{ marginTop: 12 }}>
            <Progress percent={sweepPct} status="active" size="small" />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {sweepState.total > 0
                ? `已跑 ${sweepState.done.toLocaleString()} / ${sweepState.total.toLocaleString()} 组`
                : '正在取各级别 K 线…'}
              （并发 {sweepState.liveWorkers ?? (workers > 0 ? workers : '自动')}，改上面的「并发」立刻生效）· 已用{' '}
              {fmtDur(sweepElapsed)}
              {sweepLeft != null ? ` · 预计还需 ${fmtDur(sweepLeft)}` : ''} —— 可以切到别的页面，
              回来进度和结果都还在（但别刷新页面，刷新会取消请求）
            </Typography.Text>
          </div>
        ) : null}

        {sweep ? (
          <>
            <Alert
              style={{ margin: '12px 0' }}
              type={sweep.best?.reliable ? 'success' : 'warning'}
              showIcon
              message={
                sweep.best
                  ? `最佳组合（按${objectiveLabel}）：${sweep.best.params.period}分钟 · 止损 ${
                      sweep.best.params.stop_mode === STOP_MODE_PREV_LOW
                        ? `前低/前高±${sweep.best.params.stop_points ?? 1}点`
                        : `${sweep.best.params.stop_atr}×ATR`
                    } · 盈亏比 ${sweep.best.params.rr} · 持有 ${sweep.best.params.hold_bars} · Donchian ${sweep.best.params.donchian} · ORB ${sweep.best.params.orb} · ATR周期 ${sweep.best.params.atr_period} · ATR缓冲 ${sweep.best.params.atr_k} · 量能 ${sweep.best.params.vol_ratio}`
                  : '没有可用组合'
              }
              description={
                sweep.best ? (
                  <Space size={16} wrap>
                    <span>样本 {sweep.best.trades}</span>
                    <span>胜率 {pct(sweep.best.win_rate)}</span>
                    <span>平均收益 {pct(sweep.best.avg_return)}</span>
                    <span>期望R {sweep.best.avg_r.toFixed(2)}</span>
                    <span>盈利因子 {sweep.best.profit_factor ? sweep.best.profit_factor.toFixed(2) : '-'}</span>
                    {!sweep.best.reliable ? <Tag color="orange">样本不足，别当结论</Tag> : null}
                    <Button size="small" type="primary" onClick={() => applySweepRow(sweep.best!)}>
                      用这组回测
                    </Button>
                  </Space>
                ) : null
              }
            />
            <Table
              size="small"
              rowKey={sweepRowKey}
              rowSelection={{
                selectedRowKeys: pickedKeys,
                preserveSelectedRowKeys: true,
                onChange: (keys) => setPickedKeys(keys),
              }}
              columns={sweepCols(applySweepRow, { key: sortKey, asc: sortAsc, onSort: toggleSort })}
              dataSource={sweepRows}
              pagination={{ pageSize: 20, showSizeChanger: false }}
              scroll={{ x: 1400 }}
              locale={{ emptyText: '没有组合跑出样本' }}
              title={() => (
                <Space size={12} wrap>
                  <Button size="small" type="primary" disabled={pickedRows.length === 0} onClick={openSaveFavorites}>
                    保存为收藏{pickedRows.length ? `（${pickedRows.length}）` : ''}
                  </Button>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    共 {sweep.combos} 个组合（{(sweep.periods ?? []).map((p) => `${p}分钟`).join(' / ')}）· 当前 {sweepRows.length}{' '}
                    行{onlyReliable && sweepRows.length !== sweep.rows.length ? `（已隐藏样本不足的 ${sweep.rows.length - sweepRows.length} 组）` : ''} · 排序：
                    {SWEEP_SORT_LABEL[sortKey]}{sortAsc ? ' 升序' : ' 降序'} · 并发{' '}
                    {sweep.workers ?? 0} · 回测耗时 {(sweep.elapsed_ms / 1000).toFixed(1)}s · 各级别 K 线根数{' '}
                    {(sweep.periods ?? []).map((p) => `${p}分钟 ${sweep.period_bars?.[p] ?? 0} 根`).join('，')}
                    {' '}—— <b>覆盖区间不同，样本数不可直接横向比</b>
                    {sweep.skipped?.length ? ` · 跳过：${sweep.skipped.join('；')}` : ''}
                  </Typography.Text>
                </Space>
              )}
            />
          </>
        ) : null}
      </Card>

      {snap ? (
        <Card
          ref={snapCardRef}
          size="small"
          style={{ marginBottom: 16 }}
          title={`${snap.symbol} 关键位（${snap.day}${snap.trend ? ` · 大趋势 ${snap.trend}` : ''}）`}
        >
          <Space wrap size={8} style={{ marginBottom: 8 }}>
            {snap.levels.map((lv) => (
              <Tag key={lv.name} color={lv.kind === 'R' ? 'red' : 'green'}>
                {lv.name} {fmtPrice(lv.value, 1)}
              </Tag>
            ))}
            {snap.vwap ? <Tag color="blue">VWAP {fmtPrice(snap.vwap, 1)}</Tag> : null}
            {snap.position ? <Tag>{snap.position}</Tag> : null}
          </Space>
          {snap.resonance?.length ? (
            <Typography.Paragraph type="secondary" style={{ marginBottom: 8 }}>
              {snap.resonance.join('；')}
            </Typography.Paragraph>
          ) : null}
          <FuturesChart
            bars={snap.bars_period?.length ? snap.bars_period : (period === '5' ? snap.bars_5 : snap.bars_15) ?? []}
            events={[...snap.events_5, ...snap.events_15]}
            levels={snap.levels}
          />
          <Table
            size="small"
            rowKey={(r) => `${r.time}-${r.level}`}
            columns={eventCols}
            dataSource={[...snap.events_5, ...snap.events_15]}
            pagination={false}
            locale={{ emptyText: '当日暂无突破' }}
          />
        </Card>
      ) : null}

      {result ? (
        <>
          <Space size="large" style={{ marginBottom: 8 }} wrap>
            <Statistic title="样本" value={result.trades} />
            <Statistic title="正确" value={result.correct} />
            <Statistic title="胜率" value={pct(result.win_rate)} />
            <Statistic title="平均收益" value={pct(result.avg_return)} />
            <Statistic
              title={<ParamLabel text="盈利因子" hint={TIPS.profit_factor} />}
              value={result.profit_factor ? result.profit_factor.toFixed(2) : '-'}
            />
            <Statistic
              title={<ParamLabel text="期望R" hint={TIPS.r_multiple} />}
              value={result.avg_r ? `${result.avg_r.toFixed(2)}R` : '-'}
            />
            <Statistic
              title="出场分布"
              value={`止损 ${result.stop_exits ?? 0} · 止盈 ${result.tp_exits ?? 0} · 到期 ${result.hold_exits ?? 0}${
                result.eod_exits ? ` · 日内 ${result.eod_exits}` : ''
              }`}
            />
          </Space>
          <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
            图上图层可开关（买点/卖点、出场 R 倍数、均线、成交量）；<b>点明细某一行或点图上买点附近</b> →
            聚焦这一笔，画出它的入场价 / 止损价 / 止盈价三条横线，再点一次取消。
            <br />
            回测规则：入场 = 信号那根收盘价 · 止损 = {stopATR}×ATR · 止盈 = 止损距离×{rr} · 最多持有 {holdBars} 根；
            同根既破止损又触止盈按止损算、跳空按开盘价成交；收益已按突破方向折算，未计手续费与滑点。
            {result.skipped_eod ? ` 因「禁止隔夜」跳过 ${result.skipped_eod} 个收盘后/夜盘的信号（不计入统计）。` : ''}
            {result.from || result.to
              ? ` 时间范围 ${result.from || '不限'} ~ ${result.to || '不限'}（按信号时间筛选，出场可延续到范围之后）。`
              : ' 时间范围：不限。'}
          </Typography.Paragraph>
          <FuturesChart
            bars={result.bars ?? []}
            events={result.items ?? []}
            mode="trade"
            focusIndex={focusIdx}
            onFocus={(i) => setFocusIdx((cur) => (cur === i ? undefined : i))}
          />
          <Table
            size="small"
            rowKey={(_, i) => String(i)}
            columns={outcomeCols()}
            dataSource={result.items ?? []}
            pagination={{ pageSize: 50 }}
            scroll={{ x: 1300 }}
            locale={{ emptyText: '无样本（分钟线仅覆盖近期）' }}
            rowClassName={(_, i) => (i === focusIdx ? 'row-active' : '')}
            onRow={(_, i) => ({
              onClick: () => setFocusIdx((cur) => (cur === i ? undefined : i)),
              style: { cursor: 'pointer' },
            })}
          />
        </>
      ) : null}
      <Modal
        title={`保存 ${pickedRows.length} 条收藏`}
        open={favOpen}
        confirmLoading={savingFav}
        onOk={() => void saveFavorites()}
        onCancel={() => setFavOpen(false)}
        okText="保存"
      >
        <Form layout="vertical">
          <Form.Item label="名称" extra={pickedRows.length > 1 ? '多条时会在名称后自动加上级别、盈亏比和止损，方便区分' : undefined}>
            <Input value={favName} maxLength={40} onChange={(e) => setFavName(e.target.value)} />
          </Form.Item>
          <Form.Item label="备注">
            <Input.TextArea value={favNote} rows={3} maxLength={500} onChange={(e) => setFavNote(e.target.value)} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}
