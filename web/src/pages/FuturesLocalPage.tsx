import { useCallback, useEffect, useMemo, useState } from 'react'
import { App, Button, DatePicker, Input, Progress, Select, Space, Table, Tag, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import {
  fetchFuturesLocal,
  fetchFuturesLocalProgress,
  pauseFuturesLocal,
  postFuturesLocalBackfill,
  resumeFuturesLocal,
} from '../api'
import type { FuturesBackfillStatus, FuturesLocalVariety, FuturesMemoryView } from '../types'

type RangeValue = [Dayjs | null, Dayjs | null] | null

function span(row: FuturesLocalVariety, period: string) {
  return row.periods?.find((p) => p.period === period)
}

function formatBytes(n: number) {
  if (!Number.isFinite(n) || n <= 0) return '0 MB'
  const mb = n / (1024 * 1024)
  if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`
  return `${Math.round(mb)} MB`
}

function otherPeriods(row: FuturesLocalVariety) {
  return (row.periods ?? [])
    .filter((p) => p.period !== '1' && p.period !== '1d')
    .map((p) => `${p.period}分钟 ${p.first} ~ ${p.last}（${p.bars}）`)
    .join('；')
}

// 进度条读数：批量任务把「整批进度 + 当前标的进度」叠成一个 0~100；
// 单标的（或后台自动补全）就用它自己的 percent。
function progressOf(bf?: FuturesBackfillStatus) {
  const total = bf?.total ?? 0
  const done = bf?.done ?? 0
  const percent = Math.max(0, Math.min(100, bf?.percent ?? 0))
  if (total > 0) {
    const pct = ((done + percent / 100) / total) * 100
    return { pct: Math.max(0, Math.min(100, pct)), text: `第 ${Math.min(done + 1, total)}/${total} 个` }
  }
  return { pct: percent, text: '' }
}

export default function FuturesLocalPage() {
  const { message } = App.useApp()
  const [items, setItems] = useState<FuturesLocalVariety[]>([])
  const [backfill, setBackfill] = useState<FuturesBackfillStatus>()
  const [memory, setMemory] = useState<FuturesMemoryView>()
  const [q, setQ] = useState('')
  const [kind, setKind] = useState<string>('all')
  const [prefix, setPrefix] = useState<string>()
  const [range, setRange] = useState<RangeValue>([dayjs().subtract(1, 'year'), dayjs()])
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState<string>()

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await fetchFuturesLocal()
      setItems(Array.isArray(data?.items) ? data.items : [])
      setBackfill(data?.backfill)
      setMemory(data?.memory)
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [message])

  // 覆盖表 10 秒一次（要查全库，别太频繁）；进度 1 秒一次（只回状态，很轻）
  useEffect(() => {
    void load()
    const timer = window.setInterval(() => {
      void load()
    }, 10000)
    return () => window.clearInterval(timer)
  }, [load])

  useEffect(() => {
    const tick = async () => {
      try {
        setBackfill(await fetchFuturesLocalProgress())
      } catch {
        // 进度拿不到不影响表格，下个 tick 再试
      }
    }
    const timer = window.setInterval(() => void tick(), 1000)
    return () => window.clearInterval(timer)
  }, [])

  const rows = useMemo(() => {
    const key = q.trim().toUpperCase()
    return items.filter((row) => {
      if (kind !== 'all' && row.kind !== kind) return false
      if (!key) return true
      return row.prefix.includes(key) || row.name.includes(q.trim()) || row.symbol.includes(key)
    })
  }, [items, q, kind])

  async function run(body: Parameters<typeof postFuturesLocalBackfill>[0], okText: string, key: string) {
    setSubmitting(key)
    try {
      await postFuturesLocalBackfill(body)
      message.success(okText)
      setBackfill(await fetchFuturesLocalProgress())
      void load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : '提交失败')
    } finally {
      setSubmitting(undefined)
    }
  }

  // 补一个标的：行内按钮直接给代码，主连和月份都能补
  function backfillSymbol(symbol: string) {
    const from = range?.[0]?.format('YYYY-MM-DD')
    const to = range?.[1]?.format('YYYY-MM-DD')
    void run({ symbols: [symbol], from, to }, `已加入补全：${symbol}`, symbol)
  }

  const monthCount = useMemo(() => {
    const n: Record<string, number> = {}
    for (const row of items) {
      if (row.kind === 'month') n[row.prefix] = (n[row.prefix] ?? 0) + 1
    }
    return n
  }, [items])

  // 补整个品种：主连 + 该品种在交易的全部月份合约一起排队，进度按整批算
  function backfillVariety(target: string) {
    if (!target) {
      message.warning('请选择品种')
      return
    }
    const from = range?.[0]?.format('YYYY-MM-DD')
    const to = range?.[1]?.format('YYYY-MM-DD')
    void run(
      { prefix: target, kinds: ['main', 'months'], from, to },
      `已加入补全：${target}（主连 + ${monthCount[target] ?? 0} 个月份）`,
      target,
    )
  }

  // 只补这个品种缺的月份（不动主连）
  function backfillMonths(target: string) {
    const from = range?.[0]?.format('YYYY-MM-DD')
    const to = range?.[1]?.format('YYYY-MM-DD')
    void run(
      { prefix: target, kinds: ['months'], from, to },
      `已加入补全：${target} 的 ${monthCount[target] ?? 0} 个月份合约`,
      `${target}:months`,
    )
  }

  const columns: ColumnsType<FuturesLocalVariety> = [
    { title: '品种', dataIndex: 'prefix', width: 80 },
    { title: '名称', dataIndex: 'name', width: 100 },
    {
      title: '类型',
      dataIndex: 'kind',
      width: 90,
      render: (_, row) =>
        row.kind === 'month' ? <Tag color="blue">{row.label || '月份'}</Tag> : <Tag>{row.label || '主连'}</Tag>,
    },
    { title: '代码', dataIndex: 'symbol', width: 100 },
    {
      title: '1分钟开始',
      width: 150,
      render: (_, row) => span(row, '1')?.first || '—',
    },
    {
      title: '1分钟结束',
      width: 150,
      render: (_, row) => span(row, '1')?.last || '—',
    },
    {
      title: '1分钟根数',
      width: 110,
      render: (_, row) => span(row, '1')?.bars ?? 0,
    },
    {
      title: '日线开始',
      width: 120,
      render: (_, row) => span(row, '1d')?.first || '—',
    },
    {
      title: '日线结束',
      width: 120,
      render: (_, row) => span(row, '1d')?.last || '—',
    },
    {
      title: '日线根数',
      width: 100,
      render: (_, row) => span(row, '1d')?.bars ?? 0,
    },
    {
      title: '其他周期',
      render: (_, row) => otherPeriods(row) || '—',
    },
    {
      title: '操作',
      width: 170,
      render: (_, row) => (
        <Space size={4}>
          <Button size="small" loading={submitting === row.symbol} onClick={() => backfillSymbol(row.symbol)}>
            补全
          </Button>
          {row.kind === 'main' ? (
            <Button
              size="small"
              loading={submitting === `${row.prefix}:months`}
              onClick={() => backfillMonths(row.prefix)}
            >
              补全部月份{monthCount[row.prefix] ? `（${monthCount[row.prefix]}）` : ''}
            </Button>
          ) : null}
        </Space>
      ),
    },
  ]

  const paused = !!backfill?.paused
  const fraction = Math.round((memory?.fraction ?? 0.7) * 100)
  const { pct, text } = progressOf(backfill)
  const active = !!backfill?.running || (backfill?.queued ?? 0) > 0
  const targetText = backfill?.symbol
    ? `${backfill.name ?? backfill.prefix} ${backfill.label || ''}（${backfill.symbol}）`.trim()
    : backfill?.name
      ? `${backfill.name}（${backfill.prefix}）`
      : ''

  return (
    <div className="page-wrap">
      <Space style={{ marginBottom: 12 }} wrap>
        <Typography.Title level={4} style={{ margin: 0 }}>
          本地期货数据
        </Typography.Title>
        <Input.Search allowClear placeholder="品种/名称/代码" style={{ width: 180 }} onSearch={setQ} />
        <Select
          value={kind}
          onChange={setKind}
          style={{ width: 110 }}
          options={[
            { value: 'all', label: '全部标的' },
            { value: 'main', label: '仅主连' },
            { value: 'month', label: '仅月份' },
          ]}
        />
        <Select
          showSearch
          allowClear
          placeholder="选择品种"
          style={{ width: 160 }}
          value={prefix}
          optionFilterProp="label"
          onChange={setPrefix}
          options={items
            .filter((row) => row.kind === 'main')
            .map((row) => ({ value: row.prefix, label: `${row.prefix} ${row.name}` }))}
        />
        <DatePicker.RangePicker value={range} onChange={(v) => setRange(v)} allowClear />
        <Button type="primary" loading={submitting === prefix} onClick={() => backfillVariety(prefix ?? '')}>
          补全主连+全部月份
        </Button>
        <Button
          danger
          disabled={paused}
          onClick={() =>
            void pauseFuturesLocal()
              .then((s) => {
                setBackfill(s)
                message.success('已暂停补全')
              })
              .catch((e) => message.error(e instanceof Error ? e.message : '暂停失败'))
          }
        >
          暂停补全
        </Button>
        <Button
          disabled={!paused}
          onClick={() =>
            void resumeFuturesLocal()
              .then((s) => {
                setBackfill(s)
                message.success('已继续补全')
              })
              .catch((e) => message.error(e instanceof Error ? e.message : '继续失败'))
          }
        >
          继续补全
        </Button>
      </Space>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        启动后会在后台慢慢补 1 分钟 K 线，一次只请求一页，并在请求之间留间隔，避免把接口打限流。上游能提供多远就补多远。
        内存缓存大约占用当前空闲内存的 {fraction}%：已用 {formatBytes(memory?.used_bytes ?? 0)} / 预算{' '}
        {formatBytes(memory?.budget_bytes ?? 0)}（系统空闲 {formatBytes(memory?.free_bytes ?? 0)}）。
        补全只补 1 分钟；5/15/30/60/120 分钟由本地 1 分钟合成，等 1 分钟补得比它更深就不再问上游要了。
      </Typography.Paragraph>

      <div style={{ marginBottom: 12 }}>
        <Progress
          percent={Math.round(pct)}
          status={paused ? 'exception' : active ? 'active' : 'normal'}
          size="small"
          strokeColor={paused ? '#ff4d4f' : undefined}
        />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {paused ? '补全已暂停' : active ? '正在补全' : '空闲'}
          {text ? ` · ${text}` : ''}
          {targetText ? ` · ${targetText}` : ''}
          {backfill?.oldest ? ` · 已到 ${backfill.oldest}` : ''}
          {backfill?.message ? ` · ${backfill.message}` : ''}
          {(backfill?.queued ?? 0) > 0 ? ` · 排队 ${backfill?.queued}` : ''}
          {backfill?.total ? ` · 本批 ${backfill.done}/${backfill.total} 个标的` : ''}
        </Typography.Text>
      </div>

      <Table
        rowKey={(row) => `${row.prefix}-${row.symbol}`}
        loading={loading && items.length === 0}
        columns={columns}
        dataSource={rows}
        size="small"
        pagination={{ pageSize: 30, showSizeChanger: true, showTotal: (n) => `共 ${n} 个标的` }}
        scroll={{ x: 1380 }}
      />
    </div>
  )
}
