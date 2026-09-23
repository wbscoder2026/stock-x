// 期货「回测」页与「监控」页共用：参数提示、图表、格式化工具、下拉选项。
/* eslint-disable react/only-export-components -- 故意做成共享模块：这里既有图表组件，也有提示文案与格式化工具 */
import { useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { Checkbox, Space, Tag, Tooltip, Typography } from 'antd'
import {
  CandlestickSeries,
  ColorType,
  HistogramSeries,
  LineSeries,
  createChart,
  createSeriesMarkers,
  type IChartApi,
  type IPriceLine,
  type ISeriesApi,
  type ISeriesMarkersPluginApi,
  type SeriesMarker,
  type Time,
  type UTCTimestamp,
} from 'lightweight-charts'
import type { FuturesContract, FuturesEvent, FuturesKlineBar, FuturesLevel, FuturesOutcome, FuturesVariety } from '../types'

// ---------------------------------------------------------------- 参数提示（鼠标移到 ? 上）

export const TIPS: Record<string, ReactNode> = {
  orb: (
    <>
      <b>开盘区间突破窗口（默认 30 = 前 6 根 5 分钟）</b>
      <br />
      取当日 09:00 起的前 N/5 根 5 分钟 K 线的最高/最低，作为 ORB 高/低（只在 09:00–11:00 之间取）。
      <br />
      收盘 &gt; ORB高 + ATR缓冲 → 向上突破；收盘 &lt; ORB低 − 缓冲 → 向下跌破。
      <br />
      只影响「级别 = 5分钟」的事件识别（15/30/60 只用枢轴位 + Donchian）。值越大 → 区间越宽、信号越少越晚。
    </>
  ),
  donchian: (
    <>
      <b>唐奇安通道根数（默认 20）</b>
      <br />
      上轨 = 最近 N 根的最高价，下轨 = 最近 N 根的最低价（不含当前根，前移一根）。
      <br />
      收盘突破上轨 + ATR缓冲 → 向上突破；跌破下轨 − 缓冲 → 向下跌破。
      <br />
      数值越小越灵敏（假突破多），越大越滞后（趋势确认更强）。
    </>
  ),
  atr_period: (
    <>
      <b>ATR 的均值周期（默认 14）</b>
      <br />
      真实波幅 TR = max(当根高−低, |高−昨收|, |低−昨收|)，再取最近 N 根的均值 → ATR，表示平均波动幅度。
      <br />
      它只决定「ATR缓冲」的尺度，本身不产生信号。
    </>
  ),
  atr_k: (
    <>
      <b>突破缓冲系数（默认 0.25）</b>
      <br />
      有效突破要求收盘价越过关键位 <b>ATR × 该系数</b>（不足 1 个价格单位时按 1 算）。
      <br />
      用来过滤贴着关键位的假突破：调大 → 信号更少更稳；调小 → 更早更灵敏。
    </>
  ),
  vol_ratio: (
    <>
      <b>突破放量倍数（默认 1.5）</b>
      <br />
      突破那根 K 线的成交量必须 ≥ 最近 20 根均量（至少 5 根有效）的该倍数，缩量突破不算数。
      <br />
      用来过滤无量假突破：调大 → 只保留明显放量的突破。
    </>
  ),
  hold_bars: (
    <>
      <b>回测最多持有根数（默认 6）</b>
      <br />
      回测入场后最多持有 N 根同级别 K 线：<b>止盈/止损先触发就按触发价出场</b>，两个都没触发才在第 N 根收盘平仓。
      <br />
      「正确」= 出场收益方向与突破方向一致（收益已按方向折算：向上突破且收益 &gt; 0，或向下跌破且收益 &lt; 0）。
      <br />
      只影响回测统计，不影响扫描出的关键位。
    </>
  ),
  exit_rule: (
    <>
      <b>回测出场规则（1×ATR 止损 + 盈亏比止盈，逐根 walk-forward）</b>
      <br />
      入场 = 突破那根的收盘价；止损 = 入场 ∓ 止损ATR×ATR；止盈 = 入场 ± 止损距离×盈亏比（都按品种报价单位对齐）。
      <br />
      从<b>下一根</b>开始逐根检查，最多持有「最多持有根数」根。两条保守约定：
      同根既破止损又触止盈 → 按<b>止损</b>算；跳空开盘已越过止损 → 按<b>开盘价</b>成交。
      <br />
      收益已按突破方向折算（做空跌了才是正的），<b>未计手续费与滑点</b>。
    </>
  ),
  r_multiple: (
    <>
      <b>R 倍数（以每笔风险为 1 个单位）</b>
      <br />
      1R = 入场到止损的距离（止损ATR×ATR）：止盈单约 +盈亏比 R，止损单约 −1R，持有到期介于两者之间。
      <br />
      统计区的<b>期望 R</b> 是每笔 R 的平均值：<b>&gt; 0 才说明这套规则长期有优势</b>，比单看胜率更能说明问题。
      <br />
      例：10 笔里 4 笔止盈（各 +1.5R）、5 笔止损（各 −1R）、1 笔到期 +0.5R → 期望 R = (4×1.5 − 5 + 0.5) ÷ 10 = <b>0.15R</b>；
      若每笔风险 1000 元，就是平均每笔赚 150 元（未计手续费）。
      <br />
      它比「平均收益」更好比：不同品种/周期/止损宽度都被换算到「每单位风险赚多少」这把同一把尺子上。
    </>
  ),
  rr: (
    <>
      <b>盈亏比（默认 1.5）</b>
      <br />
      决定推荐止盈距离：止盈距离 = <b>止损距离 × 盈亏比</b>（止损距离 = 止损ATR × ATR）。
      <br />
      向上突破 → 止损 = 现价 − 止损ATR×ATR，止盈 = 现价 + 止损距离×盈亏比；向下跌破方向相反。
      <br />
      调大 → 止盈更远、命中率更低但单笔赚得多；调小 → 更快落袋。
      <br />
      <b>提醒的推荐价与「回测」里都用它</b>：回测中改这个值，止盈价与统计会立刻跟着变。不改变突破识别本身。
    </>
  ),
  stop_mode: (
    <>
      <b>止损方式（两套对照策略）</b>
      <br />
      <b>ATR 倍数</b>（默认）：止损 = 入场 ∓ 止损ATR × ATR —— 跟随波动率，波动大的品种自动给更宽的止损。
      <br />
      <b>前一根低/高 + 点数</b>：做多止损 = <b>信号那根 K 线的前一根最低价 − 点数</b>；做空 = 前一根最高价 + 点数。
      这是「结构止损」：破掉上一根的低点才算错，逻辑更贴近形态，止损点数随行情结构变化而不是随 ATR。
      <br />
      两者都用同一个盈亏比算止盈，所以可以直接扫描对照（扫描里「止损方式」是一根轴，一次跑出两种）。
      <br />
      前一根数据缺失时（例如当日第一根就突破）会自动退回 ATR 方式，不会把这一笔丢掉。
    </>
  ),
  stop_points: (
    <>
      <b>前低/前高止损的缓冲点数（默认 1）</b>
      <br />
      1 点 = <b>1 个价格单位</b>（不是 1 个最小变动价位）：JM 一跳 0.5 → 1 点 = 2 跳；RB 一跳 1 → 1 点 = 1 跳；
      AU 一跳 0.02 → 1 点 = 50 跳。
      <br />
      留缓冲是为了避开「刚好插一下就回去」：把止损放在前低之下 1~2 点，比放在前低上更不容易被扫。
      <br />
      结果仍会按品种最小变动价位对齐，并保底离入场价 1 个跳。
    </>
  ),
  stop_atr: (
    <>
      <b>止损距离 = 该倍数 × ATR（默认 1，仅「ATR 倍数」止损方式用）</b>
      <br />
      ATR 是平均波动幅度：<b>15 分钟级别通常占价格 0.1~0.4%，60 分钟约 0.3~0.9%</b>（实测 JM 15m 0.34% / 60m 0.90%，
      螺纹 15m 0.12% / 60m 0.29%）。所以大周期的止损点数天然是 2~3 倍，但换算成百分比仍在 1% 以内。
      <br />
      调小 → 止损更紧、单笔风险小，但更容易被正常波动扫掉（实测多数品种期望 R 会变差：豆粕 60m 从 0.15 掉到 −0.07）；
      调大 → 扛得住噪音（止损占比从 46% 降到 23%），但单笔风险变大。
      <br />
      <b>控制风险的正确顺序</b>：先定止损距离，再按「可承受亏损 ÷ (止损点数 × 合约乘数)」反推手数 —— 而不是压缩止损。
    </>
  ),
  stop_tp: (
    <>
      <b>推荐止损 / 止盈（仅供参考，不是自动交易指令）</b>
      <br />
      止损 = 突破那根的收盘价 ∓ 止损ATR×ATR（ATR 由「ATR周期」算出）；止盈 = 止损距离 × 盈亏比（监控页表单里的值，默认 1.5）。
      <br />
      以「突破 K 线收盘」为入场参考价：向上突破现价买入，向下突破现价卖出。
      <br />
      <b>已按品种最小变动价位取整</b>（焦煤 0.5、螺纹钢 1、铜 10、黄金 0.02、原油 0.1…），显示的小数位也跟着报价单位走，
      所以给出的都是<b>真挂得出去的价格</b>。
      <br />
      ATR 比一个跳还小时，止损/止盈至少离入场价 1 个跳，避免取整把止损压到入场价上。
      <br />
      显示「-」= 该根 K 线 ATR 数据不足。实际下单请按自己的仓位与风险承受度决定手数。
    </>
  ),
  sweep: (
    <>
      <b>参数扫描（网格搜索）</b>
      <br />
      把每个参数的候选值都填上（下拉多选或直接输入），程序会跑<b>所有组合</b>，按你选的目标排序。
      <br />
      同一级别的所有组合共用一份 K 线（先取一次）；<b>级别本身也能做轴</b>，但每个级别要单独取数，级别越多越慢。
      <br />
      <b>样本数不足的组合会被标「样本不足」并排在后面</b>：3 笔 100% 胜率不算本事，别被它骗了。
      <br />
      组合数上限 <b>10000000</b>（1000 万，超了会报错让你缩小范围）。组合多时一次请求要跑几十秒到几小时 ——
      开始后有<b>进度条</b>（已跑 / 总数 + 预计剩余）；<b>切到别的页面再回来，进度和结果都还在</b>，
      表单和排序也不会丢；但<b>别刷新页面</b>（刷新 = 取消请求）。并发数可以调（<b>扫描途中改也立刻生效</b>），默认 64。
    </>
  ),
  workers: (
    <>
      <b>并发数（默认 64，上限 64；填 0 = 自动用满本机核数）</b>
      <br />
      <b>扫描途中也能改，改完立刻生效</b>：服务端跑的是 ants 协程池，改并发就是给它 Tune ——
      正在跑的那几个任务不打断，之后提交的任务按新并发上（调小则等当前任务跑完自然收敛）。
      <br />
      扫描是纯计算、组合之间互相独立，所以能并行。实测（1000 组 / 1000 根 K 线）：
      1 并发 31s、2 并发 19s、4 并发 13s、8 并发 9s、11 并发 7s —— <b>4~8 是性价比拐点</b>，再往上收益递减（抢内存带宽）。
      <br />
      并发只影响速度，<b>不影响结果</b>（排序是确定的，已验证 1 并发与多并发结果完全一致）。
      <br />
      并发越高 CPU 越吃满；用老机器或同时跑监控时，调低一点更稳。
    </>
  ),
  sweep_period: (
    <>
      <b>级别（K 线周期）也参与组合</b>
      <br />
      选多个级别 → 每个级别各自取数、各自回测，最后放在一起按目标排序，能直接看出"这套规则在哪个周期更灵"。
      <br />
      <b>注意样本覆盖不同</b>：上游分钟线只保留最近约 1000 根，所以 15 分钟 ≈ 1 个月、60 分钟 ≈ 5 个月，
      样本数差好几倍 —— 别拿"15 分钟 200 笔"和"60 分钟 1200 笔"直接比高低，优先看期望 R。
      <br />
      某个级别取不到数据（上游没这段历史）不会拖垮整次扫描，会被跳过并在结果里标注。
    </>
  ),
  alert_ttl: (
    <>
      <b>提醒保留时长（分钟，默认 30）</b>
      <br />
      超过这个时长的突破提醒会<b>自动从列表清除，也不再外发</b>（突破讲究时效，卡了很久的行情再弹出来是噪音）。
      <br />
      按<b>K 线时间</b>算「现在 − 时长」，所以不会因为刷新页面/重启服务把老提醒捡回来。
      <br />
      范围 1~1440 分钟；被清掉的事件仍留在去重表里，同一根 K 线不会被重复提醒。
    </>
  ),
  range: (
    <>
      <b>回测时间范围（默认不限）</b>
      <br />
      按<b>信号时间</b>筛选：只有落在范围内的突破才计入。可只选日期（当天整天算在内）也可以精确到分钟。
      <br />
      <b>出场可以延续到结束时间之后</b>（持有期自然走完）—— 否则会把当日尾盘的信号系统性砍掉、统计有偏。
      <br />
      指标（ATR / Donchian / 关键位）仍然用范围之前的全部历史数据计算，所以缩小区间不会让指标失准（也不会引入未来函数）。
      <br />
      数据覆盖有限：上游分钟线只留最近约 1000 根，选太早的区间会没有样本。
    </>
  ),
  overnight: (
    <>
      <b>是否允许隔夜持有（默认允许）</b>
      <br />
      关掉 = <b>日内策略</b>：当日「日盘」收盘前必须平掉（日盘口径 hour &lt; 16，即 09:00–15:00），出场原因标成「日内收盘」。
      <br />
      <b>夜盘 21:00 之后属于次日交易时段</b>，所以日内模式同样不持有夜盘 —— 不会出现「下午进场、晚上还拿着」。
      <br />
      入场就在日盘收盘后（或夜盘）的信号，日内模式<b>吃不到</b>：这类信号会被跳过并单独计数（`skipped_eod`），不计入胜率/收益率，
      免得用无法成交的信号美化统计。
      <br />
      作用：规避隔夜跳空风险（隔夜风险的主要来源），代价是拿不到趋势的隔夜段。扫描里可以把它当轴，直接比「日内 vs 隔夜」。
    </>
  ),
  profit_factor: (
    <>
      <b>盈利因子 Profit Factor = 总盈利 ÷ 总亏损</b>
      <br />
      只看金额、不看笔数：1.0 = 打平；1.6 = 赚到的钱是亏掉的钱的 1.6 倍；&lt; 1 = 亏的比赚的多。
      <br />
      例：10 笔里 4 笔胜共 +6%、6 笔亏共 −4% → PF = 6 ÷ 4 = 1.5。
      <br />
      和胜率的关系：PF = (胜率 × 平均盈利) ÷ ((1 − 胜率) × 平均亏损)，所以<b>胜率 40% 但盈亏比 3 也能 PF &gt; 1</b>。
      <br />
      局限：<b>对单笔大赚/大亏很敏感</b>，样本少时波动大 —— 配合「样本不足」标记一起看。
      显示「-」= <b>这段时间没有亏损单</b>（PF 理论无穷大），不是没有数据。
    </>
  ),
  objective: (
    <>
      <b>排序目标</b>
      <br />
      胜率 = 出场收益为正的比例；平均收益 = 每笔平均收益率；期望 R = 每笔平均 R 倍数（风险归一化，最值得看）；
      盈利因子 = 总盈利 ÷ 总亏损。
      <br />
      <b>胜率高的组合常常是「小止盈」</b>（赚一点就跑），期望 R 与平均收益更接近能不能真的赚钱。
    </>
  ),
  min_trades: (
    <>
      <b>最少样本数</b>
      <br />
      低于这个样本数的组合标记「样本不足」，并且不会排在可靠组合前面。
      <br />
      样本越少，指标越容易被运气主导（过拟合）；一般至少 30，短周期可以放宽到 20。
    </>
  ),
}

export function ParamLabel({ text, hint }: { text: string; hint: ReactNode }) {
  return (
    <span className="param-label" onClick={(e) => e.stopPropagation()}>
      {text}
      <Tooltip title={<div className="param-tip">{hint}</div>} placement="top">
        <span className="param-help" aria-label={`${text}是什么意思`}>
          ?
        </span>
      </Tooltip>
    </span>
  )
}

// ---------------------------------------------------------------- 常用选项

// 止损方式（服务端 stop_mode 的取值）
export const STOP_MODE_ATR = 'atr'
export const STOP_MODE_PREV_LOW = 'prev_low'
export const STOP_MODE_OPTIONS = [
  { value: STOP_MODE_ATR, label: 'ATR 倍数' },
  { value: STOP_MODE_PREV_LOW, label: '前一根低/高 + 点数' },
]
export const stopModeLabel = (mode?: string, points?: number) =>
  mode === STOP_MODE_PREV_LOW ? `前低${points && points !== 1 ? `±${points}` : '−1'}点` : 'ATR'

export const LEVEL_OPTIONS = [
  { value: '5', label: '5分钟' },
  { value: '15', label: '15分钟' },
  { value: '30', label: '30分钟' },
  { value: '60', label: '60分钟' },
]

export const DEFAULT_PARAMS = {
  period: '5',
  orb: 30,
  donchian: 20,
  atrPeriod: 14,
  atrK: 0.25,
  volRatio: 1.5,
  holdBars: 6,
  stopATR: 1,
  rr: 1.5,
}

export function varietyOptions(varieties: FuturesVariety[]) {
  const m = new Map<string, { value: string; label: string }[]>()
  for (const v of varieties) {
    const arr = m.get(v.exchange) ?? []
    arr.push({ value: v.prefix, label: `${v.name} ${v.prefix}` })
    m.set(v.exchange, arr)
  }
  return [...m.entries()].map(([label, options]) => ({ label, options }))
}

export function contractOptions(contracts: FuturesContract[]) {
  return contracts.map((c) => ({
    value: c.symbol,
    label: c.label || (c.kind === 'main' ? '主连' : c.symbol),
  }))
}

// ---------------------------------------------------------------- 格式化

// 价格小数位跟着「最小变动价位」走：RB(1) → 0 位、JM(0.5) → 1 位、AU(0.02) → 2 位、TS(0.002) → 3 位。
// 服务端已按报价单位对齐，这里只负责按同样精度显示（免得 623.44 被显示成 623.4）。
export function tickDecimals(tick?: number): number {
  if (!tick || tick <= 0) return 1
  const s = String(tick)
  const dot = s.indexOf('.')
  return dot < 0 ? 0 : s.length - dot - 1
}

export function fmtPrice(v: number, tick?: number): string {
  return v.toFixed(tickDecimals(tick))
}

export const pct = (v?: number) => `${((v ?? 0) * 100).toFixed(2)}%`

export const exitTag = (reason: string) => {
  if (reason === '止盈') return <Tag color="green">止盈</Tag>
  if (reason === '止损') return <Tag color="red">止损</Tag>
  if (reason === '日内收盘') return <Tag color="orange">日内收盘</Tag>
  return <Tag>持有到期</Tag>
}

// ---------------------------------------------------------------- K 线图

function toBarTime(s: string): UTCTimestamp {
  const m = s.match(/^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})/)
  if (!m) return 0 as UTCTimestamp
  return Math.floor(Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4] - 8, +m[5]) / 1000) as UTCTimestamp
}

