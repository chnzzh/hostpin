package core

import (
	"sync"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

type subscription struct {
	ch      chan model.LiveMessage
	allowed map[string]struct{}
	filter  bool
}

type Hub struct {
	mu            sync.RWMutex
	latest        map[string]model.MetricSample
	lastPersist   map[string]time.Time
	subscriptions map[*subscription]struct{}
}

func NewHub() *Hub {
	return &Hub{
		latest: make(map[string]model.MetricSample), lastPersist: make(map[string]time.Time),
		subscriptions: make(map[*subscription]struct{}),
	}
}

func (h *Hub) Load(samples map[string]model.MetricSample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, sample := range samples {
		h.latest[id] = sample
		h.lastPersist[id] = sample.ReceivedAt
	}
}

func (h *Hub) Publish(sample model.MetricSample, persistEvery time.Duration) bool {
	h.mu.Lock()
	h.latest[sample.NodeID] = sample
	lastPersisted := h.lastPersist[sample.NodeID]
	shouldPersist := lastPersisted.IsZero() || sample.ReceivedAt.Sub(lastPersisted) >= persistEvery
	if shouldPersist {
		h.lastPersist[sample.NodeID] = sample.ReceivedAt
	}
	message := model.LiveMessage{Type: "sample", At: time.Now().UTC(), NodeID: sample.NodeID, Sample: &sample}
	subscribers := make([]*subscription, 0, len(h.subscriptions))
	for sub := range h.subscriptions {
		subscribers = append(subscribers, sub)
	}
	h.mu.Unlock()

	for _, sub := range subscribers {
		if sub.filter {
			if _, ok := sub.allowed[sample.NodeID]; !ok {
				continue
			}
		}
		select {
		case sub.ch <- message:
		default:
			// Keep the newest state for slow browsers.
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- message:
			default:
			}
		}
	}
	return shouldPersist
}

// ReplaceLatest publishes a corrected view of the latest sample without
// changing the durable-persistence schedule.
func (h *Hub) ReplaceLatest(sample model.MetricSample) {
	h.mu.Lock()
	h.latest[sample.NodeID] = sample
	message := model.LiveMessage{Type: "sample", At: time.Now().UTC(), NodeID: sample.NodeID, Sample: &sample}
	subscribers := make([]*subscription, 0, len(h.subscriptions))
	for sub := range h.subscriptions {
		subscribers = append(subscribers, sub)
	}
	h.mu.Unlock()

	for _, sub := range subscribers {
		if sub.filter {
			if _, ok := sub.allowed[sample.NodeID]; !ok {
				continue
			}
		}
		select {
		case sub.ch <- message:
		default:
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- message:
			default:
			}
		}
	}
}

func (h *Hub) Latest(nodeID string) (model.MetricSample, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	sample, ok := h.latest[nodeID]
	return sample, ok
}

func (h *Hub) Delete(nodeID string) {
	h.mu.Lock()
	delete(h.latest, nodeID)
	delete(h.lastPersist, nodeID)
	h.mu.Unlock()
}

// ResetPersistence makes the next sample for every node eligible for
// persistence without disturbing the latest in-memory state.
func (h *Hub) ResetPersistence() {
	h.mu.Lock()
	clear(h.lastPersist)
	h.mu.Unlock()
}

func (h *Hub) Snapshot(allowed []string) map[string]model.MetricSample {
	h.mu.RLock()
	defer h.mu.RUnlock()
	filter := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		filter[id] = struct{}{}
	}
	result := make(map[string]model.MetricSample)
	for id, sample := range h.latest {
		if allowed != nil {
			if _, ok := filter[id]; !ok {
				continue
			}
		}
		result[id] = sample
	}
	return result
}

func (h *Hub) Subscribe(allowed []string) (<-chan model.LiveMessage, func()) {
	filter := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		filter[id] = struct{}{}
	}
	sub := &subscription{ch: make(chan model.LiveMessage, 32), allowed: filter, filter: allowed != nil}
	h.mu.Lock()
	h.subscriptions[sub] = struct{}{}
	h.mu.Unlock()
	return sub.ch, func() {
		h.mu.Lock()
		delete(h.subscriptions, sub)
		h.mu.Unlock()
	}
}
