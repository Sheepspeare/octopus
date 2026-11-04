package task

import (
	"sync"
	"time"

	"github.com/charmbracelet/log"
)

type taskEntry struct {
	name       string
	interval   time.Duration
	fn         func()
	runOnStart bool
	ticker     *time.Ticker
	stopCh     chan struct{}
	updateCh   chan time.Duration
}

var (
	tasks   = make(map[string]*taskEntry)
	tasksMu sync.RWMutex
	running bool // RUN 是否已经为已注册任务起过协程; 之后补注册的任务需要自己拉起。
)

// Register 注册一个定时任务
// runOnStart: 是否在启动时立即执行一次
func Register(name string, interval time.Duration, runOnStart bool, fn func()) {
	if interval <= 0 {
		log.Debugf("task %s not registered: interval is 0", name)
		return
	}

	tasksMu.Lock()
	defer tasksMu.Unlock()

	if _, exists := tasks[name]; exists {
		log.Warnf("task %s already registered, skipping", name)
		return
	}

	tasks[name] = &taskEntry{
		name:       name,
		interval:   interval,
		fn:         fn,
		runOnStart: runOnStart,
		stopCh:     make(chan struct{}),
		// 带缓冲: Update 用非阻塞发送, 无缓冲时若 runTask 还没阻塞在 select 上就会把这次调整丢掉。
		updateCh: make(chan time.Duration, 1),
	}
	log.Debugf("task %s registered with interval %v, runOnStart: %v", name, interval, runOnStart)
}

// Update 更新任务的执行间隔
// 当 interval 为 0 时，删除任务
func Update(name string, interval time.Duration) {
	tasksMu.Lock()
	entry, exists := tasks[name]
	if !exists {
		tasksMu.Unlock()
		log.Warnf("task %s not found", name)
		return
	}

	if interval <= 0 {
		delete(tasks, name)
		tasksMu.Unlock()
		close(entry.stopCh)
		log.Infof("task %s removed: interval is 0", name)
		return
	}
	tasksMu.Unlock()

	select {
	case entry.updateCh <- interval:
		log.Infof("task %s interval updated to %v", name, interval)
	default:
		log.Warnf("task %s update channel full, skipping", name)
	}
}

// Set 注册或调整一个定时任务: 未注册时按给定函数注册, 已注册时只改执行间隔。
// 与 Update 的区别是允许在 RUN 之后补注册 —— 间隔来自设置项时, 启动时可能取不到合法值而没注册,
// 之后用户在界面上改对了要能直接生效, 不必重启进程。
func Set(name string, interval time.Duration, fn func()) {
	tasksMu.Lock()
	entry, exists := tasks[name]
	if exists {
		tasksMu.Unlock()
		Update(name, interval)
		return
	}
	if interval <= 0 {
		tasksMu.Unlock()
		log.Debugf("task %s not registered: interval is 0", name)
		return
	}
	entry = &taskEntry{
		name:     name,
		interval: interval,
		fn:       fn,
		stopCh:   make(chan struct{}),
		updateCh: make(chan time.Duration, 1),
	}
	tasks[name] = entry
	started := running
	tasksMu.Unlock()

	log.Infof("task %s registered with interval %v", name, interval)
	if started {
		go runTask(entry)
	}
}

// RUN 启动所有注册的任务
func RUN() {
	tasksMu.Lock()
	running = true
	for _, entry := range tasks {
		go runTask(entry)
	}
	tasksMu.Unlock()

	// 阻塞主协程
	select {}
}

func runTask(entry *taskEntry) {
	// 根据配置决定是否在启动时立即执行
	if entry.runOnStart {
		go entry.fn()
	}

	entry.ticker = time.NewTicker(entry.interval)
	defer entry.ticker.Stop()

	for {
		select {
		case <-entry.ticker.C:
			go entry.fn()
		case newInterval := <-entry.updateCh:
			entry.ticker.Stop()
			entry.interval = newInterval
			entry.ticker = time.NewTicker(newInterval)
		case <-entry.stopCh:
			return
		}
	}
}
