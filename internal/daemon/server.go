//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const SocketName = "control.sock"
const MaxConnections = 16

func verifyPeer(c *net.UnixConn) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var peerErr error
	err = raw.Control(func(fd uintptr) {
		uid, e := peerUID(int(fd))
		peerErr = e
		if e == nil && int64(uid) != int64(os.Getuid()) {
			peerErr = fmt.Errorf("peer_uid_mismatch")
		}
	})
	return errors.Join(err, peerErr)
}

type boundedListener struct {
	*net.UnixListener
	slots chan struct{}
}
type boundedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *boundedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
func (l *boundedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
		default:
			_ = c.Close()
			continue
		}
		if err := verifyPeer(c); err != nil {
			_ = c.Close()
			<-l.slots
			continue
		}
		return &boundedConn{Conn: c, release: func() { <-l.slots }}, nil
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"protocol": ProtocolVersion, "error": code})
}

// Handler accepts only bodyless, queryless, versioned GET operations. Proposal
// evaluation is unsupported in this spike; an empty list is labeled as such.
func Handler(c *Coordinator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			failure(w, 405, "method_not_allowed")
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.RawQuery != "" {
			failure(w, 400, "invalid_request")
			return
		}
		switch r.URL.Path {
		case "/v1/capabilities":
			writeJSON(w, 200, map[string]any{"protocol": 1, "operations": []string{"capabilities", "status", "proposals.read"}, "sample_schema": SampleSchema, "journal_contract": "durable-record/v1", "proposal_evaluation": false, "mutation": false})
		case "/v1/status":
			writeJSON(w, 200, c.Status())
		case "/v1/proposals":
			writeJSON(w, 200, map[string]any{"protocol": 1, "state": "not_evaluated", "items": []any{}, "next_cursor": nil})
		default:
			failure(w, 404, "unsupported_operation")
		}
	})
}

// Run owns the coordinator for its full lifetime. It refuses an existing socket
// instead of unlinking a potentially live or spoofed endpoint.
func Run(ctx context.Context, dir, path string, interval time.Duration) error {
	if interval < time.Minute {
		return fmt.Errorf("sample interval must be at least 60s")
	}
	c, err := Open(dir, nil)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	socket := filepath.Join(c.owner.Dir, SocketName)
	if _, err := os.Lstat(socket); err == nil {
		return fmt.Errorf("socket_exists: inspect stale endpoint before removing it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return err
	}
	defer func() { _ = l.Close() }()
	if err = setSocketMode(socket); err != nil {
		return err
	}
	server := &http.Server{Handler: Handler(c), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
	server.SetKeepAlivesEnabled(false)
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(&boundedListener{UnixListener: l, slots: make(chan struct{}, MaxConnections)})
	}()
	defer func() { _ = server.Close() }()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	return coordinate(ctx, c, path, ticker.C, done)
}

// coordinate keeps reads available during transient observation failures. An
// uncertain journal outcome suspends writes until an explicit restart.
func coordinate(ctx context.Context, c *Coordinator, path string, ticks <-chan time.Time, done <-chan error) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if c.Status().State != "journal_unavailable" {
			sampleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, _ = c.Sample(sampleCtx, path)
			cancel()
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ticks:
		}
	}
}

// Read never falls back to an offline write after any socket error.
func Read(ctx context.Context, dir, operation string) (json.RawMessage, error) {
	if operation != "status" && operation != "capabilities" && operation != "proposals" {
		return nil, fmt.Errorf("unsupported_operation")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", filepath.Join(dir, SocketName))
		if err != nil {
			return nil, err
		}
		c, ok := conn.(*net.UnixConn)
		if !ok {
			_ = conn.Close()
			return nil, fmt.Errorf("unverified_peer")
		}
		if err = verifyPeer(c); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost/v1/"+operation, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil {
		return nil, err
	}
	if len(raw) > 65536 {
		return nil, fmt.Errorf("response_too_large")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("daemon_status_%d", resp.StatusCode)
	}
	var envelope struct {
		Protocol int `json:"protocol"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil || envelope.Protocol != 1 {
		return nil, fmt.Errorf("unsupported_protocol")
	}
	return raw, nil
}