function uniqueBars(bars: FuturesKlineBar[]) {
  const seen = new Set<number>()
  const out: FuturesKlineBar[] = []
  for (const b of bars) {
    const t = toBarTime(b.time)
    if (!t || seen.has(t)) continue
    seen.add(t)
    out.push(b)
  }
  return out
}

// 均线周期与配色（打开「均线」时画出）
const MA_CONFIG = [
  { period: 5, color: '#ef8c1b', label: 'MA5' },
  { period: 10, color: '#8e44ad', label: 'MA10' },
  { period: 20, color: '#2f7fc1', label: 'MA20' },
]

// sma 简单移动均线：前 n-1 根数据不足，不画点
function sma(bars: FuturesKlineBar[], n: number): { time: UTCTimestamp; value: number }[] {
  const out: { time: UTCTimestamp; value: number }[] = []
  let sum = 0
  for (let i = 0; i < bars.length; i++) {
    sum += bars[i].close
    if (i >= n) sum -= bars[i - n].close
    if (i >= n - 1) out.push({ time: toBarTime(bars[i].time), value: sum / n })
  }
  return out
}

// 出场原因 → 标记配色（止盈绿 / 止损红 / 未触发灰）
function exitColor(reason?: string) {
  if (reason === '止盈') return '#26a69a'
  if (reason === '止损') return '#ef5350'
  return '#9e9e9e'
}

