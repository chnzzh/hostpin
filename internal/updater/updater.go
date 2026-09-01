package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/buildinfo"
)

// PublicKey is injected at release build time. An empty key disables updates;
// this fails closed and prevents development builds from trusting any manifest.
var PublicKey string

type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Payload struct {
	Version     string     `json:"version"`
	PublishedAt time.Time  `json:"published_at"`
	Artifacts   []Artifact `json:"artifacts"`
}

type Manifest struct {
	Payload
	Signature string `json:"signature"`
}

type Result struct {
	Updated bool
	Version string
	Backup  string
}

func CheckAndApply(ctx context.Context, currentVersion string) (Result, error) {
	publicKey, err := decodePublicKey(PublicKey)
	if err != nil {
		return Result{}, err
	}
	client := updateHTTPClient()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, updateManifestURL(), nil)
	request.Header.Set("User-Agent", "Hostpin-Agent-Updater/1")
	response, err := client.Do(request)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{}, fmt.Errorf("update manifest returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return Result{}, errors.New("update manifest exceeds 1 MiB")
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Result{}, fmt.Errorf("decode update manifest: %w", err)
	}
	if err := verifyManifest(manifest, publicKey); err != nil {
		return Result{}, err
	}
	if !newerVersion(manifest.Version, currentVersion) {
		return Result{Version: manifest.Version}, nil
	}
	var selected *Artifact
	for index := range manifest.Artifacts {
		artifact := &manifest.Artifacts[index]
		if artifact.OS == runtime.GOOS && artifact.Arch == runtime.GOARCH {
			selected = artifact
			break
		}
	}
	if selected == nil {
		return Result{}, fmt.Errorf("release %s has no artifact for %s/%s", manifest.Version, runtime.GOOS, runtime.GOARCH)
	}
	return applyArtifact(ctx, client, manifest.Version, *selected)
}

func updateManifestURL() string {
	return strings.TrimRight(buildinfo.ReleaseBase, "/") + "/update-manifest.json"
}

func verifyManifest(manifest Manifest, publicKey ed25519.PublicKey) error {
	if manifest.Version == "" || len(manifest.Artifacts) == 0 || manifest.PublishedAt.IsZero() {
		return errors.New("update manifest is incomplete")
	}
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("update manifest signature is invalid")
	}
	payload, err := json.Marshal(manifest.Payload)
	if err != nil || !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("update manifest signature verification failed")
	}
	return nil
}

func decodePublicKey(encoded string) (ed25519.PublicKey, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, errors.New("automatic updates are disabled: release public key is not configured")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("automatic update public key is invalid")
	}
	return ed25519.PublicKey(key), nil
}

func applyArtifact(ctx context.Context, client *http.Client, version string, artifact Artifact) (Result, error) {
	parsed, err := url.Parse(artifact.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return Result{}, errors.New("signed artifact URL must use HTTPS")
	}
	if len(artifact.SHA256) != 64 || artifact.Size <= 0 || artifact.Size > 200<<20 {
		return Result{}, errors.New("signed artifact metadata is invalid")
	}
	executable, err := os.Executable()
	if err != nil {
		return Result{}, err
	}
	executable, _ = filepath.EvalSymlinks(executable)
	temporary := executable + ".update"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return Result{}, err
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	request.Header.Set("User-Agent", "Hostpin-Agent-Updater/1")
	response, err := client.Do(request)
	if err != nil {
		output.Close()
		return Result{}, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(response.Body, artifact.Size+1))
	response.Body.Close()
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil || response.StatusCode < 200 || response.StatusCode >= 300 || written != artifact.Size {
		_ = os.Remove(temporary)
		if copyErr != nil {
			return Result{}, copyErr
		}
		if closeErr != nil {
			return Result{}, closeErr
		}
		return Result{}, errors.New("downloaded Agent size or HTTP status did not match the signed manifest")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), artifact.SHA256) {
		_ = os.Remove(temporary)
		return Result{}, errors.New("downloaded Agent checksum did not match the signed manifest")
	}
	if err := os.Chmod(temporary, 0o755); err != nil {
		_ = os.Remove(temporary)
		return Result{}, err
	}
	backup := executable + ".rollback"
	_ = os.Remove(backup)
	if err := os.Rename(executable, backup); err != nil {
		_ = os.Remove(temporary)
		return Result{}, err
	}
	if err := os.Rename(temporary, executable); err != nil {
		_ = os.Rename(backup, executable)
		return Result{}, err
	}
	return Result{Updated: true, Version: version, Backup: backup}, nil
}

func updateHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	return &http.Client{Transport: transport, Timeout: 2 * time.Minute}
}

func newerVersion(candidate, current string) bool {
	if current == "" || current == "dev" {
		return false
	}
	left, leftOK := parseVersion(candidate)
	right, rightOK := parseVersion(current)
	if !leftOK || !rightOK {
		return false
	}
	for index := range left.core {
		if left.core[index] != right.core[index] {
			return left.core[index] > right.core[index]
		}
	}
	if len(left.prerelease) == 0 || len(right.prerelease) == 0 {
		return len(left.prerelease) == 0 && len(right.prerelease) > 0
	}
	for index := 0; index < len(left.prerelease) && index < len(right.prerelease); index++ {
		leftPart, rightPart := left.prerelease[index], right.prerelease[index]
		if leftPart == rightPart {
			continue
		}
		leftNumber, leftNumeric := numericIdentifier(leftPart)
		rightNumber, rightNumeric := numericIdentifier(rightPart)
		switch {
		case leftNumeric && rightNumeric:
			return leftNumber > rightNumber
		case leftNumeric:
			return false
		case rightNumeric:
			return true
		default:
			return leftPart > rightPart
		}
	}
	return len(left.prerelease) > len(right.prerelease)
}

type semanticVersion struct {
	core       [3]int
	prerelease []string
}

func parseVersion(value string) (semanticVersion, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if value == "" {
		return semanticVersion{}, false
	}
	value = strings.SplitN(value, "+", 2)[0]
	coreText, prereleaseText, hasPrerelease := strings.Cut(value, "-")
	coreParts := strings.Split(coreText, ".")
	if len(coreParts) == 0 || len(coreParts) > 3 {
		return semanticVersion{}, false
	}
	parsed := semanticVersion{}
	for index, part := range coreParts {
		if part == "" {
			return semanticVersion{}, false
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return semanticVersion{}, false
		}
		parsed.core[index] = number
	}
	if hasPrerelease {
		if prereleaseText == "" {
			return semanticVersion{}, false
		}
		parsed.prerelease = strings.Split(prereleaseText, ".")
		for _, part := range parsed.prerelease {
			if part == "" {
				return semanticVersion{}, false
			}
		}
	}
	return parsed, true
}

func numericIdentifier(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	number, err := strconv.Atoi(value)
	return number, err == nil
}
