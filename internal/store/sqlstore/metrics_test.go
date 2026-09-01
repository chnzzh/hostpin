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
