import { Layout, Menu, Typography } from 'antd'
import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import PicksPage from './pages/PicksPage'
import KlinePage from './pages/KlinePage'
import StrategiesPage from './pages/StrategiesPage'
import JobsPage from './pages/JobsPage'
import BacktestPage from './pages/BacktestPage'

const items = [
  { key: '/picks', label: '选股' },
  { key: '/kline', label: 'K线' },
  { key: '/strategies', label: '策略' },
  { key: '/jobs', label: '任务/回填' },
  { key: '/backtest', label: '回测' },
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
          <Route path="/strategies" element={<StrategiesPage />} />
          <Route path="/jobs" element={<JobsPage />} />
          <Route path="/backtest" element={<BacktestPage />} />
        </Routes>
      </Layout.Content>
    </Layout>
  )
}
