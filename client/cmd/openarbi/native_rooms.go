package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
	"github.com/hustLer2k/openarbi-kitty/client/internal/roomtransport"
	"github.com/hustLer2k/openarbi-kitty/client/internal/terminaltext"
	"golang.org/x/term"
)

func runRooms(ctx context.Context, api *client.Client, origin, command string, args []string) error {
	switch command {
	case "":
		if len(args) != 0 {
			return errors.New("room launcher takes no arguments")
		}
		rooms, err := api.Rooms(ctx)
		if err != nil {
			return err
		}
		if len(rooms) == 0 {
			fmt.Fprintln(os.Stdout, "No rooms. Create one with: openarbi create NAME")
			return nil
		}
		printRooms(os.Stdout, rooms)
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return nil
		}
		fmt.Fprint(os.Stdout, "\nAttach to room number (Enter to exit): ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return nil
		}
		choice, err := strconv.Atoi(line)
		if err != nil || choice < 1 || choice > len(rooms) {
			return errors.New("invalid room number")
		}
		return attachNative(ctx, api, origin, rooms[choice-1].ID)
	case "rooms":
		if len(args) != 0 {
			return errors.New("rooms takes no arguments")
		}
		rooms, err := api.Rooms(ctx)
		if err != nil {
			return err
		}
		printRooms(os.Stdout, rooms)
		return nil
	case "create":
		if len(args) != 1 {
			return errors.New("usage: openarbi create NAME")
		}
		room, err := api.CreateRoom(ctx, client.CreateRequest{Name: args[0], IdempotencyKey: uuid.NewString()})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Room %s (%s): %s\nAttach: openarbi attach %s\n", terminaltext.Sanitize(room.Name), terminaltext.Sanitize(room.ID), terminaltext.Sanitize(room.State), terminaltext.Sanitize(room.Name))
		return nil
	case "attach":
		if len(args) != 1 {
			return errors.New("usage: openarbi attach NAME_OR_ID")
		}
		return attachNative(ctx, api, origin, args[0])
	case "queue":
		if len(args) != 2 {
			return errors.New("usage: openarbi queue NAME_OR_ID MESSAGE (quote the message as one argument)")
		}
		if err := validateQueueMessage(args[1]); err != nil {
			return err
		}
		return runNative(ctx, api, origin, args[0], "queue", args[1])
	case "stop", "resume":
		if len(args) != 1 {
			return errors.New("usage: openarbi " + command + " NAME_OR_ID")
		}
		var room client.Room
		var err error
		if command == "stop" {
			room, err = api.StopRoom(ctx, args[0])
		} else {
			room, err = api.ResumeRoom(ctx, args[0])
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "%s: %s\n", terminaltext.Sanitize(room.Name), terminaltext.Sanitize(room.State))
		return nil
	case "members":
		if len(args) != 1 && len(args) != 3 {
			return errors.New("usage: openarbi members ROOM [add|remove IDENTITY_ID]")
		}
		var members []client.Member
		var err error
		if len(args) == 1 {
			members, err = api.RoomMembers(ctx, args[0])
		} else {
			switch args[1] {
			case "add":
				members, err = api.AddRoomMember(ctx, args[0], args[2])
			case "remove":
				members, err = api.RemoveRoomMember(ctx, args[0], args[2])
			default:
				return errors.New("membership operation must be add or remove")
			}
		}
		if err != nil {
			return err
		}
		for _, m := range members {
			role := "member"
			if m.Owner {
				role = "owner"
			}
			fmt.Fprintf(os.Stdout, "%s\t%s\n", terminaltext.Sanitize(m.IdentityID), role)
		}
		return nil
	default:
		return errors.New("unsupported room command")
	}
}
func printRooms(w io.Writer, rooms []client.Room) {
	for i, r := range rooms {
		fmt.Fprintf(w, "%d. %s\t%s\t%s\n", i+1, terminaltext.Sanitize(r.Name), terminaltext.Sanitize(r.State), terminaltext.Sanitize(r.ID))
	}
}

func bundledCodexPath(executable string) (string, error) {
	actual, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", errors.New("cannot resolve OpenArbi executable")
	}
	return filepath.Join(filepath.Dir(actual), "codex"), nil
}
func codexBinary(ctx context.Context) (string, error) {
	var executable string
	if os.Getenv("OPENARBI_CODEX_BIN") == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return "", errors.New("cannot locate OpenArbi executable")
		}
	}
	return resolveCodex(ctx, executable)
}
func resolveCodex(ctx context.Context, executable string) (string, error) {
	path := os.Getenv("OPENARBI_CODEX_BIN")
	if path == "" {
		bundled, err := bundledCodexPath(executable)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(bundled); err == nil {
			st, err := os.Stat(bundled)
			if err != nil || !st.Mode().IsRegular() {
				return "", errors.New("bundled Codex executable is invalid")
			}
			path = bundled
		} else if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("bundled Codex executable is missing; reinstall OpenArbi, or set OPENARBI_CODEX_BIN explicitly for source/development use")
		} else {
			return "", errors.New("cannot inspect bundled Codex executable")
		}
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("native Codex %s required; install the pinned package or set OPENARBI_CODEX_BIN", client.CodexVersion)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, resolved, "--version").Output()
	if err != nil || strings.TrimSpace(string(output)) != "codex-cli "+client.CodexVersion {
		return "", fmt.Errorf("native Codex version must be exactly %s", client.CodexVersion)
	}
	return resolved, nil
}

