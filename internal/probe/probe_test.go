package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

func TestHTTPProbeChecksStatusAndBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("hostpin-ready"))
	}))
	defer server.Close()
	task := model.ProbeTask{ID: 1, Type: model.ProbeHTTP, Target: server.URL, TimeoutSeconds: 2, ExpectedStatus: http.StatusAccepted, ExpectedValue: "ready"}
	result := Run(context.Background(), task)
	if !result.Success || result.StatusCode != http.StatusAccepted || result.Value != "hostpin-ready" {
		t.Fatalf("valid HTTP probe failed: %#v", result)
	}
	task.ExpectedValue = "missing"
	if result := Run(context.Background(), task); result.Success || result.Error == "" {
		t.Fatalf("body mismatch succeeded: %#v", result)
	}
	task.ExpectedValue, task.ExpectedStatus = "", http.StatusOK
	if result := Run(context.Background(), task); result.Success || result.StatusCode != http.StatusAccepted {
		t.Fatalf("status mismatch succeeded: %#v", result)
	}
}

func TestTCPProbeAndBoundedScheduler(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = connection.Close()
		}
	}()
	task := model.ProbeTask{ID: 7, Name: "local", Type: model.ProbeTCP, Target: listener.Addr().String(), TimeoutSeconds: 2, IntervalSeconds: 5, Enabled: true}
	scheduler := NewScheduler()
	scheduler.Sync([]model.ProbeTask{task})
	scheduler.Tick(context.Background(), time.Now())
	select {
	case result := <-scheduler.Results():
		if !result.Success || result.TaskID != task.ID {
			t.Fatalf("scheduled TCP probe failed: %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled probe did not complete")
	}
}

func TestProbeRejectsUnknownAndMalformedTargets(t *testing.T) {
	for _, task := range []model.ProbeTask{
		{Type: model.ProbeType("shell"), Target: "echo unsafe", TimeoutSeconds: 1},
		{Type: model.ProbeTCP, Target: "missing-port", TimeoutSeconds: 1},
		{Type: model.ProbeHTTP, Target: "file:///etc/passwd", TimeoutSeconds: 1},
		{Type: model.ProbeDNS, Target: "example.com /etc/passwd", TimeoutSeconds: 1},
	} {
		if result := Run(context.Background(), task); result.Success || result.Error == "" {
			t.Fatalf("malformed task succeeded: %#v", task)
		}
	}
}

func TestParseICMPLatencyUsesWireTime(t *testing.T) {
	for _, test := range []struct {
		output string
		want   float64
	}{
		{"64 bytes from 1.1.1.1: time=12.35 ms", 12.35},
		{"Reply from 192.0.2.1: time<1ms TTL=64", 0.5},
		{"unrecognized localized output", 42},
	} {
		if got := parseICMPLatency(test.output, 42); got != test.want {
			t.Errorf("parseICMPLatency(%q)=%v, want %v", test.output, got, test.want)
		}
	}
}

func TestProbeSamplesReportLoss(t *testing.T) {
	task := model.ProbeTask{ID: 9, Type: model.ProbeTCP, Target: "127.0.0.1:1", TimeoutSeconds: 1, Samples: 3}
	result := Run(context.Background(), task)
	if result.Success || result.LatencyMS != -1 || result.LossPercent != 100 {
		t.Fatalf("failed sample set was not summarized: %#v", result)
	}
}

func TestTCPProbeRunsEverySampleInTheRound(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{}, 3)
	go func() {
		for index := 0; index < 3; index++ {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = connection.Close()
			accepted <- struct{}{}
		}
	}()
	result := Run(context.Background(), model.ProbeTask{
		ID: 10, Type: model.ProbeTCP, Target: listener.Addr().String(), TimeoutSeconds: 2, Samples: 3,
	})
	if !result.Success || result.LossPercent != 0 || result.LatencyMS < 0 {
		t.Fatalf("successful sample round was not summarized: %#v", result)
	}
	for index := 0; index < 3; index++ {
		select {
		case <-accepted:
		case <-time.After(time.Second):
			t.Fatalf("only %d of 3 TCP samples were attempted", index)
		}
	}
}
