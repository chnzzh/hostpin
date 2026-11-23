package core

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

type failingPersistenceStore struct {
	failing   atomic.Bool
	permanent atomic.Bool
	mu        sync.Mutex
	saved     []uint64
}

func (s *failingPersistenceStore) Ping(context.Context) error {
	if s.failing.Load() && !s.permanent.Load() {
		return errors.New("database unavailable")
	}
	return nil
}

func (s *failingPersistenceStore) SaveMetric(_ context.Context, sample model.MetricSample) error {
	if s.failing.Load() {
		return errors.New("database unavailable")
	}
	s.mu.Lock()
	s.saved = append(s.saved, sample.Sequence)
	s.mu.Unlock()
	return nil
}

func (s *failingPersistenceStore) SaveProbeResult(context.Context, model.ProbeResult) error {
	if s.failing.Load() {
		return errors.New("database unavailable")
	}
	return nil
}

func (s *failingPersistenceStore) sequences() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.saved...)
}

func TestPersisterRetainsPendingAndDropsOldestOnOverflow(t *testing.T) {
	repository := &failingPersistenceStore{}
	repository.failing.Store(true)
	persister := NewPersister(repository, nil, 2)
	ctx, cancel := context.WithCancel(context.Background())
	persister.Start(ctx)
	persister.EnqueueMetric(model.MetricSample{Sequence: 1})
	waitFor(t, time.Second, persister.Degraded)
	for sequence := uint64(2); sequence <= 4; sequence++ {
		persister.EnqueueMetric(model.MetricSample{Sequence: sequence})
	}
	if persister.Dropped() != 1 {
		t.Fatalf("queue overflow dropped %d points, expected 1", persister.Dropped())
	}
	repository.failing.Store(false)
	waitFor(t, 3*time.Second, func() bool { return len(repository.sequences()) == 3 && !persister.Degraded() })
	cancel()
	persister.Stop()
	sequences := repository.sequences()
	if len(sequences) != 3 || sequences[0] != 1 || sequences[1] != 3 || sequences[2] != 4 {
		t.Fatalf("pending/newest retention order is %v, expected [1 3 4]", sequences)
	}
}

func TestPersisterDropsPermanentItemAndContinues(t *testing.T) {
	repository := &failingPersistenceStore{}
	repository.failing.Store(true)
	repository.permanent.Store(true)
	persister := NewPersister(repository, nil, 2)
	ctx, cancel := context.WithCancel(context.Background())
	persister.Start(ctx)
	persister.EnqueueMetric(model.MetricSample{Sequence: 1})
	waitFor(t, time.Second, func() bool { return persister.Dropped() == 1 })
	repository.failing.Store(false)
	persister.EnqueueMetric(model.MetricSample{Sequence: 2})
	waitFor(t, time.Second, func() bool { return len(repository.sequences()) == 1 })
	cancel()
	persister.Stop()
	if sequences := repository.sequences(); len(sequences) != 1 || sequences[0] != 2 {
		t.Fatalf("queue did not continue after permanent item: %v", sequences)
	}
}

type batchTestPersistenceStore struct {
	transientFail   atomic.Bool
	badMetricSeq    uint64
	badProbeTask    int64
	mu              sync.Mutex
	metricBatchLens []int
	probeBatchLens  []int
	savedMetrics    []uint64
	savedProbes     []int64
}

func (s *batchTestPersistenceStore) Ping(context.Context) error {
	if s.transientFail.Load() {
		return errors.New("database unavailable")
	}
	return nil
}

func (s *batchTestPersistenceStore) SaveMetric(_ context.Context, sample model.MetricSample) error {
	if s.transientFail.Load() {
		return errors.New("database unavailable")
	}
	if s.badMetricSeq != 0 && sample.Sequence == s.badMetricSeq {
		return errors.New("foreign key constraint failed")
	}
	s.mu.Lock()
	s.savedMetrics = append(s.savedMetrics, sample.Sequence)
	s.mu.Unlock()
	return nil
}

func (s *batchTestPersistenceStore) SaveProbeResult(_ context.Context, result model.ProbeResult) error {
	if s.transientFail.Load() {
		return errors.New("database unavailable")
	}
	if s.badProbeTask != 0 && result.TaskID == s.badProbeTask {
		return errors.New("foreign key constraint failed")
	}
	s.mu.Lock()
	s.savedProbes = append(s.savedProbes, result.TaskID)
	s.mu.Unlock()
	return nil
}

func (s *batchTestPersistenceStore) SaveMetricsBatch(_ context.Context, samples []model.MetricSample) error {
	if s.transientFail.Load() {
		return errors.New("database unavailable")
	}
	for _, sample := range samples {
		if s.badMetricSeq != 0 && sample.Sequence == s.badMetricSeq {
			return errors.New("foreign key constraint failed")
		}
	}
	s.mu.Lock()
	s.metricBatchLens = append(s.metricBatchLens, len(samples))
	for _, sample := range samples {
		s.savedMetrics = append(s.savedMetrics, sample.Sequence)
	}
	s.mu.Unlock()
	return nil
}

