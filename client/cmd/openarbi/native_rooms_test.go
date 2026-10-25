package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNativeArgsContainOnlyExplicitThreadAndBearerEnvironmentName(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "inherited-api-secret")
	t.Setenv("OPENARBI_TEST_TOKEN", "inherited-api-token")
	t.Setenv("CODEX_HOME", "/global-user-codex")
	info := client.AttachInfo{CodexVersion: client.CodexVersion, Workspace: client.Workspace, ThreadID: "12345678-aaaa-bbbb-cccc-123456789abc", WebSocketToken: "private-worker-bearer"}
	cmd, err := nativeCommand(t.Context(), "/codex", "/private-room-home", info, "ws://127.0.0.1:1234")
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"/codex", "--remote", "ws://127.0.0.1:1234", "--remote-auth-token-env", "OPENARBI_ROOM_BEARER", "resume", info.ThreadID}
	if !slices.Equal(cmd.Args, expected) {
		t.Fatal(cmd.Args)
	}
	for _, arg := range cmd.Args {
		if strings.Contains(arg, "private-worker-bearer") {
			t.Fatal("bearer in argv")
		}
	}
	if !slices.Contains(cmd.Env, "CODEX_HOME=/private-room-home") || !slices.Contains(cmd.Env, "OPENARBI_ROOM_BEARER=private-worker-bearer") {
		t.Fatal("missing isolated environment")
	}
	for _, entry := range cmd.Env {
		if strings.Contains(entry, "inherited-api-") || strings.Contains(entry, "/global-user-codex") {
			t.Fatal("inherited credential/config reached child")
		}
	}
	for _, thread := range []string{"", "--last"} {
		info.ThreadID = thread
		if _, err = nativeCommand(context.Background(), "/codex", "/home", info, "ws://127.0.0.1:1"); err == nil {
			t.Fatal("invalid implicit thread accepted")
		}
	}
}
func TestPinnedCodexVersionRejectsMismatchWithoutFallback(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'codex-cli 0.163.0\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENARBI_CODEX_BIN", binary)
	if _, err := codexBinary(t.Context()); err == nil {
		t.Fatal("version mismatch accepted")
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'codex-cli 0.164.0\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := codexBinary(t.Context()); err != nil || got != binary {
		t.Fatal("pinned version rejected", err)
	}
}
func TestNativeHomeSeparatesServersAndRooms(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	first, err := nativeHome("https://one.invalid", "room-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := nativeHome("https://two.invalid", "room-one")
	if err != nil {
		t.Fatal(err)
	}
	third, err := nativeHome("https://one.invalid", "room-two")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first == third {
		t.Fatal("client homes overlap")
	}
	st, err := os.Stat(first)
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatal("client home not private")
	}
}

