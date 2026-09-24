import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, App, Button, Card, Input, Select, Space, Switch, Table, Tag, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { fetchFuturesOverview, fetchFuturesQuotes } from '../api'
import type { FuturesQuote, FuturesVarietyContracts } from '../types'
import { MAX_TICKER_ROWS, addToTicker, patchTicker, removeFromTicker, useTicker } from '../tickerStore'

// 单次批量报价的代码数上限（服务端也是 30）：只取当前页 + 已展开的合约
const QUOTE_BATCH = 30

const OVERVIEW_INTERVALS = [
  { value: 5000, label: '5 秒' },
  { value: 10000, label: '10 秒' },
  { value: 30000, label: '30 秒' },
  { value: 60000, label: '60 秒' },
  { value: 0, label: '不自动刷新' },
]

type Row = {
  key: string
  symbol: string
  name: string
  label: string
  isMain: boolean
  contractCount?: number
  loadErr?: string
  children?: Row[]
}

function fmtHold(v?: number) {
  if (!Number.isFinite(v) || (v ?? 0) <= 0) return '—'
  const n = v as number
  return n >= 10000 ? `${(n / 10000).toFixed(2)}万` : String(Math.round(n))
}

// 价格保留真实精度：1159.5 显示 1159.5，3116 显示 3116（去掉没用的 .0）
function fmtNum(v?: number, digits = 0) {
  if (!Number.isFinite(v) || (v ?? 0) <= 0) return '—'
  const n = v as number
  if (digits === 0) {
    const s = n.toFixed(1)
    return s.endsWith('.0') ? s.slice(0, -2) : s
  }
  return n.toFixed(digits)
}

