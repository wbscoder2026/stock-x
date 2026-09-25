import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { App, Button, Divider, InputNumber, Modal, Select, Slider, Space, Switch, Tooltip, Typography } from 'antd'
import { useNavigate } from 'react-router-dom'
import { fetchFuturesQuotes, fetchFuturesVarieties } from '../api'
import type { FuturesQuote, FuturesVariety } from '../types'
import { MAX_TICKER_ROWS, TICKER_INTERVALS, patchTicker, useTicker } from '../tickerStore'

function clampNum(v: number, lo: number, hi: number) {
  if (!Number.isFinite(v)) return lo
  return Math.min(hi, Math.max(lo, v))
}

// 持仓量：中文习惯按「万手」看，别甩一长串数字
function fmtHold(v: number) {
  if (!Number.isFinite(v) || v <= 0) return '—'
  if (v >= 10000) return `${(v / 10000).toFixed(2)}万`
  return String(Math.round(v))
}

// 保留真实精度：1159.5 就是 1159.5（焦煤最小变动 0.5），3116 显示 3116
function fmtPrice(v: number) {
  if (!Number.isFinite(v) || v <= 0) return '—'
  const s = v.toFixed(1)
  return s.endsWith('.0') ? s.slice(0, -2) : s
}

// FuturesTicker 期货价格浮窗：很小一块、可拖动，最多 5 行（每行一个品种）。
export default function FuturesTicker() {
  const { message } = App.useApp()
  const nav = useNavigate()
  const cfg = useTicker()
  const [quotes, setQuotes] = useState<FuturesQuote[]>([])
  const [error, setError] = useState('')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [varieties, setVarieties] = useState<FuturesVariety[]>([])
  const [pos, setPos] = useState<{ x: number; y: number } | null>(null)
  const dragRef = useRef<{ dx: number; dy: number } | null>(null)

  const symbols = useMemo(() => cfg.symbols.slice(0, MAX_TICKER_ROWS), [cfg.symbols])
  const rows = useMemo(() => {
    const bySymbol = new Map(quotes.map((q) => [q.symbol, q]))
    return symbols
      .slice(0, cfg.count)
      .map((s) => bySymbol.get(s) ?? ({ symbol: s, name: s, price: 0, hold: 0, change_pct: 0 } as FuturesQuote))
  }, [quotes, symbols, cfg.count])

  // 轮询报价：页面切到后台就停，回到前台立刻补一次
  useEffect(() => {
    if (cfg.hidden || cfg.collapsed || symbols.length === 0) return
    let cancelled = false
    const load = async () => {
      if (typeof document !== 'undefined' && document.hidden) return
      try {
        const items = await fetchFuturesQuotes(symbols)
        if (!cancelled) {
          setQuotes(items ?? [])
          setError('')
        }
      } catch (e) {
        if (!cancelled) setError(e instanceof Error ? e.message : '报价获取失败')
      }
    }
    void load()
    // intervalMs = 0 → 只取一次（手动刷新模式）
    const timer = cfg.intervalMs > 0 ? window.setInterval(() => void load(), cfg.intervalMs) : undefined
    const onVisible = () => {
      if (!document.hidden) void load()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      cancelled = true
      if (timer !== undefined) window.clearInterval(timer)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [cfg.hidden, cfg.collapsed, cfg.intervalMs, symbols])

  // 打开设置时才拉品种表
  useEffect(() => {
    if (!settingsOpen || varieties.length > 0) return
    void fetchFuturesVarieties()
      .then((list) => setVarieties(list ?? []))
      .catch(() => setVarieties([]))
  }, [settingsOpen, varieties.length])

  // 拖动：只挂在标题文字上（挂在整条标题栏会把按钮的点击一起吞掉 —— 之前的 ⚙ 点不开就是这个原因）
  const onPointerDown = (e: React.PointerEvent) => {
    e.stopPropagation()
    dragRef.current = { dx: e.clientX - left, dy: e.clientY - cfg.y }
    ;(e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId)
  }
  const onPointerMove = (e: React.PointerEvent) => {
    const d = dragRef.current
    if (!d) return
    // 夹在窗口内，别拖出去找不回来
    setPos({
      x: clampNum(e.clientX - d.dx, 0, Math.max(0, window.innerWidth - 120)),
      y: clampNum(e.clientY - d.dy, 0, Math.max(0, window.innerHeight - 40)),
    })
  }
  const onPointerUp = () => {
    if (!dragRef.current) return
    dragRef.current = null
    if (pos) patchTicker({ x: pos.x, y: pos.y }) // 松手才落库，拖动过程不狂写 localStorage
    setPos(null)
  }

  const left = pos?.x ?? (cfg.x < 0 ? Math.max(8, window.innerWidth - 244) : cfg.x)
  const top = pos?.y ?? cfg.y

  const refreshNow = useCallback(() => {
    if (symbols.length === 0) return
    void fetchFuturesQuotes(symbols)
      .then((items) => {
        setQuotes(items ?? [])
        setError('')
        message.success('已刷新')
      })
      .catch((e) => message.error(e instanceof Error ? e.message : '刷新失败'))
  }, [symbols, message])

  // 收起来时只留一个小按钮，随时能叫回来
  if (cfg.hidden) {
    return (
      <Button
        type="primary"
        style={{ position: 'fixed', right: 16, bottom: 16, zIndex: 900, opacity: 0.9 }}
        onClick={() => patchTicker({ hidden: false })}
      >
        期货浮窗
      </Button>
    )
  }

  return (
    <>
      <div
        style={{
          position: 'fixed',
          left,
          top,
          width: cfg.showBidAsk ? 250 : 236,
          zIndex: 900,
          opacity: cfg.opacity,
          background: 'rgba(20, 23, 30, 0.94)',
          color: '#e8e8e8',
          borderRadius: 8,
          boxShadow: '0 6px 20px rgba(0,0,0,0.35)',
          fontSize: 12,
        }}
      >
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 2,
            padding: '4px 6px',
            borderBottom: cfg.collapsed ? 'none' : '1px solid rgba(255,255,255,0.08)',
          }}
        >
          <span
            onPointerDown={onPointerDown}
            onPointerMove={onPointerMove}
            onPointerUp={onPointerUp}
            onDoubleClick={() => patchTicker({ collapsed: !cfg.collapsed })}
            title="拖动浮窗 / 双击折叠"
            style={{ flex: 1, fontWeight: 600, cursor: 'move', touchAction: 'none', userSelect: 'none' }}
          >
            期货浮窗
          </span>
          {/* 按钮区：拦住 pointerdown，别让拖动逻辑把点击吞了 */}
          <span onPointerDown={(e) => e.stopPropagation()} style={{ display: 'flex', gap: 2 }}>
            <Tooltip title="设置（品种 / 数量 / 透明度）">
              <Button size="small" type="text" style={{ color: '#e8e8e8' }} onClick={() => setSettingsOpen(true)}>
                ⚙
              </Button>
            </Tooltip>
            <Tooltip title={cfg.collapsed ? '展开' : '折叠'}>
              <Button
                size="small"
                type="text"
                style={{ color: '#e8e8e8' }}
                onClick={() => patchTicker({ collapsed: !cfg.collapsed })}
              >
                {cfg.collapsed ? '▸' : '▾'}
              </Button>
            </Tooltip>
            <Tooltip title="收起（右下角留个按钮）">
              <Button size="small" type="text" style={{ color: '#e8e8e8' }} onClick={() => patchTicker({ hidden: true })}>
                ✕
              </Button>
            </Tooltip>
          </span>
        </div>

        {cfg.collapsed ? null : (
          <div style={{ padding: '4px 6px 6px' }}>
            {rows.length === 0 ? (
              <Typography.Text style={{ color: '#999', fontSize: 12 }}>
                还没选品种：点 ⚙ 或去「期货总览」页勾选后加入
              </Typography.Text>
            ) : (
              rows.map((q) => {
                const up = q.change_pct >= 0
                const color = q.error ? '#8c8c8c' : up ? '#52c41a' : '#ff4d4f'
                return (
                  <Tooltip
                    key={q.symbol}
                    title={
                      q.error
                        ? `取价失败：${q.error}`
                        : `${q.name}　最新 ${fmtPrice(q.price)}　昨收 ${fmtPrice(q.prev_close)}　持仓 ${fmtHold(
                            q.hold,
                          )}　更新 ${q.time || '—'}　来源 ${q.source || '—'}${q.stale ? '（用的是上一次的价）' : ''}`
                    }
                  >
                    <div>
                      <div style={{ display: 'flex', gap: 4, lineHeight: '18px', whiteSpace: 'nowrap' }}>
                        <span style={{ width: 66, overflow: 'hidden', textOverflow: 'ellipsis', color: '#cfcfcf' }}>
                          {q.name || q.symbol}
                        </span>
                        <span style={{ width: 52, textAlign: 'right', color }}>{fmtPrice(q.price)}</span>
                        <span style={{ width: 52, textAlign: 'right', color }}>
                          {q.error ? '—' : `${up ? '+' : ''}${q.change_pct.toFixed(2)}%`}
                        </span>
                        <span style={{ flex: 1, textAlign: 'right', color: '#9aa0aa' }}>{fmtHold(q.hold)}</span>
                      </div>
                      {cfg.showBidAsk ? (
                        <div style={{ fontSize: 11, color: '#8b9199', lineHeight: '15px', whiteSpace: 'nowrap' }}>
                          {!q.error && (q.bid || q.ask) ? (
                            <>
                              买一 <span style={{ color: '#7ac17a' }}>{fmtPrice(q.bid ?? 0)}</span>×
                              {fmtHold(q.bid_vol ?? 0)}
                              {'　'}
                              卖一 <span style={{ color: '#d98b8b' }}>{fmtPrice(q.ask ?? 0)}</span>×
                              {fmtHold(q.ask_vol ?? 0)}
                            </>
                          ) : (
                            <span style={{ color: '#6b7280' }}>买一 —　卖一 —（暂无盘口：非交易时段或上游取不到）</span>
                          )}
                        </div>
                      ) : null}
                    </div>
                  </Tooltip>
                )
              })
            )}
            {error ? (
              <div style={{ color: '#ff7875', fontSize: 11, marginTop: 2 }}>取价失败：{error}</div>
            ) : (
              <div style={{ color: '#666', fontSize: 11, marginTop: 2, display: 'flex', justifyContent: 'space-between' }}>
                <span>价 / 涨跌 / 持仓(万)</span>
                <span style={{ cursor: 'pointer' }} onClick={refreshNow}>
                  刷新
                </span>
              </div>
            )}
          </div>
        )}
      </div>

      <Modal
        open={settingsOpen}
        onCancel={() => setSettingsOpen(false)}
        footer={null}
        width={420}
        title="期货浮窗设置"
      >
        <Space direction="vertical" size={10} style={{ width: '100%' }}>
          <div>
            <Typography.Text type="secondary">品种（最多 {MAX_TICKER_ROWS} 个）</Typography.Text>
            <Select
              mode="multiple"
              style={{ width: '100%', marginTop: 4 }}
              placeholder="搜索并选择品种（主连）"
              value={cfg.symbols}
              maxCount={MAX_TICKER_ROWS}
              optionFilterProp="label"
              onChange={(v) => patchTicker({ symbols: (v as string[]).slice(0, MAX_TICKER_ROWS) })}
              options={varieties.map((v) => ({ value: `${v.prefix}0`, label: `${v.name} ${v.prefix}0` }))}
            />
          </div>
          <Space size={12} wrap>
            <span>
              显示数量{' '}
              <InputNumber
                min={1}
                max={MAX_TICKER_ROWS}
                value={cfg.count}
                onChange={(v) => patchTicker({ count: clampNum(Number(v ?? 1), 1, MAX_TICKER_ROWS) })}
              />
            </span>
            <span>
              刷新{' '}
              <Select
                style={{ width: 92 }}
                value={cfg.intervalMs}
                onChange={(v) => patchTicker({ intervalMs: Number(v) })}
                options={TICKER_INTERVALS}
              />
            </span>
          </Space>
          <div>
            <Typography.Text type="secondary">
              显示买一/卖一{' '}
              <Switch size="small" checked={cfg.showBidAsk} onChange={(v) => patchTicker({ showBidAsk: v })} />
            </Typography.Text>
            <Typography.Paragraph type="secondary" style={{ fontSize: 12, margin: 0 }}>
              盘口来自新浪实时行情；取不到（或字段校验不过）时自动退回分钟线取价，这时不显示买一卖一。
            </Typography.Paragraph>
          </div>
          <div>
            <Typography.Text type="secondary">透明度 {Math.round(cfg.opacity * 100)}%</Typography.Text>
            <Slider
              min={30}
              max={100}
              value={Math.round(cfg.opacity * 100)}
              onChange={(v) => patchTicker({ opacity: clampNum(Number(v) / 100, 0.3, 1) })}
              tooltip={{ formatter: (v) => `${v}%` }}
            />
          </div>
          <Divider style={{ margin: '4px 0' }} />
          <Space>
            <Button size="small" onClick={() => patchTicker({ symbols: [], count: MAX_TICKER_ROWS, opacity: 0.92 })}>
              清空品种
            </Button>
            <Button size="small" onClick={() => patchTicker({ x: -1, y: 96 })}>
              位置复位
            </Button>
            <Button
              size="small"
              type="link"
              onClick={() => {
                setSettingsOpen(false)
                nav('/futures/overview')
              }}
            >
              去「期货总览」批量添加 →
            </Button>
          </Space>
        </Space>
      </Modal>
    </>
  )
}
