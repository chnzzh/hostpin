package probe

import (
	"context"
	"sync"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

type Scheduler struct {
	mu      sync.Mutex
	tasks   map[int64]model.ProbeTask
	next    map[int64]time.Time
	running map[int64]bool
	results chan model.ProbeResult
	limit   int
}

func NewScheduler() *Scheduler {
	return &Scheduler{
		tasks: make(map[int64]model.ProbeTask), next: make(map[int64]time.Time),
		running: make(map[int64]bool), results: make(chan model.ProbeResult, 256), limit: 4,
	}
}

func (s *Scheduler) SetConcurrency(limit int) {
	s.mu.Lock()
	s.limit = min(max(limit, 1), 32)
	s.mu.Unlock()
}

func (s *Scheduler) Sync(tasks []model.ProbeTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := make(map[int64]struct{}, len(tasks))
	for _, task := range tasks {
		if !task.Enabled || task.ID <= 0 {
			continue
		}
		active[task.ID] = struct{}{}
		s.tasks[task.ID] = task
		if s.next[task.ID].IsZero() {
			s.next[task.ID] = time.Now()
		}
	}
	for id := range s.tasks {
		if _, ok := active[id]; !ok {
			delete(s.tasks, id)
			delete(s.next, id)
		}
	}
}

func (s *Scheduler) Tick(ctx context.Context, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := 0
	for _, value := range s.running {
		if value {
			active++
		}
	}
	for id, task := range s.tasks {
		if active >= s.limit || s.running[id] || now.Before(s.next[id]) {
			continue
		}
		interval := time.Duration(max(task.IntervalSeconds, 5)) * time.Second
		s.next[id] = now.Add(interval)
		s.running[id] = true
		active++
		go func(task model.ProbeTask) {
			result := Run(ctx, task)
			select {
			case s.results <- result:
			default:
				// Probe results are bounded so an unreachable server cannot grow memory forever.
			}
			s.mu.Lock()
			delete(s.running, task.ID)
			s.mu.Unlock()
		}(task)
	}
}

func (s *Scheduler) Results() <-chan model.ProbeResult { return s.results }
