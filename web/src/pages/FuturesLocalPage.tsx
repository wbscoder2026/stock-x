import { useCallback, useEffect, useMemo, useState } from 'react'
import { App, Button, DatePicker, Input, Select, Space, Table, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { fetchFuturesLocal, pauseFuturesLocal, postFuturesLocalBackfill, resumeFuturesLocal } from '../api'
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

export default function FuturesLocalPage() {
  const { message } = App.useApp()
  const [items, setItems] = useState<FuturesLocalVariety[]>([])
  const [backfill, setBackfill] = useState<FuturesBackfillStatus>()
  const [memory, setMemory] = useState<FuturesMemoryView>()
  const [q, setQ] = useState('')
  const [prefix, setPrefix] = useState<string>()
  const [range, setRange] = useState<RangeValue>([dayjs().subtract(1, 'year'), dayjs()])
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)

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

  useEffect(() => {
    void load()
    const timer = window.setInterval(() => {
      void load()
    }, 2000)
    return () => window.clearInterval(timer)
  }, [load])

  const rows = useMemo(() => {
    const key = q.trim().toUpperCase()
    if (!key) return items
    return items.filter((row) => row.prefix.includes(key) || row.name.includes(q.trim()) || row.symbol.includes(key))
  }, [items, q])

  async function backfillOne(target: string) {
    const from = range?.[0]?.format('YYYY-MM-DD')
    const to = range?.[1]?.format('YYYY-MM-DD')
    if (!target) {
      message.warning('请选择品种')
      return
    }
    setSubmitting(true)
    try {
      await postFuturesLocalBackfill({ prefix: target, from, to })
      message.success(`已加入补全：${target}`)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : '提交失败')
    } finally {
      setSubmitting(false)
    }
  }

  const columns: ColumnsType<FuturesLocalVariety> = [
    { title: '品种', dataIndex: 'prefix', width: 80 },
    { title: '名称', dataIndex: 'name', width: 100 },
    { title: '代码', dataIndex: 'symbol', width: 90 },
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
      width: 120,
      render: (_, row) => (
        <Button size="small" loading={submitting && prefix === row.prefix} onClick={() => void backfillOne(row.prefix)}>
          补全此品种
        </Button>
      ),
    },
  ]

  const paused = !!backfill?.paused
  const fraction = Math.round((memory?.fraction ?? 0.7) * 100)

  return (
    <div className="page-wrap">
      <Space style={{ marginBottom: 12 }} wrap>
        <Typography.Title level={4} style={{ margin: 0 }}>
          本地期货数据
        </Typography.Title>
        <Input.Search allowClear placeholder="品种或名称" style={{ width: 180 }} onSearch={setQ} />
        <Select
          showSearch
          allowClear
          placeholder="选择品种"
          style={{ width: 160 }}
          value={prefix}
          optionFilterProp="label"
          onChange={setPrefix}
          options={items.map((row) => ({ value: row.prefix, label: `${row.prefix} ${row.name}` }))}
        />
        <DatePicker.RangePicker value={range} onChange={(v) => setRange(v)} allowClear />
        <Button type="primary" loading={submitting} onClick={() => void backfillOne(prefix ?? '')}>
          补全所选区间
        </Button>
        <Button
          danger
          disabled={paused}
          onClick={() =>
            void pauseFuturesLocal()
              .then(() => message.success('已暂停补全'))
              .catch((e) => message.error(e instanceof Error ? e.message : '暂停失败'))
          }
        >
          暂停补全
        </Button>
        <Button
          disabled={!paused}
          onClick={() =>
            void resumeFuturesLocal()
              .then(() => message.success('已继续补全'))
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
      </Typography.Paragraph>
      <Typography.Paragraph style={{ marginTop: 0 }}>
        {paused ? '补全已暂停' : backfill?.running ? '正在补全' : '空闲'}
        {backfill?.name ? ` · ${backfill.name}（${backfill.prefix}）` : ''}
        {backfill?.oldest ? ` · 已到 ${backfill.oldest}` : ''}
        {backfill?.message ? ` · ${backfill.message}` : ''}
        {backfill?.queued ? ` · 排队 ${backfill.queued}` : ''}
      </Typography.Paragraph>
      <Table
        rowKey="prefix"
        loading={loading && items.length === 0}
        columns={columns}
        dataSource={rows}
        size="small"
        pagination={{ pageSize: 20, showSizeChanger: true, showTotal: (n) => `共 ${n} 个品种` }}
        scroll={{ x: 1200 }}
      />
    </div>
  )
}
