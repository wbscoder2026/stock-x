// 期货监控突破页：全市场扫描 + 提醒列表 + 黑名单。
// 与「回测」页拆开：这里只负责盯盘发提醒，点提醒行会跳到回测页加载对应价格图。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { App, Button, Card, Form, Input, InputNumber, Modal, Radio, Select, Space, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  addFuturesBlacklist,
  fetchFuturesBlacklist,
  fetchFuturesVarieties,
  fetchFuturesWatchEvents,
  fetchFuturesWatchStatus,
  removeFuturesBlacklist,
  startFuturesWatch,
  stopFuturesWatch,
  testFuturesWatchAlert,
  updateFuturesWatch,
} from '../api'
import type {
  FuturesBlacklistEntry,
  FuturesBlacklistScope,
  FuturesVariety,
  FuturesWatchEvent,
  FuturesWatchStatus,
} from '../types'
import { DEFAULT_PARAMS, LEVEL_OPTIONS, ParamLabel, TIPS, fmtPrice, varietyOptions } from './FuturesShared'

// 提醒有时效：超过 TTL 的提醒自动从列表清除（服务端 AlertTTL=30min，下发值优先）
const DEFAULT_ALERT_TTL_SEC = 1800

// pruneAlerts 剔除过期提醒；突破讲究时效，过期的信息是噪音。
function pruneAlerts(list: FuturesWatchEvent[], ttlMs: number, now = Date.now()): FuturesWatchEvent[] {
  return list.filter((e) => !e.time_ms || now - e.time_ms < ttlMs)
}

const alertCols = (onBlacklist: (row: FuturesWatchEvent) => void): ColumnsType<FuturesWatchEvent> => [
  { title: '时间', dataIndex: 'time', width: 140 },
  {
    title: '品种',
    dataIndex: 'name',
    width: 100,
    render: (v: string, r) => `${v} ${r.prefix}`,
  },
  {
    title: '合约',
    dataIndex: 'contract_label',
    width: 90,
    render: (v: string, r) => <span title={r.contract || `${r.prefix}0（主连）`}>{v || '主连'}</span>,
  },
  {
    title: '方向',
    dataIndex: 'direction',
    width: 100,
    render: (v: string) => <Tag color={v.includes('向上') ? 'red' : 'green'}>{v}</Tag>,
  },
  { title: '关键位', dataIndex: 'level' },
  { title: '现价', dataIndex: 'close', width: 90, render: (v: number) => v?.toFixed(1) },
  { title: '关键位价', dataIndex: 'level_price', width: 100, render: (v: number) => v?.toFixed(1) },
  {
    title: <ParamLabel text="推荐止损" hint={TIPS.stop_tp} />,
    dataIndex: 'stop_price',
    width: 110,
    render: (v: number, r) =>
      v ? (
        <Tooltip title={`止损 ${r.stop_atr}×ATR，按报价单位 ${r.tick_size} 取整`}>
          <span style={{ color: '#cf1322' }}>{fmtPrice(v, r.tick_size)}</span>
        </Tooltip>
      ) : (
        <span>-</span>
      ),
  },
  {
    title: <ParamLabel text="推荐止盈" hint={TIPS.stop_tp} />,
    dataIndex: 'tp_price',
    width: 110,
    render: (v: number, r) =>
      v ? (
        <Tooltip title={`止损距离 × 盈亏比 ${r.rr}，按报价单位 ${r.tick_size} 取整`}>
          <span style={{ color: '#389e0d' }}>{fmtPrice(v, r.tick_size)}</span>
        </Tooltip>
      ) : (
        <span>-</span>
      ),
  },
  { title: '量', dataIndex: 'volume', width: 90 },
  {
    title: '来源',
    dataIndex: 'fresh',
    width: 110,
    render: (v: boolean) => (v ? <Tag color="blue">实时</Tag> : <Tag>启动时已有</Tag>),
  },
  {
    title: '操作',
    key: 'ops',
    width: 120,
    render: (_, row) => (
      <Button
        size="small"
        type="link"
        onClick={(ev) => {
          ev.stopPropagation() // 别触发「点行看图」
          onBlacklist(row)
        }}
      >
        加入黑名单
      </Button>
    ),
  },
]

