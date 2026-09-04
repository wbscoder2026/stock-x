import { useEffect, useState } from 'react'
import { App, Button, Card, Input, Space, Table, Typography } from 'antd'
import { fetchJob, fetchJobs, fetchSchedule, pauseJob, postJob, putSchedule, resumeBackfill, scheduleCron } from '../api'
import type { Job } from '../types'

const PENDING = new Set(['running', 'pending', 'queued'])

async function pollJob(id: string | number, onTick: (j: Job) => void) {
  const n = 400
  for (let i = 0; i < n; i++) {
    await new Promise((r) => setTimeout(r, 2000))
    try {
      const j = await fetchJob(id)
      onTick(j)
      const st = String(j.status ?? '').toLowerCase()
      if (st && !PENDING.has(st)) return j
    } catch {
      return
    }
  }
}

export default function JobsPage() {
  const { message } = App.useApp()
  const [rows, setRows] = useState<Job[]>([])
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState<string>()
  const [cron, setCron] = useState('')
  const [savingCron, setSavingCron] = useState(false)

  async function loadJobs() {
    setLoading(true)
    try {
      const data = (await fetchJobs()) ?? []
      setRows(Array.isArray(data) ? data : [])
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载任务失败')
    } finally {
      setLoading(false)
    }
  }

  async function loadCron() {
    try {
      const data = await fetchSchedule()
      setCron(scheduleCron(data))
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载定时失败')
    }
  }

  useEffect(() => {
    void loadJobs()
    void loadCron()
  }, [])

  async function run(type: string) {
    setBusy(type)
    try {
      const job = await postJob(type)
      const id = job?.id
      message.success(id != null ? `已提交 ${type} #${id}` : `已提交 ${type}`)
      await loadJobs()
      if (id != null) {
        const done = await pollJob(id, () => {
          void loadJobs()
        })
        if (done?.status) message.info(`任务 ${id}：${done.status}`)
        await loadJobs()
      }
    } catch (e) {
      message.error(e instanceof Error ? e.message : '提交失败')
    } finally {
      setBusy(undefined)
    }
  }

  async function pauseNow() {
    try {
      await pauseJob()
      message.success('正在暂停')
      await loadJobs()
    } catch (e) {
      message.error(e instanceof Error ? e.message : '暂停失败')
    }
  }

  async function resumeNow() {
    setBusy('resume')
    try {
      const job = await resumeBackfill()
      const id = job?.id
      message.success(id != null ? `继续回填 #${id}` : '继续回填')
      await loadJobs()
      if (id != null) {
        const done = await pollJob(id, () => {
          void loadJobs()
        })
        if (done?.status) message.info(`任务 ${id}：${done.status}`)
        await loadJobs()
      }
    } catch (e) {
      message.error(e instanceof Error ? e.message : '继续失败')
    } finally {
      setBusy(undefined)
    }
  }

  async function saveCron() {
    setSavingCron(true)
    try {
      await putSchedule(cron.trim())
      message.success('已保存定时')
      await loadCron()
    } catch (e) {
      message.error(e instanceof Error ? e.message : '保存定时失败')
    } finally {
      setSavingCron(false)
    }
  }

  return (
    <div className="page-wrap">
      <Space style={{ marginBottom: 16 }} wrap>
        <Typography.Title level={4} style={{ margin: 0 }}>
          任务
        </Typography.Title>
        <Button type="primary" loading={busy === 'backfill'} disabled={!!busy} onClick={() => void run('backfill')}>
          回填历史K线
        </Button>
        <Button danger onClick={() => void pauseNow()}>
          暂停
        </Button>
        <Button loading={busy === 'resume'} disabled={!!busy} onClick={() => void resumeNow()}>
          继续回填
        </Button>
        <Button loading={busy === 'sync'} disabled={!!busy} onClick={() => void run('sync')}>
          增量更新
        </Button>
        <Button loading={busy === 'scan'} disabled={!!busy} onClick={() => void run('scan')}>
          扫描最新交易日
        </Button>
        <Button onClick={() => void loadJobs()}>刷新</Button>
      </Space>
      <Card size="small" style={{ marginBottom: 16 }} title="定时任务 cron">
        <Space wrap>
          <Input
            style={{ width: 280 }}
            value={cron}
            onChange={(e) => setCron(e.target.value)}
            placeholder="例如 0 18 * * 1-5"
          />
          <Button type="primary" loading={savingCron} onClick={() => void saveCron()}>
            保存
          </Button>
        </Space>
      </Card>
      <Table
        rowKey={(r) => String(r.id)}
        loading={loading}
        dataSource={rows}
        size="small"
        pagination={{ pageSize: 20 }}
        expandable={{
          expandedRowRender: (r) => <pre className="job-log">{r.log || r.message || r.error || '（无日志）'}</pre>,
        }}
        columns={[
          { title: 'ID', dataIndex: 'id', width: 80 },
          { title: '类型', dataIndex: 'type', width: 100 },
          { title: '状态', dataIndex: 'status', width: 100 },
          {
            title: '进度',
            dataIndex: 'progress',
            width: 80,
            render: (v: unknown) => (v == null || v === '' ? '' : String(v)),
          },
          {
            title: '日志',
            dataIndex: 'log',
            ellipsis: true,
            render: (_: unknown, r: Job) => r.log || r.message || r.error || '',
          },
          { title: '开始时间', dataIndex: 'started_at', width: 180 },
        ]}
      />
    </div>
  )
}
