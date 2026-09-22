import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import {
  App,
  Button,
  Card,
  Form,
  InputNumber,
  Select,
  Space,
  Statistic,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  CandlestickSeries,
  ColorType,
  HistogramSeries,
  createChart,
  createSeriesMarkers,
  type IChartApi,
  type IPriceLine,
  type ISeriesApi,
  type ISeriesMarkersPluginApi,
  type SeriesMarker,
  type Time,
  type UTCTimestamp,
} from 'lightweight-charts'
import {
  fetchFuturesContracts,
  fetchFuturesScan,
  fetchFuturesVarieties,
  fetchFuturesWatchEvents,
  fetchFuturesWatchStatus,
  postFuturesBacktest,
  startFuturesWatch,
  stopFuturesWatch,
  updateFuturesWatch,
} from '../api'
import type {
  FuturesBacktestResult,
  FuturesContract,
  FuturesEvent,
  FuturesKlineBar,
  FuturesLevel,
  FuturesOutcome,
  FuturesSnapshot,
  FuturesVariety,
  FuturesWatchEvent,
  FuturesWatchStatus,
} from '../types'

function toBarTime(s: string): UTCTimestamp {
  const m = s.match(/^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})/)
  if (!m) return 0 as UTCTimestamp
  return Math.floor(Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4] - 8, +m[5]) / 1000) as UTCTimestamp
}

function uniqueBars(bars: FuturesKlineBar[]) {
  const seen = new Set<number>()
  const out: FuturesKlineBar[] = []
  for (const b of bars) {
    const t = toBarTime(b.time)
    if (!t || seen.has(t)) continue
    seen.add(t)
    out.push(b)
  }
  return out
}

function FuturesChart({
  bars,
  events,
  levels,
}: {
  bars: FuturesKlineBar[]
  events: FuturesEvent[]
  levels?: FuturesLevel[]
}) {
  const wrapRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<IChartApi | null>(null)
  const candleRef = useRef<ISeriesApi<'Candlestick'> | null>(null)
  const volRef = useRef<ISeriesApi<'Histogram'> | null>(null)
  const markersRef = useRef<ISeriesMarkersPluginApi<Time> | null>(null)
  const linesRef = useRef<IPriceLine[]>([])

  useEffect(() => {
    const el = wrapRef.current
    if (!el) return
    const chart = createChart(el, {
      autoSize: true,
      layout: { background: { type: ColorType.Solid, color: '#fff' }, textColor: '#333' },
      grid: { vertLines: { color: '#eee' }, horzLines: { color: '#eee' } },
      rightPriceScale: { borderColor: '#ddd' },
      timeScale: { borderColor: '#ddd', timeVisible: true, secondsVisible: false },
    })
    const candle = chart.addSeries(CandlestickSeries, {
      upColor: '#ef5350',
      downColor: '#26a69a',
      borderVisible: false,
      wickUpColor: '#ef5350',
      wickDownColor: '#26a69a',
    })
    const vol = chart.addSeries(HistogramSeries, {
      priceFormat: { type: 'volume' },
      priceScaleId: 'vol',
    })
    chart.priceScale('vol').applyOptions({ scaleMargins: { top: 0.8, bottom: 0 } })
    chartRef.current = chart
    candleRef.current = candle
    volRef.current = vol
    markersRef.current = createSeriesMarkers(candle, [])
    return () => {
      linesRef.current = []
      markersRef.current = null
      chart.remove()
      chartRef.current = null
      candleRef.current = null
      volRef.current = null
    }
  }, [])

  useEffect(() => {
    const chart = chartRef.current
    const candle = candleRef.current
    const vol = volRef.current
    if (!chart || !candle || !vol) return
    const list = uniqueBars(bars)
    candle.setData(
      list.map((b) => ({
        time: toBarTime(b.time),
        open: b.open,
        high: b.high,
        low: b.low,
        close: b.close,
      })),
    )
    vol.setData(
      list.map((b) => ({
        time: toBarTime(b.time),
        value: b.volume,
        color: b.close >= b.open ? '#ef535088' : '#26a69a88',
      })),
    )
    const marks: SeriesMarker<Time>[] = []
    for (const e of events) {
      const up = e.direction.includes('向上')
      marks.push({
        time: toBarTime(e.time),
        position: up ? 'belowBar' : 'aboveBar',
        color: up ? '#ef5350' : '#26a69a',
        shape: up ? 'arrowUp' : 'arrowDown',
        text: e.level,
      })
    }
    markersRef.current?.setMarkers(marks)
    for (const ln of linesRef.current) candle.removePriceLine(ln)
    linesRef.current = []
    for (const lv of levels ?? []) {
      linesRef.current.push(
        candle.createPriceLine({
          price: lv.value,
          color: lv.kind === 'R' ? '#ef535099' : '#26a69a99',
          lineWidth: 1,
          lineStyle: 2,
          axisLabelVisible: true,
          title: lv.name,
        }),
      )
    }
    chart.timeScale().fitContent()
  }, [bars, events, levels])

  if (!bars.length) return null
  return <div className="futures-chart" ref={wrapRef} />
}

