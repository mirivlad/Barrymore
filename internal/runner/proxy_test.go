package runner

import (
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/worker"
)

func TestNormalizeWorkerProxy(t *testing.T) {
	t.Parallel()

	good := map[string]string{
		"":                           "",
		" http://127.0.0.1:8080/ ":   "http://127.0.0.1:8080",
		"SOCKS5://127.0.0.1:1080":    "socks5://127.0.0.1:1080",
		"socks5h://proxy.local:9050": "socks5h://proxy.local:9050",
		"https://proxy.local:8443":   "https://proxy.local:8443",
	}
	for input, want := range good {
		input, want := input, want
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeWorkerProxy(input)
			if err != nil {
				t.Fatalf("NormalizeWorkerProxy(%q): %v", input, err)
			}
			if got != want {
				t.Fatalf("NormalizeWorkerProxy(%q) = %q, want %q", input, got, want)
			}
		})
	}

	bad := []string{
		"127.0.0.1:8080",
		"ftp://proxy.local:21",
		"http://",
		"http://user:secret@proxy.local:8080",
		"http://proxy.local:8080/path",
		"http://proxy.local:8080?mode=x",
		"http://proxy.local:8080#fragment",
	}
	for _, input := range bad {
		input := input
		t.Run("bad_"+input, func(t *testing.T) {
			t.Parallel()
			if _, err := NormalizeWorkerProxy(input); err == nil {
				t.Fatalf("NormalizeWorkerProxy(%q) unexpectedly succeeded", input)
			}
		})
	}
}

func TestLocalWorkerProxyURL(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"http://proxy.local:8080":    "http://127.0.0.1:17717",
		"https://proxy.local:8443":   "http://127.0.0.1:17717",
		"socks5://proxy.local:1080":  "socks5h://127.0.0.1:17717",
		"socks5h://proxy.local:1080": "socks5h://127.0.0.1:17717",
	}
	for upstream, want := range cases {
		if got := localWorkerProxyURL(upstream); got != want {
			t.Fatalf("localWorkerProxyURL(%q) = %q, want %q", upstream, got, want)
		}
	}
}

func TestWorkerProxyEnvironmentContainsOnlyBridge(t *testing.T) {
	t.Parallel()

	upstream := "socks5://secret-route.example:1081"
	env := workerProxyEnvironment(upstream)
	for _, want := range []string{
		"HTTP_PROXY=socks5h://127.0.0.1:17717",
		"HTTPS_PROXY=socks5h://127.0.0.1:17717",
		"ALL_PROXY=socks5h://127.0.0.1:17717",
		"http_proxy=socks5h://127.0.0.1:17717",
		"https_proxy=socks5h://127.0.0.1:17717",
		"all_proxy=socks5h://127.0.0.1:17717",
		"NO_PROXY=localhost,127.0.0.1,::1",
		"no_proxy=localhost,127.0.0.1,::1",
	} {
		if !slices.Contains(env, want) {
			t.Fatalf("worker proxy env misses %q: %#v", want, env)
		}
	}
	for _, item := range env {
		if strings.Contains(item, "secret-route.example") || strings.Contains(item, ":1081") {
			t.Fatalf("worker environment leaked real upstream proxy: %q", item)
		}
	}
}

func TestMergeEnvOwnerProxyWins(t *testing.T) {
	t.Parallel()

	got := mergeEnv(
		[]string{"PATH=/usr/bin", "HTTP_PROXY=http://adapter:8000"},
		[]string{"FOO=bar"},
		[]string{"HTTP_PROXY=http://127.0.0.1:17717", "HTTPS_PROXY=http://127.0.0.1:17717"},
	)
	if !slices.Contains(got, "HTTP_PROXY=http://127.0.0.1:17717") {
		t.Fatalf("Barrymore proxy did not win: %#v", got)
	}
	if slices.Contains(got, "HTTP_PROXY=http://adapter:8000") {
		t.Fatalf("stale adapter proxy survived: %#v", got)
	}
	if !slices.Contains(got, "PATH=/usr/bin") || !slices.Contains(got, "FOO=bar") {
		t.Fatalf("non-proxy environment was lost: %#v", got)
	}
}

func TestHostRelayForwardsOnlyToConfiguredProxy(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })

	go func() {
		for {
			c, err := upstream.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()

	socketPath, err := ensureWorkerProxyRelay("http://" + upstream.Addr().String())
	if err != nil {
		t.Fatalf("relay не поднялся: %v", err)
	}
	c, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("unix relay недоступен: %v", err)
	}
	defer c.Close()

	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("relay исказил поток: %q", string(buf))
	}
}

func TestProxyOnlyRefusesWithoutBubblewrap(t *testing.T) {
	t.Setenv(WorkerProxyEnv, "http://127.0.0.1:9")
	plan := worker.RunPlan{
		Argv:    []string{"/bin/true"},
		Sandbox: worker.Sandbox{Network: true},
	}
	_, profile, err := buildCommand(Capabilities{}, plan, commandOptions{})
	if !errors.Is(err, ErrNoProxyIsolation) {
		t.Fatalf("без bwrap получили %v, ожидался ErrNoProxyIsolation", err)
	}
	if !profile.ProxyOnly || profile.Network {
		t.Fatalf("неверный профиль при отказе: %+v", profile)
	}
}

func TestProxyOnlyCommandUnsharesNetworkAndUsesBridge(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
	go func() {
		for {
			c, err := upstream.Accept()
			if err != nil {
				return
			}
			_ = c.Close() // preflight is enough for this test
		}
	}()

	t.Setenv(WorkerProxyEnv, "http://"+upstream.Addr().String())
	plan := worker.RunPlan{
		Argv:    []string{"/bin/true"},
		Sandbox: worker.Sandbox{Network: true},
	}
	argv, profile, err := buildCommand(Capabilities{Bwrap: "/usr/bin/bwrap"}, plan, commandOptions{})
	if err != nil {
		t.Fatalf("proxy-only command не собрана: %v", err)
	}
	if !slices.Contains(argv, "--unshare-net") {
		t.Fatalf("в proxy-only argv нет --unshare-net: %#v", argv)
	}
	if !slices.Contains(argv, internalWorkerProxyBridgeMode) {
		t.Fatalf("в proxy-only argv нет встроенного bridge: %#v", argv)
	}
	if !profile.ProxyOnly || profile.Network {
		t.Fatalf("неверный proxy-only профиль: %+v", profile)
	}
}
