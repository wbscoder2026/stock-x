import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { App, AutoComplete, Space, Tag, Typography } from 'antd'
import {
  CandlestickSeries,
  ColorType,
  HistogramSeries,
  LineSeries,
  LineStyle,
  createChart,
  createSeriesMarkers,
  type IChartApi,
  type ISeriesApi,
  type ISeriesMarkersPluginApi,
  type SeriesMarker,
  type Time,
  type UTCTimestamp,
} from 'lightweight-charts'
import { fetchKline, fetchPicks, fetchStocks, fetchStrategies, parseParams, stockCode } from '../api'
import { STRATEGY_COLOR, barIndex, patternCaption, strategyLabel, strategyOverlay } from '../klinePattern'
import type { KlineBar, PickRow, StockHit, Strategy } from '../types'

function toTime(date: string): Time {
  const s = date.slice(0, 10)
  if (/^\d{4}-\d{2}-\d{2}$/.test(s)) return s as Time
  const n = Date.parse(date)
  return (Math.floor(n / 1000) || 0) as UTCTimestamp
}

function dedupePicks(rows: PickRow[]): PickRow[] {
  const seen = new Set<string>()
  const out: PickRow[] = []
  for (const r of rows) {
    const k = `${r.as_of}|${r.strategy}`
    if (seen.has(k)) continue
    seen.add(k)
    out.push(r)
  }
  return out
}

function resolveFocus(rows: PickRow[], date: string, strategy: string): PickRow | undefined {
  if (date && strategy) {
    const hit = rows.find((r) => r.as_of === date && r.strategy === strategy)
    if (hit) return hit
  }
  if (date) {
    const hit = rows.find((r) => r.as_of === date)
    if (hit) return hit
  }
  return rows[rows.length - 1]
}

