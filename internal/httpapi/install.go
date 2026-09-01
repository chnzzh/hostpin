package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/chnzzh/hostpin/internal/buildinfo"
	"github.com/chnzzh/hostpin/internal/config"
)

func (a *API) handleInstallSH(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	base := publicBase(a.cfg)
	releaseBase := agentReleaseBase(a.cfg)
	script := `#!/bin/sh
set -eu

HOSTPIN_ENDPOINT='__ENDPOINT__'
HOSTPIN_RELEASE_BASE=${HOSTPIN_RELEASE_BASE:-'__RELEASE_BASE__'}

os=$(uname -s)
arch=$(uname -m)
case "$os" in
  Linux|linux) os=linux ;;
  Darwin|darwin) os=darwin ;;
  FreeBSD|freebsd) os=freebsd ;;
  *) echo "Unsupported operating system: $os" >&2; exit 1 ;;
esac
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  armv7l|armv7) arch=armv7 ;;
  i386|i686) arch=386 ;;
  mips) arch=mips ;;
  mipsel|mipsle) arch=mipsle ;;
  riscv64) arch=riscv64 ;;
  *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
esac

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/hostpin-install.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT INT TERM
binary="$tmp_dir/hostpin-agent"
url="$HOSTPIN_RELEASE_BASE/hostpin-agent-$os-$arch"
checksum_file="$tmp_dir/hostpin-agent.sha256"
echo "Downloading Hostpin agent for $os/$arch..."
if command -v curl >/dev/null 2>&1; then
  curl -fL --retry 3 "$url" -o "$binary"
  curl -fL --retry 3 "$url.sha256" -o "$checksum_file"
elif command -v wget >/dev/null 2>&1; then
  wget -O "$binary" "$url"
  wget -O "$checksum_file" "$url.sha256"
else
  echo "curl or wget is required" >&2
  exit 1
fi
expected=$(awk 'NR == 1 {print $1; exit}' "$checksum_file" | tr 'A-F' 'a-f')
case "$expected" in *[!0-9a-fA-F]*|'') echo "Invalid checksum response" >&2; exit 1 ;; esac
if [ "${#expected}" -ne 64 ]; then echo "Invalid checksum length" >&2; exit 1; fi
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$binary" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$binary" | awk '{print $1}')
elif command -v openssl >/dev/null 2>&1; then
  actual=$(openssl dgst -sha256 "$binary" | awk '{print $NF}')
else
  echo "sha256sum, shasum, or openssl is required to verify the Agent" >&2
  exit 1
fi
if [ "$actual" != "$expected" ]; then
  echo "Hostpin Agent checksum verification failed" >&2
  exit 1
fi
chmod 0755 "$binary"
"$binary" install --endpoint "$HOSTPIN_ENDPOINT" "$@"
`
	script = strings.ReplaceAll(script, "__ENDPOINT__", strings.ReplaceAll(base, "'", ""))
	script = strings.ReplaceAll(script, "__RELEASE_BASE__", strings.ReplaceAll(releaseBase, "'", ""))
	_, _ = fmt.Fprint(w, script)
}

func (a *API) handleInstallPowerShell(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	base := strings.ReplaceAll(publicBase(a.cfg), "'", "")
	releaseBase := strings.ReplaceAll(agentReleaseBase(a.cfg), "'", "")
	script := `param([switch]$ProbeNode, [switch]$Advanced)
$ErrorActionPreference = 'Stop'
$Endpoint = '__ENDPOINT__'
$ReleaseBase = if ($env:HOSTPIN_RELEASE_BASE) { $env:HOSTPIN_RELEASE_BASE } else { '__RELEASE_BASE__' }
$Arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq 'Arm64') { 'arm64' } else { 'amd64' }
$TempPath = Join-Path $env:TEMP ("hostpin-agent-{0}.exe" -f [Guid]::NewGuid().ToString('N'))
$Url = "$ReleaseBase/hostpin-agent-windows-$Arch.exe"
try {
  Write-Host "Downloading Hostpin agent for windows/$Arch..."
  Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $TempPath
  $Expected = (Invoke-WebRequest -UseBasicParsing -Uri "$Url.sha256").Content.Trim()
  if ($Expected -notmatch '^[a-fA-F0-9]{64}$') { throw 'Invalid Agent checksum response' }
  $Actual = (Get-FileHash -Algorithm SHA256 -Path $TempPath).Hash.ToLowerInvariant()
  if ($Actual -ne $Expected.ToLowerInvariant()) { throw 'Hostpin Agent checksum verification failed' }
	  $InstallArgs = @('install', '--endpoint', $Endpoint)
	  if ($ProbeNode) { $InstallArgs += '--probe-node' }
	  if ($Advanced) { $InstallArgs += '--advanced' }
	  & $TempPath @InstallArgs
  if ($LASTEXITCODE -ne 0) { throw "Hostpin Agent installer exited with code $LASTEXITCODE" }
} finally {
  Remove-Item -Force -ErrorAction SilentlyContinue $TempPath
}
`
	script = strings.ReplaceAll(script, "__ENDPOINT__", base)
	script = strings.ReplaceAll(script, "__RELEASE_BASE__", releaseBase)
	_, _ = fmt.Fprint(w, script)
}

