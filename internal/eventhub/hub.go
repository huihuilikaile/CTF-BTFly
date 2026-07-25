package eventhub

import (
	"sync"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
)

// Hub 是按任务分组的进程内发布/订阅中心。
// SQLite 才是事实来源，Hub 仅用于降低实时 UI 的事件延迟。
type Hub struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan platform.Event]struct{}
}

// New 创建一个尚无订阅者的事件中心。
func New() *Hub {
	return &Hub{subscribers: make(map[string]map[chan platform.Event]struct{})}
}

// Subscribe 为指定任务创建带缓冲的事件通道，并返回幂等取消函数。
func (h *Hub) Subscribe(taskID string) (<-chan platform.Event, func()) {
	// 128 条缓冲允许前端短时处理变慢而不立即丢失实时通知。
	channel := make(chan platform.Event, 128)
	h.mu.Lock()
	if h.subscribers[taskID] == nil {
		h.subscribers[taskID] = make(map[chan platform.Event]struct{})
	}
	h.subscribers[taskID][channel] = struct{}{}
	h.mu.Unlock()

	// sync.Once 保证 WebSocket 清理路径重复调用 cancel 时不会二次关闭通道。
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subscribers[taskID], channel)
			if len(h.subscribers[taskID]) == 0 {
				delete(h.subscribers, taskID)
			}
			close(channel)
			h.mu.Unlock()
		})
	}
	return channel, cancel
}

// Publish 以非阻塞方式向当前任务的所有订阅者广播。
// 慢客户端发生丢弃后可按最后 sequence 从 SQLite 补齐历史。
func (h *Hub) Publish(event platform.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for subscriber := range h.subscribers[event.TaskID] {
		select {
		case subscriber <- event:
		default:
			// 历史已持久化；慢客户端会从其最后序号重新拉取。
		}
	}
}
