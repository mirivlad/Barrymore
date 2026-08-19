package runner

import (
	"slices"
	"testing"
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

func TestWorkerProxyEnvironment(t *testing.T) {
	t.Parallel()

	env := workerProxyEnvironment("socks5://127.0.0.1:1081")
	for _, want := range []string{
		"HTTP_PROXY=socks5://127.0.0.1:1081",
		"HTTPS_PROXY=socks5://127.0.0.1:1081",
		"ALL_PROXY=socks5://127.0.0.1:1081",
		"http_proxy=socks5://127.0.0.1:1081",
		"https_proxy=socks5://127.0.0.1:1081",
		"all_proxy=socks5://127.0.0.1:1081",
		"NO_PROXY=localhost,127.0.0.1,::1",
		"no_proxy=localhost,127.0.0.1,::1",
	} {
		if !slices.Contains(env, want) {
			t.Fatalf("worker proxy env misses %q: %#v", want, env)
		}
	}
}

func TestMergeEnvOwnerProxyWins(t *testing.T) {
	t.Parallel()

	got := mergeEnv(
		[]string{"PATH=/usr/bin", "HTTP_PROXY=http://adapter:8000"},
		[]string{"FOO=bar"},
		[]string{"HTTP_PROXY=http://owner:8080", "HTTPS_PROXY=http://owner:8080"},
	)
	if !slices.Contains(got, "HTTP_PROXY=http://owner:8080") {
		t.Fatalf("owner proxy did not win: %#v", got)
	}
	if slices.Contains(got, "HTTP_PROXY=http://adapter:8000") {
		t.Fatalf("stale adapter proxy survived: %#v", got)
	}
	if !slices.Contains(got, "PATH=/usr/bin") || !slices.Contains(got, "FOO=bar") {
		t.Fatalf("non-proxy environment was lost: %#v", got)
	}
}