export default function KlinePage() {
  const { message } = App.useApp()
  const [params, setParams] = useSearchParams()
  const code = params.get('code') || ''
  const date = params.get('date') || ''
  const strategy = params.get('strategy') || ''
  const [q, setQ] = useState(code)
  const [options, setOptions] = useState<{ value: string; label: string }[]>([])
  const [picks, setPicks] = useState<PickRow[]>([])
  const [caption, setCaption] = useState('')
  const [captionColor, setCaptionColor] = useState<string>()
  const wrapRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<IChartApi | null>(null)
  const candleRef = useRef<ISeriesApi<'Candlestick'> | null>(null)
  const volRef = useRef<ISeriesApi<'Histogram'> | null>(null)
  const markersRef = useRef<ISeriesMarkersPluginApi<Time> | null>(null)
  const overlayRef = useRef<ISeriesApi<'Line'>[]>([])

  useEffect(() => {
    const el = wrapRef.current
    if (!el) return
    const chart = createChart(el, {
      autoSize: true,
      layout: { background: { type: ColorType.Solid, color: '#fff' }, textColor: '#333' },
      grid: { vertLines: { color: '#eee' }, horzLines: { color: '#eee' } },
      rightPriceScale: { borderColor: '#ddd' },
      timeScale: { borderColor: '#ddd' },
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
    chart.priceScale('vol').applyOptions({
      scaleMargins: { top: 0.8, bottom: 0 },
    })
    chartRef.current = chart
    candleRef.current = candle
    volRef.current = vol
    markersRef.current = createSeriesMarkers(candle, [])
    return () => {
      overlayRef.current = []
      markersRef.current = null
      chart.remove()
      chartRef.current = null
      candleRef.current = null
      volRef.current = null
    }
  }, [])

  useEffect(() => {
    if (!code) return
    let cancelled = false
    void (async () => {
      try {
        const [bars, pickRows, strats] = await Promise.all([
          fetchKline(code),
          fetchPicks({ symbol: code }),
          fetchStrategies().catch(() => [] as Strategy[]),
        ])
        const chart = chartRef.current
        const candle = candleRef.current
        const vol = volRef.current
        if (cancelled || !chart || !candle || !vol) return
        const list = bars ?? []
        const stratList = Array.isArray(strats) ? strats : []
        const names: Record<string, string> = {}
        const paramMap: Record<string, Record<string, number>> = {}
        for (const s of stratList) {
          names[String(s.id)] = s.name || String(s.id)
          paramMap[String(s.id)] = parseParams(s)
        }
        candle.setData(
          list.map((b) => ({
            time: toTime(b.date),
            open: Number(b.open),
            high: Number(b.high),
            low: Number(b.low),
            close: Number(b.close),
          })),
        )
        vol.setData(
          list.map((b) => ({
            time: toTime(b.date),
            value: Number(b.volume),
            color: Number(b.close) >= Number(b.open) ? '#ef535088' : '#26a69a88',
          })),
        )
        const uniq = dedupePicks(Array.isArray(pickRows) ? pickRows : [])
        setPicks(uniq)
        const markers: SeriesMarker<Time>[] = uniq.map((p) => {
          const focused = p.as_of === date && p.strategy === strategy
          return {
            time: toTime(p.as_of || ''),
            position: 'aboveBar',
            color: STRATEGY_COLOR[p.strategy] || '#f68410',
            shape: 'arrowDown',
            size: focused || (!date && !strategy) ? 3 : 2,
          }
        })
        markersRef.current?.setMarkers(markers)
        for (const s of overlayRef.current) {
          try {
            chart.removeSeries(s)
          } catch {
            /* ignore */
          }
        }
        overlayRef.current = []
        const focus = resolveFocus(uniq, date, strategy)
        if (focus) {
          const lines = strategyOverlay(focus.strategy, list, focus.as_of || '', paramMap[focus.strategy] || {})
          for (const line of lines) {
            const s = chart.addSeries(LineSeries, {
              color: line.color,
              lineWidth: 2,
              lineStyle: line.dashed ? LineStyle.Dashed : LineStyle.Solid,
              lastValueVisible: false,
              priceLineVisible: false,
              title: line.title,
            })
            s.setData(line.points)
            overlayRef.current.push(s)
          }
          zoomTo(chart, list, focus.as_of || '')
          setCaption(patternCaption(focus.as_of || '', strategyLabel(focus.strategy, names), lines))
          setCaptionColor(STRATEGY_COLOR[focus.strategy] || '#e67e22')
        } else {
          chart.timeScale().fitContent()
          setCaption(uniq.length ? `${uniq.length} 次选股，点下方标签看形态` : '')
          setCaptionColor(undefined)
        }
      } catch (e) {
        message.error(e instanceof Error ? e.message : '加载K线失败')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [code, date, strategy, message])

  async function search(text: string) {
    setQ(text)
    if (!text.trim()) {
      setOptions([])
      return
    }
    try {
      const hits = (await fetchStocks(text.trim())) ?? []
      setOptions(
        hits.map((h: StockHit) => {
          const c = stockCode(h)
          return { value: c, label: `${c} ${h.name ?? ''}`.trim() }
        }),
      )
    } catch (e) {
      message.error(e instanceof Error ? e.message : '搜索失败')
    }
  }

  return (
    <div className="page-wrap">
      <Space style={{ marginBottom: 16 }} wrap>
        <Typography.Title level={4} style={{ margin: 0 }}>
          K线{code ? ` ${code}` : ''}
          {picks[0]?.name ? ` ${picks[0].name}` : ''}
        </Typography.Title>
        <AutoComplete
          style={{ width: 320 }}
          value={q}
          options={options}
          onSearch={(v) => void search(v)}
          onChange={setQ}
          onSelect={(v) => {
            setQ(v)
            setParams({ code: v })
          }}
          placeholder="搜索代码或名称"
          allowClear
        />
        {caption ? (
          <Typography.Text strong style={{ color: captionColor || '#333', fontSize: 16 }}>
            {caption}
          </Typography.Text>
        ) : null}
      </Space>
      {picks.length ? (
        <Space style={{ marginBottom: 12 }} wrap size={[4, 8]}>
          {picks.map((p) => {
            const active = p.as_of === date && p.strategy === strategy
            return (
              <Tag
                key={`${p.as_of}-${p.strategy}`}
                color={active ? STRATEGY_COLOR[p.strategy] : undefined}
                style={{ cursor: 'pointer' }}
                onClick={() =>
                  setParams({
                    code,
                    date: p.as_of || '',
                    strategy: p.strategy,
                  })
                }
              >
                {p.as_of} {strategyLabel(p.strategy)}
              </Tag>
            )
          })}
        </Space>
      ) : null}
      <div ref={wrapRef} className="kline-chart" />
    </div>
  )
}

function zoomTo(chart: IChartApi, bars: KlineBar[], asOf: string) {
  const i = barIndex(bars, asOf)
  if (i < 0) {
    chart.timeScale().fitContent()
    return
  }
  chart.timeScale().setVisibleLogicalRange({
    from: Math.max(0, i - 50),
    to: Math.min(bars.length - 1, i + 12),
  })
}
