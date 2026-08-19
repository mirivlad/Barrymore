package runner

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// internalWorkerProxyBridgeMode is a private execution mode of the same
// barrymored binary. Runner starts it inside bwrap's private network namespace;
// it is not a user-facing command.
const internalWorkerProxyBridgeMode = "__worker-proxy-bridge"

// init handles the private bridge before cmd/barrymored's flag parser sees its
// arguments. Keeping the bridge in the same binary avoids a socat/helper
// runtime dependency while still letting it live inside the worker namespace.
func init() {
	if len(os.Args) < 2 || os.Args[1] != internalWorkerProxyBridgeMode {
		return
	}
	code := runWorkerProxyBridge(os.Args[2:])
	os.Exit(code)
}

type hostProxyRelay struct {
	upstream string
	socket   string
	listener net.Listener
}

var workerRelayState struct {
	sync.Mutex
	relay *hostProxyRelay
}

// ensureWorkerProxyRelay makes the host-side half of the fail-closed route
// available and proves that the configured proxy can be reached before a
// worker starts.
//
// A worker never connects to the upstream directly. It has no host network at
// all; its loopback bridge connects to this Unix socket, and only this relay
// owns a normal host-side network socket.
func ensureWorkerProxyRelay(raw string) (string, error) {
	proxy, err := NormalizeWorkerProxy(raw)
	if err != nil {
		return "", err
	}
	if proxy == "" {
		return "", nil
	}

	workerRelayState.Lock()
	defer workerRelayState.Unlock()

	if r := workerRelayState.relay; r != nil && r.upstream == proxy {
		return r.socket, nil
	}

	// Fail before the agent is even exec'd when the owner explicitly required a
	// proxy that is not reachable. A half-configured route must never fall back
	// to the machine's ordinary network.
	conn, err := dialProxyUpstream(proxy, 3*time.Second)
	if err != nil {
		return "", fmt.Errorf("прокси персонала недоступен: %w", err)
	}
	_ = conn.Close()

	path, err := workerProxySocketPath()
	if err != nil {
		return "", err
	}
	if old := workerRelayState.relay; old != nil {
		_ = old.listener.Close()
		_ = os.Remove(old.socket)
		workerRelayState.relay = nil
	}
	_ = os.Remove(path) // stale socket after an unclean previous exit

	ln, err := net.Listen("unix", path)
	if err != nil {
		return "", fmt.Errorf("relay прокси персонала не слушает: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("права relay-сокета не выставлены: %w", err)
	}

	r := &hostProxyRelay{upstream: proxy, socket: path, listener: ln}
	workerRelayState.relay = r
	go r.serve()
	return path, nil
}

func workerProxySocketPath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("каталог relay прокси персонала не определён: %w", err)
	}
	dir := filepath.Join(base, "barrymore")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("каталог relay прокси персонала: %w", err)
	}
	// It is Barrymore's directory, not the whole user cache. Tightening it is
	// safe and prevents another local user from injecting a fake relay socket.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("права каталога relay прокси персонала: %w", err)
	}
	return filepath.Join(dir, "worker-proxy.sock"), nil
}

func (r *hostProxyRelay) serve() {
	for {
		client, err := r.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer client.Close()
			upstream, err := dialProxyUpstream(r.upstream, 10*time.Second)
			if err != nil {
				return
			}
			defer upstream.Close()
			pipeBoth(client, upstream)
		}()
	}
}

// dialProxyUpstream opens exactly one connection: to the proxy itself.
// Destination hosts requested by the agent remain bytes inside HTTP CONNECT or
// SOCKS protocol and are never resolved/dialled by Barrymore.
func dialProxyUpstream(raw string, timeout time.Duration) (net.Conn, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			return nil, fmt.Errorf("неизвестная схема прокси %q", u.Scheme)
		}
	}
	addr := net.JoinHostPort(host, port)
	dialer := &net.Dialer{Timeout: timeout}
	if u.Scheme != "https" {
		return dialer.Dial("tcp", addr)
	}

	// The worker talks plain HTTP to its private loopback bridge. The secure
	// hop, when the owner chose an HTTPS proxy, starts here on the host and uses
	// the real proxy hostname for certificate verification.
	return tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: host,
	})
}

// runWorkerProxyBridge runs inside bwrap --unshare-net. It exposes a local
// TCP endpoint for ordinary CLI proxy support and forwards each connection to
// the host relay through a Unix socket. No path from this namespace reaches a
// host/internet IP directly.
func runWorkerProxyBridge(args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "barrymore worker proxy bridge: не хватает аргументов")
		return 2
	}
	socketPath := args[0]
	sep := -1
	for i := 1; i < len(args); i++ {
		if args[i] == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "barrymore worker proxy bridge: не задан worker после --")
		return 2
	}

	ln, err := net.Listen("tcp", workerProxyBridgeAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "barrymore worker proxy bridge:", err)
		return 125
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				d := net.Dialer{Timeout: 5 * time.Second}
				host, err := d.Dial("unix", socketPath)
				if err != nil {
					// Relay gone or proxy unavailable: close the request. There is
					// deliberately no fallback path from this namespace.
					return
				}
				defer host.Close()
				pipeBoth(client, host)
			}()
		}
	}()

	workerArgv := args[sep+1:]
	cmd := exec.Command(workerArgv[0], workerArgv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	err = cmd.Run()
	_ = ln.Close()
	<-done
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "barrymore worker proxy bridge: worker не запущен:", err)
	return 126
}

func pipeBoth(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		if c, ok := a.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		if c, ok := b.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
	}()
	wg.Wait()
}
