package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/agentconfig"
	"github.com/chnzzh/hostpin/internal/buildinfo"
	"github.com/chnzzh/hostpin/internal/collector"
	"github.com/chnzzh/hostpin/internal/enrollment"
	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/security"
	"github.com/chnzzh/hostpin/internal/service"
	"github.com/google/uuid"
)

type Options struct {
	Endpoint  string
	Config    string
	PINFile   string
	Advanced  bool
	NoService bool
	AllowHTTP bool
	ProbeNode bool
}

type Result struct {
	NodeID     string
	ConfigPath string
	BinaryPath string
	Created    bool
}

const enrollmentNetworkTimeout = 65 * time.Second

func Run(ctx context.Context, options Options) (Result, error) {
	endpoint, err := enrollment.NormalizeEndpoint(options.Endpoint)
	if err != nil {
		return Result{}, err
	}
	nonInteractive := envBool("HOSTPIN_NONINTERACTIVE")
	var console *prompt
	if !nonInteractive {
		console, err = openPrompt()
		if err != nil {
			return Result{}, err
		}
		defer console.close()
	}
	parsed, _ := url.Parse(endpoint)
	if parsed.Scheme == "http" {
		if !enrollment.IsSafePlainHTTP(endpoint) {
			return Result{}, errors.New("plain HTTP enrollment is limited to loopback or private-network addresses")
		}
		confirmed := options.AllowHTTP
		if !confirmed && console != nil {
			confirmed, err = console.confirm("The PIN will cross the network without TLS. Continue", false)
			if err != nil {
				return Result{}, err
			}
		}
		if !confirmed {
			return Result{}, errors.New("plain HTTP enrollment requires explicit confirmation or --allow-http")
		}
	}

	configPath := options.Config
	if strings.TrimSpace(configPath) == "" {
		configPath = agentconfig.DefaultPath()
	}
	existing, existingErr := agentconfig.Load(configPath)
	role := model.NodeRoleMonitor
	if options.ProbeNode {
		role = model.NodeRoleProbe
	}
	installID, token := "", ""
	if existingErr == nil {
		if model.NormalizeNodeRole(existing.Role) != role {
			return Result{}, errors.New("existing Agent identity has a different role; use a separate --config path or remove the old installation")
		}
		installID, token = existing.InstallID, existing.Token
	} else if !errors.Is(existingErr, os.ErrNotExist) {
		return Result{}, fmt.Errorf("read existing identity: %w", existingErr)
	} else {
		pending, pendingErr := agentconfig.LoadPending(configPath)
		if pendingErr == nil {
			if pending.Endpoint != endpoint || model.NormalizeNodeRole(pending.Role) != role {
				return Result{}, errors.New("a pending enrollment exists for a different endpoint or role; finish it first or remove the pending identity file")
			}
			installID, token = pending.InstallID, pending.Token
		} else if !errors.Is(pendingErr, os.ErrNotExist) {
			return Result{}, fmt.Errorf("read pending enrollment identity: %w", pendingErr)
		}
	}
	if installID == "" {
		installID = uuid.NewString()
	}
	if token == "" {
		token, _, _, err = security.NewAgentToken()
		if err != nil {
			return Result{}, err
		}
	}

	pin, err := readPIN(options.PINFile, console)
	if err != nil {
		return Result{}, err
	}
	identityCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	identity := collector.Identity(identityCtx, buildinfo.Version)
	cancel()
	metadata, agentSettings, err := readMetadata(console, nonInteractive, options.Advanced, identity, existing, role)
	if err != nil {
		return Result{}, err
	}
	request := model.EnrollmentRequest{
		PIN: pin, InstallID: installID, Token: token, Role: role, Identity: identity,
		Metadata: metadata, Config: agentSettings,
	}
	if existingErr != nil {
		if err := agentconfig.SavePending(configPath, agentconfig.PendingEnrollment{
			Endpoint: endpoint, InstallID: installID, Token: token, Role: role,
		}); err != nil {
			return Result{}, fmt.Errorf("save pending enrollment identity: %w", err)
		}
	}
	response, err := enrollWithRetry(ctx, endpoint, request)
	request.PIN, request.Token, pin = "", "", ""
	if err != nil {
		return Result{}, err
	}
	if model.NormalizeNodeRole(response.Role) != role {
		return Result{}, errors.New("server returned an enrollment identity with a different role")
	}
	config := agentconfig.Config{
		Endpoint: endpoint, NodeID: response.NodeID, InstallID: installID, Token: token,
		Role: role, Agent: response.Config, Metadata: agentconfig.LocalMetadata{Name: metadata.Name, Group: metadata.Group, Tags: metadata.Tags},
	}
	if err := agentconfig.Save(configPath, config); err != nil {
		return Result{}, fmt.Errorf("save agent identity: %w", err)
	}
	if err := agentconfig.RemovePending(configPath); err != nil {
		return Result{}, fmt.Errorf("remove pending enrollment identity: %w", err)
	}
	binaryPath := agentconfig.InstallBinaryPath()
	if err := installCurrentExecutable(binaryPath); err != nil {
		return Result{}, fmt.Errorf("install agent binary: %w", err)
	}
	if !options.NoService {
		if err := service.Install(binaryPath, configPath); err != nil {
			return Result{}, fmt.Errorf("node enrolled and files installed, but service setup failed: %w", err)
		}
	}
	return Result{NodeID: response.NodeID, ConfigPath: configPath, BinaryPath: binaryPath, Created: response.Created}, nil
}

