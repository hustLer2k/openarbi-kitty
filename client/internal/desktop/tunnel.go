package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
)

// Tunnel owns one system OpenSSH process and both private forwards. API and
// gateway stream-local forwards avoid trusting an unidentified TCP port.
// A listener owned by this process bridges the gateway for the native SSH client.
type Tunnel struct {
	directory   string
	ctx         context.Context
	cancel      context.CancelFunc
	relayCancel context.CancelFunc
	mu          sync.Mutex
	command     *exec.Cmd
	done        chan struct{}
	gateway     net.Listener
	wg          sync.WaitGroup
	active      atomic.Int64
}

func NewTunnel(ctx context.Context, directory string) *Tunnel {
	ctx, cancel := context.WithCancel(ctx)
	return &Tunnel{ctx: ctx, cancel: cancel, directory: directory}
}
func (t *Tunnel) sshArgs() []string {
	return []string{"-M", "-S", filepath.Join(t.directory, "ssh-control.sock"), "-N", "-T", "-o", "ControlPersist=no", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ExitOnForwardFailure=yes", "-o", "StreamLocalBindUnlink=no", "-o", "StreamLocalBindMask=0177", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-L", filepath.Join(t.directory, "api.sock") + ":10.53.0.20:8090", "-L", filepath.Join(t.directory, "gateway.sock") + ":10.53.0.21:2222", "revenge"}
}
func (t *Tunnel) Ensure(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.command != nil {
		select {
		case <-t.done:
			t.stopLocked()
			t.wg.Wait()
		default:
			return nil
		}
	}
	if err := PrivateDirectory(t.directory); err != nil {
		return err
	}
	// A broker crash can leave an SSH master. Ask that known private control
	// socket to exit, then discard only our own named socket paths.
	control := filepath.Join(t.directory, "ssh-control.sock")
	if _, err := os.Lstat(control); err == nil {
		cleanupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = exec.CommandContext(cleanupCtx, "ssh", "-S", control, "-O", "exit", "revenge").Run()
		cancel()
	}
	for _, name := range []string{"ssh-control.sock", "api.sock", "gateway.sock"} {
		path := filepath.Join(t.directory, name)
		if st, err := os.Lstat(path); err == nil {
			if st.Mode()&os.ModeSocket == 0 {
				return fmt.Errorf("SSH socket path %s is occupied by a non-socket", name)
			}
			if err = os.Remove(path); err != nil {
				return errors.New("cannot remove stale SSH socket")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot inspect SSH socket")
		}
	}
	gateway, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return errors.New("cannot reserve private gateway listener")
	}
	command := exec.CommandContext(t.ctx, "ssh", t.sshArgs()...)
	// OpenSSH errors can include arbitrary user configuration; never echo them
	// through broker diagnostics or persist them beside workspace identifiers.
	if err = command.Start(); err != nil {
		gateway.Close()
		return errors.New("system OpenSSH is required; verify ssh revenge in a terminal")
	}
	t.command = command
	t.gateway = gateway
	t.done = make(chan struct{})
	go func() { _ = command.Wait(); close(t.done) }()
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	poll := time.NewTicker(40 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			t.stopLocked()
			return ctx.Err()
		case <-t.ctx.Done():
			t.stopLocked()
			return errors.New("desktop broker is stopping")
		case <-t.done:
			t.stopLocked()
			return errors.New("SSH authentication or forwarding failed; run ssh revenge and check its host key, agent and key access")
		case <-timeout.C:
			t.stopLocked()
			return errors.New("SSH connection timed out; run ssh revenge to check access")
		case <-poll.C:
			if _, err := os.Stat(filepath.Join(t.directory, "api.sock")); err != nil {
				continue
			}
			if _, err := os.Stat(filepath.Join(t.directory, "gateway.sock")); err != nil {
				continue
			}
			relayCtx, relayCancel := context.WithCancel(t.ctx)
			t.relayCancel = relayCancel
			t.wg.Go(func() { t.accept(relayCtx, gateway) })
			return nil
		}
	}
}
func (t *Tunnel) stopLocked() {
	if t.relayCancel != nil {
		t.relayCancel()
		t.relayCancel = nil
	}
	if t.gateway != nil {
		_ = t.gateway.Close()
		t.gateway = nil
	}
	if t.command != nil {
		_ = t.command.Process.Kill()
		<-t.done
		t.command = nil
	}
}
func (t *Tunnel) Close() {
	t.cancel()
	t.mu.Lock()
	t.stopLocked()
	t.mu.Unlock()
	t.wg.Wait()
}
func (t *Tunnel) accept(ctx context.Context, listener net.Listener) {
	for {
		incoming, err := listener.Accept()
		if err != nil {
			return
		}
		t.active.Add(1)
		t.wg.Go(func() {
			defer t.active.Add(-1)
			stopIncoming := context.AfterFunc(ctx, func() { _ = incoming.Close() })
			defer stopIncoming()
			defer incoming.Close()
			outgoing, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(t.directory, "gateway.sock"))
			if err != nil {
				return
			}
			stopOutgoing := context.AfterFunc(ctx, func() { _ = outgoing.Close() })
			defer stopOutgoing()
			defer outgoing.Close()
			finished := make(chan struct{})
			go func() {
				_, _ = io.Copy(outgoing, incoming)
				if conn, ok := outgoing.(*net.UnixConn); ok {
					_ = conn.CloseWrite()
				}
				close(finished)
			}()
			_, _ = io.Copy(incoming, outgoing)
			incoming.Close()
			outgoing.Close()
			<-finished
		})
	}
}
func (t *Tunnel) HTTP() *http.Client { return SocketHTTP(filepath.Join(t.directory, "api.sock")) }
func (t *Tunnel) GatewayAddress() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.gateway == nil {
		return ""
	}
	return t.gateway.Addr().String()
}
func (t *Tunnel) Proxy() http.Handler {
	proxy := &httputil.ReverseProxy{Transport: t.HTTP().Transport, Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme = "http"
		pr.Out.URL.Host = "openarbi"
		pr.Out.Host = "openarbi"
		pr.Out.Header.Del("Forwarded")
		pr.Out.Header.Del("X-Forwarded-For")
		pr.Out.Header.Del("X-Forwarded-Host")
		pr.Out.Header.Del("X-Forwarded-Proto")
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "OpenArbi tunnel unavailable", http.StatusBadGateway)
	}}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode != http.StatusOK || !strings.HasSuffix(resp.Request.URL.Path, "/attach") {
			return nil
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
		if err != nil || len(raw) > 16<<20 {
			return errors.New("invalid attach response")
		}
		var info client.AttachInfo
		if json.Unmarshal(raw, &info) != nil || info.RoomID == "" {
			return errors.New("invalid attach response")
		}
		info.SSHAddress = t.GatewayAddress()
		if info.SSHAddress == "" {
			return errors.New("gateway is unavailable")
		}
		body, err := json.Marshal(info)
		if err != nil {
			return err
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := t.Ensure(r.Context()); err != nil {
			http.Error(w, "OpenArbi SSH connection failed; check ssh revenge", http.StatusBadGateway)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

// Backend obtains credentials only from the operating-system store. Its HTTP
// transport can reach only the API socket owned by this broker's SSH process.
type Backend struct {
	Tunnel *Tunnel
	Origin string
}

func (b Backend) api(ctx context.Context) (*client.Client, error) {
	token, err := (client.Credentials{}).Get(b.Origin)
	if err != nil {
		return nil, err
	}
	if err = b.Tunnel.Ensure(ctx); err != nil {
		return nil, err
	}
	return client.New(b.Origin, token, b.Tunnel.HTTP())
}
func (b Backend) DesktopCapabilities(ctx context.Context) (client.DesktopAllocation, error) {
	api, err := b.api(ctx)
	if err != nil {
		return client.DesktopAllocation{}, err
	}
	defer api.CloseIdleConnections()
	return api.DesktopCapabilities(ctx)
}
func (b Backend) ClaimRoom(ctx context.Context, in client.ClaimRequest) (client.RoomBinding, error) {
	api, err := b.api(ctx)
	if err != nil {
		return client.RoomBinding{}, err
	}
	defer api.CloseIdleConnections()
	return api.ClaimRoom(ctx, in)
}
func (b Backend) RoomBinding(ctx context.Context, device, session string) (client.RoomBinding, error) {
	api, err := b.api(ctx)
	if err != nil {
		return client.RoomBinding{}, err
	}
	defer api.CloseIdleConnections()
	return api.RoomBinding(ctx, device, session)
}

func (b Backend) Rooms(ctx context.Context) ([]client.Room, error) {
	api, err := b.api(ctx)
	if err != nil {
		return nil, err
	}
	defer api.CloseIdleConnections()
	return api.Rooms(ctx)
}
func (b Backend) Room(ctx context.Context, id string) (client.Room, error) {
	api, err := b.api(ctx)
	if err != nil {
		return client.Room{}, err
	}
	defer api.CloseIdleConnections()
	return api.Room(ctx, id)
}
func (b Backend) StopRoom(ctx context.Context, id string) (client.Room, error) {
	api, err := b.api(ctx)
	if err != nil {
		return client.Room{}, err
	}
	defer api.CloseIdleConnections()
	return api.StopRoom(ctx, id)
}
func (b Backend) ResumeRoom(ctx context.Context, id string) (client.Room, error) {
	api, err := b.api(ctx)
	if err != nil {
		return client.Room{}, err
	}
	defer api.CloseIdleConnections()
	return api.ResumeRoom(ctx, id)
}

func (t *Tunnel) ActiveConnections() int64 { return t.active.Load() }