const eventCols: ColumnsType<FuturesEvent> = [
  { title: '时间', dataIndex: 'time', width: 150 },
  { title: '方向', dataIndex: 'direction', width: 100 },
  { title: '关键位', dataIndex: 'level', width: 160 },
  { title: '收盘', dataIndex: 'close', render: (v: number) => v?.toFixed(1) },
  { title: '关键位价', dataIndex: 'level_price', render: (v: number) => v?.toFixed(1) },
  { title: '量', dataIndex: 'volume' },
]

const outcomeCols: ColumnsType<FuturesOutcome> = [
  { title: '时间', dataIndex: 'time', width: 150 },
  { title: '方向', dataIndex: 'direction', width: 100 },
  { title: '关键位', dataIndex: 'level', width: 160 },
  { title: '收盘', dataIndex: 'close', render: (v: number) => v?.toFixed(1) },
  { title: '关键位价', dataIndex: 'level_price', render: (v: number) => v?.toFixed(1) },
  { title: '量', dataIndex: 'volume' },
  { title: '平仓时间', dataIndex: 'exit_time', width: 150 },
  { title: '平仓价', dataIndex: 'exit_price', render: (v: number) => v?.toFixed(1) },
  {
    title: '收益',
    dataIndex: 'return',
    render: (v: number) => (v == null ? '' : `${(v * 100).toFixed(2)}%`),
  },
  { title: '判断', dataIndex: 'correct', render: (v: boolean) => (v ? '正确' : '错误') },
]

const alertCols: ColumnsType<FuturesWatchEvent> = [
  { title: '时间', dataIndex: 'time', width: 140 },
  {
    title: '品种',
    dataIndex: 'name',
    width: 100,
    render: (v: string, r) => `${v} ${r.prefix}`,
  },
  {
    title: '方向',
    dataIndex: 'direction',
    width: 100,
    render: (v: string) => <Tag color={v.includes('向上') ? 'red' : 'green'}>{v}</Tag>,
  },
  { title: '关键位', dataIndex: 'level' },
  { title: '现价', dataIndex: 'close', width: 90, render: (v: number) => v?.toFixed(1) },
  { title: '关键位价', dataIndex: 'level_price', width: 100, render: (v: number) => v?.toFixed(1) },
  { title: '量', dataIndex: 'volume', width: 90 },
  {
    title: '来源',
    dataIndex: 'fresh',
    width: 110,
    render: (v: boolean) => (v ? <Tag color="blue">实时</Tag> : <Tag>启动时已有</Tag>),
  },
]

