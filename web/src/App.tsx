import { Layout, Menu, Typography } from 'antd'
import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import PicksPage from './pages/PicksPage'
import KlinePage from './pages/KlinePage'
import HistoryPage from './pages/HistoryPage'
import StrategiesPage from './pages/StrategiesPage'
import JobsPage from './pages/JobsPage'
import BacktestPage from './pages/BacktestPage'
import FuturesBacktestPage from './pages/FuturesBacktestPage'
import FuturesWatchPage from './pages/FuturesWatchPage'
import FuturesLocalPage from './pages/FuturesLocalPage'
import FuturesFavoritesPage from './pages/FuturesFavoritesPage'

const items = [
  { key: '/picks', label: '选股' },
  { key: '/kline', label: 'K线' },
  { key: '/history', label: '历史K线' },
  { key: '/strategies', label: '策略' },
  { key: '/jobs', label: '任务/回填' },
  { key: '/backtest', label: '回测' },
  { key: '/futures/backtest', label: '期货回测' },
  { key: '/futures/favorites', label: '期货收藏' },
  { key: '/futures/watch', label: '期货监控' },
  { key: '/futures/local', label: '本地期货' },
]

export default function App() {
  const loc = useLocation()
  const nav = useNavigate()
  const selected = items.some((i) => i.key === loc.pathname)
    ? loc.pathname
    : loc.pathname.startsWith('/kline')
      ? '/kline'
      : loc.pathname

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Layout.Header style={{ display: 'flex', alignItems: 'center', gap: 24, paddingInline: 20 }}>
        <Typography.Title level={4} style={{ color: '#fff', margin: 0, whiteSpace: 'nowrap' }}>
          选股系统
        </Typography.Title>
        <Menu
          theme="dark"
          mode="horizontal"
          selectedKeys={[selected]}
          items={items}
          onClick={({ key }) => nav(key)}
          style={{ flex: 1, minWidth: 0 }}
        />
      </Layout.Header>
      <Layout.Content>
        <Routes>
          <Route path="/" element={<Navigate to="/picks" replace />} />
          <Route path="/picks" element={<PicksPage />} />
          <Route path="/kline" element={<KlinePage />} />
          <Route path="/history" element={<HistoryPage />} />
          <Route path="/strategies" element={<StrategiesPage />} />
          <Route path="/jobs" element={<JobsPage />} />
          <Route path="/backtest" element={<BacktestPage />} />
          <Route path="/futures" element={<Navigate to="/futures/backtest" replace />} />
          <Route path="/futures/backtest" element={<FuturesBacktestPage />} />
          <Route path="/futures/favorites" element={<FuturesFavoritesPage />} />
          <Route path="/futures/watch" element={<FuturesWatchPage />} />
          <Route path="/futures/local" element={<FuturesLocalPage />} />
        </Routes>
      </Layout.Content>
    </Layout>
  )
}