func TestInstalledSymlinkFindsCodexInsideOwnedRelease(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "release", "bundle")
	if err := os.MkdirAll(release, 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(release, "openarbi")
	if err := os.WriteFile(executable, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "openarbi")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	got, err := bundledCodexPath(link)
	canonicalRelease, canonicalErr := filepath.EvalSymlinks(release)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	if err != nil || got != filepath.Join(canonicalRelease, "codex") {
		t.Fatal("installed symlink missed bundled Codex", got, err)
	}
	if err := os.WriteFile(got, []byte("#!/bin/sh\nprintf 'codex-cli "+client.CodexVersion+"\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENARBI_CODEX_BIN", "")
	if resolved, err := resolveCodex(t.Context(), link); err != nil || resolved != got {
		t.Fatalf("installed symlink did not select pinned sibling: %q, %v", resolved, err)
	}
}

func TestNativeQueueTargetsExactThreadWithoutBearerInArguments(t *testing.T) {
	info := client.AttachInfo{CodexVersion: client.CodexVersion, Workspace: client.Workspace, ThreadID: "12345678-aaaa-bbbb-cccc-123456789abc", WebSocketToken: "private-queue-bearer"}
	message := "--leading-hyphen follow-up\nwith newline"
	cmd, err := nativeRoomCommand(t.Context(), "/codex", "/private-room-home", info, "ws://127.0.0.1:1234", "queue", message)
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"/codex", "queue", "--remote", "ws://127.0.0.1:1234", "--remote-auth-token-env", "OPENARBI_ROOM_BEARER", "--thread", info.ThreadID, "--message=" + message}
	if !slices.Equal(cmd.Args, expected) {
		t.Fatal("queue did not target exact thread")
	}
	for _, arg := range cmd.Args {
		if strings.Contains(arg, info.WebSocketToken) {
			t.Fatal("bearer leaked into queue arguments")
		}
	}
	if !slices.Contains(cmd.Env, "CODEX_HOME=/private-room-home") || !slices.Contains(cmd.Env, "OPENARBI_ROOM_BEARER="+info.WebSocketToken) {
		t.Fatal("queue did not share isolated attachment environment")
	}
}
func TestInvalidQueueMessageFailsBeforeAnyAPIOrNativeProcess(t *testing.T) {
	for _, message := range []string{"", " \n\t", strings.Repeat("x", maxQueueMessage+1), "contains\x00nul", string([]byte{0xff})} {
		if err := runRooms(t.Context(), nil, "https://fixture.invalid", "queue", []string{"room-one", message}); err == nil {
			t.Fatal("invalid queue message accepted")
		}
	}
	if err := validateQueueMessage(strings.Repeat("x", maxQueueMessage)); err != nil {
		t.Fatal("bounded message rejected", err)
	}
	for _, args := range [][]string{{}, {"room-one"}, {"room-one", "message", "extra"}} {
		if err := runRooms(t.Context(), nil, "https://fixture.invalid", "queue", args); err == nil {
			t.Fatal("invalid queue arguments accepted")
		}
	}
}
func TestNativeQueueProcessStopsOnRevocationAndCancellation(t *testing.T) {
	for _, mode := range []string{"revocation", "parent-cancel", "monitor-closed"} {
		t.Run(mode, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "codex")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0700); err != nil {
				t.Fatal(err)
			}
			parent, cancelParent := context.WithCancel(t.Context())
			defer cancelParent()
			failures := make(chan error, 1)
			ctx, cancel := attachmentContext(parent, failures)
			defer cancel(context.Canceled)
			info := client.AttachInfo{CodexVersion: client.CodexVersion, Workspace: client.Workspace, ThreadID: "12345678-aaaa-bbbb-cccc-123456789abc", WebSocketToken: "private-queue-bearer"}
			cmd, err := nativeRoomCommand(ctx, binary, t.TempDir(), info, "ws://127.0.0.1:1234", "queue", "follow-up")
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			revoked := errors.New("room attachment revoked")
			switch mode {
			case "revocation":
				failures <- revoked
			case "parent-cancel":
				cancelParent()
			case "monitor-closed":
				close(failures)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("revoked/cancelled native queue completed successfully")
				}
			case <-time.After(time.Second):
				t.Fatal("native queue child survived attachment shutdown")
			}
			if mode == "revocation" && !errors.Is(context.Cause(ctx), revoked) {
				t.Fatal("revocation cause lost")
			}
		})
	}
}
func TestPinnedVersionCheckObeysOperationCancellation(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENARBI_CODEX_BIN", binary)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started := time.Now()
	if _, err := codexBinary(ctx); err == nil {
		t.Fatal("canceled native version check succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("version check ignored cancellation")
	}
}

func TestNativeQueuePostLaunchErrorsPreserveUncertaintyAndCause(t *testing.T) {
	exitErr := errors.New("native process failed")
	revoked := errors.New("room attachment revoked")
	monitor := errors.New("room attachment monitoring ended")
	for _, test := range []struct {
		name  string
		cause error
	}{
		{"deadline", context.DeadlineExceeded}, {"parent-cancel", context.Canceled}, {"revocation", revoked}, {"monitor-failure", monitor}, {"native-exit", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := nativeProcessError("queue", exitErr, test.cause)
			if err == nil || !strings.Contains(err.Error(), "may have been accepted") || !strings.Contains(err.Error(), "inspect the room before retrying") {
				t.Fatal("post-launch queue error omitted submission uncertainty", err)
			}
			expected := test.cause
			if expected == nil {
				expected = exitErr
			}
			if !errors.Is(err, expected) {
				t.Fatal("queue failure lost underlying cause", err)
			}
		})
	}
	if err := nativeProcessError("queue", nil, context.Canceled); err != nil {
		t.Fatal("native acknowledgement success was changed to uncertainty", err)
	}
	if err := nativeProcessError("attach", exitErr, revoked); !errors.Is(err, revoked) || strings.Contains(err.Error(), "may have been accepted") {
		t.Fatal("queue uncertainty leaked into attachment error", err)
	}
}

func TestRoomListingSanitizesRemoteTerminalControls(t *testing.T) {
	var output bytes.Buffer
	printRooms(&output, []client.Room{{
		Name:  "arbi\x1b[2J\x1b]52;c;AAAA\a",
		State: "rea\u202edy",
		ID:    "room\x1bPcontrol\x1b\\-id",
	}})
	if got, want := output.String(), "1. arbi\tready\troom-id\n"; got != want {
		t.Fatalf("remote terminal text was not sanitized: %q; want %q", got, want)
	}
}

func TestMissingBundledCodexRefusesGlobalExecutable(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "openarbi")
	if err := os.WriteFile(executable, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	globalBin := filepath.Join(root, "global")
	if err := os.Mkdir(globalBin, 0700); err != nil {
		t.Fatal(err)
	}
	globalCodex := filepath.Join(globalBin, "codex")
	if err := os.WriteFile(globalCodex, []byte("#!/bin/sh\nprintf 'codex-cli "+client.CodexVersion+"\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", globalBin)
	t.Setenv("OPENARBI_CODEX_BIN", "")
	if got, err := resolveCodex(t.Context(), executable); err == nil || got != "" || !strings.Contains(err.Error(), "bundled Codex executable is missing") || !strings.Contains(err.Error(), "OPENARBI_CODEX_BIN") {
		t.Fatalf("missing bundle did not fail with repair instructions: %q, %v", got, err)
	}
	// Development builds can select a global installation only by explicit opt-in.
	t.Setenv("OPENARBI_CODEX_BIN", "codex")
	if got, err := resolveCodex(t.Context(), executable); err != nil || got != globalCodex {
		t.Fatalf("explicit Codex override was rejected: %q, %v", got, err)
	}
}
