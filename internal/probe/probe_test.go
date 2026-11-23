package probe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
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

func TestTCPProbeIPVersionsAndFamilyMismatch(t *testing.T) {
	listener4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener4.Close()
	go func() {
		for {
			conn, acceptErr := listener4.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	res4 := Run(context.Background(), model.ProbeTask{
		ID: 20, Type: model.ProbeTCP, IPVersion: model.ProbeIPVersion4, Target: listener4.Addr().String(), TimeoutSeconds: 2,
	})
	if !res4.Success {
		t.Fatalf("IPv4 TCP probe failed: %#v", res4)
	}

	mismatch6 := Run(context.Background(), model.ProbeTask{
		ID: 21, Type: model.ProbeTCP, IPVersion: model.ProbeIPVersion6, Target: listener4.Addr().String(), TimeoutSeconds: 1,
	})
	if mismatch6.Success || mismatch6.Error == "" {
		t.Fatalf("IPv4 literal with ip_version=6 should fail: %#v", mismatch6)
	}

	mismatch4 := Run(context.Background(), model.ProbeTask{
		ID: 22, Type: model.ProbeTCP, IPVersion: model.ProbeIPVersion4, Target: "[::1]:443", TimeoutSeconds: 1,
	})
	if mismatch4.Success || mismatch4.Error == "" {
		t.Fatalf("IPv6 literal with ip_version=4 should fail: %#v", mismatch4)
	}

	for _, badTarget := range []string{"127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:https", "127.0.0.1:+443", ":443", "bad host:443", "bad/host:443"} {
		bad := Run(context.Background(), model.ProbeTask{
			ID: 23, Type: model.ProbeTCP, IPVersion: model.ProbeIPVersion4, Target: badTarget, TimeoutSeconds: 1,
		})
		if bad.Success || bad.Error == "" {
			t.Fatalf("invalid TCP target %q unexpectedly succeeded: %#v", badTarget, bad)
		}
	}

	invalidVer := Run(context.Background(), model.ProbeTask{
		ID: 24, Type: model.ProbeTCP, IPVersion: 5, Target: listener4.Addr().String(), TimeoutSeconds: 1,
	})
	if invalidVer.Success || invalidVer.Error == "" {
		t.Fatalf("invalid ip_version=5 unexpectedly succeeded: %#v", invalidVer)
	}

	listener6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback listener unavailable: %v", err)
	}
	defer listener6.Close()
	go func() {
		for {
			conn, acceptErr := listener6.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	res6 := Run(context.Background(), model.ProbeTask{
		ID: 25, Type: model.ProbeTCP, IPVersion: model.ProbeIPVersion6, Target: listener6.Addr().String(), TimeoutSeconds: 2,
	})
	if !res6.Success {
		t.Fatalf("IPv6 TCP probe failed: %#v", res6)
	}
}

func TestICMPTargetNormalizationAndRejection(t *testing.T) {
	validCases := []struct {
		target     string
		ipVersion  int
		wantHost   string
		wantFamily int
	}{
		{"1.1.1.1", 0, "1.1.1.1", 0},
		{"1.1.1.1", 4, "1.1.1.1", 4},
		{" 2001:db8::1 ", 0, "2001:db8::1", 6},
		{"[2001:db8::1]", 6, "2001:db8::1", 6},
		{"[2001:db8::1]", 0, "2001:db8::1", 6},
		{"example.com", 4, "example.com", 4},
		{"example.com", 6, "example.com", 6},
	}
	for _, tc := range validCases {
		host, err := model.NormalizeICMPTarget(tc.target, tc.ipVersion)
		if err != nil {
			t.Fatalf("NormalizeICMPTarget(%q, %d) failed: %v", tc.target, tc.ipVersion, err)
		}
		if host != tc.wantHost {
			t.Errorf("NormalizeICMPTarget(%q, %d) = %q, want %q", tc.target, tc.ipVersion, host, tc.wantHost)
		}
		if family := resolveICMPFamily(host, tc.ipVersion); family != tc.wantFamily {
			t.Errorf("resolveICMPFamily(%q, %d) = %d, want %d", host, tc.ipVersion, family, tc.wantFamily)
		}
	}

	invalidCases := []struct {
		target    string
		ipVersion int
	}{
		{"", 4},
		{"   ", 4},
		{"1.1.1.1 /bin/sh", 4},
		{"1.1.1.1:443", 4},
		{"example.com:443", 4},
		{"[2001:db8::1]:443", 6},
		{"2001:db8:1:2:3:4:5:6:443", 6},
		{"[1.1.1.1]", 4},
		{"[example.com]", 4},
		{"2001:db8::1", 4},
		{"1.1.1.1", 6},
		{"1.1.1.1", 5},
	}
	for _, tc := range invalidCases {
		if host, err := model.NormalizeICMPTarget(tc.target, tc.ipVersion); err == nil {
			t.Errorf("NormalizeICMPTarget(%q, %d) = %q, expected error", tc.target, tc.ipVersion, host)
		}
		res := Run(context.Background(), model.ProbeTask{
			ID: 30, Type: model.ProbeICMP, IPVersion: tc.ipVersion, Target: tc.target, TimeoutSeconds: 1,
		})
		if res.Success || res.Error == "" {
			t.Errorf("Run ICMP(%q, %d) unexpectedly succeeded: %#v", tc.target, tc.ipVersion, res)
		}
	}
}

func TestBuildICMPCommandByOSAndFamily(t *testing.T) {
	bothLookPath := func(name string) (string, error) {
		return "/bin/" + name, nil
	}
	onlyPingLookPath := func(name string) (string, error) {
		if name == "ping" {
			return "/bin/ping", nil
		}
		return "", errors.New("not found")
	}
	onlyPing6LookPath := func(name string) (string, error) {
		if name == "ping6" {
			return "/bin/ping6", nil
		}
		return "", errors.New("not found")
	}

	cases := []struct {
		name             string
		goos             string
		family           int
		host             string
		timeoutSeconds   int
		lookPath         func(string) (string, error)
		wantBin          string
		wantArgs         []string
		wantFallbackBin  string
		wantFallbackArgs []string
	}{
		{
			name: "windows v4", goos: "windows", family: 4, host: "1.1.1.1", timeoutSeconds: 2, lookPath: bothLookPath,
			wantBin: "/bin/ping", wantArgs: []string{"-4", "-n", "1", "-w", "2000", "1.1.1.1"},
		},
		{
			name: "windows v6", goos: "windows", family: 6, host: "2001:db8::1", timeoutSeconds: 1, lookPath: bothLookPath,
			wantBin: "/bin/ping", wantArgs: []string{"-6", "-n", "1", "-w", "1000", "2001:db8::1"},
		},
		{
			name: "windows auto", goos: "windows", family: 0, host: "example.com", timeoutSeconds: 1, lookPath: bothLookPath,
			wantBin: "/bin/ping", wantArgs: []string{"-n", "1", "-w", "1000", "example.com"},
		},
		{
			name: "darwin v4", goos: "darwin", family: 4, host: "1.1.1.1", timeoutSeconds: 1, lookPath: bothLookPath,
			wantBin: "/bin/ping", wantArgs: []string{"-n", "-c", "1", "-W", "1000", "1.1.1.1"},
		},
		{
			name: "darwin v6 never passes -W", goos: "darwin", family: 6, host: "2001:db8::1", timeoutSeconds: 3, lookPath: bothLookPath,
			wantBin: "/bin/ping6", wantArgs: []string{"-n", "-c", "1", "2001:db8::1"},
		},
		{
			name: "freebsd v6 prefers ping6", goos: "freebsd", family: 6, host: "2001:db8::1", timeoutSeconds: 1, lookPath: bothLookPath,
			wantBin: "/bin/ping6", wantArgs: []string{"-n", "-c", "1", "2001:db8::1"},
		},
		{
			name: "freebsd v6 falls back to ping -6", goos: "freebsd", family: 6, host: "2001:db8::1", timeoutSeconds: 1, lookPath: onlyPingLookPath,
			wantBin: "/bin/ping", wantArgs: []string{"-6", "-n", "-c", "1", "-W", "1000", "2001:db8::1"},
		},
		{
			name: "freebsd v4", goos: "freebsd", family: 4, host: "1.1.1.1", timeoutSeconds: 1, lookPath: bothLookPath,
			wantBin: "/bin/ping", wantArgs: []string{"-4", "-n", "-c", "1", "-W", "1000", "1.1.1.1"},
		},
		{
			name: "linux v4", goos: "linux", family: 4, host: "1.1.1.1", timeoutSeconds: 3, lookPath: bothLookPath,
			wantBin: "/bin/ping", wantArgs: []string{"-4", "-n", "-c", "1", "-W", "3", "1.1.1.1"},
		},
		{
			name: "linux v6 with ping6 fallback", goos: "linux", family: 6, host: "2001:db8::1", timeoutSeconds: 3, lookPath: bothLookPath,
			wantBin: "/bin/ping", wantArgs: []string{"-6", "-n", "-c", "1", "-W", "3", "2001:db8::1"},
			wantFallbackBin: "/bin/ping6", wantFallbackArgs: []string{"-n", "-c", "1", "-W", "3", "2001:db8::1"},
		},
		{
			name: "linux v6 ping6 only", goos: "linux", family: 6, host: "2001:db8::1", timeoutSeconds: 2, lookPath: onlyPing6LookPath,
			wantBin: "/bin/ping6", wantArgs: []string{"-n", "-c", "1", "-W", "2", "2001:db8::1"},
		},
		{
			name: "linux auto", goos: "linux", family: 0, host: "example.com", timeoutSeconds: 0, lookPath: bothLookPath,
			wantBin: "/bin/ping", wantArgs: []string{"-n", "-c", "1", "-W", "1", "example.com"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := buildICMPCommand(tc.goos, tc.family, tc.host, tc.timeoutSeconds, tc.lookPath)
			if err != nil {
				t.Fatalf("buildICMPCommand failed: %v", err)
			}
			if spec.primaryBin != tc.wantBin || !slices.Equal(spec.primaryArgs, tc.wantArgs) {
				t.Fatalf("primary = (%q, %v), want (%q, %v)", spec.primaryBin, spec.primaryArgs, tc.wantBin, tc.wantArgs)
			}
			if spec.fallbackBin != tc.wantFallbackBin || !slices.Equal(spec.fallbackArgs, tc.wantFallbackArgs) {
				t.Fatalf("fallback = (%q, %v), want (%q, %v)", spec.fallbackBin, spec.fallbackArgs, tc.wantFallbackBin, tc.wantFallbackArgs)
			}
		})
	}
}

func TestExecuteICMPCommandFallsBackToPing6(t *testing.T) {
	spec := icmpCommandSpec{
		primaryBin:   "/bin/ping",
		primaryArgs:  []string{"-6", "-n", "-c", "1", "-W", "2", "2001:db8::1"},
		fallbackBin:  "/bin/ping6",
		fallbackArgs: []string{"-n", "-c", "1", "-W", "2", "2001:db8::1"},
	}
	var calls []string
	line, err := executeICMPCommand(context.Background(), spec, func(_ context.Context, bin string, args []string) ([]byte, error) {
		calls = append(calls, bin)
		if bin == "/bin/ping" {
			return []byte("ping: unrecognized option: 6"), errors.New("exit status 1")
		}
		return []byte("64 bytes from 2001:db8::1: icmp_seq=1 ttl=57 time=14.2 ms\n"), nil
	})
	if err != nil {
		t.Fatalf("expected ping6 fallback to succeed: %v", err)
	}
	if !slices.Equal(calls, []string{"/bin/ping", "/bin/ping6"}) {
		t.Fatalf("calls = %v, want [/bin/ping /bin/ping6]", calls)
	}
	if got := parseICMPLatency(line, -1); got != 14.2 {
		t.Fatalf("parsed latency = %v, want 14.2", got)
	}
}
