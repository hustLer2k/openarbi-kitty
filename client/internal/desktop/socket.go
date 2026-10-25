package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var ErrBrokerRunning = errors.New("desktop broker already owns this workspace")

// Ownership is an advisory kernel lock, never a PID-file guess. Its inode stays
// in place across restarts; only the holder may remove a stale socket.
type Ownership struct {
	lock     *os.File
	Listener net.Listener
	socket   string
}

func Acquire(directory string) (*Ownership, error) {
	if err := PrivateDirectory(directory); err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(directory, "broker.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, errors.New("cannot open desktop ownership lock")
	}
	file := os.NewFile(uintptr(fd), "broker.lock")
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrBrokerRunning
		}
		return nil, errors.New("cannot lock desktop workspace")
	}
	ownership := &Ownership{lock: file, socket: filepath.Join(directory, "broker.sock")}
	if st, err := os.Lstat(ownership.socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			ownership.Close()
			return nil, errors.New("broker socket path is occupied by a non-socket")
		}
		if err = os.Remove(ownership.socket); err != nil {
			ownership.Close()
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		ownership.Close()
		return nil, err
	}
	listener, err := net.Listen("unix", ownership.socket)
	if err != nil {
		ownership.Close()
		return nil, errors.New("cannot listen on desktop socket")
	}
	ownership.Listener = listener
	if err = os.Chmod(ownership.socket, 0600); err != nil {
		ownership.Close()
		return nil, err
	}
	return ownership, nil
}
func (o *Ownership) Close() error {
	var errs []error
	if o.Listener != nil {
		errs = append(errs, o.Listener.Close())
		o.Listener = nil
	}
	if o.lock != nil {
		errs = append(errs, o.lock.Close())
		o.lock = nil
	}
	return errors.Join(errs...)
}
func SocketHTTP(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if err := PrivateDirectory(filepath.Dir(socket)); err != nil {
			return nil, err
		}
		st, err := os.Lstat(socket)
		if err != nil {
			return nil, errors.New("desktop socket is unavailable")
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm()&0077 != 0 || !ok || int(owner.Uid) != os.Getuid() {
			return nil, errors.New("desktop socket must be private and owned by the current user")
		}
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, Timeout: 45 * time.Second}
}
func Send(ctx context.Context, directory string, c Command) (Result, error) {
	var out Result
	raw, err := json.Marshal(c)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://desktop/desktop/command", bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	transport := SocketHTTP(filepath.Join(directory, "broker.sock"))
	defer transport.CloseIdleConnections()
	resp, err := transport.Do(req)
	if err != nil {
		return out, errors.New("desktop broker is unavailable")
	}
	defer resp.Body.Close()
	if err = json.NewDecoder(io.LimitReader(resp.Body, maxStateBytes)).Decode(&out); err != nil {
		return out, errors.New("invalid desktop broker response")
	}
	if resp.StatusCode != http.StatusOK {
		return out, errors.New(out.Error)
	}
	return out, nil
}

// Handler is a narrow local protocol. Socket permissions enforce the same-user
// boundary; no authentication token is placed in command payloads or results.
func Handler(manager *Manager, api http.Handler) http.Handler {
	return HandlerWithShutdown(manager, api, nil)
}
func HandlerWithShutdown(manager *Manager, api http.Handler, shutdown func() error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /desktop/command", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var command Command
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&command) != nil || decoder.Decode(new(any)) != io.EOF {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(Result{Error: "invalid desktop command"})
			return
		}
		var result Result
		var err error
		if command.Action == "shutdown" {
			if shutdown == nil {
				err = errors.New("this broker does not support shutdown")
			} else {
				err = shutdown()
			}
			result = Result{Workspace: manager.Snapshot(), Status: "stopping"}
		} else {
			result, err = manager.Apply(r.Context(), command)
		}
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			result.Error = err.Error()
		}
		json.NewEncoder(w).Encode(result)
	})
	if api != nil {
		mux.Handle("/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := manager.startOperation(); err != nil {
				http.Error(w, "desktop broker is stopping", http.StatusServiceUnavailable)
				return
			}
			defer manager.finishOperation()
			api.ServeHTTP(w, r)
		}))
	}
	return mux
}

// BrokerRunning probes the kernel lock without claiming/removing a socket. It
// distinguishes an absent/stale broker from an unreachable but still-live owner.
func BrokerRunning(directory string) (bool, error) {
	if err := PrivateDirectory(directory); err != nil {
		return false, err
	}
	fd, err := unix.Open(filepath.Join(directory, "broker.lock"), unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("cannot inspect desktop ownership lock")
	}
	defer unix.Close(fd)
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return true, nil
		}
		return false, errors.New("cannot inspect desktop ownership lock")
	}
	return false, nil
}

// Shutdown succeeds only after the broker has released ownership, which happens
// after its server, SSH process and native gateway relays have finished closing.
func Shutdown(ctx context.Context, directory string) error {
	running, err := BrokerRunning(directory)
	if err != nil || !running {
		return err
	}
	if _, err = Send(ctx, directory, Command{Action: "shutdown"}); err != nil {
		return err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		running, err = BrokerRunning(directory)
		if err != nil || !running {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.New("desktop broker did not finish shutting down; leave the current installation active")
		case <-ticker.C:
		}
	}
}
