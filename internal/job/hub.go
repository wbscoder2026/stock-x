package job

import (
	"sync"

	"github.com/wbscoder2026/stock-x/internal/store"
)

// Hub 按 job id 推送进度（SSE）。
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan store.JobRun]struct{}
}

func newHub() *Hub {
	return &Hub{subs: map[string]map[chan store.JobRun]struct{}{}}
}

func (h *Hub) Subscribe(id string) (<-chan store.JobRun, func()) {
	ch := make(chan store.JobRun, 8)
	h.mu.Lock()
	if h.subs[id] == nil {
		h.subs[id] = map[chan store.JobRun]struct{}{}
	}
	h.subs[id][ch] = struct{}{}
	h.mu.Unlock()
	unsub := func() {
		h.mu.Lock()
		if m := h.subs[id]; m != nil {
			delete(m, ch)
			if len(m) == 0 {
				delete(h.subs, id)
			}
		}
		h.mu.Unlock()
		close(ch)
	}
	return ch, unsub
}

func (h *Hub) Publish(j store.JobRun) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[j.ID] {
		select {
		case ch <- j:
		default:
			// ponytail: 订阅者慢则丢中间帧，只保证最终状态靠 GET
		}
	}
}
