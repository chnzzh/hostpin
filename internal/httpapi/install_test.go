package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/chnzzh/hostpin/internal/buildinfo"
	"github.com/chnzzh/hostpin/internal/config"
)

func TestDefaultAgentReleaseBaseUsesBuildMetadata(t *testing.T) {
	previous := buildinfo.ReleaseBase
	buildinfo.ReleaseBase = "https://github.example.test/owner/repository/releases/latest/download/"
	t.Cleanup(func() { buildinfo.ReleaseBase = previous })
	if result := agentReleaseBase(config.Config{}); result != "https://github.example.test/owner/repository/releases/latest/download" {
		t.Fatalf("default Agent release base = %q", result)
	}
}

func TestAgentInstallAndUninstallScripts(t *testing.T) {
	api := &API{cfg: config.Config{
		PublicURL:        "https://monitor.example.test/base",
		AgentReleaseBase: "https://downloads.example.test/hostpin",
	}}
	for _, test := range []struct {
		path      string
		handler   http.HandlerFunc
		required  []string
		forbidden []string
	}{
		{
			path: "/install.sh", handler: api.handleInstallSH,
			required:  []string{"HOSTPIN_ENDPOINT='https://monitor.example.test/base'", "HOSTPIN_RELEASE_BASE=${HOSTPIN_RELEASE_BASE:-'https://downloads.example.test/hostpin'}", "os=$(uname -s)", "Linux|linux", "mktemp -d", ".sha256", "sha256sum", "awk 'NR == 1 {print $1; exit}'", "tr 'A-F' 'a-f'", `install --endpoint "$HOSTPIN_ENDPOINT"`},
			forbidden: []string{"tr '[:upper:]' '[:lower:]'", "tr -d '[:space:]'"},
		},
		{
			path: "/install.ps1", handler: api.handleInstallPowerShell,
			required: []string{"$Endpoint = 'https://monitor.example.test/base'", "$ReleaseBase = if ($env:HOSTPIN_RELEASE_BASE) { $env:HOSTPIN_RELEASE_BASE } else { 'https://downloads.example.test/hostpin' }", "[switch]$ProbeNode", "[switch]$Advanced", "--probe-node", "--advanced", "[Guid]::NewGuid()", ".sha256", "Get-FileHash", "finally"},
		},
		{
			path: "/uninstall.sh", handler: api.handleUninstallSH,
			required:  []string{"--purge", "--dry-run", "os=$(uname -s)", "Linux|linux", "systemctl --user disable --now", "/sbin/procd", "rc-service hostpin-agent stop", "launchctl bootout", "service hostpin_agent stop", `remove_file "$binary_path"`, "identity is preserved"},
			forbidden: []string{"rm -rf", "agent.json &&", "curl ", "wget ", "tr '[:upper:]' '[:lower:]'"},
		},
		{
			path: "/uninstall.ps1", handler: api.handleUninstallPowerShell,
			required:  []string{"[switch]$Purge", "[switch]$DryRun", "Get-Service -Name 'HostpinAgent'", "sc.exe delete HostpinAgent", "hostpin-agent.exe", "agent.json", "identity is preserved"},
			forbidden: []string{"Recurse", "Invoke-WebRequest", "Remove-Item $ConfigDir"},
		},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		recorder := httptest.NewRecorder()
		test.handler.ServeHTTP(recorder, request)
		body := recorder.Body.String()
		for _, required := range test.required {
			if !strings.Contains(body, required) {
				t.Errorf("%s is missing %q", test.path, required)
			}
		}
		if strings.Contains(body, "--pin ") {
			t.Errorf("%s exposed a command-line PIN argument", test.path)
		}
		for _, forbidden := range test.forbidden {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s unexpectedly contains %q", test.path, forbidden)
			}
		}
		if strings.HasSuffix(test.path, ".sh") {
			if shell, err := exec.LookPath("sh"); err == nil {
				command := exec.Command(shell, "-n")
				command.Stdin = strings.NewReader(body)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("%s has invalid shell syntax: %v: %s", test.path, err, output)
				}
			}
		}
	}
}