const TIPS: Record<string, ReactNode> = {
  orb: (
    <>
      <b>开盘区间突破窗口（默认 30 = 前 6 根 5 分钟）</b>
      <br />
      取当日 09:00 起的前 N/5 根 5 分钟 K 线的最高/最低，作为 ORB 高/低（只在 09:00–11:00 之间取）。
      <br />
      收盘 &gt; ORB高 + ATR缓冲 → 向上突破；收盘 &lt; ORB低 − 缓冲 → 向下跌破。
      <br />
      只影响「级别 = 5分钟」的事件识别（15/30/60 只用枢轴位 + Donchian）。值越大 → 区间越宽、信号越少越晚。
    </>
  ),
  donchian: (
    <>
      <b>唐奇安通道根数（默认 20）</b>
      <br />
      上轨 = 最近 N 根的最高价，下轨 = 最近 N 根的最低价（不含当前根，前移一根）。
      <br />
      收盘突破上轨 + ATR缓冲 → 向上突破；跌破下轨 − 缓冲 → 向下跌破。
      <br />
      数值越小越灵敏（假突破多），越大越滞后（趋势确认更强）。
    </>
  ),
  atr_period: (
    <>
      <b>ATR 的均值周期（默认 14）</b>
      <br />
      真实波幅 TR = max(当根高−低, |高−昨收|, |低−昨收|)，再取最近 N 根的均值 → ATR，表示平均波动幅度。
      <br />
      它只决定「ATR缓冲」的尺度，本身不产生信号。
    </>
  ),
  atr_k: (
    <>
      <b>突破缓冲系数（默认 0.25）</b>
      <br />
      有效突破要求收盘价越过关键位 <b>ATR × 该系数</b>（不足 1 个价格单位时按 1 算）。
      <br />
      用来过滤贴着关键位的假突破：调大 → 信号更少更稳；调小 → 更早更灵敏。
    </>
  ),
  vol_ratio: (
    <>
      <b>突破放量倍数（默认 1.5）</b>
      <br />
      突破那根 K 线的成交量必须 ≥ 最近 20 根均量（至少 5 根有效）的该倍数，缩量突破不算数。
      <br />
      用来过滤无量假突破：调大 → 只保留明显放量的突破。
    </>
  ),
  hold_bars: (
    <>
      <b>回测持有根数（默认 6）</b>
      <br />
      回测入场后固定持有 N 根同级别 K 线，在第 N 根收盘平仓；收益 = 平仓收盘 / 入场收盘 − 1。
      <br />
      「正确」= 收益方向与突破方向一致（向上突破且收益 &gt; 0，或向下跌破且收益 &lt; 0）。
      <br />
      只影响回测统计，不影响扫描出的关键位。
    </>
  ),
}

function ParamLabel({ text, hint }: { text: string; hint: ReactNode }) {
  return (
    <span className="param-label">
      {text}
      <Tooltip title={<div className="param-tip">{hint}</div>} placement="top">
        <span className="param-help" aria-label={`${text}是什么意思`}>
          ?
        </span>
      </Tooltip>
    </span>
  )
}

