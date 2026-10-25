// openarbi-installer installs verified public desktop releases without touching
// credentials during update/removal. Shell PATH changes use owned removable blocks.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/hustLer2k/openarbi-kitty/client/internal/desktop"
	"github.com/hustLer2k/openarbi-kitty/client/internal/install"
	"github.com/hustLer2k/openarbi-kitty/client/internal/terminaltext"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "OpenArbi:", terminaltext.Sanitize(err.Error()))
		os.Exit(1)
	}
}
func run() error {
	flags := flag.NewFlagSet("openarbi-installer", flag.ContinueOnError)
	version := flags.String("version", "", "explicit release version")
	url := flags.String("url", "", "HTTPS release archive URL")
	digest := flags.String("sha256", "", "release archive SHA-256")
	archive := flags.String("archive", "", "local archive for offline installation")
	legacyPrefix := flags.String("legacy-prefix", "", "explicit previous managed CLI prefix (default user data directory)")
	releaseBase := flags.String("release-base", install.ReleaseBase, "versioned HTTPS release repository")
	noSound := flags.Bool("no-sound", false, "skip completion chime")
	noConnect := flags.Bool("no-connect", false, "install without login or launch")
	uninstall := flags.Bool("uninstall", false, "remove managed software; retain credentials and workspace state")
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "uninstall":
			args = append([]string{"--uninstall"}, args[1:]...)
		case "update":
			args = append([]string{"--no-connect"}, args[1:]...)
		}
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected installer arguments")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return errors.New("supported platforms: Linux amd64 and macOS arm64")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	o := install.Options{Root: filepath.Join(home, ".local/share/openarbi"), Bin: filepath.Join(home, ".local/bin/openarbi"), Version: *version, SHA256: *digest, Progress: progress}
	if runtime.GOOS == "darwin" {
		o.Root = filepath.Join(home, "Library/Application Support/OpenArbi/install")
		o.App = filepath.Join(home, "Applications/OpenArbi.app")
	}
	if *legacyPrefix != "" {
		o.LegacyRoot = *legacyPrefix
	} else {
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local/share")
		}
		o.LegacyRoot = filepath.Join(data, "openarbi")
	}
	if !filepath.IsAbs(o.LegacyRoot) {
		return errors.New("legacy CLI prefix must be absolute")
	}
	if *uninstall {
		current, e := install.CurrentRelease(o.Root)
		if e != nil {
			return e
		}
		directory, e := desktopDirectory()
		if e != nil {
			return e
		}
		held, e := upgradeLock(directory)
		if e != nil {
			return e
		}
		defer held.Close()
		uninstallCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		running, e := desktop.BrokerRunning(directory)
		if e != nil {
			return e
		}
		if e = reconcileGUI(uninstallCtx, current, directory, running); e != nil {
			return e
		}
		if e = brokerCommand(uninstallCtx, current, directory, "shutdown"); e != nil {
			return e
		}
		if err = install.RemovePATH(o.Root); err != nil {
			return err
		}
		if err = install.Uninstall(o); err != nil {
			return err
		}
		if runtime.GOOS == "linux" {
			name := filepath.Join(home, ".local/share/applications/openarbi.desktop")
			if raw, e := os.ReadFile(name); e == nil && strings.HasPrefix(string(raw), "# Managed by OpenArbi installer\n") {
				if e = os.Remove(name); e != nil {
					return e
				}
			}
		}
		fmt.Fprintln(os.Stderr, "OpenArbi removed. Credentials, local workspace state and remote rooms were preserved.")
		return nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	progress("checking", "Checking this computer")
	if !*noConnect {
		if _, err = exec.LookPath("ssh"); err != nil {
			return errors.New("install OpenSSH and configure the revenge alias in ~/.ssh/config first")
		}
		if err = checkAlias(ctx); err != nil {
			return err
		}
	}
	var tty *os.File
	if !*noConnect {
		tty, err = os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			return errors.New("login requires a controlling terminal; use --no-connect then open OpenArbi from a terminal")
		}
		defer tty.Close()
	}
	if runtime.GOOS == "linux" && !*noConnect {
		if err = checkLinux(ctx, tty); err != nil {
			return err
		}
	}
	archivePath := *archive
	if archivePath == "" && *url == "" {
		resolved, pkg, e := (install.Resolver{Base: *releaseBase}).Resolve(ctx, *version, runtime.GOOS+"-"+runtime.GOARCH)
		if e != nil {
			return e
		}
		*version = resolved
		*url = pkg.URL
		*digest = pkg.SHA256
		o.Version = resolved
		o.SHA256 = pkg.SHA256
	}
	if archivePath == "" {
		temp, err := os.MkdirTemp("", "openarbi-download-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temp)
		archivePath = filepath.Join(temp, "release.tar.gz")
		progress("downloading", "Fetching "+*version)
		if err = install.Download(ctx, *url, *digest, archivePath); err != nil {
			return err
		}
	}
	o.Validate = func(ctx context.Context, stage string) error {
		// Exercise native loading before switching the active release.
		for _, binary := range []string{"openarbi-client", "codex", "starship", "kitty"} {
			argument := "--version"
			if binary == "openarbi-client" {
				argument = "--help"
			}
			if err := exec.CommandContext(ctx, filepath.Join(stage, "bin", binary), argument).Run(); err != nil {
				return fmt.Errorf("%s could not start: %w", binary, err)
			}
		}
		return nil
	}
	desktopDir, err := desktopDirectory()
	if err != nil {
		return err
	}
	var guiLock *os.File
	defer func() {
		if guiLock != nil {
			_ = guiLock.Close()
		}
	}()
	config, e := os.UserConfigDir()
	if e != nil {
		return e
	}
	brokerWasRunning := false
	o.BeforeActivate = func(ctx context.Context, stage, previous string) (func() error, error) {
		guiLock, err = upgradeLock(desktopDir)
		if err != nil {
			return nil, err
		}
		if previous != "" {
			brokerWasRunning, err = desktop.BrokerRunning(desktopDir)
			if err != nil {
				return nil, err
			}
			if err = reconcileGUI(ctx, previous, desktopDir, brokerWasRunning); err != nil {
				return nil, err
			}
			if err = brokerCommand(ctx, previous, desktopDir, "shutdown"); err != nil {
				return nil, err
			}
		}
		restore, e := retireLegacyTunnel(ctx, home, config, o.Root, runtime.GOOS, serviceCommand)
		restartOld := func() error {
			if brokerWasRunning {
				return restartBroker(previous, desktopDir)
			}
			return nil
		}
		if e != nil {
			return nil, errors.Join(e, restartOld())
		}
		return func() error {
			var e error
			if restore != nil {
				e = restore()
			}
			return errors.Join(e, restartOld())
		}, nil
	}
	o.AfterActivate = func(ctx context.Context, stage string) error {
		if brokerWasRunning {
			if e := brokerCommand(ctx, stage, desktopDir, "ensure"); e != nil {
				cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				return errors.Join(e, brokerCommand(cleanup, stage, desktopDir, "shutdown"))
			}
		}
		return nil
	}
	stage, err := install.InstallArchive(ctx, o, archivePath)
	if err != nil {
		return err
	}
	if guiLock != nil {
		if err = guiLock.Close(); err != nil {
			return err
		}
		guiLock = nil
	}
	if err = install.ConfigurePATH(home, os.Getenv("ZDOTDIR"), o.Root, o.Bin); err != nil {
		return fmt.Errorf("installed, but PATH setup failed: %w", err)
	}
	if err = desktopEntry(o); err != nil {
		fmt.Fprintln(os.Stderr, "OpenArbi installed; desktop shortcut could not be created:", terminaltext.Sanitize(err.Error()))
	}
	if !*noConnect {
		progress("connecting", "Connecting through revenge")
		if err = command(ctx, tty, filepath.Join(stage, "bin/openarbi-desktop"), "--server", "http://127.0.0.1:8090", "ensure"); err != nil {
			return fmt.Errorf("installed; broker startup failed: %w", err)
		}
		config, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		if err = command(ctx, tty, filepath.Join(stage, "bin/openarbi-client"), "--server", "http://127.0.0.1:8090", "--desktop-socket", filepath.Join(config, "OpenArbi/desktop/broker.sock"), "login"); err != nil {
			return fmt.Errorf("installed; login failed, rerun the login step: %w", err)
		}
		if err = command(ctx, tty, filepath.Join(stage, "bin/openarbi")); err != nil {
			return fmt.Errorf("installed; desktop launch failed: %w", err)
		}
	}
	progress("ready", "OpenArbi is installed")
	if !*noSound {
		if err = playSound(ctx, filepath.Join(stage, "share/openarbi/assets/ready.wav")); err != nil {
			fmt.Fprintln(os.Stderr, "Completion sound unavailable; installation succeeded.")
		}
	}
	fmt.Fprintln(os.Stderr, "Launch:", o.Bin)
	if !strings.Contains(":"+os.Getenv("PATH")+":", ":"+filepath.Dir(o.Bin)+":") {
		fmt.Fprintln(os.Stderr, "Open a new terminal to use openarbi from PATH.")
	}
	return nil
}
func progress(phase, detail string) { fmt.Fprintf(os.Stderr, "%-12s %s\n", phase, detail) }
func command(ctx context.Context, tty *os.File, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	return cmd.Run()
}
func checkAlias(ctx context.Context) error {
	output, err := exec.CommandContext(ctx, "ssh", "-G", "revenge").Output()
	if err != nil {
		return errors.New("could not read SSH configuration; configure and test ssh revenge first")
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "hostname ") && strings.TrimPrefix(line, "hostname ") != "revenge" {
			return nil
		}
	}
	return errors.New("SSH alias revenge is not configured; configure ~/.ssh/config and verify ssh revenge first")
}
func checkLinux(ctx context.Context, tty *os.File) error {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("no graphical session found; run from your desktop terminal or use --no-connect")
	}
	output, err := exec.CommandContext(ctx, "ldconfig", "-p").Output()
	if err != nil {
		return errors.New("could not inspect graphical libraries with ldconfig; install libc-bin or use --no-connect")
	}
	var missing []string
	for _, lib := range []string{"libGL.so.1", "libEGL.so.1", "libX11.so.6", "libxkbcommon.so.0"} {
		if !strings.Contains(string(output), lib) {
			missing = append(missing, lib)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	fmt.Fprintln(tty, "Missing graphical libraries:", strings.Join(missing, ", "))
	fmt.Fprintln(tty, "Ubuntu/Debian packages: libgl1 libegl1 libx11-6 libxkbcommon0")
	if _, err = exec.LookPath("apt-get"); err != nil {
		return errors.New("install the listed graphical libraries with your system package manager and retry")
	}
	fmt.Fprint(tty, "Install these packages with sudo apt-get now? [y/N] ")
	answer, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(strings.ToLower(answer)) != "y" {
		return errors.New("install the listed graphical libraries and retry")
	}
	return command(ctx, tty, "sudo", "apt-get", "install", "libgl1", "libegl1", "libx11-6", "libxkbcommon0")
}
func playSound(ctx context.Context, file string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "afplay", file).Run()
	case "linux":
		for _, name := range []string{"paplay", "aplay"} {
			if _, err := exec.LookPath(name); err == nil {
				return exec.CommandContext(ctx, name, file).Run()
			}
		}
		return errors.New("no audio player")
	default:
		return errors.New("unsupported audio platform")
	}
}
func desktopEntry(o install.Options) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	name := filepath.Join(home, ".local/share/applications/openarbi.desktop")
	const signature = "# Managed by OpenArbi installer\n"
	if info, e := os.Lstat(name); e == nil && !info.Mode().IsRegular() {
		return errors.New("desktop shortcut is not a regular managed file; preserved")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if existing, e := os.ReadFile(name); e == nil && !strings.HasPrefix(string(existing), signature) {
		return errors.New("existing desktop shortcut is unrelated; preserved")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if strings.ContainsAny(o.Bin, "\n\r") {
		return errors.New("desktop path contains newline")
	}
	quote := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", "\\$", "%", "%%").Replace(o.Bin)
	body := signature + "[Desktop Entry]\nType=Application\nName=OpenArbi\nComment=Remote Codex workspaces\nTerminal=false\nCategories=Development;TerminalEmulator;\nExec=\"" + quote + "\"\n"
	if err = os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return err
	}
	return os.WriteFile(name, []byte(body), 0644)
}
