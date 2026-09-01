package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/agent"
	"github.com/chnzzh/hostpin/internal/agentconfig"
	"github.com/chnzzh/hostpin/internal/buildinfo"
	"github.com/chnzzh/hostpin/internal/collector"
	"github.com/chnzzh/hostpin/internal/installer"
	"github.com/chnzzh/hostpin/internal/model"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hostpin-agent:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 1 && (arguments[0] == "-h" || arguments[0] == "--help" || arguments[0] == "help") {
		printHelp()
		return nil
	}
	command := "run"
	if len(arguments) > 0 && !strings.HasPrefix(arguments[0], "-") {
		command, arguments = arguments[0], arguments[1:]
	}
	switch command {
	case "run":
		return runAgent(arguments)
	case "install":
		return installAgent(arguments)
	case "collect":
		return collectOnce(arguments)
	case "version":
		fmt.Printf("hostpin-agent %s (%s) protocol=%d\n", buildinfo.Version, buildinfo.Commit, model.ProtocolVersion)
		return nil
	case "help", "-h", "--help":
		printHelp()
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func runAgent(arguments []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := flags.String("config", agentconfig.DefaultPath(), "path to agent configuration")
	logLevel := flags.String("log-level", envOr("HOSTPIN_AGENT_LOG_LEVEL", "info"), "debug, info, warn, or error")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	logger := newLogger(*logLevel)
	runtime, err := agent.New(*configPath, logger)
	if err != nil {
		return err
	}
	handled, err := executeAsService(runtime.Run)
	if handled {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), platformSignals()...)
	defer stop()
	logger.Info("Hostpin agent started", "version", buildinfo.Version, "config", *configPath)
	return runtime.Run(ctx)
}

func installAgent(arguments []string) error {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	endpoint := flags.String("endpoint", envOr("HOSTPIN_ENDPOINT", ""), "Hostpin panel base URL")
	configPath := flags.String("config", agentconfig.DefaultPath(), "path to save agent configuration")
	pinFile := flags.String("pin-file", "", "read enrollment PIN from a mode-0600 file")
	advanced := flags.Bool("advanced", false, "ask advanced node and collector questions")
	noService := flags.Bool("no-service", false, "install files without registering a service")
	allowHTTP := flags.Bool("allow-http", false, "explicitly allow plain HTTP enrollment (high risk on public networks)")
	probeNode := flags.Bool("probe-node", false, "install as an outbound-only latency measurement node")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*endpoint) == "" {
		return fmt.Errorf("--endpoint is required")
	}
	// Interactive enrollment can legitimately take several minutes. Keep the
	// questionnaire outside any network deadline; installer.Run starts a fresh
	// bounded context only when it is ready to send the enrollment request.
	ctx, stop := signal.NotifyContext(context.Background(), platformSignals()...)
	defer stop()
	result, err := installer.Run(ctx, installer.Options{
		Endpoint: *endpoint, Config: *configPath, PINFile: *pinFile, Advanced: *advanced,
		NoService: *noService, AllowHTTP: *allowHTTP, ProbeNode: *probeNode,
	})
	if err != nil {
		return err
	}
	verb := "reconnected"
	if result.Created {
		verb = "enrolled"
	}
	kind := "node"
	if *probeNode {
		kind = "Probe Node"
	}
	fmt.Printf("Hostpin %s %s successfully: %s\n", kind, verb, result.NodeID)
	fmt.Printf("Agent binary: %s\nConfiguration: %s\n", result.BinaryPath, result.ConfigPath)
	if *noService {
		fmt.Printf("Run: %s run --config %s\n", result.BinaryPath, result.ConfigPath)
	}
	return nil
}

func collectOnce(arguments []string) error {
	flags := flag.NewFlagSet("collect", flag.ContinueOnError)
	full := flags.Bool("full", true, "include disks, processes, connections, temperatures, and GPUs")
	gpu := flags.Bool("gpu", false, "collect optional GPU metrics")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	collector := collector.New(model.AgentConfig{EnableGPU: *gpu})
	sample, err := collector.Collect(ctx, *full)
	if err != nil {
		return err
	}
	result := map[string]any{"identity": collectorIdentity(ctx), "sample": sample}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func collectorIdentity(ctx context.Context) model.AgentIdentity {
	return collector.Identity(ctx, buildinfo.Version)
}

func newLogger(level string) *slog.Logger {
	parsed := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn", "warning":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parsed}))
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func printHelp() {
	fmt.Println(`Hostpin monitoring agent

	Usage:
	  hostpin-agent install --endpoint https://monitor.example.com
	  hostpin-agent install --endpoint https://monitor.example.com --probe-node
  hostpin-agent run [--config PATH]
  hostpin-agent collect [--gpu]
  hostpin-agent version

The enrollment PIN is deliberately not accepted as a command-line option.
Use the interactive prompt, HOSTPIN_PIN, or --pin-file with mode 0600.`)
}
