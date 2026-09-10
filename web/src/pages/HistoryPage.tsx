import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { App, Button, DatePicker, Input, Space, Table, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { fetchJob, fetchKlineCoverage, pauseJob, postJob } from '../api'
import type { Job, KlineCoverage } from '../types'

type RangeValue = [Dayjs | null, Dayjs | null] | null

const PENDING = new Set(['running', 'pending', 'queued'])

async function waitJob(id: string | number, onTick?: (j: Job) => void) {
  for (let i = 0; i < 400; i++) {
    await new Promise((r) => setTimeout(r, 2000))
    const j = await fetchJob(id)
    onTick?.(j)
    const st = String(j.status ?? '').toLowerCase()
    if (st && !PENDING.has(st)) return j
  }
  return null
}

export default function HistoryPage() {
  const { message } = App.useApp()
  const [q, setQ] = useState('')
  const [rows, setRows] = useState<KlineCoverage[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(50)
  const [loading, setLoading] = useState(false)
  const [filling, setFilling] = useState<string>()
  const [hint, setHint] = useState('')
  const [range, setRange] = useState<RangeValue>([dayjs().subtract(1, 'year'), dayjs()])

  const from = range?.[0]?.format('YYYY-MM-DD')
  const to = range?.[1]?.format('YYYY-MM-DD')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await fetchKlineCoverage({ q: q.trim() || undefined, offset: (page - 1) * pageSize, limit: pageSize })
      setRows(Array.isArray(data?.items) ? data.items : [])
      setTotal(data?.total ?? 0)
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [q, page, pageSize, message])

  useEffect(() => {
    void load()
  }, [load])

  async function fill(symbols?: string[]) {
    if (!from || !to) {
      message.warning('请选择回填日期区间')
      return
    }
    const key = symbols?.length ? symbols.join(',') : 'all'
    setFilling(key)
    setHint('正在提交回填…')
    try {
      const job = await postJob('backfill', { from, to, symbols })
      const id = job?.id
      message.success(id != null ? `已开始回填 #${id}` : '已开始回填')
      if (id != null) {
        const done = await waitJob(id, (j) => {
          setHint(`回填 ${j.status ?? ''} ${j.progress ?? 0}% ${j.log?.split('\n').pop() ?? ''}`)
        })
        if (done?.status) message.info(`回填 ${done.status}`)
      }
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : '回填提交失败')
    } finally {
      setFilling(undefined)
    }
  }

  const columns: ColumnsType<KlineCoverage> = [
    {
      title: '代码',
      dataIndex: 'symbol',
      width: 120,
      render: (v: string) => <Link to={`/kline?code=${encodeURIComponent(v)}`}>{v}</Link>,
    },
    { title: '名称', dataIndex: 'name', width: 140 },
    { title: '市场', dataIndex: 'market', width: 80 },
    { title: '开始日期', dataIndex: 'start_date', width: 130, render: (v: string) => v || '—' },
    { title: '结束日期', dataIndex: 'end_date', width: 130, render: (v: string) => v || '—' },
    { title: 'K线根数', dataIndex: 'bars', width: 100 },
    {
      title: '操作',
      key: 'act',
      width: 140,
      render: (_: unknown, row) => (
        <Button size="small" loading={filling === row.symbol} disabled={!!filling} onClick={() => void fill([row.symbol])}>
          回填此区间
        </Button>
      ),
    },
  ]

  return (
    <div className="page-wrap">
      <Space style={{ marginBottom: 16 }} wrap>
        <Typography.Title level={4} style={{ margin: 0 }}>
          历史K线
        </Typography.Title>
        <Input.Search
          allowClear
          placeholder="代码或名称"
          style={{ width: 220 }}
          onSearch={(v) => {
            setQ(v)
            setPage(1)
          }}
        />
        <DatePicker.RangePicker value={range} onChange={(v) => setRange(v)} allowClear={false} />
        <Button type="primary" loading={filling === 'all'} disabled={!!filling} onClick={() => void fill()}>
          回填全部该区间
        </Button>
        <Button danger disabled={!filling} onClick={() => void pauseJob('backfill').then(() => message.success('正在暂停'))}>
          暂停回填
        </Button>
        <Button onClick={() => void load()}>刷新</Button>
      </Space>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        列出每只股票本地 K 线起止日期。选择区间后可回填单只或全市场（会覆盖该区间已有数据）。
      </Typography.Paragraph>
      {hint ? <Typography.Paragraph type="secondary">{hint}</Typography.Paragraph> : null}
      <Table
        rowKey="symbol"
        loading={loading}
        columns={columns}
        dataSource={rows}
        size="small"
        pagination={{
          current: page,
          pageSize,
          total,
          showSizeChanger: true,
          showTotal: (n) => `共 ${n} 只`,
          onChange: (p, ps) => {
            setPage(p)
            setPageSize(ps)
          },
        }}
      />
    </div>
  )
}