func (s *batchTestPersistenceStore) SaveProbeResultsBatch(_ context.Context, results []model.ProbeResult) error {
	if s.transientFail.Load() {
		return errors.New("database unavailable")
	}
	for _, result := range results {
		if s.badProbeTask != 0 && result.TaskID == s.badProbeTask {
			return errors.New("foreign key constraint failed")
		}
	}
	s.mu.Lock()
	s.probeBatchLens = append(s.probeBatchLens, len(results))
	for _, result := range results {
		s.savedProbes = append(s.savedProbes, result.TaskID)
	}
	s.mu.Unlock()
	return nil
}

func (s *batchTestPersistenceStore) snapshot() ([]int, []int, []uint64, []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.metricBatchLens...),
		append([]int(nil), s.probeBatchLens...),
		append([]uint64(nil), s.savedMetrics...),
		append([]int64(nil), s.savedProbes...)
}

func TestPersisterMicroBatchesAndFallsBackOnInvalidOrTransientFailure(t *testing.T) {
	repository := &batchTestPersistenceStore{}
	persister := NewPersister(repository, nil, 64)
	for seq := uint64(1); seq <= 5; seq++ {
		persister.EnqueueMetric(model.MetricSample{Sequence: seq})
	}
	for taskID := int64(1); taskID <= 4; taskID++ {
		persister.EnqueueProbe(model.ProbeResult{TaskID: taskID})
	}
	ctx, cancel := context.WithCancel(context.Background())
	persister.Start(ctx)
	waitFor(t, time.Second, func() bool {
		_, _, metrics, probes := repository.snapshot()
		return len(metrics) == 5 && len(probes) == 4
	})
	cancel()
	persister.Stop()

	metricBatches, probeBatches, metrics, probes := repository.snapshot()
	if len(metricBatches) != 1 || metricBatches[0] != 5 {
		t.Fatalf("expected one 5-item metric batch, got %v", metricBatches)
	}
	if len(probeBatches) != 1 || probeBatches[0] != 4 {
		t.Fatalf("expected one 4-item probe batch, got %v", probeBatches)
	}
	if persister.Dropped() != 0 || persister.Degraded() {
		t.Fatalf("unexpected dropped=%d degraded=%v", persister.Dropped(), persister.Degraded())
	}
	if len(metrics) != 5 || len(probes) != 4 {
		t.Fatalf("saved counts metrics=%v probes=%v", metrics, probes)
	}

	// Single permanently invalid item inside a batch must be discarded without losing valid siblings.
	fallbackRepo := &batchTestPersistenceStore{badMetricSeq: 12, badProbeTask: 22}
	fallbackPersister := NewPersister(fallbackRepo, nil, 64)
	for _, seq := range []uint64{11, 12, 13} {
		fallbackPersister.EnqueueMetric(model.MetricSample{Sequence: seq})
	}
	for _, taskID := range []int64{21, 22, 23} {
		fallbackPersister.EnqueueProbe(model.ProbeResult{TaskID: taskID})
	}
	fallbackCtx, fallbackCancel := context.WithCancel(context.Background())
	fallbackPersister.Start(fallbackCtx)
	waitFor(t, time.Second, func() bool {
		_, _, savedM, savedP := fallbackRepo.snapshot()
		return len(savedM) == 2 && len(savedP) == 2 && fallbackPersister.Dropped() == 2
	})
	fallbackCancel()
	fallbackPersister.Stop()
	_, _, savedM, savedP := fallbackRepo.snapshot()
	if len(savedM) != 2 || savedM[0] != 11 || savedM[1] != 13 {
		t.Fatalf("expected valid metrics [11 13] preserved, got %v", savedM)
	}
	if len(savedP) != 2 || savedP[0] != 21 || savedP[1] != 23 {
		t.Fatalf("expected valid probes [21 23] preserved, got %v", savedP)
	}

	// Transient database unavailability on a batch must enter degraded backoff and retain all items.
	transientRepo := &batchTestPersistenceStore{}
	transientRepo.transientFail.Store(true)
	transientPersister := NewPersister(transientRepo, nil, 64)
	for _, seq := range []uint64{31, 32, 33} {
		transientPersister.EnqueueMetric(model.MetricSample{Sequence: seq})
	}
	transientCtx, transientCancel := context.WithCancel(context.Background())
	transientPersister.Start(transientCtx)
	waitFor(t, time.Second, transientPersister.Degraded)
	transientRepo.transientFail.Store(false)
	waitFor(t, 3*time.Second, func() bool {
		_, _, savedTransient, _ := transientRepo.snapshot()
		return len(savedTransient) == 3 && !transientPersister.Degraded()
	})
	transientCancel()
	transientPersister.Stop()
	if transientPersister.Dropped() != 0 {
		t.Fatalf("transient batch failure dropped %d items", transientPersister.Dropped())
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not reached before timeout")
}
