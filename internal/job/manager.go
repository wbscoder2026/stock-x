package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wbscoder2026/stock-x/internal/config"
	"github.com/wbscoder2026/stock-x/internal/notify"
	"github.com/wbscoder2026/stock-x/internal/store"
	"github.com/wbscoder2026/stock-x/internal/strategy"
	"github.com/wbscoder2026/stock-x/internal/syncer"
)

// ProgressFunc 任务进度：pct 0-100，line 追加到日志。
type ProgressFunc func(pct int, line string)

func toSyncProgress(p ProgressFunc) func(done, total int, msg string) {
	return func(done, total int, msg string) {
		pct := 0
		if total > 0 {
			pct = done * 100 / total
			if pct > 100 {
				pct = 100
			}
		}
		if p != nil {
			p(pct, msg)
		}
	}
}

// Manager 同一时刻只跑一个重任务。
type Manager struct {
	Store *store.Store
	Sync  *syncer.Syncer
	Cfg   config.Config

	runMu    sync.Mutex
	hub      *Hub
	scanArgs sync.Map // jobID -> ScanArg

	ctlMu   sync.Mutex
	cancel  context.CancelFunc
	curID   string
	curType string
	busy    bool
}

// ScanArg 扫描区间；都空则用库内最新交易日。
type ScanArg struct {
	From, To string
}

func New(st *store.Store, sy *syncer.Syncer, cfg config.Config) *Manager {
	return &Manager{Store: st, Sync: sy, Cfg: cfg, hub: newHub()}
}

func (m *Manager) Hub() *Hub { return m.hub }

// SeedConfigs 把 strategy.All() 转成可入库的配置。
func SeedConfigs() []store.StrategyConfig {
	all := strategy.All()
	out := make([]store.StrategyConfig, 0, len(all))
	for _, s := range all {
		raw, _ := json.Marshal(s.DefaultParams())
		desc := s.Name()
		if d, ok := s.(interface{ Description() string }); ok {
			desc = d.Description()
		}
		out = append(out, store.StrategyConfig{
			ID: s.ID(), Name: s.Name(), Description: desc,
			Enabled: true, ParamsJSON: string(raw),
		})
	}
	return out
}

// Submit 立即落库并后台执行，返回初始 Job。
func (m *Manager) Submit(ctx context.Context, typ string, arg ...ScanArg) (store.JobRun, error) {
	typ = strings.TrimSpace(typ)
	switch typ {
	case "backfill", "sync", "scan", "backtest":
	default:
		return store.JobRun{}, fmt.Errorf("未知任务类型 %s", typ)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	m.ctlMu.Lock()
	if m.busy {
		m.ctlMu.Unlock()
		return store.JobRun{}, fmt.Errorf("已有任务在运行，请先暂停")
	}
	m.busy = true
	m.ctlMu.Unlock()

	j := store.JobRun{
		ID: newID(), Type: typ, Status: "pending",
		StartedAt: now,
	}
	if err := m.Store.InsertJob(j); err != nil {
		m.ctlMu.Lock()
		m.busy = false
		m.ctlMu.Unlock()
		return store.JobRun{}, err
	}
	var sa ScanArg
	if len(arg) > 0 {
		sa = arg[0]
	}
	if typ == "scan" {
		m.scanArgs.Store(j.ID, sa)
	}
	go m.run(j.ID, typ)
	return j, nil
}

func (m *Manager) run(id, typ string) {
	ctx, cancel := context.WithCancel(context.Background())
	m.ctlMu.Lock()
	m.cancel = cancel
	m.curID = id
	m.curType = typ
	m.ctlMu.Unlock()
	defer func() {
		cancel()
		m.ctlMu.Lock()
		m.cancel = nil
		m.curID = ""
		m.curType = ""
		m.busy = false
		m.ctlMu.Unlock()
	}()

	m.runMu.Lock()
	defer m.runMu.Unlock()
	j, err := m.Store.GetJob(id)
	if err != nil {
		return
	}
	j.Status = "running"
	j.StartedAt = time.Now().UTC().Format(time.RFC3339)
	_ = m.update(j)

	progress := func(pct int, line string) {
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		j.Progress = pct
		if line != "" {
			if j.Log != "" {
				j.Log += "\n"
			}
			j.Log += line
			if len(j.Log) > 64<<10 {
				j.Log = j.Log[len(j.Log)-64<<10:]
			}
		}
		_ = m.update(j)
	}

	var runErr error
	switch typ {
	case "backfill":
		runErr = m.DoBackfill(ctx, progress)
	case "sync":
		_, runErr = m.DoSync(ctx, progress)
	case "scan":
		var sa ScanArg
		if v, ok := m.scanArgs.LoadAndDelete(id); ok {
			sa, _ = v.(ScanArg)
		}
		runErr = m.DoScan(ctx, progress, sa.From, sa.To)
	case "backtest":
		progress(100, "请使用 POST /api/backtest 指定参数")
	}
	j.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if runErr != nil && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) {
		j.Status = "paused"
		progress(j.Progress, "已暂停：已写入的 K 线会保留，点「继续回填」即可接着跑")
		_ = m.update(j)
		return
	}
	j.Progress = 100
	if runErr != nil {
		j.Status = "failed"
		progress(100, "失败: "+runErr.Error())
		j.Status = "failed"
	} else {
		j.Status = "success"
	}
	_ = m.update(j)
}

func (m *Manager) update(j store.JobRun) error {
	err := m.Store.UpdateJob(j)
	m.hub.Publish(j)
	return err
}

