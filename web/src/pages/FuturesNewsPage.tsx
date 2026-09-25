import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Alert,
  App,
  Button,
  Card,
  Empty,
  Input,
  Segmented,
  Select,
  Space,
  Tag,
  Typography,
} from 'antd'
import { fetchFuturesNews, refreshFuturesNews } from '../api'
import type { FuturesNewsItem, FuturesNewsReport } from '../types'

const PROVIDER_LABEL: Record<string, string> = {
  sina: '新浪',
  eastmoney: '东财',
}

// 相对时间：新闻讲究「多新」，光显示绝对时间不够直观
function ago(ts: number, now: number) {
  if (!ts) return ''
  const diff = Math.max(0, now - ts)
  const min = Math.floor(diff / 60)
  if (min < 1) return '刚刚'
  if (min < 60) return `${min} 分钟前`
  const hour = Math.floor(min / 60)
  if (hour < 24) return `${hour} 小时前`
  return `${Math.floor(hour / 24)} 天前`
}

export default function FuturesNewsPage() {
  const { message } = App.useApp()
  const [data, setData] = useState<FuturesNewsReport>()
  const [q, setQ] = useState('')
  const [provider, setProvider] = useState<string>('all')
  const [tag, setTag] = useState<string>()
  const [loading, setLoading] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setData(await fetchFuturesNews())
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [message])

  // 新闻 60 秒拉一次：后端有缓存和落库，这里只是读库，很轻
  useEffect(() => {
    void load()
    const timer = window.setInterval(() => void load(), 60000)
    return () => window.clearInterval(timer)
  }, [load])

  // 让「几分钟前」自己走起来
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Math.floor(Date.now() / 1000)), 30000)
    return () => window.clearInterval(timer)
  }, [])

  async function doRefresh() {
    setRefreshing(true)
    try {
      setData(await refreshFuturesNews())
      message.success('已刷新')
    } catch (e) {
      message.error(e instanceof Error ? e.message : '刷新失败')
    } finally {
      setRefreshing(false)
    }
  }

  const items = data?.items ?? []

  const tags = useMemo(() => {
    const set = new Set<string>()
    for (const it of items) for (const t of it.tags ?? []) set.add(t)
    return [...set].sort((a, b) => a.localeCompare(b, 'zh'))
  }, [items])

  const rows = useMemo(() => {
    const key = q.trim().toLowerCase()
    return items.filter((row) => {
      if (provider !== 'all' && row.provider !== provider) return false
      if (tag && !(row.tags ?? []).includes(tag)) return false
      if (!key) return true
      return (
        row.title.toLowerCase().includes(key) ||
        row.summary.toLowerCase().includes(key) ||
        (row.media ?? '').toLowerCase().includes(key)
      )
    })
  }, [items, q, provider, tag])

  const failed = (data?.sources ?? []).filter((s) => !s.ok)

  return (
    <div className="page-wrap">
      <Space style={{ marginBottom: 12 }} wrap>
        <Typography.Title level={4} style={{ margin: 0 }}>
          期货新闻
        </Typography.Title>
        <Input.Search allowClear placeholder="搜索标题/内容/媒体" style={{ width: 220 }} onSearch={setQ} />
        <Segmented
          value={provider}
          onChange={(v) => setProvider(String(v))}
          options={[
            { value: 'all', label: '全部来源' },
            { value: 'eastmoney', label: '东财' },
            { value: 'sina', label: '新浪' },
          ]}
        />
        <Select
          showSearch
          allowClear
          placeholder="按品种筛选"
          style={{ width: 160 }}
          value={tag}
          onChange={setTag}
          options={tags.map((t) => ({ value: t, label: t }))}
        />
        <Button type="primary" loading={refreshing} onClick={() => void doRefresh()}>
          刷新
        </Button>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {data?.updated ? `更新于 ${data.updated}` : '尚未抓取'}
          {data?.interval ? ` · 每 ${data.interval} 自动抓一次` : ''}
        </Typography.Text>
      </Space>

      {failed.length > 0 ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="warning"
          showIcon
          message={
            <span>
              有 {failed.length} 个源没抓到（不影响其他源）：
              {failed.map((s) => `${PROVIDER_LABEL[s.name] ?? s.name} ${s.error ?? ''}`).join('；')}
            </span>
          }
        />
      ) : null}

      <Card size="small" loading={loading && items.length === 0}>
        {rows.length === 0 ? (
          <Empty description={items.length === 0 ? '还没有抓到新闻' : '没有匹配的新闻'} />
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
            {rows.map((it) => (
              <NewsRow key={`${it.provider}-${it.id}`} item={it} now={now} />
            ))}
          </div>
        )}
      </Card>
    </div>
  )
}

function NewsRow({ item, now }: { item: FuturesNewsItem; now: number }) {
  return (
    <div
      style={{
        padding: '10px 0',
        borderBottom: '1px solid rgba(5, 5, 5, 0.06)',
      }}
    >
      <Space size={6} wrap style={{ marginBottom: 2 }}>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {item.published || '—'}
        </Typography.Text>
        {ago(item.ts, now) ? (
          <Tag color="blue" style={{ marginInlineEnd: 0, fontSize: 11 }}>
            {ago(item.ts, now)}
          </Tag>
        ) : null}
        <Tag color={item.provider === 'eastmoney' ? 'orange' : 'red'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
          {PROVIDER_LABEL[item.provider] ?? item.provider}
        </Tag>
        {item.media ? (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {item.media}
          </Typography.Text>
        ) : null}
        {(item.tags ?? []).slice(0, 4).map((t) => (
          <Tag key={t} style={{ marginInlineEnd: 0, fontSize: 11 }}>
            {t}
          </Tag>
        ))}
      </Space>
      <div>
        {item.url ? (
          <Typography.Link href={item.url} target="_blank" rel="noreferrer" style={{ fontWeight: 500 }}>
            {item.title}
          </Typography.Link>
        ) : (
          <Typography.Text style={{ fontWeight: 500 }}>{item.title}</Typography.Text>
        )}
      </div>
      {item.summary ? (
        <Typography.Paragraph type="secondary" style={{ margin: '2px 0 0', fontSize: 12 }}>
          {item.summary}
        </Typography.Paragraph>
      ) : null}
    </div>
  )
}
