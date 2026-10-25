package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const serviceTimeout = 20 * time.Second
const legacyUnit = "openarbi-revenge-tunnel.service"
const legacyLabel = "io.openarbi.revenge-tunnel"

func legacySSH(executable string) []string {
	return []string{executable, "-N", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-o", "ConnectTimeout=10", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ForkAfterAuthentication=no", "-L", "127.0.0.1:29090:127.0.0.1:29090", "-L", "127.0.0.1:29222:127.0.0.1:29222", "revenge"}
}

// The original macOS managed template predates the explicit foreground option.
// Keep its exact argv separate: arbitrary option removal is not ownership proof.
func legacyMacOriginalSSH() []string {
	return []string{"/usr/bin/ssh", "-N", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-o", "ConnectTimeout=10", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-L", "127.0.0.1:29090:127.0.0.1:29090", "-L", "127.0.0.1:29222:127.0.0.1:29222", "revenge"}
}
func legacyUnitText(ssh string) string {
	return "[Unit]\nDescription=OpenArbi private revenge tunnel\nAfter=network-online.target\n\n[Service]\nType=simple\nExecStart=" + strings.Join(legacySSH(ssh), " ") + "\nRestart=always\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n"
}

type serviceRunner func(context.Context, string, ...string) ([]byte, error)

func serviceCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, serviceTimeout)
	defer cancel()
	return exec.CommandContext(bounded, name, args...).CombinedOutput()
}

// retireLegacyTunnel checks both the installed definition and the loaded process
// before stopping a supervisor. It never searches for or kills arbitrary SSH PIDs.
func retireLegacyTunnel(ctx context.Context, home, config, root, platform string, run serviceRunner) (func() error, error) {
	var path string
	switch platform {
	case "linux":
		path = filepath.Join(config, "systemd/user", legacyUnit)
	case "darwin":
		path = filepath.Join(home, "Library/LaunchAgents", legacyLabel+".plist")
	default:
		return nil, errors.New("unsupported legacy tunnel platform")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("legacy tunnel definition is not an owned regular file; preserved")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	active, enabled := false, false
	if platform == "linux" {
		var ssh string
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "ExecStart=") {
				fields := strings.Fields(strings.TrimPrefix(line, "ExecStart="))
				if len(fields) > 0 {
					ssh = fields[0]
				}
			}
		}
		if !filepath.IsAbs(ssh) || filepath.Base(ssh) != "ssh" || string(data) != legacyUnitText(ssh) {
			return nil, errors.New("legacy tunnel unit differs from its managed template; preserved")
		}
		output, err := run(ctx, "systemctl", "--user", "show", legacyUnit, "--property=FragmentPath,DropInPaths,MainPID,ActiveState,UnitFileState")
		if err != nil {
			return nil, errors.New("could not inspect legacy systemd tunnel; preserved")
		}
		fields := map[string]string{}
		for _, line := range strings.Split(string(output), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				fields[k] = v
			}
		}
		if fields["FragmentPath"] != path || fields["DropInPaths"] != "" {
			return nil, errors.New("loaded legacy unit has unexpected source or overrides; preserved")
		}
		active = fields["ActiveState"] == "active"
		switch fields["UnitFileState"] {
		case "enabled":
			enabled = true
		case "disabled":
			enabled = false
		default:
			return nil, errors.New("legacy tunnel enablement state is ambiguous; preserved")
		}
		if fields["ActiveState"] != "active" && fields["ActiveState"] != "inactive" && fields["ActiveState"] != "failed" {
			return nil, errors.New("legacy tunnel has an ambiguous active state; retry after it settles")
		}
		pid, err := strconv.Atoi(fields["MainPID"])
		if err != nil || pid < 0 {
			return nil, errors.New("invalid legacy tunnel PID")
		}
		if pid != 0 {
			process := filepath.Join("/proc", strconv.Itoa(pid))
			pi, e := os.Stat(process)
			if e != nil {
				return nil, e
			}
			ps, ok := pi.Sys().(*syscall.Stat_t)
			if !ok || ps.Uid != uint32(os.Getuid()) {
				return nil, errors.New("legacy tunnel process owner differs")
			}
			argv, e := os.ReadFile(filepath.Join(process, "cmdline"))
			if e != nil || !bytes.Equal(argv, []byte(strings.Join(legacySSH(ssh), "\x00")+"\x00")) {
				return nil, errors.New("loaded legacy tunnel process does not match; preserved")
			}
		} else if active {
			return nil, errors.New("active legacy tunnel has no verified process")
		}
	} else {
		expectedArgs, err := validateLegacyPlist(data, home)
		if err != nil {
			return nil, err
		}
		output, e := run(ctx, "launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), legacyLabel))
		if e != nil {
			if !strings.Contains(string(output), "Could not find service") {
				return nil, errors.New("could not inspect legacy LaunchAgent; preserved")
			}
		} else {
			active = true
			program := regexp.MustCompile(`(?m)^\s*program = /usr/bin/ssh\s*$`)
			args := regexp.MustCompile(`(?ms)^\s*arguments = \{\n(.*?)^\s*\}`).FindSubmatch(output)
			if !program.Match(output) || len(args) != 2 {
				return nil, errors.New("legacy LaunchAgent loaded program differs; preserved")
			}
			var actual []string
			for _, line := range strings.Split(string(args[1]), "\n") {
				if value := strings.TrimSpace(line); value != "" {
					actual = append(actual, value)
				}
			}
			if !slices.Equal(actual, expectedArgs) {
				return nil, errors.New("legacy LaunchAgent loaded arguments differ; preserved")
			}
		}
	}
	backups := filepath.Join(root, "legacy-backups")
	if err = os.MkdirAll(backups, 0700); err != nil {
		return nil, err
	}
	backup, err := os.CreateTemp(backups, filepath.Base(path)+"-")
	if err != nil {
		return nil, err
	}
	backupName := backup.Name()
	if _, err = backup.Write(data); err != nil {
		backup.Close()
		return nil, err
	}
	if err = backup.Close(); err != nil {
		return nil, err
	}
	restore := func() error {
		if e := os.WriteFile(path, data, info.Mode().Perm()); e != nil {
			return e
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), serviceTimeout)
		defer cancel()
		if platform == "linux" {
			if _, e := run(rollbackCtx, "systemctl", "--user", "daemon-reload"); e != nil {
				return e
			}
			if enabled {
				if _, e := run(rollbackCtx, "systemctl", "--user", "enable", legacyUnit); e != nil {
					return e
				}
			}
			if active {
				_, e := run(rollbackCtx, "systemctl", "--user", "start", legacyUnit)
				return e
			}
		} else if active {
			_, e := run(rollbackCtx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), path)
			return e
		}
		return nil
	}
	if platform == "linux" {
		_, err = run(ctx, "systemctl", "--user", "disable", "--now", legacyUnit)
	} else if active {
		_, err = run(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), legacyLabel))
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("could not stop managed legacy tunnel; backup at %s", backupName), restore())
	}
	if err = os.Remove(path); err != nil {
		return nil, errors.Join(err, restore())
	}
	if platform == "linux" {
		if _, err = run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return nil, errors.Join(err, restore())
		}
	}
	return restore, nil
}

