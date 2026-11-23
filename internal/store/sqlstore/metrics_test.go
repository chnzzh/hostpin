package sqlstore

import (
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

func TestDownsampleKeepsBoundaries(t *testing.T) {
	input := make([]model.MetricSample, 100)
	for index := range input {
		input[index].ReceivedAt = time.Unix(int64(index), 0)
	}
	result := downsample(input, 10)
	if len(result) != 10 || !result[0].ReceivedAt.Equal(input[0].ReceivedAt) || !result[9].ReceivedAt.Equal(input[99].ReceivedAt) {
		t.Fatalf("downsample did not retain boundaries: %#v", result)
	}
	one := downsample(input, 1)
	if len(one) != 1 || !one[0].ReceivedAt.Equal(input[99].ReceivedAt) {
		t.Fatal("one-point downsample should return latest sample")
	}
}

func TestMetricCountersSaturateAtDatabaseRange(t *testing.T) {
	if got := metricCounter(^uint64(0)); got != int64(maxSignedMetricCounter) {
		t.Fatalf("oversized counter became %d", got)
	}
	if got := metricCounter(42); got != 42 {
		t.Fatalf("ordinary counter became %d", got)
	}
}

func TestRollupWindowBoundsAndIncrementalOverlap(t *testing.T) {
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	if got := rollupWindow(base, time.Time{}, 15*time.Minute, 10*time.Minute, 48*time.Hour); !got.Equal(base.Add(-48 * time.Hour)) {
		t.Fatalf("initial 5m rollup window = %v, want %v", got, base.Add(-48*time.Hour))
	}
	if got := rollupWindow(base, time.Time{}, 2*time.Hour, time.Hour, 14*24*time.Hour); !got.Equal(base.Add(-14 * 24 * time.Hour)) {
		t.Fatalf("initial 1h rollup window = %v, want %v", got, base.Add(-14*24*time.Hour))
	}

	next := base.Add(5 * time.Minute)
	if got := rollupWindow(next, base, 15*time.Minute, 10*time.Minute, 48*time.Hour); !got.Equal(base.Add(-10 * time.Minute)) {
		t.Fatalf("incremental 5m rollup window = %v, want %v", got, base.Add(-10*time.Minute))
	}

	delayed := base.Add(45 * time.Minute)
	if got := rollupWindow(delayed, base, 15*time.Minute, 10*time.Minute, 48*time.Hour); !got.Equal(base.Add(-10 * time.Minute)) {
		t.Fatalf("delayed 5m rollup window = %v, want %v", got, base.Add(-10*time.Minute))
	}

	farFuture := base.Add(100 * time.Hour)
	if got := rollupWindow(farFuture, base, 15*time.Minute, 10*time.Minute, 48*time.Hour); !got.Equal(farFuture.Add(-48 * time.Hour)) {
		t.Fatalf("far-future 5m rollup window = %v, want %v", got, farFuture.Add(-48*time.Hour))
	}

	backward := base.Add(-5 * time.Minute)
	if got := rollupWindow(backward, base, 15*time.Minute, 10*time.Minute, 48*time.Hour); !got.Equal(backward.Add(-48 * time.Hour)) {
		t.Fatalf("backward clock jump 5m rollup window = %v, want %v", got, backward.Add(-48*time.Hour))
	}
}

func TestIsSQLiteMemoryDSN(t *testing.T) {
	for _, dsn := range []string{"", ":memory:", " :MEMORY: ", "file::memory:?cache=shared", "file:test?mode=memory&cache=shared"} {
		if !isSQLiteMemoryDSN(dsn) {
			t.Fatalf("expected %q to be detected as in-memory SQLite DSN", dsn)
		}
	}
	for _, dsn := range []string{"./data/hostpin.db", "/var/lib/hostpin/hostpin.db", "file:/tmp/hostpin.db"} {
		if isSQLiteMemoryDSN(dsn) {
			t.Fatalf("expected %q to be detected as file-backed SQLite DSN", dsn)
		}
	}
}