func (a *API) handleUninstallSH(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	script := `#!/bin/sh
set -eu

purge=0
dry_run=0
for argument in "$@"; do
  case "$argument" in
    --purge) purge=1 ;;
    --dry-run) dry_run=1 ;;
    -h|--help)
      echo "Usage: uninstall.sh [--purge] [--dry-run]"
      echo "  --purge    also remove the local Agent identity and credentials"
      echo "  --dry-run  show the actions without changing the system"
      exit 0
      ;;
    *) echo "Unknown option: $argument" >&2; exit 2 ;;
  esac
done

run_step() {
  description=$1
  shift
  if [ "$dry_run" -eq 1 ]; then
    printf 'Would %s:' "$description"
    for part in "$@"; do printf ' %s' "$part"; done
    printf '\n'
    return 0
  fi
  if ! "$@"; then
    echo "Warning: could not $description; continuing cleanup." >&2
  fi
}

remove_file() {
  target=$1
  if [ ! -e "$target" ] && [ ! -L "$target" ]; then return 0; fi
  if [ "$dry_run" -eq 1 ]; then
    echo "Would remove file: $target"
  elif ! rm -f "$target"; then
    echo "Warning: could not remove $target" >&2
  fi
}

remove_empty_dir() {
  target=$1
  if [ ! -d "$target" ]; then return 0; fi
  if [ "$dry_run" -eq 1 ]; then
    echo "Would remove directory if empty: $target"
  else
    rmdir "$target" 2>/dev/null || true
  fi
}

os=$(uname -s)
uid=$(id -u)
case "$os" in
  Linux|linux) os=linux ;;
  Darwin|darwin) os=darwin ;;
  FreeBSD|freebsd) os=freebsd ;;
  *) echo "Unsupported operating system: $os" >&2; exit 1 ;;
esac
home=${HOME:-}
if [ "$os" = darwin ] && [ "$uid" -eq 0 ]; then home=/var/root; fi
case "$home" in
  /*) ;;
  *) echo "HOME must be an absolute path" >&2; exit 1 ;;
esac
if [ "$uid" -ne 0 ]; then
  if [ -e /etc/systemd/system/hostpin-agent.service ] || [ -e /etc/init.d/hostpin-agent ] || [ -e /Library/LaunchDaemons/io.hostpin.agent.plist ]; then
    echo "A system-wide Hostpin Agent installation was found." >&2
    echo "Run the one-line uninstall command again with 'sudo sh' instead of 'sh'." >&2
    exit 1
  fi
fi

case "$os" in
  linux)
    if command -v systemctl >/dev/null 2>&1; then
      if [ "$uid" -eq 0 ]; then
        run_step "stop and disable the system service" systemctl disable --now hostpin-agent.service
        remove_file /etc/systemd/system/hostpin-agent.service
        run_step "reload systemd" systemctl daemon-reload
        run_step "clear the systemd failure state" systemctl reset-failed hostpin-agent.service
      else
        run_step "stop and disable the user service" systemctl --user disable --now hostpin-agent.service
        remove_file "$home/.config/systemd/user/hostpin-agent.service"
        run_step "reload the user service manager" systemctl --user daemon-reload
        run_step "clear the user-service failure state" systemctl --user reset-failed hostpin-agent.service
      fi
    elif [ "$uid" -eq 0 ] && [ -x /sbin/procd ]; then
      run_step "stop the procd service" /etc/init.d/hostpin-agent stop
      run_step "disable the procd service" /etc/init.d/hostpin-agent disable
      remove_file /etc/init.d/hostpin-agent
    elif [ "$uid" -eq 0 ] && command -v rc-service >/dev/null 2>&1; then
      run_step "stop the OpenRC service" rc-service hostpin-agent stop
      run_step "remove the OpenRC service" rc-update del hostpin-agent default
      remove_file /etc/init.d/hostpin-agent
    else
      echo "No managed Hostpin service was found; removing default user files only."
    fi
    ;;
  darwin)
    label=io.hostpin.agent
    if [ "$uid" -eq 0 ]; then
      run_step "unload the system launch daemon" launchctl bootout "system/$label"
      remove_file /Library/LaunchDaemons/io.hostpin.agent.plist
    else
      run_step "unload the user launch agent" launchctl bootout "gui/$uid/$label"
      remove_file "$home/Library/LaunchAgents/io.hostpin.agent.plist"
    fi
    ;;
  freebsd)
    if [ "$uid" -ne 0 ]; then
      echo "FreeBSD Agent removal must run as root" >&2
      exit 1
    fi
    run_step "stop the FreeBSD service" service hostpin_agent stop
    remove_file /usr/local/etc/rc.d/hostpin_agent
    ;;
  *) echo "Unsupported operating system: $os" >&2; exit 1 ;;
esac

if [ "$os" = darwin ]; then
  install_dir="$home/Library/Application Support/Hostpin"
  binary_path="$install_dir/hostpin-agent"
  config_path="$install_dir/agent.json"
elif [ "$uid" -eq 0 ]; then
  binary_path=/usr/local/bin/hostpin-agent
  config_path=/etc/hostpin/agent.json
else
  binary_path="$home/.local/bin/hostpin-agent"
  config_root=${XDG_CONFIG_HOME:-"$home/.config"}
  case "$config_root" in /*) ;; *) config_root="$home/.config" ;; esac
  config_path="$config_root/hostpin/agent.json"
fi

remove_file "$binary_path"
remove_file "$binary_path.rollback"
if [ "$purge" -eq 1 ]; then
  remove_file "$config_path"
  remove_file /var/log/hostpin-agent.log
  remove_empty_dir "$(dirname "$config_path")"
  if [ "$os" = darwin ]; then remove_empty_dir "$install_dir"; fi
fi

if [ "$dry_run" -eq 1 ]; then
  echo "Hostpin Agent uninstall dry run complete; nothing was changed."
elif [ "$purge" -eq 1 ]; then
  echo "Hostpin Agent and its local identity were removed."
else
  echo "Hostpin Agent was removed. Its identity is preserved at: $config_path"
  echo "Run again with --purge only if you want to remove that identity."
fi
echo "The node and its history remain in the Hostpin panel until you delete them there."
`
	_, _ = fmt.Fprint(w, script)
}