func enrollWithRetry(parent context.Context, endpoint string, request model.EnrollmentRequest) (model.EnrollmentResponse, error) {
	ctx, cancel := context.WithTimeout(parent, enrollmentNetworkTimeout)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		response, err := enrollment.Enroll(ctx, endpoint, request)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !shouldRetryEnrollment(err) {
			return model.EnrollmentResponse{}, err
		}
		if attempt == 1 || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return model.EnrollmentResponse{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return model.EnrollmentResponse{}, lastErr
}

func shouldRetryEnrollment(err error) bool {
	var responseErr *enrollment.ResponseError
	if !errors.As(err, &responseErr) {
		return true
	}
	return responseErr.Status >= 500 || responseErr.Status == 408 || responseErr.Status == 429
}

func readPIN(path string, console *prompt) (string, error) {
	if path != "" {
		if err := validatePINFile(path); err != nil {
			return "", err
		}
		value, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return validatePIN(strings.TrimSpace(string(value)))
	}
	if value := strings.TrimSpace(os.Getenv("HOSTPIN_PIN")); value != "" {
		return validatePIN(value)
	}
	if console == nil {
		return "", errors.New("HOSTPIN_PIN or a 0600 --pin-file is required in non-interactive mode")
	}
	value, err := console.password("Enrollment PIN")
	if err != nil {
		return "", err
	}
	return validatePIN(value)
}

func validatePIN(value string) (string, error) {
	if len(value) < 6 || len(value) > 64 {
		return "", errors.New("PIN must contain 6 to 64 characters")
	}
	return value, nil
}

func parseTrafficLimitGiB(value string) (int64, error) {
	gib, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(gib) || math.IsInf(gib, 0) || gib < 0 {
		return 0, errors.New("traffic limit must be a non-negative GiB value")
	}
	bytes := math.Round(gib * float64(1<<30))
	if math.IsInf(bytes, 0) || bytes >= float64(math.MaxInt64) {
		return 0, errors.New("traffic limit GiB value is too large")
	}
	return int64(bytes), nil
}

func readMetadata(console *prompt, nonInteractive, forcedAdvanced bool, identity model.AgentIdentity, existing agentconfig.Config, role model.NodeRole) (model.EnrollmentMetadata, model.AgentConfig, error) {
	name := firstNonEmpty(os.Getenv("HOSTPIN_NODE_NAME"), existing.Metadata.Name, identity.Hostname, "unnamed-node")
	group := firstNonEmpty(os.Getenv("HOSTPIN_NODE_GROUP"), existing.Metadata.Group)
	region := strings.TrimSpace(os.Getenv("HOSTPIN_NODE_REGION"))
	tags := splitCSV(firstNonEmpty(os.Getenv("HOSTPIN_NODE_TAGS"), strings.Join(existing.Metadata.Tags, ",")))
	if !nonInteractive {
		var err error
		if name, err = console.ask("Node name", name); err != nil {
			return model.EnrollmentMetadata{}, model.AgentConfig{}, err
		}
		if group, err = console.ask("Group", group); err != nil {
			return model.EnrollmentMetadata{}, model.AgentConfig{}, err
		}
		if region, err = console.ask("Region", region); err != nil {
			return model.EnrollmentMetadata{}, model.AgentConfig{}, err
		}
		tagText, err := console.ask("Tags (comma-separated)", strings.Join(tags, ","))
		if err != nil {
			return model.EnrollmentMetadata{}, model.AgentConfig{}, err
		}
		tags = splitCSV(tagText)
	}
	metadata := model.EnrollmentMetadata{Name: name, Group: group, Region: region, Tags: tags, Currency: "USD", TrafficLimitType: "sum", TrafficResetDay: 1}
	agentSettings := model.DefaultAgentConfig()
	if existing.Agent.CollectIntervalSeconds > 0 {
		agentSettings = existing.Agent
	}
	if role == model.NodeRoleProbe {
		if existing.Agent.CollectIntervalSeconds <= 0 {
			agentSettings.CollectIntervalSeconds = 5
			agentSettings.PersistIntervalSeconds = 60
			agentSettings.ProbeConcurrency = 4
		}
		publicVisible := true
		if raw := strings.TrimSpace(os.Getenv("HOSTPIN_PROBE_PUBLIC")); raw != "" {
			parsed, parseErr := strconv.ParseBool(raw)
			if parseErr != nil {
				return metadata, agentSettings, errors.New("HOSTPIN_PROBE_PUBLIC must be true or false")
			}
			publicVisible = parsed
		}
		if !nonInteractive {
			var confirmErr error
			publicVisible, confirmErr = console.confirm("Show this measurement node on the public latency page", publicVisible)
			if confirmErr != nil {
				return metadata, agentSettings, confirmErr
			}
		}
		metadata.Hidden = !publicVisible
		if forcedAdvanced || envBool("HOSTPIN_ADVANCED") {
			if !nonInteractive {
				metadata.CountryCode, _ = console.ask("Country code", "")
				metadata.PublicRemark, _ = console.ask("Public remark", "")
				metadata.PrivateRemark, _ = console.ask("Private remark", "")
				concurrency, askErr := askInt(console, "Concurrent latency checks", agentSettings.ProbeConcurrency)
				if askErr != nil || concurrency < 1 || concurrency > 32 {
					return metadata, agentSettings, errors.New("probe concurrency must be between 1 and 32")
				}
				agentSettings.ProbeConcurrency = concurrency
				agentSettings.AutoUpdate, _ = console.confirm("Enable signed automatic agent updates", agentSettings.AutoUpdate)
			}
		}
		return metadata, agentSettings, nil
	}
	advanced := !nonInteractive && (forcedAdvanced || envBool("HOSTPIN_ADVANCED"))
	if !nonInteractive && !advanced {
		advanced, _ = console.confirm("Configure advanced metadata and collectors", false)
	}
	if !advanced {
		return metadata, agentSettings, nil
	}
	var err error
	metadata.CountryCode, _ = console.ask("Country code", "")
	metadata.PublicRemark, _ = console.ask("Public remark", "")
	metadata.PrivateRemark, _ = console.ask("Private remark", "")
	metadata.Hidden, err = console.confirm("Hide this node from visitors", false)
	if err != nil {
		return metadata, agentSettings, err
	}
	if metadata.Price, err = askFloat(console, "Price", 0); err != nil {
		return metadata, agentSettings, err
	}
	metadata.Currency, _ = console.ask("Currency", "USD")
	if metadata.BillingCycleDays, err = askInt(console, "Billing cycle (days)", 30); err != nil {
		return metadata, agentSettings, err
	}
	expires, _ := console.ask("Expiry date (YYYY-MM-DD, blank for none)", "")
	if expires != "" {
		value, parseErr := time.Parse("2006-01-02", expires)
		if parseErr != nil {
			return metadata, agentSettings, errors.New("expiry date must use YYYY-MM-DD")
		}
		value = value.UTC()
		metadata.ExpiresAt = &value
	}
	metadata.AutoRenewal, _ = console.confirm("Automatically renew", false)
	traffic, _ := console.ask("Monthly traffic limit in GiB (0 for unlimited)", "0")
	if metadata.TrafficLimit, err = parseTrafficLimitGiB(traffic); err != nil {
		return metadata, agentSettings, err
	}
	metadata.TrafficLimitType, _ = console.ask("Traffic mode (sum/max/up/down)", "sum")
	if !contains([]string{"sum", "max", "up", "down"}, metadata.TrafficLimitType) {
		return metadata, agentSettings, errors.New("traffic mode must be sum, max, up, or down")
	}
	if metadata.TrafficResetDay, err = askInt(console, "Monthly traffic reset day", 1); err != nil || metadata.TrafficResetDay < 1 || metadata.TrafficResetDay > 31 {
		return metadata, agentSettings, errors.New("traffic reset day must be between 1 and 31")
	}
	nics, _ := console.ask("Network interfaces (comma-separated, blank for automatic)", strings.Join(agentSettings.IncludeNICs, ","))
	mounts, _ := console.ask("Mountpoints (comma-separated, blank for automatic)", strings.Join(agentSettings.IncludeMountpoints, ","))
	agentSettings.IncludeNICs, agentSettings.IncludeMountpoints = splitCSV(nics), splitCSV(mounts)
	agentSettings.EnableGPU, _ = console.confirm("Collect NVIDIA/AMD GPU metrics", agentSettings.EnableGPU)
	agentSettings.AutoUpdate, _ = console.confirm("Enable signed automatic agent updates", agentSettings.AutoUpdate)
	return metadata, agentSettings, nil
}

func installCurrentExecutable(target string) error {
	source, err := os.Executable()
	if err != nil {
		return err
	}
	source, _ = filepath.EvalSymlinks(source)
	if samePath(source, target) {
		return os.Chmod(target, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary := target + ".new"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Chmod(temporary, 0o755); err != nil {
		return err
	}
	backup := target + ".old"
	if _, err := os.Stat(target); err == nil {
		_ = os.Remove(backup)
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	return nil
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func askInt(console *prompt, label string, fallback int) (int, error) {
	value, err := console.ask(label, strconv.Itoa(fallback))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(value)
}

func askFloat(console *prompt, label string, fallback float64) (float64, error) {
	value, err := console.ask(label, strconv.FormatFloat(fallback, 'f', -1, 64))
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(value, 64)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, item := range parts {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func envBool(name string) bool {
	value, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	return value
}