const maxQueueMessage = 64 << 10

func validateQueueMessage(message string) error {
	if strings.TrimSpace(message) == "" || len(message) > maxQueueMessage || strings.ContainsRune(message, 0) || !utf8.ValidString(message) {
		return errors.New("queue message must be valid nonempty text of at most 65536 bytes without NUL")
	}
	return nil
}
func nativeCommand(ctx context.Context, binary, home string, info client.AttachInfo, address string) (*exec.Cmd, error) {
	return nativeRoomCommand(ctx, binary, home, info, address, "attach", "")
}
func nativeRoomCommand(ctx context.Context, binary, home string, info client.AttachInfo, address, operation, message string) (*exec.Cmd, error) {
	thread, threadErr := uuid.Parse(info.ThreadID)
	if threadErr != nil || thread.String() != info.ThreadID || info.WebSocketToken == "" || info.CodexVersion != client.CodexVersion || info.Workspace != client.Workspace {
		return nil, errors.New("incompatible or incomplete native room attachment")
	}
	var args []string
	switch operation {
	case "attach":
		args = []string{"--remote", address, "--remote-auth-token-env", "OPENARBI_ROOM_BEARER", "resume", info.ThreadID}
	case "queue":
		if err := validateQueueMessage(message); err != nil {
			return nil, err
		}
		// The equals form keeps leading-hyphen message text from becoming CLI options.
		args = []string{"queue", "--remote", address, "--remote-auth-token-env", "OPENARBI_ROOM_BEARER", "--thread", info.ThreadID, "--message=" + message}
	default:
		return nil, errors.New("unsupported native room operation")
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	// Only the environment variable name appears in argv. Inherited Codex settings
	// and credentials never leak into this isolated remote client home.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "CODEX_") && name != "OPENARBI_ROOM_BEARER" && name != "OPENARBI_TEST_TOKEN" && !strings.HasPrefix(name, "OPENAI_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "CODEX_HOME="+home, "OPENARBI_ROOM_BEARER="+info.WebSocketToken)
	return cmd, nil
}
func nativeHome(origin, roomID string) (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	originHash := sha256.Sum256([]byte(origin))
	roomHash := sha256.Sum256([]byte(roomID))
	home := filepath.Join(root, "openarbi", "native", fmt.Sprintf("%x", originHash[:16]), fmt.Sprintf("%x", roomHash[:16]))
	if err = os.MkdirAll(home, 0700); err != nil {
		return "", errors.New("cannot create private native client home")
	}
	if err = os.Chmod(home, 0700); err != nil {
		return "", err
	}
	return home, nil
}
func attachNative(ctx context.Context, api *client.Client, origin, id string) error {
	return runNative(ctx, api, origin, id, "attach", "")
}
func attachmentContext(parent context.Context, failures <-chan error) (context.Context, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	go func() {
		select {
		case err, ok := <-failures:
			if !ok || err == nil {
				err = errors.New("room attachment monitoring ended")
			}
			cancel(err)
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
func runNative(ctx context.Context, api *client.Client, origin, id, operation, message string) error {
	if operation == "queue" {
		if err := validateQueueMessage(message); err != nil {
			return err
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
	}
	binary, err := codexBinary(ctx)
	if err != nil {
		return err
	}
	info, err := api.AttachRoom(ctx, id)
	if err != nil {
		return err
	}
	home, err := nativeHome(origin, info.RoomID)
	if err != nil {
		return err
	}
	// Validate before opening any transport; an empty thread must never create one.
	if _, err = nativeRoomCommand(ctx, binary, home, info, "ws://127.0.0.1:1", operation, message); err != nil {
		return err
	}
	tunnel, err := roomtransport.OpenTunnel(ctx, info, func(ctx context.Context) (client.AttachInfo, error) { return api.AttachRoom(ctx, info.RoomID) })
	if err != nil {
		return err
	}
	defer tunnel.Close()
	runCtx, cancelRun := attachmentContext(ctx, tunnel.Errors())
	defer cancelRun(context.Canceled)
	cmd, err := nativeRoomCommand(runCtx, binary, home, info, tunnel.Address(), operation, message)
	if err != nil {
		return err
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if term.IsTerminal(int(os.Stdin.Fd())) {
		state, e := term.GetState(int(os.Stdin.Fd()))
		if e != nil {
			return e
		}
		defer term.Restore(int(os.Stdin.Fd()), state)
	}
	if err = cmd.Run(); err != nil {
		return nativeProcessError(operation, err, context.Cause(runCtx))
	}
	return nil
}

// Once the native process has run, an error cannot establish whether a queue
// submission reached Codex before its acknowledgement was lost.
func nativeProcessError(operation string, processErr, cause error) error {
	if processErr == nil {
		return nil
	}
	switch operation {
	case "queue":
		if cause == nil {
			cause = processErr
		}
		return fmt.Errorf("native Codex queue failed; the message may have been accepted, so inspect the room before retrying: %w", cause)
	case "attach":
		if cause != nil && !errors.Is(cause, context.Canceled) {
			return cause
		}
		return errors.New("native Codex exited unsuccessfully; cloud room remains available")
	default:
		return errors.New("unsupported native room operation")
	}
}
