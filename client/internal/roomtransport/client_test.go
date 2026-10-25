package roomtransport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/hustLer2k/openarbi-kitty/client/internal/client"
	"golang.org/x/crypto/ssh"
)

func fixtureSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// The fixture only echoes forwarded bytes. It has no production authorization,
// room lifecycle, provider, or gateway implementation dependency.
func echoGateway(t *testing.T) api.AttachInfo {
	t.Helper()
	signer := fixtureSigner(t)
	cfg := &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if meta.User() != "fixture-room" || string(password) != "fixture-ticket" {
			return nil, errors.New("fixture rejected credentials")
		}
		return nil, nil
	}}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer raw.Close()
				stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
				defer stop()
				conn, channels, requests, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				workers.Go(func() { ssh.DiscardRequests(requests) })
				for incoming := range channels {
					var destination struct {
						Host       string
						Port       uint32
						OriginHost string
						OriginPort uint32
					}
					if incoming.ChannelType() != "direct-tcpip" || ssh.Unmarshal(incoming.ExtraData(), &destination) != nil || destination.Host != "fixture-room" || destination.Port != 9091 {
						_ = incoming.Reject(ssh.Prohibited, "fixture only accepts the exact room destination")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					workers.Go(func() { ssh.DiscardRequests(requests) })
					workers.Go(func() { defer channel.Close(); _, _ = io.Copy(channel, channel) })
				}
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = listener.Close(); workers.Wait() })
	return api.AttachInfo{RoomID: "fixture-room", ThreadID: "fixture-thread", SSHAddress: listener.Addr().String(), SSHHostKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), Ticket: "fixture-ticket", WebSocketToken: "fixture-bearer"}
}

func probeTunnel(t *testing.T, tunnel *Tunnel) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(tunnel.Address(), "ws://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("fixture bytes")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("fixture bytes"))
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "fixture bytes" {
		t.Fatalf("forwarding failed: %v", err)
	}
}

func TestTunnelForwardsAndReauthorizesAfterDisconnect(t *testing.T) {
	info := echoGateway(t)
	var fetched atomic.Int32
	tunnel, err := OpenTunnel(t.Context(), info, func(context.Context) (api.AttachInfo, error) { fetched.Add(1); return info, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	if !strings.HasPrefix(tunnel.Address(), "ws://127.0.0.1:") {
		t.Fatal("listener is not loopback")
	}
	probeTunnel(t, tunnel)
	tunnel.mu.Lock()
	_ = tunnel.client.Close()
	tunnel.mu.Unlock()
	probeTunnel(t, tunnel)
	if fetched.Load() == 0 {
		t.Fatal("reattachment reused an old ticket")
	}
}

func TestHostKeyAndTicketRejected(t *testing.T) {
	info := echoGateway(t)
	for _, test := range []struct {
		name   string
		modify func(*api.AttachInfo)
	}{
		{"host pin", func(i *api.AttachInfo) { i.SSHHostKey = string(ssh.MarshalAuthorizedKey(fixtureSigner(t).PublicKey())) }},
		{"ticket", func(i *api.AttachInfo) { i.Ticket = "invalid-fixture-ticket" }},
		{"trailing host key", func(i *api.AttachInfo) { i.SSHHostKey += "unexpected trailing material" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := info
			test.modify(&candidate)
			conn, err := dialSSH(t.Context(), candidate)
			if err == nil {
				_ = conn.Close()
				t.Fatal("invalid gateway attachment accepted")
			}
		})
	}
}

func TestLostAuthorizationOrRoomChangeEndsTunnel(t *testing.T) {
	for _, change := range []string{"revoked", "thread", "bearer", "host", "address", "room"} {
		t.Run(change, func(t *testing.T) {
			info := echoGateway(t)
			tunnel, err := OpenTunnel(t.Context(), info, func(context.Context) (api.AttachInfo, error) {
				next := info
				switch change {
				case "revoked":
					return api.AttachInfo{}, errors.New("fixture revoked")
				case "thread":
					next.ThreadID = "another-thread"
				case "bearer":
					next.WebSocketToken = "another-bearer"
				case "host":
					next.SSHHostKey = "another-host"
				case "address":
					next.SSHAddress = "another-address"
				case "room":
					next.RoomID = "another-room"
				default:
					t.Fatalf("unknown fixture change %s", change)
				}
				return next, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer tunnel.Close()
			tunnel.mu.Lock()
			_ = tunnel.client.Close()
			tunnel.mu.Unlock()
			select {
			case err := <-tunnel.Errors():
				if err == nil {
					t.Fatal("missing attachment error")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("lost authorization was not surfaced")
			}
		})
	}
}

func TestCancellationInterruptsHandshake(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	info := api.AttachInfo{SSHAddress: listener.Addr().String(), SSHHostKey: string(ssh.MarshalAuthorizedKey(fixtureSigner(t).PublicKey()))}
	done := make(chan error, 1)
	go func() {
		conn, err := dialSSH(ctx, info)
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	select {
	case conn := <-accepted:
		defer conn.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("handshake did not connect")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("handshake ignored cancellation")
	}
}
