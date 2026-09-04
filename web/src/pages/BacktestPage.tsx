import { useEffect, useMemo, useState } from 'react'
import { App, Button, Card, DatePicker, Form, InputNumber, Select, Space, Statistic, Table, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import dayjs, { type Dayjs } from 'dayjs'
import { fetchStrategies, postBacktest } from '../api'
import type { BacktestResult, Strategy } from '../types'

type Trade = Record<string, unknown>

function pct(n: number | undefined) {
  if (n == null || !Number.isFinite(n)) return '-'
  const v = Math.abs(n) <= 1.5 ? n * 100 : n
  return `${v.toFixed(2)}%`
}

export default function BacktestPage() {
  const { message } = App.useApp()
  const [strategies, setStrategies] = useState<Strategy[]>([])
  const [strategy, setStrategy] = useState<string>()
  const [from, setFrom] = useState<Dayjs | null>(dayjs().subtract(1, 'year'))
  const [to, setTo] = useState<Dayjs | null>(dayjs())
  const [holdDays, setHoldDays] = useState(5)
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<BacktestResult>()

  useEffect(() => {
    void (async () => {
      try {
        const data = (await fetchStrategies()) ?? []
        const list = Array.isArray(data) ? data : []
        setStrategies(list)
        setStrategy((prev) => prev ?? (list[0]?.name || String(list[0]?.id ?? '')))
      } catch (e) {
        message.error(e instanceof Error ? e.message : '加载策略失败')
      }
    })()
  }, [message])

  const trades: Trade[] = useMemo(() => {
    const r = result
    if (!r) return []
    const list = r.trades ?? r.items ?? r.rows
    return Array.isArray(list) ? list : []
  }, [result])

  const tradeColumns: ColumnsType<Trade> = useMemo(() => {
    const keys = new Set<string>()
    for (const t of trades) {
      for (const k of Object.keys(t)) keys.add(k)
    }
    const ordered = [...keys]
    if (ordered.length === 0) {
      return [{ title: '记录', render: (_: unknown, row: Trade) => JSON.stringify(row) }]
    }
    return ordered.map((k) => ({
      title: k,
      dataIndex: k,
      ellipsis: true,
      render: (v: unknown) => (v == null ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v)),
    }))
  }, [trades])

  async function run() {
    if (!strategy) {
      message.warning('请选择策略')
      return
    }
    if (!from || !to) {
      message.warning('请选择起止日期')
      return
    }
    setRunning(true)
    try {
      const data = await postBacktest({
        strategy,
        from: from.format('YYYY-MM-DD'),
        to: to.format('YYYY-MM-DD'),
        holdDays,
      })
      setResult(data)
      message.success('回测完成')
    } catch (e) {
      message.error(e instanceof Error ? e.message : '回测失败')
    } finally {
      setRunning(false)
    }
  }

  const winRate = result?.win_rate ?? result?.winRate
  const avgReturn = result?.avg_return ?? result?.avgReturn

  return (
    <div className="page-wrap">
      <Typography.Title level={4} style={{ marginBottom: 16 }}>
        回测
      </Typography.Title>
      <Card size="small" style={{ marginBottom: 16 }}>
        <Form layout="inline">
          <Form.Item label="策略">
            <Select
              style={{ width: 220 }}
              value={strategy}
              onChange={setStrategy}
              options={strategies.map((s) => ({
                value: s.name || String(s.id),
                label: s.name || String(s.id),
              }))}
            />
          </Form.Item>
          <Form.Item label="从">
            <DatePicker value={from} onChange={setFrom} />
          </Form.Item>
          <Form.Item label="到">
            <DatePicker value={to} onChange={setTo} />
          </Form.Item>
          <Form.Item label="持有天数">
            <InputNumber min={1} value={holdDays} onChange={(v) => setHoldDays(Number(v ?? 1))} />
          </Form.Item>
          <Form.Item>
            <Button type="primary" loading={running} onClick={() => void run()}>
              运行回测
            </Button>
          </Form.Item>
        </Form>
      </Card>
      {result ? (
        <Space size="large" style={{ marginBottom: 16 }}>
          <Statistic title="胜率 win_rate" value={pct(winRate)} />
          <Statistic title="平均收益 avg_return" value={pct(avgReturn)} />
        </Space>
      ) : null}
      <Table
        rowKey={(_, i) => String(i)}
        size="small"
        columns={tradeColumns}
        dataSource={trades}
        pagination={{ pageSize: 50 }}
        locale={{ emptyText: result ? '无成交' : '尚未回测' }}
      />
    </div>
  )
}
