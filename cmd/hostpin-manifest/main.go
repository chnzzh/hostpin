package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/updater"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hostpin-manifest:", err)
		os.Exit(1)
	}
}

func run() error {
	version := flag.String("version", "", "release version")
	directory := flag.String("dir", "dist", "artifact directory")
	baseURL := flag.String("base-url", "", "HTTPS release download base URL")
	output := flag.String("output", "update-manifest.json", "output path")
	keyDirectory := flag.String("generate-key-dir", "", "create a new Ed25519 update-signing key pair in this new directory")
	flag.Parse()
	if strings.TrimSpace(*keyDirectory) != "" {
		if *version != "" || *baseURL != "" {
			return errors.New("--generate-key-dir cannot be combined with manifest generation flags")
		}
		return generateSigningKeys(*keyDirectory)
	}
	if *version == "" || *baseURL == "" {
		return errors.New("--version and --base-url are required")
	}
	_, private, err := signingKeysFromEnvironment()
	if err != nil {
		return err
	}
	return writeManifest(*version, *directory, *baseURL, *output, private)
}

func writeManifest(version, directory, baseURL, output string, private ed25519.PrivateKey) error {
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil || parsedBaseURL.Scheme != "https" || parsedBaseURL.Host == "" {
		return errors.New("--base-url must be an absolute HTTPS URL")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	payload := updater.Payload{Version: version, PublishedAt: time.Now().UTC(), Artifacts: []updater.Artifact{}}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "hostpin-agent-") || strings.HasSuffix(entry.Name(), ".sha256") {
			continue
		}
		osName, arch, ok := targetFromName(entry.Name())
		if !ok {
			continue
		}
		filename := filepath.Join(directory, entry.Name())
		file, err := os.Open(filename)
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, err := io.Copy(hash, file)
		file.Close()
		if err != nil {
			return err
		}
		payload.Artifacts = append(payload.Artifacts, updater.Artifact{OS: osName, Arch: arch, URL: strings.TrimRight(baseURL, "/") + "/" + entry.Name(), SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size})
	}
	if len(payload.Artifacts) == 0 {
		return errors.New("no Agent artifacts found")
	}
	canonical, _ := json.Marshal(payload)
	manifest := updater.Manifest{Payload: payload, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(private), canonical))}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	return os.WriteFile(output, append(data, '\n'), 0o644)
}

func generateSigningKeys(directory string) error {
	directory = filepath.Clean(strings.TrimSpace(directory))
	if directory == "." || directory == "" {
		return errors.New("--generate-key-dir must name a new directory")
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		_ = os.Remove(directory)
		return err
	}
	publicPath := filepath.Join(directory, "public.key")
	privatePath := filepath.Join(directory, "private.key")
	if err := os.WriteFile(publicPath, []byte(base64.StdEncoding.EncodeToString(public)+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(directory)
		return err
	}
	if err := os.WriteFile(privatePath, []byte(base64.StdEncoding.EncodeToString(private)+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(directory)
		return err
	}
	fmt.Printf("Created update-signing keys:\n  public:  %s\n  private: %s\n", publicPath, privatePath)
	return nil
}

func signingKeysFromEnvironment() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	publicRaw := strings.TrimSpace(os.Getenv("HOSTPIN_UPDATE_PUBLIC_KEY"))
	public, err := base64.StdEncoding.DecodeString(publicRaw)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return nil, nil, errors.New("HOSTPIN_UPDATE_PUBLIC_KEY must be base64 encoding of an Ed25519 public key")
	}
	privateRaw := strings.TrimSpace(os.Getenv("HOSTPIN_UPDATE_PRIVATE_KEY"))
	private, err := base64.StdEncoding.DecodeString(privateRaw)
	if err != nil || len(private) != ed25519.PrivateKeySize {
		return nil, nil, errors.New("HOSTPIN_UPDATE_PRIVATE_KEY must be base64 encoding of an Ed25519 private key")
	}
	derived := ed25519.PrivateKey(private).Public().(ed25519.PublicKey)
	if !bytes.Equal(public, derived) {
		return nil, nil, errors.New("HOSTPIN_UPDATE_PUBLIC_KEY does not match HOSTPIN_UPDATE_PRIVATE_KEY")
	}
	return ed25519.PublicKey(public), ed25519.PrivateKey(private), nil
}

func targetFromName(name string) (string, string, bool) {
	name = strings.TrimSuffix(name, ".exe")
	parts := strings.Split(strings.TrimPrefix(name, "hostpin-agent-"), "-")
	if len(parts) != 2 {
		return "", "", false
	}
	if parts[1] == "armv7" {
		parts[1] = "arm"
	}
	return parts[0], parts[1], true
}
