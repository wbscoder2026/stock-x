import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Alert,
  App,
  Button,
  Card,
  Collapse,
  DatePicker,
  Empty,
  Input,
  Modal,
  Progress,
  Select,
  Space,
  Spin,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import {
  fetchFuturesContracts,
  fetchFuturesLocal,
  fetchFuturesLocalDetail,
  pauseFuturesLocal,
  postFuturesLocalBackfill,
  resumeFuturesLocal,
} from '../api'
import type {
  FuturesBackfillStatus,
  FuturesContract,
  FuturesContractSpan,
  FuturesLocalDetail,
  FuturesLocalVariety,
  FuturesMemoryView,
} from '../types'

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

function fmtDur(sec: number) {
  if (sec < 60) return `${sec} 秒`
  if (sec < 3600) return `${Math.floor(sec / 60)} 分 ${sec % 60} 秒`
  return `${Math.floor(sec / 3600)} 小时 ${Math.floor((sec % 3600) / 60)} 分`
}

function otherPeriods(row: FuturesLocalVariety) {
  return (row.periods ?? [])
    .filter((p) => p.period !== '1' && p.period !== '1d')
    .map((p) => `${p.period}分钟 ${p.first} ~ ${p.last}（${p.bars}）`)
    .join('；')
}

// MonthlyPanel 展开行：按月份看这个品种补到什么程度（二级分类的「月份」层）。
function MonthlyPanel({
  prefix,
  contracts,
  onOpen,
}: {
  prefix: string
  contracts?: FuturesContractSpan[]
  onOpen: (symbol: string) => void
}) {
  const [detail, setDetail] = useState<FuturesLocalDetail>()
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    void fetchFuturesLocalDetail(prefix)
      .then((d) => {
        if (!cancelled) setDetail(d)
      })
      .catch(() => undefined)
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [prefix])

  const minute = detail?.periods.find((p) => p.period === '1')
  const months = minute?.months ?? []

  return (
    <div>
      {loading ? (
        <Spin size="small" />
      ) : months.length === 0 ? (
        <Typography.Text type="secondary">这个品种还没有 1 分钟数据</Typography.Text>
      ) : (
        <Table
          size="small"
          rowKey="month"
          pagination={false}
          scroll={{ x: 560 }}
          columns={[
            { title: '月份', dataIndex: 'month', width: 110 },
            { title: '天数', dataIndex: 'days', width: 80 },
            { title: '根数', dataIndex: 'bars', width: 90 },
            {
              title: '疑似缺失',
              dataIndex: 'missing',
              render: (v?: string[]) =>
                v?.length ? (
                  <Tooltip title={v.join('、')}>
                    <Tag color="orange">{v.length} 天</Tag>
                  </Tooltip>
                ) : (
                  '—'
                ),
            },
          ]}
          dataSource={[...months].reverse()}
        />
      )}
      {contracts?.length ? (
        <div style={{ marginTop: 8 }}>
          <Typography.Text type="secondary" style={{ fontSize: 12, marginRight: 6 }}>
            月份合约（点击查看该合约明细）：
          </Typography.Text>
          {contracts.map((c) => (
            <Tag key={c.symbol} color="blue" style={{ cursor: 'pointer' }} onClick={() => onOpen(c.symbol)}>
              {c.label} · {c.days} 天
            </Tag>
          ))}
        </div>
      ) : null}
    </div>
  )
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
  const [detailOpen, setDetailOpen] = useState(false)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detail, setDetail] = useState<FuturesLocalDetail>()
  const [contracts, setContracts] = useState<FuturesContract[]>([])
  const [targetSymbol, setTargetSymbol] = useState('') // 空 = 补主连

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

  // 选了品种就把它的合约拉出来，好让用户选「补主连还是补某个月份合约」
  useEffect(() => {
    if (!prefix) return
    let cancelled = false
    void fetchFuturesContracts(prefix)
      .then((list) => {
        if (!cancelled) setContracts(list ?? [])
      })
      .catch(() => {
        if (!cancelled) setContracts([])
      })
    return () => {
      cancelled = true
    }
  }, [prefix])

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
      await postFuturesLocalBackfill({ prefix: target, symbol: targetSymbol || undefined, from, to })
      message.success(`已加入补全：${targetSymbol || `${target}0（主连）`}`)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : '提交失败')
    } finally {
      setSubmitting(false)
    }
  }

  async function openDetail(target: string, symbol?: string) {
    setDetailOpen(true)
    setDetailLoading(true)
    try {
      setDetail(await fetchFuturesLocalDetail(target, symbol))
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载明细失败')
      setDetailOpen(false)
    } finally {
      setDetailLoading(false)
    }
  }

  const columns: ColumnsType<FuturesLocalVariety> = [
    { title: '品种', dataIndex: 'prefix', width: 80 },
    { title: '名称', dataIndex: 'name', width: 100 },
    { title: '代码', dataIndex: 'symbol', width: 90 },
    {
      title: '月份合约',
      width: 100,
      render: (_, row) =>
        row.contracts?.length ? <Tooltip title={row.contracts.map((c) => c.symbol).join('、')}><Tag color="purple">{row.contracts.length} 个</Tag></Tooltip> : '—',
    },
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
      title: '1分钟天数',
      width: 110,
      render: (_, row) => {
        const n = span(row, '1')?.days ?? 0
        return n ? <Tag color="blue">{n} 天</Tag> : <Tag>无</Tag>
      },
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
        <Space size={0}>
          <Button size="small" loading={submitting && prefix === row.prefix} onClick={() => void backfillOne(row.prefix)}>
            补全
          </Button>
          <Button size="small" type="link" onClick={() => void openDetail(row.prefix)}>
            详情
          </Button>
        </Space>
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
          onChange={(v) => {
            setPrefix(v)
            setContracts([])
            setTargetSymbol('')
          }}
          options={items.map((row) => ({ value: row.prefix, label: `${row.prefix} ${row.name}` }))}
        />
        <Select
          showSearch
          allowClear
          placeholder="补全目标（默认主连）"
          style={{ width: 200 }}
          value={targetSymbol || undefined}
          onChange={(v) => setTargetSymbol(v ?? '')}
          options={[
            { value: '', label: prefix ? `主连 ${prefix}0` : '主连（先选品种）' },
            ...contracts
              .filter((c) => c.kind !== 'main')
              .map((c) => ({ value: c.symbol, label: `${c.label} · ${c.symbol}` })),
          ]}
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
      {backfill && (backfill.running || paused || backfill.percent) ? (
        <Card size="small" style={{ marginBottom: 12 }} title="同步进度">
          <Progress
            percent={backfill.percent ?? 0}
            status={paused ? 'exception' : backfill.running ? 'active' : 'normal'}
          />
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {paused ? '已暂停' : backfill.running ? '正在补全' : '空闲'}
            {backfill.round_all ? ` · 本轮第 ${backfill.round_idx ?? 0}/${backfill.round_all} 个品种` : ''}
            {backfill.name ? ` · 当前 ${backfill.name}（${backfill.prefix}）` : ''}
            {backfill.total ? ` · 已翻 ${backfill.done ?? 0}/${backfill.total} 页` : ''}
            {backfill.elapsed_sec ? ` · 已跑 ${fmtDur(backfill.elapsed_sec)}` : ''}
            {backfill.saved ? ` · 已存 ${backfill.saved} 根` : ''}
            {backfill.oldest ? ` · 已补到 ${backfill.oldest}` : ''}
            {backfill.queued ? ` · 排队 ${backfill.queued}` : ''}
          </Typography.Text>
          {backfill.total ? (
            <Progress
              percent={Math.round(((backfill.done ?? 0) / backfill.total) * 100)}
              size="small"
              showInfo={false}
              style={{ marginTop: 6, marginBottom: 0 }}
            />
          ) : null}
          {backfill.message ? (
            <div style={{ fontSize: 12, color: '#8c8c8c', marginTop: 4 }}>{backfill.message}</div>
          ) : null}
        </Card>
      ) : null}
      <Modal
        open={detailOpen}
        onCancel={() => setDetailOpen(false)}
        footer={null}
        width={900}
        title={detail ? `${detail.name}（${detail.symbol}）同步明细` : '同步明细'}
      >
        {detailLoading || !detail ? (
          <div style={{ textAlign: 'center', padding: 24 }}>
            <Spin />
          </div>
        ) : detail.periods.length === 0 ? (
          <Empty description="这个品种还没有任何本地数据" />
        ) : (
          <>
            <Space wrap style={{ marginBottom: 10 }}>
              <Tag color={detail.symbol.toUpperCase() === detail.prefix.toUpperCase() + '0' ? 'geekblue' : 'purple'}>
                {detail.symbol.toUpperCase() === detail.prefix.toUpperCase() + '0' ? '主连' : '月份合约'}
              </Tag>
              <Tag color={detail.minute_synced ? 'green' : 'red'}>
                {detail.minute_synced ? '已同步 1 分钟级别' : '没有 1 分钟级别数据'}
              </Tag>
              {detail.minute_synced ? (
                <Typography.Text>
                  1 分钟覆盖 <b>{detail.minute_days}</b> 天：{detail.minute_first} ~ {detail.minute_last}
                </Typography.Text>
              ) : null}
            </Space>
            {detail.periods.map((pd) => (
              <Card
                key={pd.period}
                size="small"
                type="inner"
                style={{ marginBottom: 8 }}
                title={`${pd.period === '1d' ? '日线' : pd.period + ' 分钟'}${
                  pd.is_minute ? ' · 1 分钟级别' : ''
                } · 共 ${pd.bars} 根 / ${pd.days.length} 天（${pd.first} ~ ${pd.last}）`}
              >
                {pd.missing?.length ? (
                  <Alert
                    type="warning"
                    showIcon
                    style={{ marginBottom: 8 }}
                    message={`首末之间有 ${pd.missing.length} 个工作日没有数据（节假日会有误报）`}
                    description={pd.missing.join('、')}
                  />
                ) : null}
                <Collapse
                  size="small"
                  items={[...pd.months].reverse().map((m) => ({
                    key: m.month,
                    label: `${m.month} · ${m.days} 天 · ${m.bars} 根${
                      m.missing?.length ? ` · 疑似缺 ${m.missing.length} 天` : ''
                    }`,
                    children: (
                      <Table
                        size="small"
                        rowKey="day"
                        columns={[
                          { title: '日期', dataIndex: 'day', width: 130 },
                          { title: '星期', dataIndex: 'weekday', width: 90 },
                          { title: '根数', dataIndex: 'bars', width: 100 },
                        ]}
                        dataSource={pd.days.filter((d) => d.day.startsWith(m.month)).reverse()}
                        pagination={false}
                        scroll={{ y: 240 }}
                      />
                    ),
                  }))}
                />
              </Card>
            ))}
          </>
        )}
      </Modal>
      <Table
        rowKey="prefix"
        expandable={{
          expandedRowRender: (row) => (
            <MonthlyPanel
              prefix={row.prefix}
              contracts={row.contracts}
              onOpen={(sym) => void openDetail(row.prefix, sym)}
            />
          ),
        }}
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
