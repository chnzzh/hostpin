package probe

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

const maxResponseBytes = 64 << 10

func Run(parent context.Context, task model.ProbeTask) model.ProbeResult {
	started := time.Now()
	result := model.ProbeResult{TaskID: task.ID, CollectedAt: started.UTC()}
	attempts := min(max(task.Samples, 1), 10)
	latencies := make([]float64, 0, attempts)
	lastValue, lastError := "", ""
	lastStatus := 0
	for attempt := 0; attempt < attempts; attempt++ {
		value, status, latency, err := runAttempt(parent, task)
		lastStatus = status
		if value != "" {
			lastValue = value
		}
		if err != nil {
			lastError = err.Error()
			continue
		}
		latencies = append(latencies, latency)
	}
	result.StatusCode = lastStatus
	result.Value = truncate(lastValue, 2048)
	result.LossPercent = float64(attempts-len(latencies)) / float64(attempts) * 100
	if len(latencies) == 0 {
		result.LatencyMS = -1
		result.Error = truncate(lastError, 512)
		if result.Error == "" {
			result.Error = "all probe attempts failed"
		}
		return result
	}
	for _, latency := range latencies {
		result.LatencyMS += latency
	}
	result.LatencyMS /= float64(len(latencies))
	result.Success = true
	return result
}

func runAttempt(parent context.Context, task model.ProbeTask) (string, int, float64, error) {
	timeout := time.Duration(max(task.TimeoutSeconds, 1)) * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	started := time.Now()
	var value string
	var status int
	var err error
	switch task.Type {
	case model.ProbeICMP:
		value, err = runICMP(ctx, task.Target)
	case model.ProbeTCP:
		value, err = runTCP(ctx, task.Target)
	case model.ProbeHTTP:
		value, status, err = runHTTP(ctx, task)
	case model.ProbeDNS:
		value, err = runDNS(ctx, task.Target)
	default:
		err = fmt.Errorf("unsupported probe type %q", task.Type)
	}
	latency := float64(time.Since(started).Microseconds()) / 1000
	if task.Type == model.ProbeICMP && err == nil {
		latency = parseICMPLatency(value, latency)
	}
	if err != nil {
		return value, status, latency, err
	}
	if task.ExpectedValue != "" && !strings.Contains(value, task.ExpectedValue) {
		return value, status, latency, errors.New("response did not contain expected value")
	}
	return value, status, latency, nil
}

var pingTimePattern = regexp.MustCompile(`(?i)time\s*([=<])\s*([0-9]+(?:\.[0-9]+)?)\s*ms`)

func parseICMPLatency(output string, fallback float64) float64 {
	match := pingTimePattern.FindStringSubmatch(output)
	if len(match) != 3 {
		return fallback
	}
	value, err := strconv.ParseFloat(match[2], 64)
	if err != nil {
		return fallback
	}
	if match[1] == "<" {
		return value / 2
	}
	return value
}

func runTCP(ctx context.Context, target string) (string, error) {
	if _, _, err := net.SplitHostPort(target); err != nil {
		return "", errors.New("TCP target must be host:port")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", target)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	return connection.RemoteAddr().String(), nil
}

func runHTTP(ctx context.Context, task model.ProbeTask) (string, int, error) {
	parsed, err := url.Parse(task.Target)
	if err != nil || !slices.Contains([]string{"http", "https"}, parsed.Scheme) || parsed.Host == "" {
		return "", 0, errors.New("HTTP target must be an absolute http(s) URL")
	}
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: time.Duration(max(task.TimeoutSeconds, 1)) * time.Second}).DialContext,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	client.CheckRedirect = func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	request.Header.Set("User-Agent", "Hostpin-Agent/1")
	response, err := client.Do(request)
	if err != nil {
		return "", 0, err
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if readErr != nil {
		return "", response.StatusCode, readErr
	}
	expectedStatus := task.ExpectedStatus
	if expectedStatus == 0 {
		expectedStatus = http.StatusOK
	}
	if response.StatusCode != expectedStatus {
		return string(body), response.StatusCode, fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	return string(body), response.StatusCode, nil
}

func runDNS(ctx context.Context, target string) (string, error) {
	hostname := strings.TrimSuffix(strings.TrimSpace(target), ".")
	if hostname == "" || strings.ContainsAny(hostname, " /\\") {
		return "", errors.New("invalid DNS target")
	}
	addresses, err := net.DefaultResolver.LookupHost(ctx, hostname)
	if err != nil {
		return "", err
	}
	slices.Sort(addresses)
	return strings.Join(addresses, ","), nil
}

func runICMP(ctx context.Context, target string) (string, error) {
	hostname := strings.TrimSpace(target)
	if hostname == "" || strings.ContainsAny(hostname, " /\\") {
		return "", errors.New("invalid ICMP target")
	}
	path, err := exec.LookPath("ping")
	if err != nil {
		return "", errors.New("ping utility is unavailable")
	}
	var arguments []string
	switch runtime.GOOS {
	case "windows":
		arguments = []string{"-n", "1", "-w", "1000", hostname}
	case "darwin", "freebsd":
		arguments = []string{"-n", "-c", "1", "-W", "1000", hostname}
	default:
		arguments = []string{"-n", "-c", "1", "-W", "1", hostname}
	}
	output, err := exec.CommandContext(ctx, path, arguments...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ICMP failed: %w", err)
	}
	line := firstUsefulLine(string(output))
	return line, nil
}

func firstUsefulLine(value string) string {
	scanner := bufio.NewScanner(strings.NewReader(value))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(strings.ToLower(line), "time") {
			return line
		}
	}
	return strings.TrimSpace(value)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
