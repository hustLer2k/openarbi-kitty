package desktop

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
)

func TestSSHArgumentsUseAliasAndPrivateForwards(t *testing.T) {
	tunnel := NewTunnel(t.Context(), "/private state")
	defer tunnel.Close()
	args := tunnel.sshArgs()
	for _, required := range []string{"revenge", "BatchMode=yes", "StrictHostKeyChecking=yes", "ExitOnForwardFailure=yes", "StreamLocalBindMask=0177", "/private state/api.sock:10.53.0.20:8090", "/private state/gateway.sock:10.53.0.21:2222"} {
		if !slices.Contains(args, required) {
			t.Errorf("missing %s", required)
		}
	}
	if args[len(args)-1] != "revenge" {
		t.Fatal("SSH alias not used")
	}
}

// A real subprocess stands in for system ssh. It implements the two private
// forwards, allowing process lifecycle and the native gateway route to be tested
// without a remote account, OS credentials or network dependency.
func TestSSHHelperProcess(t *testing.T) {
	if os.Getenv("OPENARBI_FAKE_SSH") != "1" {
		return
	}
	split := slices.Index(os.Args, "--")
	if split < 0 {
		os.Exit(2)
	}
	args := os.Args[split+1:]
	if slices.Contains(args, "-O") {
		os.Exit(0)
	}
	var apiPath, gatewayPath string
	for i := 0; i < len(args)-1; i++ {
		if args[i] != "-L" {
			continue
		}
		value := args[i+1]
		path, _, _ := strings.Cut(value, ":")
		if strings.HasSuffix(path, "api.sock") {
			apiPath = path
		}
		if strings.HasSuffix(path, "gateway.sock") {
			gatewayPath = path
		}
	}
	if apiPath == "" || gatewayPath == "" {
		os.Exit(3)
	}
	api, err := net.Listen("unix", apiPath)
	if err != nil {
		os.Exit(4)
	}
	if os.Chmod(apiPath, 0600) != nil {
		os.Exit(5)
	}
	gateway, err := net.Listen("unix", gatewayPath)
	if err != nil {
		os.Exit(6)
	}
	if os.Chmod(gatewayPath, 0600) != nil {
		os.Exit(7)
	}
	go func() {
		for {
			conn, err := gateway.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	server := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/attach") {
			json.NewEncoder(w).Encode(client.AttachInfo{RoomID: "room", SSHAddress: "10.53.0.21:2222", Ticket: "ephemeral-ticket", WebSocketToken: "ephemeral-token"})
			return
		}
		w.Header().Set("Observed-Authorization", r.Header.Get("Authorization"))
		w.Write([]byte("ready"))
	})}
	_ = server.Serve(api)
	os.Exit(0)
}
func TestSharedSSHTunnelAndAttachProxy(t *testing.T) {
	dir := privateTemp(t)
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Quote the executable path for the test-only shell launcher.
	quoted := "'" + strings.ReplaceAll(executable, "'", "'\\''") + "'"
	script := "#!/bin/sh\nexec " + quoted + " -test.run=^TestSSHHelperProcess$ -- \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENARBI_FAKE_SSH", "1")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tunnel := NewTunnel(ctx, dir)
	defer tunnel.Close()
	if err = tunnel.Ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	pid := tunnel.command.Process.Pid
	if err = tunnel.Ensure(t.Context()); err != nil || tunnel.command.Process.Pid != pid {
		t.Fatal("shared SSH process replaced")
	}
	proxy := httptest.NewServer(tunnel.Proxy())
	defer proxy.Close()
	req, _ := http.NewRequestWithContext(t.Context(), "GET", proxy.URL+"/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer vault-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Observed-Authorization") != "Bearer vault-token" {
		t.Fatal("authorization did not reach API")
	}
	req, _ = http.NewRequestWithContext(t.Context(), "POST", proxy.URL+"/v1/rooms/room/attach", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var info client.AttachInfo
	if err = json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if info.SSHAddress != tunnel.GatewayAddress() || info.Ticket != "ephemeral-ticket" || info.WebSocketToken != "ephemeral-token" {
		t.Fatal("native attach proxy lost credentials or private gateway routing")
	}
	conn, err := net.DialTimeout("tcp", info.SSHAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	conn.Write([]byte("hello"))
	raw := make([]byte, 5)
	if _, err = io.ReadFull(conn, raw); err != nil || string(raw) != "hello" {
		t.Fatalf("gateway: %s %v", raw, err)
	}
	// Closing with an active connection must cancel both copy directions and join.
	closed := make(chan struct{})
	go func() { tunnel.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel shutdown leaked an attachment")
	}
	if _, err = os.FindProcess(pid); err != nil {
		t.Log("SSH child reaped: " + strconv.Itoa(pid))
	}
}
