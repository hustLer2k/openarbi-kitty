package roomtransport

import (
	"bytes"
	"context"
	"errors"
	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type Attach func(context.Context) (client.AttachInfo, error)
type Tunnel struct {
	listener net.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	fetch    Attach
	initial  client.AttachInfo
	mu       sync.Mutex
	client   *ssh.Client
	workers  sync.WaitGroup
	errors   chan error
}

// OpenTunnel verifies the host key and credentials before exposing a loopback
// listener. Every SSH reconnect obtains a new short-lived attachment ticket.
func OpenTunnel(ctx context.Context, info client.AttachInfo, fetch Attach) (*Tunnel, error) {
	if info.RoomID == "" || info.ThreadID == "" || info.Ticket == "" || info.WebSocketToken == "" || fetch == nil {
		return nil, errors.New("incomplete room attachment")
	}
	ctx, cancel := context.WithCancel(ctx)
	t := &Tunnel{ctx: ctx, cancel: cancel, fetch: fetch, initial: info, errors: make(chan error, 1)}
	client, err := dialSSH(ctx, info)
	if err != nil {
		cancel()
		return nil, err
	}
	t.client = client
	t.watchClient(client)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		_ = client.Close()
		cancel()
		return nil, errors.New("cannot open local room listener")
	}
	t.listener = listener
	t.workers.Go(t.serve)
	return t, nil
}
func (t *Tunnel) Errors() <-chan error { return t.errors }
func (t *Tunnel) fail(err error) {
	select {
	case t.errors <- err:
	default:
	}
	t.cancel()
}
func (t *Tunnel) watchClient(client *ssh.Client) {
	t.workers.Go(func() {
		_ = client.Wait()
		if t.ctx.Err() != nil {
			return
		}
		ctx, done := context.WithTimeout(t.ctx, 15*time.Second)
		defer done()
		info, err := t.fetch(ctx)
		if err != nil {
			t.fail(errors.New("room attachment ended; access could not be reauthorized"))
			return
		}
		if !t.sameRoom(info) {
			t.fail(errors.New("room changed; attach again explicitly"))
		}
	})
}
func (t *Tunnel) sameRoom(info client.AttachInfo) bool {
	return info.RoomID == t.initial.RoomID && info.ThreadID == t.initial.ThreadID && info.WebSocketToken == t.initial.WebSocketToken && info.SSHHostKey == t.initial.SSHHostKey && info.SSHAddress == t.initial.SSHAddress
}
func (t *Tunnel) Address() string { return "ws://" + t.listener.Addr().String() }
func (t *Tunnel) Close() error {
	t.cancel()
	_ = t.listener.Close()
	t.mu.Lock()
	if t.client != nil {
		_ = t.client.Close()
	}
	t.mu.Unlock()
	t.workers.Wait()
	return nil
}
func (t *Tunnel) serve() {
	stop := context.AfterFunc(t.ctx, func() {
		_ = t.listener.Close()
		t.mu.Lock()
		if t.client != nil {
			_ = t.client.Close()
		}
		t.mu.Unlock()
	})
	defer stop()
	slots := make(chan struct{}, 8)
	for {
		local, err := t.listener.Accept()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = local.Close()
			continue
		}
		t.workers.Go(func() {
			defer func() { <-slots }()
			defer local.Close()
			stop := context.AfterFunc(t.ctx, func() { _ = local.Close() })
			defer stop()
			remote, err := t.dial()
			if err != nil {
				return
			}
			defer remote.Close()
			bridge(local, remote)
		})
	}
}
func (t *Tunnel) dial() (net.Conn, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ctx.Err() != nil {
		return nil, t.ctx.Err()
	}
	dialCtx, dialDone := context.WithTimeout(t.ctx, 10*time.Second)
	defer dialDone()
	if t.client != nil {
		remote, err := t.client.DialContext(dialCtx, "tcp", net.JoinHostPort(t.initial.RoomID, strconv.Itoa(DestinationPort)))
		if err == nil {
			return remote, nil
		}
		_ = t.client.Close()
		t.client = nil
	}
	// This retries authentication only; it never creates/resumes rooms or replays input.
	ctx, done := context.WithTimeout(t.ctx, 15*time.Second)
	defer done()
	info, err := t.fetch(ctx)
	if err != nil {
		t.fail(errors.New("room attachment ended; access could not be reauthorized"))
		return nil, errors.New("room reattachment denied")
	}
	if !t.sameRoom(info) {
		t.fail(errors.New("room changed; attach again explicitly"))
		return nil, errors.New("room changed; attach again explicitly")
	}
	client, err := dialSSH(ctx, info)
	if err != nil {
		return nil, err
	}
	t.client = client
	t.watchClient(client)
	return client.DialContext(ctx, "tcp", net.JoinHostPort(info.RoomID, strconv.Itoa(DestinationPort)))
}
func dialSSH(ctx context.Context, info client.AttachInfo) (*ssh.Client, error) {
	key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(info.SSHHostKey))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid room SSH host-key pin")
	}
	cfg := &ssh.ClientConfig{User: info.RoomID, Auth: []ssh.AuthMethod{ssh.Password(info.Ticket)}, HostKeyCallback: ssh.FixedHostKey(key), Timeout: 10 * time.Second}
	ctx, done := context.WithTimeout(ctx, 10*time.Second)
	defer done()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", info.SSHAddress)
	if err != nil {
		return nil, errors.New("room gateway unavailable")
	}
	closeOnCancel := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer closeOnCancel()
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	conn, channels, requests, err := ssh.NewClientConn(raw, info.SSHAddress, cfg)
	if err != nil {
		_ = raw.Close()
		return nil, errors.New("room gateway authentication or host-key verification failed")
	}
	_ = raw.SetDeadline(time.Time{})
	return ssh.NewClient(conn, channels, requests), nil
}
