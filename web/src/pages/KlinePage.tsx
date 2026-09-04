import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { App, AutoComplete, Space, Typography } from 'antd'
import {
  CandlestickSeries,
  ColorType,
  HistogramSeries,
  createChart,
  type IChartApi,
  type ISeriesApi,
  type Time,
  type UTCTimestamp,
} from 'lightweight-charts'
import { fetchKline, fetchStocks, stockCode } from '../api'
import type { StockHit } from '../types'

function toTime(date: string): Time {
  const s = date.slice(0, 10)
  if (/^\d{4}-\d{2}-\d{2}$/.test(s)) return s as Time
  const n = Date.parse(date)
  return (Math.floor(n / 1000) || 0) as UTCTimestamp
}

export default function KlinePage() {
  const { message } = App.useApp()
  const [params, setParams] = useSearchParams()
  const code = params.get('code') || ''
  const [q, setQ] = useState(code)
  const [options, setOptions] = useState<{ value: string; label: string }[]>([])
  const wrapRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<IChartApi | null>(null)
  const candleRef = useRef<ISeriesApi<'Candlestick'> | null>(null)
  const volRef = useRef<ISeriesApi<'Histogram'> | null>(null)

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
    return () => {
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
        const bars = (await fetchKline(code)) ?? []
        if (cancelled || !candleRef.current || !volRef.current) return
        candleRef.current.setData(
          bars.map((b) => ({
            time: toTime(b.date),
            open: Number(b.open),
            high: Number(b.high),
            low: Number(b.low),
            close: Number(b.close),
          })),
        )
        volRef.current.setData(
          bars.map((b) => ({
            time: toTime(b.date),
            value: Number(b.volume),
            color: Number(b.close) >= Number(b.open) ? '#ef535088' : '#26a69a88',
          })),
        )
        chartRef.current?.timeScale().fitContent()
      } catch (e) {
        message.error(e instanceof Error ? e.message : '加载K线失败')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [code, message])

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
          K线
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
        {code ? <Typography.Text type="secondary">{code}</Typography.Text> : null}
      </Space>
      <div ref={wrapRef} className="kline-chart" />
    </div>
  )
}