export default function FuturesPage() {
  const { message, notification } = App.useApp()
  const [prefix, setPrefix] = useState('JM')
  const [symbol, setSymbol] = useState('JM0')
  const [varieties, setVarieties] = useState<FuturesVariety[]>([])
  const [contracts, setContracts] = useState<FuturesContract[]>([])
  const [loadingContracts, setLoadingContracts] = useState(false)
  const [period, setPeriod] = useState('5')
  const [orb, setOrb] = useState(30)
  const [donchian, setDonchian] = useState(20)
  const [atrPeriod, setAtrPeriod] = useState(14)
  const [atrK, setAtrK] = useState(0.25)
  const [volRatio, setVolRatio] = useState(1.5)
  const [holdBars, setHoldBars] = useState(6)
  const [scanning, setScanning] = useState(false)
  const [running, setRunning] = useState(false)
  const [snap, setSnap] = useState<FuturesSnapshot>()
  const [result, setResult] = useState<FuturesBacktestResult>()
  const [watchPrefixes, setWatchPrefixes] = useState<string[]>([])
  const [watchInterval, setWatchInterval] = useState(30)
  const [watch, setWatch] = useState<FuturesWatchStatus>()
  const [alerts, setAlerts] = useState<FuturesWatchEvent[]>([])
  const [watchBusy, setWatchBusy] = useState(false)
  const cursorRef = useRef(0)
  const runningRef = useRef(false)
  const firstPullRef = useRef(true)

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

  const varietyOpts = useMemo(() => {
    const m = new Map<string, { value: string; label: string }[]>()
    for (const v of varieties) {
      const arr = m.get(v.exchange) ?? []
      arr.push({ value: v.prefix, label: `${v.name} ${v.prefix}` })
      m.set(v.exchange, arr)
    }
    return [...m.entries()].map(([label, options]) => ({ label, options }))
  }, [varieties])

  const contractOpts = useMemo(
    () =>
      contracts.map((c) => ({
        value: c.symbol,
        label: c.label || (c.kind === 'main' ? '主连' : c.symbol),
      })),
    [contracts],
  )

  const params = {
    symbol,
    period,
    orb,
    donchian,
    atr_period: atrPeriod,
    atr_k: atrK,
    vol_ratio: volRatio,
    hold_bars: holdBars,
  }

  const watchRunning = watch?.running ?? false
  const coolingSources = (watch?.sources ?? [])
    .filter((s) => s.cooldown > 0)
    .map((s) => `${s.name} 冷却 ${s.cooldown}s`)
    .join('，')

  const watchConfig = useMemo(
    () => ({
      period,
      orb,
      donchian,
      atr_period: atrPeriod,
      atr_k: atrK,
      vol_ratio: volRatio,
      interval: watchInterval,
      prefixes: watchPrefixes,
    }),
    [period, orb, donchian, atrPeriod, atrK, volRatio, watchInterval, watchPrefixes],
  )

  const notifyBreakout = useCallback(
    (e: FuturesWatchEvent) => {
      const up = e.direction.includes('向上')
      const title = `${e.name} ${e.prefix} ${e.direction} ${e.level}`
      const desc = `现价 ${e.close} · 关键位 ${e.level_price} · ${e.time}`
      notification.open({
        key: `futures-watch-${e.seq}`,
        message: title,
        description: desc,
        type: up ? 'success' : 'warning',
        placement: 'topRight',
        duration: 0,
      })
      try {
        if (typeof Notification !== 'undefined' && Notification.permission === 'granted') {
          new Notification(title, { body: desc })
        }
      } catch {
        /* 浏览器原生通知不可用就只用站内弹窗 */
      }
    },
    [notification],
  )

  // 轮询状态 + 增量提醒（3s）；首次只补历史，不补弹窗
  useEffect(() => {
    let cancelled = false
    async function pull() {
      try {
        const [status, events] = await Promise.all([
          fetchFuturesWatchStatus(),
          fetchFuturesWatchEvents(cursorRef.current),
        ])
        if (cancelled) return
        setWatch(status)
        runningRef.current = status.running
        if (events.length) {
          const last = events[events.length - 1]
          if (last) cursorRef.current = Math.max(cursorRef.current, last.seq)
          setAlerts((prev) => [...events.slice().reverse(), ...prev].slice(0, 200))
          if (!firstPullRef.current) {
            for (const e of events) {
              if (e.fresh) notifyBreakout(e)
            }
          }
        }
        firstPullRef.current = false
      } catch {
        /* 轮询失败静默重试 */
      }
    }
    void pull()
    const timer = window.setInterval(() => void pull(), 3000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [notifyBreakout])

  // 监控运行中改「级别 / ORB / Donchian / ATR / 量能 / 间隔 / 品种」→ 自动热更新（防抖 500ms）
  useEffect(() => {
    if (!runningRef.current) return
    const timer = window.setTimeout(() => {
      void updateFuturesWatch(watchConfig)
        .then((status) => {
          setWatch(status)
          message.success('监控配置已更新')
        })
        .catch((e) => message.error(e instanceof Error ? e.message : '监控配置更新失败'))
    }, 500)
    return () => window.clearTimeout(timer)
  }, [watchConfig, message])

  async function toggleWatch(next: boolean) {
    setWatchBusy(true)
    try {
      if (next) {
        if (typeof Notification !== 'undefined' && Notification.permission === 'default') {
          void Notification.requestPermission()
        }
        const status = await startFuturesWatch(watchConfig)
        setWatch(status)
        runningRef.current = true
        message.success(
          `已开始监控 ${status.varieties} 个品种，每 ${status.config.interval ?? watchInterval} 秒扫一轮`,
        )
      } else {
        const status = await stopFuturesWatch()
        setWatch(status)
        runningRef.current = false
        message.info('已停止监控')
      }
    } catch (e) {
      message.error(e instanceof Error ? e.message : '监控操作失败')
    } finally {
      setWatchBusy(false)
    }
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

  return (
    <div className="page-wrap">
      <Typography.Title level={4} style={{ marginBottom: 16 }}>
        期货突破
      </Typography.Title>
      <Card size="small" style={{ marginBottom: 16 }}>
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
            <Select
              style={{ width: 100 }}
              value={period}
              onChange={setPeriod}
              options={[
                { value: '5', label: '5分钟' },
                { value: '15', label: '15分钟' },
                { value: '30', label: '30分钟' },
                { value: '60', label: '60分钟' },
              ]}
            />
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
          <Form.Item>
            <Space>
              <Button onClick={() => void scan()} loading={scanning}>
                扫描
              </Button>
              <Button type="primary" onClick={() => void backtest()} loading={running}>
                回测突破对错
              </Button>
            </Space>
          </Form.Item>
        </Form>
      </Card>

      <Card
        size="small"
        style={{ marginBottom: 16 }}
        title={
          <Space size={8}>
            市场监控
            <Tag color={watchRunning ? 'green' : 'default'}>{watchRunning ? '运行中' : '未开启'}</Tag>
          </Space>
        }
        extra={
          <Space>
            <Switch
              checked={watchRunning}
              loading={watchBusy}
              checkedChildren="监控中"
              unCheckedChildren="已停止"
              onChange={(v) => void toggleWatch(v)}
            />
            <Button
              size="small"
              disabled={!alerts.length}
              onClick={() => {
                setAlerts([])
                cursorRef.current = watch?.latest_seq ?? cursorRef.current
              }}
            >
              清空提醒
            </Button>
          </Space>
        }
      >
        <Space wrap size={8} style={{ marginBottom: 8 }}>
          <span>间隔(秒)</span>
          <InputNumber
            min={5}
            max={600}
            value={watchInterval}
            onChange={(v) => setWatchInterval(Number(v ?? 30))}
          />
          <Select
            mode="multiple"
            allowClear
            showSearch
            optionFilterProp="label"
            style={{ minWidth: 260 }}
            placeholder="监控品种：留空 = 全市场"
            value={watchPrefixes}
            onChange={setWatchPrefixes}
            options={varietyOpts}
          />
          <Typography.Text type="secondary">
            运行中修改「级别 / ORB / Donchian / ATR / 量能」会自动生效
          </Typography.Text>
        </Space>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 8 }}>
          覆盖 {watch?.varieties ?? 0} 个品种 · 第 {watch?.ticks ?? 0} 轮 · 上一轮扫描 {watch?.scanned ?? 0} 个（失败{' '}
          {watch?.failures ?? 0}）· 耗时 {watch?.last_ms ?? 0}ms · 提醒 {watch?.events ?? 0} 条
          {watch?.source ? ` · 数据源 ${watch.source}` : ''}
          {coolingSources ? `（${coolingSources}）` : ''}
          {watch && watch.backoff > 1 ? ` · 限流退避 ×${watch.backoff}` : ''}
          {watch?.last_tick ? ` · 最后扫描 ${watch.last_tick}` : ''}
          {watch?.last_error ? ` · 最近错误：${watch.last_error}` : ''}
        </Typography.Paragraph>
        <Table
          size="small"
          rowKey={(r) => String(r.seq)}
          columns={alertCols}
          dataSource={alerts}
          pagination={{ pageSize: 10, showSizeChanger: false }}
          locale={{ emptyText: watchRunning ? '监控中，暂未出现突破' : '未开启监控' }}
        />
      </Card>

      {snap ? (
        <Card size="small" style={{ marginBottom: 16 }} title={`${snap.symbol} ${snap.day}${snap.upcoming ? ' 盘前' : ''} · 趋势 ${snap.trend}`}>
          <Typography.Paragraph>
            {snap.last_5 ? `5min ${snap.last_5.time} 收 ${snap.last_5.close} 量 ${snap.last_5.volume}` : ''}
            {snap.last_15 ? ` ｜ 15min ${snap.last_15.time} 收 ${snap.last_15.close}` : ''}
          </Typography.Paragraph>
          <Typography.Paragraph>{snap.position}</Typography.Paragraph>
          {snap.resonance?.length ? (
            <Typography.Paragraph>多周期共振：{snap.resonance.join('，')}</Typography.Paragraph>
          ) : null}
          <FuturesChart
            bars={(period === '5' ? snap.bars_5 : snap.bars_15) ?? snap.bars_15 ?? snap.bars_5 ?? []}
            events={(period === '5' ? snap.events_5 : snap.events_15) ?? []}
            levels={snap.levels}
          />
          <Table
            size="small"
            pagination={false}
            rowKey={(r) => r.name}
            dataSource={snap.levels ?? []}
            columns={[
              { title: '关键位', dataIndex: 'name' },
              { title: '类型', dataIndex: 'kind' },
              { title: '价格', dataIndex: 'value', render: (v: number) => v?.toFixed(1) },
            ]}
            style={{ marginBottom: 16 }}
          />
          <Typography.Text strong>5分钟确认事件</Typography.Text>
          <Table
            size="small"
            pagination={false}
            rowKey={(_, i) => `5-${i}`}
            columns={eventCols}
            dataSource={snap.events_5 ?? []}
            locale={{ emptyText: '无' }}
            style={{ marginBottom: 16, marginTop: 8 }}
          />
          <Typography.Text strong>15分钟确认事件</Typography.Text>
          <Table
            size="small"
            pagination={false}
            rowKey={(_, i) => `15-${i}`}
            columns={eventCols}
            dataSource={snap.events_15 ?? []}
            locale={{ emptyText: '无' }}
            style={{ marginTop: 8 }}
          />
        </Card>
      ) : null}

      {result ? (
        <>
          <Space size="large" style={{ marginBottom: 16 }}>
            <Statistic title="样本" value={result.trades} />
            <Statistic title="正确" value={result.correct} />
            <Statistic title="胜率" value={`${((result.win_rate ?? 0) * 100).toFixed(1)}%`} />
            <Statistic title="平均收益" value={`${((result.avg_return ?? 0) * 100).toFixed(2)}%`} />
          </Space>
          <FuturesChart bars={result.bars ?? []} events={result.items ?? []} />
          <Table
            size="small"
            rowKey={(_, i) => String(i)}
            columns={outcomeCols}
            dataSource={result.items ?? []}
            pagination={{ pageSize: 50 }}
            locale={{ emptyText: '无样本（新浪分钟线仅覆盖近期）' }}
          />
        </>
      ) : null}
    </div>
  )
}