// Pause 取消当前任务；回填进度按已入库日期保留。
func (m *Manager) Pause() error {
	m.ctlMu.Lock()
	defer m.ctlMu.Unlock()
	if m.cancel == nil {
		return fmt.Errorf("没有正在运行的任务")
	}
	m.cancel()
	return nil
}

// Current 正在跑的任务。
func (m *Manager) Current() (id, typ string, ok bool) {
	m.ctlMu.Lock()
	defer m.ctlMu.Unlock()
	if m.curID == "" {
		return "", "", false
	}
	return m.curID, m.curType, true
}

func (m *Manager) DoBackfill(ctx context.Context, progress ProgressFunc) error {
	if m.Sync == nil {
		return fmt.Errorf("未配置 syncer")
	}
	if progress == nil {
		progress = func(int, string) {}
	}
	progress(1, "开始回填")
	return m.Sync.Backfill(ctx, toSyncProgress(progress))
}

func (m *Manager) DoSync(ctx context.Context, progress ProgressFunc) (int, error) {
	if m.Sync == nil {
		return 0, fmt.Errorf("未配置 syncer")
	}
	if progress == nil {
		progress = func(int, string) {}
	}
	progress(1, "增量同步")
	return m.Sync.SyncIncremental(ctx, toSyncProgress(progress))
}

func (m *Manager) DoScan(ctx context.Context, progress ProgressFunc, from, to string) error {
	if progress == nil {
		progress = func(int, string) {}
	}
	days, err := m.resolveScanDays(from, to)
	if err != nil {
		return err
	}
	if len(days) == 0 {
		return fmt.Errorf("区间内无交易日 K 线，请先回填数据")
	}
	progress(2, fmt.Sprintf("扫描 %s ~ %s，共 %d 个交易日", days[0], days[len(days)-1], len(days)))

	market, err := m.Store.LoadMarketAsOf(days[len(days)-1])
	if err != nil {
		return err
	}
	cfgs, err := m.Store.ListStrategyConfigs()
	if err != nil {
		return err
	}
	latest, _, _ := m.Store.MaxBarDate()
	notifyOK := len(days) == 1 && days[0] == latest

	totalHits := 0
	for i, asOf := range days {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, err := m.scanOneDay(ctx, progress, market, cfgs, asOf, notifyOK)
		if err != nil {
			return err
		}
		totalHits += n
		pct := 5 + (i+1)*90/len(days)
		progress(pct, fmt.Sprintf("%s 选出 %d 只（累计 %d）", asOf, n, totalHits))
	}
	progress(100, fmt.Sprintf("完成 %d 个交易日，共选出 %d 条", len(days), totalHits))
	return nil
}

const maxScanDays = 30

func (m *Manager) resolveScanDays(from, to string) ([]string, error) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" && to == "" {
		asOf, err := scanAsOf(m.Store)
		if err != nil {
			return nil, err
		}
		from, to = asOf, asOf
	} else if from == "" {
		from = to
	} else if to == "" {
		to = from
	}
	if from > to {
		from, to = to, from
	}
	days, err := m.Store.TradingDays(from, to)
	if err != nil {
		return nil, err
	}
	if len(days) == 0 {
		all, err := m.Store.TradingDays("", to)
		if err != nil {
			return nil, err
		}
		if len(all) == 0 {
			return nil, nil
		}
		last := all[len(all)-1]
		if from == to || last >= from {
			return []string{last}, nil
		}
	}
	if len(days) > maxScanDays {
		days = days[len(days)-maxScanDays:]
	}
	return days, nil
}

func (m *Manager) scanOneDay(_ context.Context, progress ProgressFunc, market map[string][]store.Bar, cfgs []store.StrategyConfig, asOf string, doNotify bool) (int, error) {
	runID, err := m.Store.InsertScanRun(asOf)
	if err != nil {
		return 0, err
	}
	status := "failed"
	defer func() { _ = m.Store.FinishScanRun(runID, status) }()

	var allPicks []store.ScanPick
	for _, c := range cfgs {
		if !c.Enabled {
			continue
		}
		st, ok := strategy.ByID(c.ID)
		if !ok {
			continue
		}
		override := strategy.Params{}
		if strings.TrimSpace(c.ParamsJSON) != "" {
			_ = json.Unmarshal([]byte(c.ParamsJSON), &override)
		}
		p := strategy.MergeParams(st.DefaultParams(), override)
		picks := st.Run(market, asOf, p)
		var notifyPicks []struct{ Symbol, Name string }
		for _, pk := range picks {
			name := ""
			if info, ok, _ := m.Store.GetStock(pk.Symbol); ok {
				name = info.Name
			}
			extra, _ := json.Marshal(pk.Extra)
			allPicks = append(allPicks, store.ScanPick{
				RunID: runID, Strategy: st.ID(), Symbol: pk.Symbol, Name: name, ExtraJSON: string(extra), AsOf: asOf,
			})
			notifyPicks = append(notifyPicks, struct{ Symbol, Name string }{pk.Symbol, name})
		}
		if doNotify {
			hook := strings.TrimSpace(c.WebhookURL)
			if hook == "" {
				hook = m.Cfg.FeishuWebhook
			}
			if err := notify.Send(hook, st.Name(), notifyPicks); err != nil {
				progress(0, "推送失败 "+st.Name()+": "+err.Error())
			}
		}
	}
	if len(allPicks) > 0 {
		if err := m.Store.InsertPicks(allPicks); err != nil {
			return 0, err
		}
	}
	status = "success"
	return len(allPicks), nil
}

func scanAsOf(st *store.Store) (string, error) {
	today := time.Now().Format("2006-01-02")
	max, ok, err := st.MaxBarDate()
	if err != nil {
		return "", err
	}
	if ok && max < today {
		return max, nil
	}
	return today, nil
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
