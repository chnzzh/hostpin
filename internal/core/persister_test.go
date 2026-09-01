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
