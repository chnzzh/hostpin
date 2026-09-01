package core

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

type persistenceStore interface {
	Ping(context.Context) error
	SaveMetric(context.Context, model.MetricSample) error
	SaveProbeResult(context.Context, model.ProbeResult) error
}

type persistItem struct {
	sample *model.MetricSample
	probe  *model.ProbeResult
}

type Persister struct {
	store    persistenceStore
	logger   *slog.Logger
	queue    chan persistItem
	degraded atomic.Bool
	dropped  atomic.Uint64
	wg       sync.WaitGroup
}

func NewPersister(repository persistenceStore, logger *slog.Logger, size int) *Persister {
	return &Persister{store: repository, logger: logger, queue: make(chan persistItem, size)}
}

func (p *Persister) Start(ctx context.Context) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-ctx.Done():
				p.drain(nil)
				return
			case item := <-p.queue:
				if !p.persist(ctx, item) {
					p.drain(&item)
					return
				}
			}
		}
	}()
}

func (p *Persister) Stop() { p.wg.Wait() }

func (p *Persister) EnqueueMetric(sample model.MetricSample) {
	p.enqueue(persistItem{sample: &sample})
}

func (p *Persister) EnqueueProbe(result model.ProbeResult) {
	p.enqueue(persistItem{probe: &result})
}

func (p *Persister) enqueue(item persistItem) {
	select {
	case p.queue <- item:
	default:
		select {
		case <-p.queue:
			p.dropped.Add(1)
		default:
		}
		select {
		case p.queue <- item:
		default:
			p.dropped.Add(1)
		}
		p.degraded.Store(true)
	}
}

func (p *Persister) persist(ctx context.Context, item persistItem) bool {
	backoff := 200 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err := p.write(ctx, item)
		if err == nil {
			p.degraded.Store(false)
			return true
		}
		if ctx.Err() == nil && p.store.Ping(ctx) == nil {
			p.dropped.Add(1)
			p.degraded.Store(false)
			if p.logger != nil {
				p.logger.Error("discarding permanently invalid persistence item", "error", err)
			}
			return true
		}
		p.degraded.Store(true)
		if p.logger != nil && ctx.Err() == nil && (attempt == 1 || attempt%10 == 0) {
			p.logger.Error("metric persistence unavailable; retaining queued history", "error", err, "attempt", attempt)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
		backoff = min(backoff*2, 15*time.Second)
	}
}

func (p *Persister) write(ctx context.Context, item persistItem) error {
	if item.sample != nil {
		return p.store.SaveMetric(ctx, *item.sample)
	}
	if item.probe != nil {
		return p.store.SaveProbeResult(ctx, *item.probe)
	}
	return nil
}

func (p *Persister) drain(pending *persistItem) {
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if pending != nil && !p.persist(deadline, *pending) {
		return
	}
	for {
		select {
		case item := <-p.queue:
			if !p.persist(deadline, item) {
				return
			}
		default:
			return
		}
	}
}

func (p *Persister) Degraded() bool  { return p.degraded.Load() }
func (p *Persister) Dropped() uint64 { return p.dropped.Load() }