// FuturesChart K 线图 + 可开关的图层。
// mode='trade'：买点/卖点标记（出场带 R 倍数），聚焦某一笔时画入场/止损/止盈三条价线。
// mode='signal'：只标突破信号（按方向箭头 + 关键位名）。
export function FuturesChart({
  bars,
  events,
  levels,
  mode = 'signal',
  focusIndex,
  onFocus,
}: {
  bars: FuturesKlineBar[]
  events: FuturesEvent[]
  levels?: FuturesLevel[]
  mode?: 'signal' | 'trade'
  focusIndex?: number
  onFocus?: (index: number) => void
}) {
  const wrapRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<IChartApi | null>(null)
  const candleRef = useRef<ISeriesApi<'Candlestick'> | null>(null)
  const volRef = useRef<ISeriesApi<'Histogram'> | null>(null)
  const markersRef = useRef<ISeriesMarkersPluginApi<Time> | null>(null)
  const linesRef = useRef<IPriceLine[]>([])
  const maRefs = useRef<(ISeriesApi<'Line'> | null)[]>([])

  const [showEntry, setShowEntry] = useState(true)
  const [showExit, setShowExit] = useState(true)
  const [showStopTP, setShowStopTP] = useState(true)
  const [showMA, setShowMA] = useState(true)
  const [showVol, setShowVol] = useState(true)
  const [showLevels, setShowLevels] = useState(true)

  // 点击回调只在挂载时订阅一次，所以用 ref 拿最新的 events / onFocus
  const eventsRef = useRef(events)
  const onFocusRef = useRef(onFocus)
  useEffect(() => {
    eventsRef.current = events
    onFocusRef.current = onFocus
  }, [events, onFocus])

  useEffect(() => {
    const el = wrapRef.current
    if (!el) return
    const chart = createChart(el, {
      autoSize: true,
      layout: { background: { type: ColorType.Solid, color: '#fff' }, textColor: '#333' },
      grid: { vertLines: { color: '#eee' }, horzLines: { color: '#eee' } },
      rightPriceScale: { borderColor: '#ddd' },
      timeScale: { borderColor: '#ddd', timeVisible: true, secondsVisible: false },
    })
    const candle = chart.addSeries(CandlestickSeries, {
      upColor: '#ef5350',
      downColor: '#26a69a',
      borderVisible: false,
      wickUpColor: '#ef5350',
      wickDownColor: '#26a69a',
    })
    const vol = chart.addSeries(HistogramSeries, {
      priceFormat: { type: 'volume' },
      priceScaleId: 'vol',
    })
    chart.priceScale('vol').applyOptions({ scaleMargins: { top: 0.8, bottom: 0 } })
    maRefs.current = MA_CONFIG.map((m) =>
      chart.addSeries(LineSeries, {
        color: m.color,
        lineWidth: 1,
        priceLineVisible: false,
        lastValueVisible: false,
        crosshairMarkerVisible: false,
      }),
    )
    chartRef.current = chart
    candleRef.current = candle
    volRef.current = vol
    markersRef.current = createSeriesMarkers(candle, [])

    // 点图上任意位置 → 聚焦最近的一笔（入场或出场时间 1 小时内）
    chart.subscribeClick((param) => {
      if (param.time === undefined || !onFocusRef.current) return
      const clicked = Number(param.time)
      let best = -1
      let bestDiff = Number.POSITIVE_INFINITY
      eventsRef.current.forEach((e, i) => {
        const t = e as FuturesOutcome
        const times = [Number(toBarTime(e.time)), t.exit_time ? Number(toBarTime(t.exit_time)) : Number.NaN]
        for (const c of times) {
          if (Number.isNaN(c)) continue
          const d = Math.abs(c - clicked)
          if (d < bestDiff) {
            bestDiff = d
            best = i
          }
        }
      })
      if (best >= 0 && bestDiff <= 3600) onFocusRef.current(best)
    })

    return () => {
      linesRef.current = []
      maRefs.current = []
      markersRef.current = null
      chart.remove()
      chartRef.current = null
      candleRef.current = null
      volRef.current = null
    }
  }, [])

  // 蜡烛 / 成交量 / 均线
  useEffect(() => {
    const candle = candleRef.current
    const vol = volRef.current
    if (!candle || !vol) return
    const list = uniqueBars(bars)
    candle.setData(
      list.map((b) => ({
        time: toBarTime(b.time),
        open: b.open,
        high: b.high,
        low: b.low,
        close: b.close,
      })),
    )
    vol.setData(
      list.map((b) => ({
        time: toBarTime(b.time),
        value: b.volume,
        color: b.close >= b.open ? '#ef535088' : '#26a69a88',
      })),
    )
    MA_CONFIG.forEach((m, i) => {
      const series = maRefs.current[i]
      if (!series) return
      series.applyOptions({ visible: showMA })
      series.setData(showMA ? sma(list, m.period) : [])
    })
    chartRef.current?.timeScale().fitContent()
  }, [bars, showMA])

  useEffect(() => {
    volRef.current?.applyOptions({ visible: showVol })
  }, [showVol])

  // 买点 / 卖点 / 突破信号标记（必须按时间升序，否则 lightweight-charts 会乱序丢标记）
  useEffect(() => {
    const marks: SeriesMarker<Time>[] = []
    if (showEntry) {
      events.forEach((e) => {
        const up = !e.direction.includes('向下')
        marks.push({
          time: toBarTime(e.time),
          position: up ? 'belowBar' : 'aboveBar',
          color: up ? '#ef5350' : '#26a69a',
          shape: up ? 'arrowUp' : 'arrowDown',
          text: mode === 'trade' ? (up ? '买' : '卖') : e.level,
        })
      })
    }
    if (mode === 'trade' && showExit) {
      events.forEach((e) => {
        const t = e as FuturesOutcome
        if (!t.exit_time) return
        const up = !e.direction.includes('向下')
        const r = t.r_multiple ?? 0
        marks.push({
          time: toBarTime(t.exit_time),
          position: up ? 'aboveBar' : 'belowBar',
          color: exitColor(t.exit_reason),
          shape: up ? 'arrowDown' : 'arrowUp',
          text: `${r >= 0 ? '+' : ''}${r.toFixed(2)}R`,
        })
      })
    }
    marks.sort((a, b) => Number(a.time) - Number(b.time))
    markersRef.current?.setMarkers(marks)
  }, [events, showEntry, showExit, mode])

  // 关键位水平线 + 聚焦那一笔的入场/止损/止盈线
  useEffect(() => {
    const candle = candleRef.current
    if (!candle) return
    for (const ln of linesRef.current) candle.removePriceLine(ln)
    linesRef.current = []

    if (showLevels) {
      for (const lv of levels ?? []) {
        linesRef.current.push(
          candle.createPriceLine({
            price: lv.value,
            color: lv.kind === 'R' ? '#ef535099' : '#26a69a99',
            lineWidth: 1,
            lineStyle: 2,
            axisLabelVisible: true,
            title: lv.name,
          }),
        )
      }
    }

    if (mode === 'trade' && showStopTP && focusIndex !== undefined) {
      const t = events[focusIndex] as FuturesOutcome | undefined
      if (t) {
        const push = (price: number, color: string, title: string, style: 0 | 1 | 2 | 3 | 4) => {
          if (!price || price <= 0) return
          linesRef.current.push(
            candle.createPriceLine({ price, color, lineWidth: 2, lineStyle: style, axisLabelVisible: true, title }),
          )
        }
        push(t.close, '#607d8b', t.direction.includes('向下') ? '开空' : '开多', 1)
        push(t.stop_price, '#ef5350', `止损(${stopModeLabel(t.stop_mode, t.stop_points)})`, 2)
        push(t.tp_price, '#26a69a', '止盈', 2)
      }
    }
  }, [levels, showLevels, events, focusIndex, showStopTP, mode])

  const focusTrade = useMemo(
    () =>
      mode === 'trade' && focusIndex !== undefined ? (events[focusIndex] as FuturesOutcome | undefined) : undefined,
    [mode, focusIndex, events],
  )

  if (!bars.length) return null
  return (
    <div>
      <Space size={14} wrap style={{ marginBottom: 6 }}>
        <Checkbox checked={showEntry} onChange={(e) => setShowEntry(e.target.checked)}>
          {mode === 'trade' ? '买点 / 卖点方向' : '突破信号'}
        </Checkbox>
        {mode === 'trade' ? (
          <Checkbox checked={showExit} onChange={(e) => setShowExit(e.target.checked)}>
            出场标记（R 倍数）
          </Checkbox>
        ) : null}
        {mode === 'trade' ? (
          <Tooltip
            title={
              focusIndex === undefined
                ? '先在下方明细点一行（或点图上买点附近），再打开它'
                : '把这一笔的入场价 / 止损价 / 止盈价画成横线'
            }
          >
            <Checkbox
              checked={showStopTP}
              disabled={focusIndex === undefined}
              onChange={(e) => setShowStopTP(e.target.checked)}
            >
              止损止盈线
            </Checkbox>
          </Tooltip>
        ) : null}
        <Checkbox checked={showMA} onChange={(e) => setShowMA(e.target.checked)}>
          均线
        </Checkbox>
        {MA_CONFIG.map((m) => (
          <span key={m.period} style={{ color: m.color, fontSize: 12, fontWeight: 600 }}>
            {m.label}
          </span>
        ))}
        <Checkbox checked={showVol} onChange={(e) => setShowVol(e.target.checked)}>
          成交量
        </Checkbox>
        {levels?.length ? (
          <Checkbox checked={showLevels} onChange={(e) => setShowLevels(e.target.checked)}>
            关键位
          </Checkbox>
        ) : null}
        {focusTrade ? (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            已聚焦：{focusTrade.time} {focusTrade.direction} · {focusTrade.exit_reason}
            {focusTrade.r_multiple
              ? ` ${focusTrade.r_multiple >= 0 ? '+' : ''}${focusTrade.r_multiple.toFixed(2)}R`
              : ''}
            （再点一次取消）
          </Typography.Text>
        ) : mode === 'trade' ? (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            点明细行或图上买点 → 聚焦单笔
          </Typography.Text>
        ) : null}
      </Space>
      <div className="futures-chart" ref={wrapRef} />
    </div>
  )
}