func (a *API) handleUninstallPowerShell(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	script := `param([switch]$Purge, [switch]$DryRun)
$ErrorActionPreference = 'Stop'

function Remove-HostpinFile([string]$Path) {
  if (-not (Test-Path -LiteralPath $Path)) { return }
  if ($DryRun) { Write-Host "Would remove file: $Path"; return }
  Remove-Item -LiteralPath $Path -Force
}

$ProgramFilesRoot = if ($env:ProgramFiles) { $env:ProgramFiles } else { 'C:\Program Files' }
$ProgramDataRoot = if ($env:ProgramData) { $env:ProgramData } else { 'C:\ProgramData' }
$InstallDir = Join-Path $ProgramFilesRoot 'Hostpin'
$ConfigDir = Join-Path $ProgramDataRoot 'Hostpin'
$BinaryPath = Join-Path $InstallDir 'hostpin-agent.exe'
$ConfigPath = Join-Path $ConfigDir 'agent.json'

if (-not $DryRun) {
  $Principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
  if (-not $Principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this command in an Administrator PowerShell window.'
  }
}

$Service = Get-Service -Name 'HostpinAgent' -ErrorAction SilentlyContinue
if ($Service) {
  if ($DryRun) {
    Write-Host 'Would stop and delete Windows service: HostpinAgent'
  } else {
    if ($Service.Status -ne 'Stopped') {
      Stop-Service -Name 'HostpinAgent' -Force
      $Service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(15))
    }
    & sc.exe delete HostpinAgent | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "Could not delete HostpinAgent service (exit $LASTEXITCODE)" }
  }
}

Remove-HostpinFile $BinaryPath
Remove-HostpinFile "$BinaryPath.rollback"
if ($Purge) { Remove-HostpinFile $ConfigPath }

if (-not $DryRun) {
  if ((Test-Path -LiteralPath $InstallDir) -and -not (Get-ChildItem -LiteralPath $InstallDir -Force | Select-Object -First 1)) {
    Remove-Item -LiteralPath $InstallDir -Force
  }
  if ($Purge -and (Test-Path -LiteralPath $ConfigDir) -and -not (Get-ChildItem -LiteralPath $ConfigDir -Force | Select-Object -First 1)) {
    Remove-Item -LiteralPath $ConfigDir -Force
  }
}

if ($DryRun) {
  Write-Host 'Hostpin Agent uninstall dry run complete; nothing was changed.'
} elseif ($Purge) {
  Write-Host 'Hostpin Agent and its local identity were removed.'
} else {
  Write-Host "Hostpin Agent was removed. Its identity is preserved at: $ConfigPath"
  Write-Host 'Run again with -Purge only if you want to remove that identity.'
}
Write-Host 'The node and its history remain in the Hostpin panel until you delete them there.'
`
	_, _ = fmt.Fprint(w, script)
}

func agentReleaseBase(cfg config.Config) string {
	if releaseBase := strings.TrimRight(strings.TrimSpace(cfg.AgentReleaseBase), "/"); releaseBase != "" {
		return releaseBase
	}
	return strings.TrimRight(buildinfo.ReleaseBase, "/")
}
