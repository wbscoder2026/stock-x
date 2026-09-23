package futures

import (
	"sync"
	"sync/atomic"

	"github.com/panjf2000/ants/v2"
)

// SweepRun 一次扫描的运行时句柄：并发可以中途调，进度随时可读。
//
// HTTP 层一个 token 建一个，扫描开始时把 ants 协程池绑进来；池就绪之前调
// SetWorkers 只会记下期望值，开跑时按它建池（所以「还没开始就改并发」也生效）。
type SweepRun struct {
	mu    sync.Mutex
	pool  *ants.Pool
	want  int
	done  atomic.Int64
	total atomic.Int64
}

func NewSweepRun(workers int) *SweepRun {
	return &SweepRun{want: NormalizeSweepWorkers(workers)}
}

// SetWorkers 实时改并发（0 = 自动用满本机核数，上限 SweepMaxWorkers）。
// 已经开跑就地对协程池 Tune，立刻生效：正在跑的那几个任务不打断，
// 之后提交的任务按新并发上（调小则等当前任务跑完自然收敛）。
func (r *SweepRun) SetWorkers(n int) {
	n = NormalizeSweepWorkers(n)
	r.mu.Lock()
	r.want = n
	pool := r.pool
	r.mu.Unlock()
	if pool == nil {
		return // 还没开跑：记下期望值就行，建池时按它来
	}
	pool.Tune(n)
}

// Workers 当前并发（池还没建时是期望值）。
func (r *SweepRun) Workers() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.want
}

// Report 进度回调（接到 SweepRequest.OnProgress 上）。
func (r *SweepRun) Report(done, total int) {
	r.done.Store(int64(done))
	r.total.Store(int64(total))
}

// Progress 已跑完的组合数与总数；total=0 表示还没开跑。
func (r *SweepRun) Progress() (done, total int64) {
	return r.done.Load(), r.total.Load()
}

func (r *SweepRun) bindPool(pool *ants.Pool) {
	r.mu.Lock()
	r.pool = pool
	r.mu.Unlock()
}

// releasePool 解绑（池已经释放，之后再 SetWorkers 不该去 Tune 一个关掉的池）。
func (r *SweepRun) releasePool() {
	r.mu.Lock()
	r.pool = nil
	r.mu.Unlock()
}

// newSweepPool 建扫描用的协程池：容量 = 并发数。
// 关键一点是**阻塞式提交**（ants 默认行为）：池满时 Submit 会等，
// 于是内存里同时挂着的任务数约等于并发数而不是组合数（千万组也不会把任务全堆进内存），
// 而且 Tune 之后阻塞的提交立刻就能用上新容量 —— 这就是「动态并发」能生效的原因。
func newSweepPool(workers int) (*ants.Pool, error) {
	return ants.NewPool(workers, ants.WithNonblocking(false))
}