// FuturesOverviewPage 期货总览：全部品种 + 月份合约，勾选后加到浮窗（或移出）。
export default function FuturesOverviewPage() {
  const { message } = App.useApp()
  const ticker = useTicker()

  const [items, setItems] = useState<FuturesVarietyContracts[]>([])
  const [loading, setLoading] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  const [kw, setKw] = useState('')
  const [auto, setAuto] = useState(true)
  const [quotes, setQuotes] = useState<Record<string, FuturesQuote>>({})
  const [quoteErr, setQuoteErr] = useState('')
  const [selected, setSelected] = useState<React.Key[]>([])
  const [expanded, setExpanded] = useState<React.Key[]>([])
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [intervalMs, setIntervalMs] = useState(10000)
  const [showBidAsk, setShowBidAsk] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setItems((await fetchFuturesOverview()) ?? [])
      setLoadErr('')
    } catch (e) {
      setLoadErr(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const rows = useMemo<Row[]>(() => {
    const k = kw.trim().toUpperCase()
    return items
      .filter((v) => {
        if (!k) return true
        return (
          v.prefix.toUpperCase().includes(k) ||
          v.name.includes(kw.trim()) ||
          v.contracts.some((c) => c.symbol.toUpperCase().includes(k) || c.label.includes(k))
        )
      })
      .map((v) => ({
        key: v.main_symbol,
        symbol: v.main_symbol,
        name: `${v.name}主连`,
        label: '主连',
        isMain: true,
        contractCount: v.contracts.length,
        loadErr: v.error,
        children: v.contracts
          .filter((c) => c.kind !== 'main')
          .map((c) => ({ key: c.symbol, symbol: c.symbol, name: c.name, label: c.label, isMain: false })),
      }))
  }, [items, kw])

  const pageRows = useMemo(() => rows.slice((page - 1) * pageSize, page * pageSize), [rows, page, pageSize])

  // 要报价的代码：当前页的主连 + 已展开品种的月份合约（去重后截断到 30 个）
  const wanted = useMemo(() => {
    const out: string[] = []
    for (const r of pageRows) out.push(r.symbol)
    const byPrefix = new Map(items.map((v) => [v.main_symbol, v]))
    for (const key of expanded) {
      const item = byPrefix.get(String(key))
      if (!item) continue
      for (const c of item.contracts) out.push(c.symbol)
    }
    return [...new Set(out)].slice(0, QUOTE_BATCH)
  }, [pageRows, expanded, items])

  useEffect(() => {
    if (!auto || intervalMs <= 0 || wanted.length === 0) return
    let cancelled = false
    const tick = async () => {
      if (typeof document !== 'undefined' && document.hidden) return
      try {
        const list = await fetchFuturesQuotes(wanted)
        if (cancelled) return
        setQuotes((prev) => {
          const next = { ...prev }
          for (const q of list ?? []) next[q.symbol] = q
          return next
        })
        setQuoteErr('')
      } catch (e) {
        if (!cancelled) setQuoteErr(e instanceof Error ? e.message : '报价获取失败')
      }
    }
    void tick()
    const timer = window.setInterval(() => void tick(), intervalMs)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [auto, wanted, intervalMs])

  const inTicker = useCallback((symbol: string) => ticker.symbols.includes(symbol), [ticker.symbols])

  const add = useCallback(
    (symbols: string[]) => {
      const { added, skipped } = addToTicker(symbols)
      if (added.length === 0 && skipped.length === 0) {
        message.info('这些已经在浮窗里了')
        return
      }
      if (added.length > 0) message.success(`已加入浮窗：${added.join('、')}`)
      if (skipped.length > 0) message.warning(`浮窗最多 ${MAX_TICKER_ROWS} 个，${skipped.length} 个没进去（先移出一些再加）`)
    },
    [message],
  )

  const remove = useCallback(
    (symbols: string[]) => {
      removeFromTicker(symbols)
      message.success(`已从浮窗移出：${symbols.join('、')}`)
    },
    [message],
  )

  const columns: ColumnsType<Row> = [
    {
      title: '合约',
      dataIndex: 'symbol',
      width: 130,
      render: (v: string, r) => (
        <Space size={4}>
          <span style={{ fontFamily: 'monospace' }}>{v}</span>
          {r.isMain ? <Tag color="geekblue">主连</Tag> : <Tag color="purple">{r.label}</Tag>}
        </Space>
      ),
    },
    { title: '名称', dataIndex: 'name', width: 150, ellipsis: true },
    {
      title: '最新价',
      width: 96,
      align: 'right' as const,
      render: (_, r) => {
        const q = quotes[r.symbol]
        if (!q) return '—'
        const color = q.change_pct >= 0 ? '#cf1322' : '#389e0d'
        return <span style={{ color: q.error ? '#999' : color }}>{fmtNum(q.price)}</span>
      },
    },
    {
      title: '涨跌',
      width: 88,
      align: 'right' as const,
      render: (_, r) => {
        const q = quotes[r.symbol]
        if (!q || q.error) return '—'
        const color = q.change_pct >= 0 ? '#cf1322' : '#389e0d'
        return (
          <span style={{ color }}>
            {q.change_pct >= 0 ? '+' : ''}
            {q.change_pct.toFixed(2)}%
          </span>
        )
      },
    },
    {
      title: '持仓量',
      width: 100,
      align: 'right' as const,
      render: (_, r) => (quotes[r.symbol]?.error ? '—' : fmtHold(quotes[r.symbol]?.hold)),
    },
    {
      title: '成交量',
      width: 100,
      align: 'right' as const,
      render: (_, r) => (quotes[r.symbol]?.error ? '—' : fmtHold(quotes[r.symbol]?.volume)),
    },
    ...(showBidAsk
      ? ([
          {
            title: '买一(价×量)',
            width: 122,
            align: 'right' as const,
            render: (_: unknown, r: Row) => {
              const q = quotes[r.symbol]
              if (!q || q.error || !q.bid) return '—'
              return (
                <span>
                  <span style={{ color: '#389e0d' }}>{fmtNum(q.bid)}</span>
                  <span style={{ color: '#999' }}>×{fmtHold(q.bid_vol)}</span>
                </span>
              )
            },
          },
          {
            title: '卖一(价×量)',
            width: 122,
            align: 'right' as const,
            render: (_: unknown, r: Row) => {
              const q = quotes[r.symbol]
              if (!q || q.error || !q.ask) return '—'
              return (
                <span>
                  <span style={{ color: '#cf1322' }}>{fmtNum(q.ask)}</span>
                  <span style={{ color: '#999' }}>×{fmtHold(q.ask_vol)}</span>
                </span>
              )
            },
          },
        ] as ColumnsType<Row>)
      : []),
    {
      title: '更新时间',
      width: 110,
      render: (_, r) => quotes[r.symbol]?.time?.slice(11) || '—',
    },
    {
      title: '浮窗',
      width: 110,
      fixed: 'right' as const,
      render: (_, r) =>
        inTicker(r.symbol) ? (
          <Button size="small" danger onClick={() => remove([r.symbol])}>
            移出
          </Button>
        ) : (
          <Button size="small" type="primary" onClick={() => add([r.symbol])}>
            加入
          </Button>
        ),
    },
  ]

  const tickerFull = ticker.symbols.length >= MAX_TICKER_ROWS

  return (
    <div className="page-wrap">
      <Space style={{ marginBottom: 12 }} wrap>
        <Typography.Title level={4} style={{ margin: 0 }}>
          期货总览
        </Typography.Title>
        <Input.Search allowClear placeholder="品种 / 代码 / 月份" style={{ width: 200 }} onSearch={setKw} />
        <Button loading={loading} onClick={() => void load()}>
          刷新清单
        </Button>
        <span>
          自动报价 <Switch size="small" checked={auto} onChange={setAuto} />
        </span>
        <Select
          size="small"
          style={{ width: 150 }}
          value={intervalMs}
          onChange={setIntervalMs}
          disabled={!auto}
          options={OVERVIEW_INTERVALS}
        />
        <span>
          买一/卖一{' '}
          <Switch size="small" checked={showBidAsk} onChange={setShowBidAsk} />
        </span>
        <Space size={4}>
          <Typography.Text type={tickerFull ? 'warning' : 'secondary'}>
            浮窗 {ticker.symbols.length}/{MAX_TICKER_ROWS}：{ticker.symbols.join('、') || '空'}
          </Typography.Text>
          <Button size="small" onClick={() => patchTicker({ hidden: !ticker.hidden })}>
            {ticker.hidden ? '显示浮窗' : '隐藏浮窗'}
          </Button>
        </Space>
      </Space>

      <Space style={{ marginBottom: 8 }} wrap>
        <Button type="primary" disabled={selected.length === 0} onClick={() => add(selected.map(String))}>
          加入浮窗（已选 {selected.length}）
        </Button>
        <Button disabled={selected.length === 0} onClick={() => remove(selected.map(String))}>
          移出浮窗
        </Button>
        <Button size="small" onClick={() => setSelected([])} disabled={selected.length === 0}>
          清空选择
        </Button>
        <Typography.Text type="secondary">
          浮窗最多 {MAX_TICKER_ROWS} 个；勾选任意合约后加入/移出，浮窗会立刻更新。
        </Typography.Text>
      </Space>

      {loadErr ? (
        <Alert type="error" showIcon style={{ marginBottom: 8 }} message={`清单加载失败：${loadErr}`} />
      ) : null}
      {quoteErr ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 8 }}
          message={`报价获取失败：${quoteErr}（价格列会显示 —，清单仍可用）`}
        />
      ) : null}

      <Card size="small" styles={{ body: { padding: 0 } }}>
        <Table<Row>
          rowKey="key"
          size="small"
          loading={loading}
          columns={columns}
          dataSource={pageRows}
          rowSelection={{
            checkStrictly: true, // 主连和月份合约分别勾选，避免「选品种顺手带上全部合约」
            selectedRowKeys: selected,
            onChange: setSelected,
          }}
          expandable={{
            expandedRowKeys: expanded,
            onExpandedRowsChange: (keys) => setExpanded([...keys]),
          }}
          pagination={{
            current: page,
            pageSize,
            showSizeChanger: true,
            showTotal: (n) => `共 ${n} 个品种`,
            onChange: (p, s) => {
              setPage(p)
              setPageSize(s)
            },
          }}
          scroll={{ x: 1100 }}
        />
      </Card>

      <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginTop: 8 }}>
        说明：清单来自上游（带缓存，10 分钟过期）；报价只取「当前页 + 已展开的合约」，最多 {QUOTE_BATCH} 个，
        避免一次向上游要上百个代码。取不到的项价格显示「—」，悬浮在浮窗那一行能看到具体原因。
      </Typography.Paragraph>
    </div>
  )
}
