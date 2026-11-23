package core

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

const maxPersistBatch = 128

type persistenceStore interface {
	Ping(context.Context) error
	SaveMetric(context.Context, model.MetricSample) error
	SaveProbeResult(context.Context, model.ProbeResult) error
}

type batchPersistenceStore interface {
	SaveMetricsBatch(context.Context, []model.MetricSample) error
	SaveProbeResultsBatch(context.Context, []model.ProbeResult) error
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
				batch := p.dequeueBatch(&item)
				if remaining := p.persistBatch(ctx, batch); len(remaining) > 0 {
					p.drain(remaining)
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

func (p *Persister) dequeueBatch(first *persistItem) []persistItem {
	var batch []persistItem
	if first != nil {
		batch = make([]persistItem, 1, maxPersistBatch)
		batch[0] = *first
	}
	for len(batch) < maxPersistBatch {
		select {
		case item := <-p.queue:
			if batch == nil {
				batch = make([]persistItem, 0, maxPersistBatch)
			}
			batch = append(batch, item)
		default:
			return batch
		}
	}
	return batch
}

func remainingPersistItems(metrics []model.MetricSample, probes []model.ProbeResult) []persistItem {
	remaining := make([]persistItem, 0, len(metrics)+len(probes))
	for i := range metrics {
		sample := metrics[i]
		remaining = append(remaining, persistItem{sample: &sample})
	}
	for i := range probes {
		probe := probes[i]
		remaining = append(remaining, persistItem{probe: &probe})
	}
	return remaining
}

func (p *Persister) persistBatch(ctx context.Context, batch []persistItem) []persistItem {
	if len(batch) == 0 {
		return nil
	}
	batchStore, canBatch := p.store.(batchPersistenceStore)
	if !canBatch || len(batch) == 1 {
		for i, item := range batch {
			if !p.persist(ctx, item) {
				return batch[i:]
			}
		}
		return nil
	}

	var metrics []model.MetricSample
	var probes []model.ProbeResult
	for _, item := range batch {
		if item.sample != nil {
			metrics = append(metrics, *item.sample)
		}
		if item.probe != nil {
			probes = append(probes, *item.probe)
		}
	}

	if len(metrics) == 1 {
		sample := metrics[0]
		if !p.persist(ctx, persistItem{sample: &sample}) {
			return remainingPersistItems(metrics, probes)
		}
	} else if len(metrics) > 1 {
		if err := batchStore.SaveMetricsBatch(ctx, metrics); err == nil {
			p.degraded.Store(false)
		} else {
			for i := range metrics {
				sample := metrics[i]
				if !p.persist(ctx, persistItem{sample: &sample}) {
					return remainingPersistItems(metrics[i:], probes)
				}
			}
		}
	}

	if len(probes) == 1 {
		probe := probes[0]
		if !p.persist(ctx, persistItem{probe: &probe}) {
			return remainingPersistItems(nil, probes)
		}
	} else if len(probes) > 1 {
		if err := batchStore.SaveProbeResultsBatch(ctx, probes); err == nil {
			p.degraded.Store(false)
		} else {
			for i := range probes {
				probe := probes[i]
				if !p.persist(ctx, persistItem{probe: &probe}) {
					return remainingPersistItems(nil, probes[i:])
				}
			}
		}
	}

	return nil
}

func (p *Persister) drain(pending []persistItem) {
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if len(pending) > 0 {
		if len(p.persistBatch(deadline, pending)) > 0 {
			return
		}
	}
	for {
		batch := p.dequeueBatch(nil)
		if len(batch) == 0 {
			return
		}
		if len(p.persistBatch(deadline, batch)) > 0 {
			return
		}
	}
}

func (p *Persister) Degraded() bool  { return p.degraded.Load() }
func (p *Persister) Dropped() uint64 { return p.dropped.Load() }
