import { useEffect, useState } from 'react'
import { App, Button, Card, Form, Input, InputNumber, Space, Switch, Table, Typography } from 'antd'
import { fetchStrategies, parseParams, putStrategy } from '../api'
import type { Strategy } from '../types'

type Draft = {
  enabled: boolean
  params: Record<string, number>
  webhook_url: string
}

export default function StrategiesPage() {
  const { message } = App.useApp()
  const [rows, setRows] = useState<Strategy[]>([])
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})
  const [saving, setSaving] = useState<string>()
  const [loading, setLoading] = useState(false)

  function sid(s: Strategy) {
    return String(s.id)
  }

  async function load() {
    setLoading(true)
    try {
      const data = (await fetchStrategies()) ?? []
      const list = Array.isArray(data) ? data : []
      setRows(list)
      const next: Record<string, Draft> = {}
      for (const s of list) {
        next[sid(s)] = {
          enabled: Boolean(s.enabled),
          params: parseParams(s),
          webhook_url: s.webhook_url ?? '',
        }
      }
      setDrafts(next)
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载策略失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  function patch(id: string, part: Partial<Draft>) {
    setDrafts((prev) => ({ ...prev, [id]: { ...prev[id], ...part } }))
  }

  async function save(s: Strategy) {
    const id = sid(s)
    const d = drafts[id]
    if (!d) return
    setSaving(id)
    try {
      await putStrategy(s.id, {
        enabled: d.enabled,
        params: d.params,
        webhook_url: d.webhook_url,
      })
      message.success(`已保存 ${s.name}`)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : '保存失败')
    } finally {
      setSaving(undefined)
    }
  }

  return (
    <div className="page-wrap">
      <Space style={{ marginBottom: 16 }} wrap>
        <Typography.Title level={4} style={{ margin: 0 }}>
          策略
        </Typography.Title>
        <Button onClick={() => void load()}>刷新</Button>
      </Space>
      <Table
        rowKey={(r) => sid(r)}
        loading={loading}
        dataSource={rows}
        pagination={false}
        size="small"
        columns={[
          { title: '名称', dataIndex: 'name', width: 160 },
          {
            title: '启用',
            width: 90,
            render: (_: unknown, s: Strategy) => {
              const id = sid(s)
              const d = drafts[id]
              return (
                <Switch
                  checked={d?.enabled}
                  onChange={(enabled) => patch(id, { enabled })}
                />
              )
            },
          },
          {
            title: '参数',
            render: (_: unknown, s: Strategy) => {
              const id = sid(s)
              const d = drafts[id]
              const keys = Object.keys(d?.params ?? {})
              if (!d || keys.length === 0) return <Typography.Text type="secondary">无</Typography.Text>
              return (
                <Form layout="inline" size="small">
                  {keys.map((k) => (
                    <Form.Item key={k} label={k} style={{ marginBottom: 8 }}>
                      <InputNumber
                        value={d.params[k]}
                        onChange={(v) =>
                          patch(id, { params: { ...d.params, [k]: Number(v ?? 0) } })
                        }
                      />
                    </Form.Item>
                  ))}
                </Form>
              )
            },
          },
          {
            title: 'Webhook',
            width: 280,
            render: (_: unknown, s: Strategy) => {
              const id = sid(s)
              const d = drafts[id]
              return (
                <Input
                  value={d?.webhook_url}
                  placeholder="https://"
                  onChange={(e) => patch(id, { webhook_url: e.target.value })}
                />
              )
            },
          },
          {
            title: '操作',
            width: 100,
            render: (_: unknown, s: Strategy) => (
              <Button type="primary" size="small" loading={saving === sid(s)} onClick={() => void save(s)}>
                保存
              </Button>
            ),
          },
        ]}
      />
      <Card size="small" style={{ marginTop: 16 }} title="说明">
        修改启用状态、数值参数或 webhook 后点保存，将调用 putStrategy。
      </Card>
    </div>
  )
}
