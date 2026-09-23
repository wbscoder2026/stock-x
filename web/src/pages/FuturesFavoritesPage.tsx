import { useCallback, useEffect, useMemo, useState, type Key } from 'react'
import {
  Alert,
  App,
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Space,
  Statistic,
  Switch,
  Table,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  deleteFuturesFavorites,
  fetchFuturesFavorites,
  putFuturesFavorite,
  scanFuturesFavorites,
} from '../api'
import type { FuturesAcrossScan, FuturesConfigScan, FuturesFavorite, FuturesParams, FuturesSymbolStat } from '../types'
import { pct } from './FuturesShared'

const PERIODS = [
  { value: '1', label: '1分钟' },
  { value: '5', label: '5分钟' },
  { value: '15', label: '15分钟' },
  { value: '30', label: '30分钟' },
  { value: '60', label: '60分钟' },
  { value: '120', label: '120分钟' },
]

function num(v: number | null, fallback: number) {
  return v == null || Number.isNaN(v) ? fallback : v
}

function signed(v: number) {
  return <span style={{ color: v >= 0 ? '#389e0d' : '#cf1322' }}>{pct(v)}</span>
}

export default function FuturesFavoritesPage() {
  const { message } = App.useApp()
  const [rows, setRows] = useState<FuturesFavorite[]>([])
  const [loading, setLoading] = useState(false)
  const [picked, setPicked] = useState<Key[]>([])
  const [editing, setEditing] = useState<FuturesFavorite>()
  const [name, setName] = useState('')
  const [note, setNote] = useState('')
  const [params, setParams] = useState<FuturesParams>({})
  const [saving, setSaving] = useState(false)
  const [scanning, setScanning] = useState(false)
  const [workers, setWorkers] = useState(4)
  const [scan, setScan] = useState<FuturesAcrossScan>()
  const [onlyTraded, setOnlyTraded] = useState(true)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setRows(await fetchFuturesFavorites())
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载收藏失败')
    } finally {
      setLoading(false)
    }
  }, [message])

  useEffect(() => {
    void load()
  }, [load])

  function openEdit(row: FuturesFavorite) {
    setEditing(row)
    setName(row.name)
    setNote(row.note ?? '')
    setParams({ ...row.params })
  }

  async function saveEdit() {
    if (!editing) return
    const next = name.trim()
    if (!next) {
      message.warning('名称不能为空')
      return
    }
    setSaving(true)
    try {
      await putFuturesFavorite(editing.id, {
        name: next,
        note: note.trim(),
        params,
        origin_symbol: editing.origin_symbol,
        origin_win_rate: editing.origin_win_rate,
        origin_avg_return: editing.origin_avg_return,
        origin_avg_r: editing.origin_avg_r,
        origin_profit_factor: editing.origin_profit_factor,
        origin_trades: editing.origin_trades,
      })
      message.success('已保存')
      setEditing(undefined)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  async function removeIds(ids: number[]) {
    if (ids.length === 0) return
    try {
      const res = await deleteFuturesFavorites(ids)
      message.success(`已删除 ${res.removed} 条`)
      setPicked((keys) => keys.filter((k) => !ids.includes(Number(k))))
      setScan(undefined)
      await load()
    } catch (e) {
      message.error(e instanceof Error ? e.message : '删除失败')
    }
  }

  async function runScan(ids: number[]) {
    if (ids.length === 0) {
      message.warning('先勾选要扫描的收藏')
      return
    }
    setScanning(true)
    try {
      const res = await scanFuturesFavorites({ ids, workers, min_trades: 20 })
      setScan(res)
      message.success(`扫完 ${res.symbols} 个品种，用时 ${(res.elapsed_ms / 1000).toFixed(1)}s`)
    } catch (e) {
      message.error(e instanceof Error ? e.message : '扫描失败')
    } finally {
      setScanning(false)
    }
  }

  const pickedIds = picked.map((k) => Number(k)).filter((id) => rows.some((r) => r.id === id))

  const cols: ColumnsType<FuturesFavorite> = [
    { title: '名称', dataIndex: 'name', width: 220 },
    { title: '备注', dataIndex: 'note', ellipsis: true },
    { title: '来源', dataIndex: 'origin_symbol', width: 80 },
    { title: '级别', width: 80, render: (_, r) => `${r.params.period ?? '-'}分钟` },
    { title: '盈亏比', width: 72, render: (_, r) => r.params.rr ?? '-' },
    { title: '止损ATR', width: 80, render: (_, r) => r.params.stop_atr ?? '-' },
    { title: '持有', width: 64, render: (_, r) => r.params.hold_bars ?? '-' },
    { title: '隔夜', width: 72, render: (_, r) => (r.params.no_overnight ? '日内' : '允许') },
    { title: '原胜率', width: 88, render: (_, r) => pct(r.origin_win_rate) },
    { title: '原收益', width: 88, render: (_, r) => signed(r.origin_avg_return) },
    { title: '原样本', dataIndex: 'origin_trades', width: 72 },
    {
      title: '操作',
      key: 'ops',
      width: 200,
      render: (_, row) => (
        <Space size={0}>
          <Button size="small" type="link" onClick={() => openEdit(row)}>
            编辑
          </Button>
          <Button size="small" type="link" disabled={scanning} onClick={() => void runScan([row.id])}>
            扫全品种
          </Button>
          <Popconfirm title="删除这条收藏？" onConfirm={() => void removeIds([row.id])}>
            <Button size="small" type="link" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <div style={{ padding: 16 }}>
      <Card
        size="small"
        title="期货收藏"
        extra={
          <Space wrap>
            <span>
              并发{' '}
              <InputNumber min={1} max={8} value={workers} onChange={(v) => setWorkers(num(v, 4))} />
            </span>
            <Button onClick={() => void load()} loading={loading}>
              刷新
            </Button>
            <Button type="primary" loading={scanning} disabled={pickedIds.length === 0} onClick={() => void runScan(pickedIds)}>
              扫描所选全品种{pickedIds.length ? `（${pickedIds.length}）` : ''}
            </Button>
            <Popconfirm title={`删除选中的 ${pickedIds.length} 条？`} disabled={pickedIds.length === 0} onConfirm={() => void removeIds(pickedIds)}>
              <Button danger disabled={pickedIds.length === 0}>
                删除所选
              </Button>
            </Popconfirm>
          </Space>
        }
      >
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          每条收藏是一组固定参数。扫全品种时，这条规则会套到每个主力连续上；某个品种没数据就跳过，不影响其他品种。
          平均胜率、平均收益按「有成交的品种等权」计算，用来看规则离了原来那个品种还管不管用。
        </Typography.Paragraph>
        <Table
          size="small"
          rowKey="id"
          loading={loading}
          columns={cols}
          dataSource={rows}
          rowSelection={{ selectedRowKeys: picked, onChange: (keys) => setPicked(keys) }}
          pagination={{ pageSize: 20, showSizeChanger: false }}
          scroll={{ x: 1400 }}
          locale={{ emptyText: '还没有收藏。到「期货回测」的扫描结果里勾选后保存。' }}
        />
      </Card>

      {scan ? <ScanReport scan={scan} onlyTraded={onlyTraded} onOnlyTraded={setOnlyTraded} /> : null}

      <Modal
        title="编辑收藏"
        open={!!editing}
        confirmLoading={saving}
        onOk={() => void saveEdit()}
        onCancel={() => setEditing(undefined)}
        okText="保存"
        width={640}
      >
        <Form layout="vertical">
          <Form.Item label="名称">
            <Input value={name} maxLength={80} onChange={(e) => setName(e.target.value)} />
          </Form.Item>
          <Form.Item label="备注">
            <Input.TextArea value={note} rows={2} maxLength={500} onChange={(e) => setNote(e.target.value)} />
          </Form.Item>
          <Space wrap size={12}>
            <Form.Item label="级别">
              <Select
                style={{ width: 110 }}
                options={PERIODS}
                value={params.period}
                onChange={(period) => setParams({ ...params, period })}
              />
            </Form.Item>
            <Form.Item label="盈亏比">
              <InputNumber value={params.rr} min={0.1} step={0.1} onChange={(rr) => setParams({ ...params, rr: num(rr, 1.5) })} />
            </Form.Item>
            <Form.Item label="止损ATR">
              <InputNumber value={params.stop_atr} min={0.1} step={0.1} onChange={(stop_atr) => setParams({ ...params, stop_atr: num(stop_atr, 1) })} />
            </Form.Item>
            <Form.Item label="持有根数">
              <InputNumber value={params.hold_bars} min={1} onChange={(hold_bars) => setParams({ ...params, hold_bars: num(hold_bars, 6) })} />
            </Form.Item>
            <Form.Item label="Donchian">
              <InputNumber value={params.donchian} min={1} onChange={(donchian) => setParams({ ...params, donchian: num(donchian, 20) })} />
            </Form.Item>
            <Form.Item label="ORB">
              <InputNumber value={params.orb} min={1} onChange={(orb) => setParams({ ...params, orb: num(orb, 30) })} />
            </Form.Item>
            <Form.Item label="ATR周期">
              <InputNumber value={params.atr_period} min={1} onChange={(atr_period) => setParams({ ...params, atr_period: num(atr_period, 14) })} />
            </Form.Item>
            <Form.Item label="ATR缓冲">
              <InputNumber value={params.atr_k} min={0} step={0.05} onChange={(atr_k) => setParams({ ...params, atr_k: num(atr_k, 0.25) })} />
            </Form.Item>
            <Form.Item label="量能">
              <InputNumber value={params.vol_ratio} min={0.1} step={0.1} onChange={(vol_ratio) => setParams({ ...params, vol_ratio: num(vol_ratio, 1.5) })} />
            </Form.Item>
            <Form.Item label="禁止隔夜">
              <Switch checked={!!params.no_overnight} onChange={(no_overnight) => setParams({ ...params, no_overnight })} />
            </Form.Item>
          </Space>
        </Form>
      </Modal>
    </div>
  )
}

function ScanReport({
  scan,
  onlyTraded,
  onOnlyTraded,
}: {
  scan: FuturesAcrossScan
  onlyTraded: boolean
  onOnlyTraded: (v: boolean) => void
}) {
  const overall = scan.overall
  const configCols: ColumnsType<FuturesConfigScan> = useMemo(
    () => [
      { title: '配置', dataIndex: 'name', width: 220 },
      { title: '级别', width: 80, render: (_, r) => `${r.params.period}分钟` },
      { title: '有样本', width: 88, render: (_, r) => `${r.covered}/${r.symbols}` },
      { title: '样本足', dataIndex: 'reliable', width: 72 },
      { title: '无成交', dataIndex: 'no_sample', width: 72 },
      { title: '失败', dataIndex: 'failed', width: 64 },
      { title: '总成交', dataIndex: 'total_trades', width: 80 },
      { title: '平均胜率', dataIndex: 'avg_win_rate', width: 96, render: (v: number) => pct(v) },
      { title: '平均收益', dataIndex: 'avg_return', width: 96, render: (v: number) => signed(v) },
      { title: '平均期望R', dataIndex: 'avg_r', width: 96, render: (v: number) => v.toFixed(2) },
      {
        title: '平均盈利因子',
        dataIndex: 'avg_profit_factor',
        width: 110,
        render: (v: number) => (v ? v.toFixed(2) : '-'),
      },
      { title: '加权胜率', dataIndex: 'pooled_win_rate', width: 96, render: (v: number) => pct(v) },
      { title: '加权收益', dataIndex: 'pooled_avg_return', width: 96, render: (v: number) => signed(v) },
    ],
    [],
  )
  const detailCols: ColumnsType<FuturesSymbolStat> = [
    { title: '品种', dataIndex: 'name', width: 100 },
    { title: '代码', dataIndex: 'symbol', width: 80 },
    { title: '成交', dataIndex: 'trades', width: 70 },
    { title: '胜率', dataIndex: 'win_rate', width: 90, render: (v: number) => pct(v) },
    { title: '平均收益', dataIndex: 'avg_return', width: 100, render: (v: number) => signed(v) },
    { title: '期望R', dataIndex: 'avg_r', width: 80, render: (v: number) => v.toFixed(2) },
    { title: '盈利因子', dataIndex: 'profit_factor', width: 90, render: (v: number) => (v ? v.toFixed(2) : '-') },
    { title: '说明', dataIndex: 'error', ellipsis: true },
  ]

  return (
    <Card size="small" style={{ marginTop: 16 }} title="全品种扫描">
      <Typography.Paragraph type="secondary">
        上面这一组是所选规则的总平均：每条规则先按品种等权算出自己的胜率和收益，再对规则等权。
        加权胜率按成交笔数，样本多的品种权重大。无成交的品种不进平均，只记在「无成交」里。
        样本足 = 成交不少于 {scan.min_trades} 笔。
      </Typography.Paragraph>
      <Space size={28} wrap style={{ marginBottom: 16 }}>
        <Statistic title="参与平均的规则" value={overall.configs} suffix={`/ ${scan.configs.length}`} />
        <Statistic title="平均胜率" value={pct(overall.avg_win_rate)} />
        <Statistic title="平均收益" value={pct(overall.avg_return)} valueStyle={{ color: overall.avg_return >= 0 ? '#389e0d' : '#cf1322' }} />
        <Statistic title="平均期望R" value={overall.avg_r.toFixed(2)} />
        <Statistic title="平均盈利因子" value={overall.avg_profit_factor ? overall.avg_profit_factor.toFixed(2) : '-'} />
        <Statistic title="加权胜率" value={pct(overall.pooled_win_rate)} />
        <Statistic title="加权收益" value={pct(overall.pooled_avg_return)} valueStyle={{ color: overall.pooled_avg_return >= 0 ? '#389e0d' : '#cf1322' }} />
        <Statistic title="总成交" value={overall.total_trades} />
      </Space>
      {scan.skipped?.length ? (
        <Alert style={{ marginBottom: 12 }} type="warning" showIcon message={`跳过 ${scan.skipped.length} 个品种`} description={scan.skipped.join('；')} />
      ) : null}
      <Space style={{ marginBottom: 8 }}>
        <span>品种明细只看有成交或失败</span>
        <Switch size="small" checked={onlyTraded} onChange={onOnlyTraded} />
      </Space>
      <Table
        size="small"
        rowKey="id"
        columns={configCols}
        dataSource={scan.configs}
        pagination={false}
        scroll={{ x: 1300 }}
        expandable={{
          expandedRowRender: (row) => (
            <Table
              size="small"
              rowKey="symbol"
              columns={detailCols}
              dataSource={(row.details ?? []).filter((d) => !onlyTraded || d.trades > 0 || !!d.error)}
              pagination={{ pageSize: 15, showSizeChanger: false }}
            />
          ),
        }}
      />
    </Card>
  )
}