type plistNode struct {
	XMLName  xml.Name
	Text     string      `xml:",chardata"`
	Children []plistNode `xml:",any"`
}

func plistValue(n plistNode) (any, error) {
	switch n.XMLName.Local {
	case "string":
		return n.Text, nil
	case "true":
		return true, nil
	case "integer":
		return strconv.Atoi(strings.TrimSpace(n.Text))
	case "array":
		values := []string{}
		for _, child := range n.Children {
			if child.XMLName.Local != "string" {
				return nil, errors.New("unexpected plist array value")
			}
			values = append(values, child.Text)
		}
		return values, nil
	default:
		return nil, errors.New("unsupported legacy plist value")
	}
}
func validateLegacyPlist(data []byte, home string) ([]string, error) {
	var root plistNode
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, errors.New("invalid legacy LaunchAgent; preserved")
	}
	if root.XMLName.Local != "plist" || len(root.Children) != 1 || root.Children[0].XMLName.Local != "dict" {
		return nil, errors.New("invalid legacy LaunchAgent structure")
	}
	nodes := root.Children[0].Children
	if len(nodes)%2 != 0 {
		return nil, errors.New("invalid legacy LaunchAgent dictionary")
	}
	actual := map[string]any{}
	for i := 0; i < len(nodes); i += 2 {
		if nodes[i].XMLName.Local != "key" {
			return nil, errors.New("invalid legacy LaunchAgent key")
		}
		key := nodes[i].Text
		if _, exists := actual[key]; exists {
			return nil, errors.New("duplicate legacy LaunchAgent key")
		}
		value, err := plistValue(nodes[i+1])
		if err != nil {
			return nil, err
		}
		actual[key] = value
	}
	logs := filepath.Join(home, "Library/Logs/OpenArbi")
	expected := map[string]any{"Label": legacyLabel, "ProgramArguments": legacySSH("/usr/bin/ssh"), "RunAtLoad": true, "KeepAlive": true, "ThrottleInterval": 10, "ProcessType": "Background", "StandardOutPath": filepath.Join(logs, "revenge-tunnel.stdout.log"), "StandardErrorPath": filepath.Join(logs, "revenge-tunnel.stderr.log")}
	for _, argv := range [][]string{legacySSH("/usr/bin/ssh"), legacyMacOriginalSSH()} {
		expected["ProgramArguments"] = argv
		if reflect.DeepEqual(actual, expected) {
			return argv, nil
		}
	}
	return nil, errors.New("legacy LaunchAgent differs from managed templates; preserved")
}