export default function FuturesWatchPage() {
  const { message, notification } = App.useApp()
  const navigate = useNavigate()
  const [varieties, setVarieties] = useState<FuturesVariety[]>([])
  const [period, setPeriod] = useState(DEFAULT_PARAMS.period)
  const [orb, setOrb] = useState(DEFAULT_PARAMS.orb)
  const [donchian, setDonchian] = useState(DEFAULT_PARAMS.donchian)
  const [atrPeriod, setAtrPeriod] = useState(DEFAULT_PARAMS.atrPeriod)
  const [atrK, setAtrK] = useState(DEFAULT_PARAMS.atrK)
  const [volRatio, setVolRatio] = useState(DEFAULT_PARAMS.volRatio)
  const [stopATR, setStopATR] = useState(DEFAULT_PARAMS.stopATR)
  const [rr, setRr] = useState(DEFAULT_PARAMS.rr)
  const [watchPrefixes, setWatchPrefixes] = useState<string[]>([])
  const [watchInterval, setWatchInterval] = useState(30)
  const [watch, setWatch] = useState<FuturesWatchStatus>()
  const [alerts, setAlerts] = useState<FuturesWatchEvent[]>([])
  const [watchBusy, setWatchBusy] = useState(false)
  const [alertFeishu, setAlertFeishu] = useState(false)
  const [alertDesktop, setAlertDesktop] = useState(false)
  const [testingAlert, setTestingAlert] = useState(false)
  const [blacklist, setBlacklist] = useState<FuturesBlacklistEntry[]>([])
  const [blacklistOpen, setBlacklistOpen] = useState(false)
  const [blTarget, setBlTarget] = useState<FuturesWatchEvent>()
  const [blScope, setBlScope] = useState<FuturesBlacklistScope>('contract')
  const [blNote, setBlNote] = useState('')
  const [blBusy, setBlBusy] = useState(false)
  const cursorRef = useRef(0)
  const runningRef = useRef(false)
  const firstPullRef = useRef(true)
  const blacklistLoadedRef = useRef(false)

  useEffect(() => {
    void (async () => {
      try {
        const list = (await fetchFuturesVarieties()) ?? []
        setVarieties(Array.isArray(list) ? list : [])
      } catch (e) {
        message.error(e instanceof Error ? e.message : '加载品种失败')
      }
    })()
  }, [message])

  const varietyOpts = useMemo(() => varietyOptions(varieties), [varieties])

  const watchRunning = watch?.running ?? false
  const alertTTLSec = watch?.alert_ttl_sec ?? DEFAULT_ALERT_TTL_SEC
  const coolingSources = (watch?.sources ?? [])
    .filter((s) => s.cooldown > 0)
    .map((s) => `${s.name} 冷却 ${s.cooldown}s`)
    .join('，')

  const watchConfig = useMemo(
    () => ({
      period,
      orb,
      donchian,
      atr_period: atrPeriod,
      atr_k: atrK,
      vol_ratio: volRatio,
      stop_atr: stopATR,
      rr,
      interval: watchInterval,
      prefixes: watchPrefixes,
      alert: { feishu: alertFeishu, desktop: alertDesktop },
    }),
    [period, orb, donchian, atrPeriod, atrK, volRatio, stopATR, rr, watchInterval, watchPrefixes, alertFeishu, alertDesktop],
  )

  // 点提醒行 → 跳到「回测」页加载该月份合约 + 对应级别的价格图（关键位会画在图上）
  function openAlert(e: FuturesWatchEvent) {
    const target = e.contract || `${e.prefix}0`
    const level = (watch?.config?.period as string) || period
    navigate(`/futures/backtest?symbol=${encodeURIComponent(target)}&period=${encodeURIComponent(level)}`)
  }

  const alertColumns = useMemo(() => alertCols(openBlacklistModal), [])

  function openBlacklistModal(row: FuturesWatchEvent) {
    setBlTarget(row)
    setBlNote('')
    setBlScope(row.contract ? 'contract' : 'variety') // 没解析出月份就只能按品种屏蔽
  }

  const reloadBlacklist = useCallback(async () => {
    try {
      setBlacklist(await fetchFuturesBlacklist())
    } catch {
      /* 列表拉取失败不打断页面 */
    }
  }, [])

  async function submitBlacklist() {
    const target = blTarget
    if (!target) return
    const value = blScope === 'contract' ? target.contract || '' : target.prefix
    if (!value) {
      message.error('该行还没解析出月份合约，请改用「屏蔽整个品种」')
      return
    }
    setBlBusy(true)
    try {
      await addFuturesBlacklist({ scope: blScope, value, note: blNote })
      await reloadBlacklist()
      // 已提醒过的行直接从列表里拿掉（服务端也不再产生新提醒）
      setAlerts((prev) =>
        prev.filter((a) => (blScope === 'contract' ? a.contract !== value : a.prefix !== target.prefix)),
      )
      setBlTarget(undefined)
      message.success(
        blScope === 'contract'
          ? `已加入黑名单：${value} 的突破不再提醒`
          : `已加入黑名单：${target.name} ${target.prefix} 所有合约都不再提醒`,
      )
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加入黑名单失败')
    } finally {
      setBlBusy(false)
    }
  }

  async function removeBlacklist(entry: FuturesBlacklistEntry) {
    try {
      await removeFuturesBlacklist(entry.scope, entry.value)
      await reloadBlacklist()
      message.success(`已移除 ${entry.value}`)
    } catch (e) {
      message.error(e instanceof Error ? e.message : '移除失败')
    }
  }

  async function testAlert() {
    setTestingAlert(true)
    try {
      const res = await testFuturesWatchAlert({ feishu: alertFeishu, desktop: alertDesktop })
      message.info(res.note || '测试提醒已发出')
    } catch (e) {
      message.error(e instanceof Error ? e.message : '测试提醒失败')
    } finally {
      setTestingAlert(false)
    }
  }

  const notifyBreakout = useCallback(
    (e: FuturesWatchEvent) => {
      const up = e.direction.includes('向上')
      const title = `${e.name} ${e.prefix} ${e.direction} ${e.level}`
      const advice =
        e.stop_price && e.tp_price
          ? ` · 推荐止损 ${fmtPrice(e.stop_price, e.tick_size)} / 止盈 ${fmtPrice(e.tp_price, e.tick_size)}（盈亏比 ${e.rr}）`
          : ''
      const desc = `现价 ${e.close} · 关键位 ${e.level_price}${advice} · ${e.time}`
      notification.open({
        key: `futures-watch-${e.seq}`,
        message: title,
        description: desc,
        type: up ? 'success' : 'warning',
        placement: 'topRight',
        duration: 0,
      })
      try {
        if (typeof Notification !== 'undefined' && Notification.permission === 'granted') {
          new Notification(title, { body: desc })
        }
      } catch {
        /* 浏览器原生通知不可用就只用站内弹窗 */
      }
    },
    [notification],
  )

  // 轮询状态 + 增量提醒（3s）；首次只补历史，不补弹窗
  useEffect(() => {
    let cancelled = false
    async function pull() {
      try {
        const [status, events] = await Promise.all([
          fetchFuturesWatchStatus(),
          fetchFuturesWatchEvents(cursorRef.current),
        ])
        if (cancelled) return
        setWatch(status)
        runningRef.current = status.running
        if (events.length) {
          const last = events[events.length - 1]
          if (last) cursorRef.current = Math.max(cursorRef.current, last.seq)
          if (!firstPullRef.current) {
            for (const e of events) {
              if (e.fresh) notifyBreakout(e)
            }
          }
        }
        // 每轮都按「提醒时效」清理一次（不只是有新提醒时），过期的自动消失
        const ttlMs = (status.alert_ttl_sec ?? DEFAULT_ALERT_TTL_SEC) * 1000
        setAlerts((prev) => {
          const merged = events.length ? [...events.slice().reverse(), ...prev] : prev
          const live = pruneAlerts(merged, ttlMs)
          if (events.length === 0 && live.length === merged.length) return prev
          return live.slice(0, 200)
        })
        firstPullRef.current = false
        if (!blacklistLoadedRef.current) {
          blacklistLoadedRef.current = true
          void reloadBlacklist() // 首轮顺带把黑名单拉下来
        }
      } catch {
        /* 轮询失败静默重试 */
      }
    }
    void pull()
    const timer = window.setInterval(() => void pull(), 3000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [notifyBreakout, reloadBlacklist])

  // 监控运行中改「级别 / ORB / Donchian / ATR / 量能 / 盈亏比 / 间隔 / 品种」→ 自动热更新（防抖 500ms）
  useEffect(() => {
    if (!runningRef.current) return
    const timer = window.setTimeout(() => {
      void updateFuturesWatch(watchConfig)
        .then((status) => {
          setWatch(status)
          message.success('监控配置已更新')
        })
        .catch((e) => message.error(e instanceof Error ? e.message : '监控配置更新失败'))
    }, 500)
    return () => window.clearTimeout(timer)
  }, [watchConfig, message])

  async function toggleWatch(next: boolean) {
    setWatchBusy(true)
    try {
      if (next) {
        if (typeof Notification !== 'undefined' && Notification.permission === 'default') {
          void Notification.requestPermission()
        }
        const status = await startFuturesWatch(watchConfig)
        setWatch(status)
        runningRef.current = true
        message.success(
          `已开始监控 ${status.varieties} 个品种，每 ${status.config.interval ?? watchInterval} 秒扫一轮`,
        )
      } else {
        const status = await stopFuturesWatch()
        setWatch(status)
        runningRef.current = false
        message.info('已停止监控')
      }
    } catch (e) {
      message.error(e instanceof Error ? e.message : '监控操作失败')
    } finally {
      setWatchBusy(false)
    }
  }

  return (
    <div className="page-wrap">
      <Typography.Title level={4} style={{ marginBottom: 16 }}>
        监控突破
      </Typography.Title>

      <Card size="small" style={{ marginBottom: 16 }}>
        <Form layout="inline">
          <Form.Item label="级别">
            <Select style={{ width: 100 }} value={period} onChange={setPeriod} options={LEVEL_OPTIONS} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="ORB分钟" hint={TIPS.orb} />}>
            <InputNumber min={5} value={orb} onChange={(v) => setOrb(Number(v ?? 30))} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="Donchian" hint={TIPS.donchian} />}>
            <InputNumber min={2} value={donchian} onChange={(v) => setDonchian(Number(v ?? 20))} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="ATR周期" hint={TIPS.atr_period} />}>
            <InputNumber min={2} value={atrPeriod} onChange={(v) => setAtrPeriod(Number(v ?? 14))} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="ATR缓冲" hint={TIPS.atr_k} />}>
            <InputNumber min={0.05} step={0.05} value={atrK} onChange={(v) => setAtrK(Number(v ?? 0.25))} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="量能倍数" hint={TIPS.vol_ratio} />}>
            <InputNumber min={0.5} step={0.1} value={volRatio} onChange={(v) => setVolRatio(Number(v ?? 1.5))} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="止损ATR" hint={TIPS.stop_atr} />}>
            <InputNumber min={0.1} max={5} step={0.25} value={stopATR} onChange={(v) => setStopATR(Number(v ?? 1))} />
          </Form.Item>
          <Form.Item label={<ParamLabel text="盈亏比" hint={TIPS.rr} />}>
            <InputNumber min={0.1} step={0.1} value={rr} onChange={(v) => setRr(Number(v ?? 1.5))} />
          </Form.Item>
          <Form.Item>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              提醒里的推荐止损（止损ATR × ATR）/ 推荐止盈（止损距离 × 盈亏比）用这些参数；运行中修改会自动生效
            </Typography.Text>
          </Form.Item>
        </Form>
      </Card>

      <Card
        size="small"
        style={{ marginBottom: 16 }}
        title={
          <Space size={8}>
            市场监控
            <Tag color={watchRunning ? 'green' : 'default'}>{watchRunning ? '运行中' : '未开启'}</Tag>
          </Space>
        }
        extra={
          <Space>
            <Switch
              checked={watchRunning}
              loading={watchBusy}
              checkedChildren="监控中"
              unCheckedChildren="已停止"
              onChange={(v) => void toggleWatch(v)}
            />
            <Button size="small" onClick={() => setBlacklistOpen(true)}>
              黑名单{blacklist.length ? `(${blacklist.length})` : ''}
            </Button>
            <Button
              size="small"
              disabled={!alerts.length}
              onClick={() => {
                setAlerts([])
                cursorRef.current = watch?.latest_seq ?? cursorRef.current
              }}
            >
              清空提醒
            </Button>
          </Space>
        }
      >
        <Space wrap size={8} style={{ marginBottom: 8 }}>
          <span>间隔(秒)</span>
          <InputNumber
            min={5}
            max={600}
            value={watchInterval}
            onChange={(v) => setWatchInterval(Number(v ?? 30))}
          />
          <Select
            mode="multiple"
            allowClear
            showSearch
            optionFilterProp="label"
            style={{ minWidth: 260 }}
            placeholder="监控品种：留空 = 全市场"
            value={watchPrefixes}
            onChange={setWatchPrefixes}
            options={varietyOpts}
          />
          <Tooltip title="服务端推送到飞书群（需配 FEISHU_WEBHOOK_URL）；关掉浏览器也能收到">
            <span className="param-label">
              飞书推送
              <Switch size="small" checked={alertFeishu} onChange={setAlertFeishu} />
            </span>
          </Tooltip>
          <Tooltip title="服务端所在机器弹系统通知（macOS 通知中心 / Linux notify-send），带提示音；不依赖浏览器">
            <span className="param-label">
              桌面通知
              <Switch size="small" checked={alertDesktop} onChange={setAlertDesktop} />
            </span>
          </Tooltip>
          <Button size="small" loading={testingAlert} onClick={() => void testAlert()}>
            测试提醒
          </Button>
        </Space>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 8 }}>
          覆盖 {watch?.varieties ?? 0} 个品种 · 第 {watch?.ticks ?? 0} 轮 · 上一轮扫描 {watch?.scanned ?? 0} 个（失败{' '}
          {watch?.failures ?? 0}）· 耗时 {watch?.last_ms ?? 0}ms · 提醒 {watch?.events ?? 0} 条
          {watch?.source ? ` · 数据源 ${watch.source}` : ''}
          {coolingSources ? `（${coolingSources}）` : ''}
          {watch && watch.backoff > 1 ? ` · 限流退避 ×${watch.backoff}` : ''}
          {watch?.last_tick ? ` · 最后扫描 ${watch.last_tick}` : ''}
          {watch?.alert_note ? ` · 提醒：${watch.alert_note}` : ''}
          {watch?.last_error ? ` · 最近错误：${watch.last_error}` : ''}
        </Typography.Paragraph>
        <Table
          size="small"
          rowKey={(r) => String(r.seq)}
          columns={alertColumns}
          dataSource={alerts}
          pagination={{ pageSize: 10, showSizeChanger: false }}
          scroll={{ x: 1500 }}
          onRow={(r) => ({ onClick: () => openAlert(r), style: { cursor: 'pointer' } })}
          locale={{ emptyText: watchRunning ? '监控中，暂未出现突破' : '未开启监控' }}
        />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          点任意一行 → 跳到「期货回测」页看该月份合约对应级别的价格图；「加入黑名单」后该合约/品种不再进提醒列表；
          提醒有时效：超过 {Math.round(alertTTLSec / 60)} 分钟的突破自动从列表清除
        </Typography.Text>
      </Card>

      <Modal
        open={!!blTarget}
        title="加入监控黑名单"
        okText="加入黑名单"
        cancelText="取消"
        confirmLoading={blBusy}
        onOk={() => void submitBlacklist()}
        onCancel={() => setBlTarget(undefined)}
        destroyOnHidden
      >
        {blTarget ? (
          <>
            <Typography.Paragraph type="secondary">
              {blTarget.name} {blTarget.prefix}：加入后<b>不再出现在突破提醒列表</b>（单品种扫描、回测不受影响）
            </Typography.Paragraph>
            <Radio.Group value={blScope} onChange={(e) => setBlScope(e.target.value as FuturesBlacklistScope)}>
              <Space direction="vertical" size={8}>
                <Radio value="contract" disabled={!blTarget.contract}>
                  只屏蔽该合约{' '}
                  <Tag color={blTarget.contract ? 'blue' : 'default'}>
                    {blTarget.contract || '未解析出月份，稍后可重试'}
                  </Tag>
                </Radio>
                <Radio value="variety">
                  屏蔽整个品种 <Tag color="orange">{blTarget.prefix}</Tag>
                  <Typography.Text type="secondary">（所有月份合约都不提醒）</Typography.Text>
                </Radio>
              </Space>
            </Radio.Group>
            <Input
              style={{ marginTop: 12 }}
              placeholder="备注（可选，例如：日内波动太大）"
              value={blNote}
              onChange={(e) => setBlNote(e.target.value)}
            />
          </>
        ) : null}
      </Modal>

      <Modal
        open={blacklistOpen}
        title={`监控黑名单（${blacklist.length}）`}
        footer={null}
        onCancel={() => setBlacklistOpen(false)}
      >
        <Table
          size="small"
          rowKey={(r) => `${r.scope}:${r.value}`}
          dataSource={blacklist}
          pagination={false}
          locale={{ emptyText: '黑名单为空' }}
          columns={[
            {
              title: '类型',
              dataIndex: 'scope',
              width: 90,
              render: (v: FuturesBlacklistScope) =>
                v === 'variety' ? <Tag color="orange">品种</Tag> : <Tag color="blue">合约</Tag>,
            },
            { title: '值', dataIndex: 'value', width: 120 },
            { title: '备注', dataIndex: 'note' },
            {
              title: '操作',
              key: 'ops',
              width: 80,
              render: (_, row) => (
                <Button size="small" danger type="link" onClick={() => void removeBlacklist(row)}>
                  移除
                </Button>
              ),
            },
          ]}
        />
      </Modal>
    </div>
  )
}
