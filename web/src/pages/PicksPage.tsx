import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Alert, App, Button, DatePicker, Space, Table, Tabs, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { fetchHealth, fetchJob, fetchPicks, parseExtra, pauseJob, postJob, resumeBackfill } from '../api'
import type { Health, Job, PickRow } from '../types'

type RangeValue = [Dayjs | null, Dayjs | null] | null

const PENDING = new Set(['running', 'pending', 'queued'])

async function waitJob(id: string | number, rounds: number, onTick?: (j: Job) => void) {
  for (let i = 0; i < rounds; i++) {
    await new Promise((r) => setTimeout(r, 2000))
    const j = await fetchJob(id)
    onTick?.(j)
    const st = String(j.status ?? '').toLowerCase()
    if (st && !PENDING.has(st)) return j
  }
  return null
}

export default function PicksPage() {
  const { message } = App.useApp()
  const nav = useNavigate()
  const [rows, setRows] = useState<PickRow[]>([])
  const [health, setHealth] = useState<Health>()
  const [loading, setLoading] = useState(false)
  const [scanning, setScanning] = useState(false)
  const [filling, setFilling] = useState(false)
  const [fillHint, setFillHint] = useState('')
  const [tab, setTab] = useState<string>()
  const [range, setRange] = useState<RangeValue>([dayjs().subtract(6, 'day'), dayjs()])

  const from = range?.[0]?.format('YYYY-MM-DD')
  const to = range?.[1]?.format('YYYY-MM-DD')
  const emptyDB = (health?.bars ?? 0) === 0

  const loadHealth = useCallback(async () => {
    try {
      setHealth(await fetchHealth())
    } catch {
      /* ignore */
    }
  }, [])

  async function load() {
    setLoading(true)
    try {
      const data = (await fetchPicks(from && to ? { from, to } : undefined)) ?? []
      setRows(Array.isArray(data) ? data : [])
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载选股失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void loadHealth()
  }, [loadHealth])

  useEffect(() => {
    void load()
  }, [from, to])

  const groups = useMemo(() => {
    const m = new Map<string, PickRow[]>()
    for (const r of rows) {
      const k = r.strategy || '未分类'
      const list = m.get(k)
      if (list) list.push(r)
      else m.set(k, [r])
    }
    return m
  }, [rows])

  const strategies = useMemo(() => [...groups.keys()], [groups])
  const active = tab && groups.has(tab) ? tab : strategies[0]
  const current = active ? (groups.get(active) ?? []) : []

  const extraKeys = useMemo(() => {
    const keys = new Set<string>()
    for (const r of current) {
      for (const k of Object.keys(parseExtra(r.extra))) keys.add(k)
    }
    return [...keys]
  }, [current])

  const multiDay = new Set(current.map((r) => r.as_of).filter(Boolean)).size > 1

  const columns: ColumnsType<PickRow> = [
    ...(multiDay || from !== to
      ? [{ title: '日期', dataIndex: 'as_of', width: 120 } satisfies ColumnsType<PickRow>[number]]
      : []),
    {
      title: '代码',
      dataIndex: 'symbol',
      render: (v: string) => <Link to={`/kline?code=${encodeURIComponent(v)}`}>{v}</Link>,
    },
    { title: '名称', dataIndex: 'name' },
    ...extraKeys.map((k) => ({
      title: k,
      key: k,
      render: (_: unknown, row: PickRow) => {
        const v = parseExtra(row.extra)[k]
        return v == null ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v)
      },
    })),
  ]

  async function backfill() {
    setFilling(true)
    setFillHint('正在提交回填…已入库的票会自动跳过')
    try {
      const job = await postJob('backfill')
      const id = job?.id
      message.success(id != null ? `已开始回填 #${id}` : '已开始回填')
      if (id != null) {
        const done = await waitJob(id, 400, (j) => {
          setFillHint(`回填 ${j.status ?? ''} ${j.progress ?? 0}% ${j.log?.split('\n').pop() ?? ''}`)
        })
        const st = String(done?.status ?? '')
        if (st === 'paused') {
          message.info('已暂停，可点「继续回填」')
        } else if (st === 'success' || st === 'failed') {
          message.info(`回填 ${st}`)
        } else {
          message.info('回填仍在后台跑，可点「暂停」或到任务页看日志')
        }
      }
      await loadHealth()
    } catch (e) {
      message.error(e instanceof Error ? e.message : '回填提交失败')
    } finally {
      setFilling(false)
    }
  }

  async function pauseFill() {
    try {
      await pauseJob()
      message.success('正在暂停，稍等当前批次写完')
    } catch (e) {
      message.error(e instanceof Error ? e.message : '暂停失败')
    }
  }

  async function continueFill() {
    setFilling(true)
    setFillHint('继续回填…')
    try {
      const job = await resumeBackfill()
      const id = job?.id
      message.success(id != null ? `继续回填 #${id}` : '继续回填')
      if (id != null) {
        const done = await waitJob(id, 400, (j) => {
          setFillHint(`回填 ${j.status ?? ''} ${j.progress ?? 0}% ${j.log?.split('\n').pop() ?? ''}`)
        })
        const st = String(done?.status ?? '')
        if (st === 'paused') message.info('已暂停')
        else if (st) message.info(`回填 ${st}`)
      }
      await loadHealth()
    } catch (e) {
      message.error(e instanceof Error ? e.message : '继续失败')
    } finally {
      setFilling(false)
    }
  }

  async function scanNow() {
    if (!from || !to) {
      message.warning('请选择扫描日期区间')
      return
    }
    if (emptyDB) {
      message.warning('本地还没有 K 线，请先点「回填历史K线」')
      return
    }
    setScanning(true)
    try {
      const job = await postJob('scan', { from, to })
      const id = job?.id
      message.success(id != null ? `已提交扫描 ${from} ~ ${to} #${id}` : '已提交扫描')
      if (id != null) {
        const done = await waitJob(id, 90)
        if (done?.status) {
          const st = String(done.status)
          const log = done.log || ''
          if (st === 'failed' && log.includes('回填')) {
            message.error('还没有行情数据，请先回填历史K线')
          } else {
            message.info(`任务 ${id}：${st}`)
          }
          await load()
          await loadHealth()
          return
        }
      }
      message.info('可前往任务页查看日志')
    } catch (e) {
      message.error(e instanceof Error ? e.message : '提交失败')
    } finally {
      setScanning(false)
    }
  }

  return (
    <div className="page-wrap">
      {emptyDB ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="本地还没有行情数据"
          description="选股依赖日 K。首次使用请先回填（从 baostock 拉全市场历史，大约 10 分钟）。回填按钮在本页，不必去任务页。"
          action={
            <Space>
              <Button type="primary" loading={filling} onClick={() => void backfill()}>
                回填历史K线
              </Button>
              {health?.running?.type === 'backfill' ? (
                <Button onClick={() => void pauseFill()}>暂停</Button>
              ) : null}
            </Space>
          }
        />
      ) : (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message={`本地 K 线 ${health?.bars ?? 0} 条，最新交易日 ${health?.max_date || '—'}`}
        />
      )}
      {fillHint ? (
        <Typography.Paragraph type="secondary">{fillHint}</Typography.Paragraph>
      ) : null}
      <Space style={{ marginBottom: 16 }} wrap>
        <Typography.Title level={4} style={{ margin: 0 }}>
          选股结果
        </Typography.Title>
        <DatePicker.RangePicker value={range} onChange={(v) => setRange(v)} allowClear={false} />
        <Button type="primary" loading={scanning} disabled={filling} onClick={() => void scanNow()}>
          扫描该区间
        </Button>
        <Button loading={filling} disabled={!!health?.running} onClick={() => void backfill()}>
          回填历史K线
        </Button>
        <Button danger disabled={!filling && health?.running?.type !== 'backfill'} onClick={() => void pauseFill()}>
          暂停回填
        </Button>
        <Button disabled={filling || !!health?.running} onClick={() => void continueFill()}>
          继续回填
        </Button>
        <Button onClick={() => void load()}>刷新</Button>
        <Button onClick={() => nav('/jobs')}>任务日志</Button>
      </Space>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        按区间内每个交易日分别选股（最多 30 个交易日）。没有 K 线时先回填；当天 0 只可以把区间往前调。
      </Typography.Paragraph>
      <Tabs
        activeKey={active}
        onChange={setTab}
        items={strategies.map((s) => ({ key: s, label: `${s} (${groups.get(s)?.length ?? 0})` }))}
      />
      <Table
        rowKey={(r, i) => `${r.as_of}-${r.strategy}-${r.symbol}-${i}`}
        loading={loading}
        columns={columns}
        dataSource={current}
        pagination={{ pageSize: 50 }}
        size="small"
      />
    </div>
  )
}
