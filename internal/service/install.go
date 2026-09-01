package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func Install(binaryPath, configPath string) error {
	switch runtime.GOOS {
	case "linux":
		return installLinux(binaryPath, configPath)
	case "darwin":
		return installLaunchd(binaryPath, configPath)
	case "freebsd":
		return installFreeBSD(binaryPath, configPath)
	case "windows":
		return installWindows(binaryPath, configPath)
	default:
		return fmt.Errorf("automatic service installation is unsupported on %s", runtime.GOOS)
	}
}

func installLinux(binaryPath, configPath string) error {
	if _, err := exec.LookPath("systemctl"); err == nil {
		return installSystemd(binaryPath, configPath)
	}
	if isRoot() && fileExists("/sbin/procd") {
		return installProcd(binaryPath, configPath)
	}
	if isRoot() && fileExists("/sbin/openrc-run") {
		return installOpenRC(binaryPath, configPath)
	}
	return errors.New("no supported service manager found; rerun with --no-service and supervise `hostpin-agent run` manually")
}

func installSystemd(binaryPath, configPath string) error {
	unit := `[Unit]
Description=Hostpin monitoring agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + systemdQuote(binaryPath) + ` run --config ` + systemdQuote(configPath) + `
Restart=always
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=read-only
ProtectSystem=strict
ReadWritePaths=` + systemdQuote(filepath.Dir(configPath)) + ` ` + systemdQuote(filepath.Dir(binaryPath)) + `

[Install]
WantedBy=multi-user.target
`
	args := []string{}
	unitPath := "/etc/systemd/system/hostpin-agent.service"
	if !isRoot() {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		unitPath = filepath.Join(home, ".config", "systemd", "user", "hostpin-agent.service")
		unit = strings.Replace(unit, "WantedBy=multi-user.target", "WantedBy=default.target", 1)
		args = append(args, "--user")
	}
	if err := writeServiceFile(unitPath, unit, 0o644); err != nil {
		return err
	}
	if err := run("systemctl", append(args, "daemon-reload")...); err != nil {
		return err
	}
	return run("systemctl", append(args, "enable", "--now", "hostpin-agent.service")...)
}

func installOpenRC(binaryPath, configPath string) error {
	script := `#!/sbin/openrc-run
name="Hostpin agent"
description="Hostpin monitoring agent"
command=` + shellQuote(binaryPath) + `
command_args="run --config ` + shellQuote(configPath) + `"
command_background=true
pidfile="/run/hostpin-agent.pid"
output_log="/var/log/hostpin-agent.log"
error_log="/var/log/hostpin-agent.log"
depend() { need net; }
`
	if err := writeServiceFile("/etc/init.d/hostpin-agent", script, 0o755); err != nil {
		return err
	}
	if err := run("rc-update", "add", "hostpin-agent", "default"); err != nil {
		return err
	}
	return run("rc-service", "hostpin-agent", "restart")
}

func installProcd(binaryPath, configPath string) error {
	script := `#!/bin/sh /etc/rc.common
START=95
USE_PROCD=1
start_service() {
  procd_open_instance
  procd_set_param command ` + shellQuote(binaryPath) + ` run --config ` + shellQuote(configPath) + `
  procd_set_param respawn 3600 5 5
  procd_set_param stdout 1
  procd_set_param stderr 1
  procd_close_instance
}
`
	if err := writeServiceFile("/etc/init.d/hostpin-agent", script, 0o755); err != nil {
		return err
	}
	if err := run("/etc/init.d/hostpin-agent", "enable"); err != nil {
		return err
	}
	return run("/etc/init.d/hostpin-agent", "restart")
}

func installLaunchd(binaryPath, configPath string) error {
	label := "io.hostpin.agent"
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>` + label + `</string>
<key>ProgramArguments</key><array><string>` + xmlEscape(binaryPath) + `</string><string>run</string><string>--config</string><string>` + xmlEscape(configPath) + `</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>ProcessType</key><string>Background</string>
</dict></plist>
`
	var path, domain string
	if isRoot() {
		path, domain = "/Library/LaunchDaemons/io.hostpin.agent.plist", "system"
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, "Library", "LaunchAgents", "io.hostpin.agent.plist")
		domain = "gui/" + strconv.Itoa(currentUID())
	}
	if err := writeServiceFile(path, plist, 0o644); err != nil {
		return err
	}
	_ = run("launchctl", "bootout", domain+"/"+label)
	if err := run("launchctl", "bootstrap", domain, path); err != nil {
		return err
	}
	return run("launchctl", "enable", domain+"/"+label)
}

func installFreeBSD(binaryPath, configPath string) error {
	if !isRoot() {
		return errors.New("FreeBSD rc.d installation requires root")
	}
	script := `#!/bin/sh
# PROVIDE: hostpin_agent
# REQUIRE: NETWORKING
# KEYWORD: shutdown
. /etc/rc.subr
name="hostpin_agent"
rcvar="hostpin_agent_enable"
command=` + shellQuote(binaryPath) + `
command_args="run --config ` + shellQuote(configPath) + `"
pidfile="/var/run/${name}.pid"
command_background="yes"
load_rc_config "$name"
: ${hostpin_agent_enable:="YES"}
run_rc_command "$1"
`
	if err := writeServiceFile("/usr/local/etc/rc.d/hostpin_agent", script, 0o755); err != nil {
		return err
	}
	return run("service", "hostpin_agent", "restart")
}

func installWindows(binaryPath, configPath string) error {
	command := strconv.Quote(binaryPath) + " run --config " + strconv.Quote(configPath)
	_ = run("sc.exe", "stop", "HostpinAgent")
	_ = run("sc.exe", "delete", "HostpinAgent")
	if err := run("sc.exe", "create", "HostpinAgent", "binPath=", command, "start=", "auto", "DisplayName=", "Hostpin Agent"); err != nil {
		return err
	}
	_ = run("sc.exe", "description", "HostpinAgent", "Hostpin host monitoring agent")
	_ = run("sc.exe", "failure", "HostpinAgent", "reset=", "86400", "actions=", "restart/5000/restart/15000/restart/60000")
	return run("sc.exe", "start", "HostpinAgent")
}

func writeServiceFile(path, contents string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(contents), mode); err != nil {
		return err
	}
	if err := os.Chmod(temporary, mode); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func run(name string, arguments ...string) error {
	command := exec.Command(name, arguments...)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func systemdQuote(value string) string {
	return strconv.Quote(strings.ReplaceAll(value, "%", "%%"))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}
