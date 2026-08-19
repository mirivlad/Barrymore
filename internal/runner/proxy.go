package runner

import (
	"fmt"
	"net/url"
	"strings"
)

// WorkerProxyEnv is deliberately Barrymore-specific. barrymored may carry
// this marker in its own environment, but only runner turns it into a network
// route for an external worker process.
//
// The worker never receives the real proxy endpoint in HTTP_PROXY. With a
// configured proxy it sees only a loopback bridge inside its private network
// namespace; the host-side relay is the only process allowed to reach the real
// endpoint (ADR 0023).
const WorkerProxyEnv = "BARRYMORE_WORKER_PROXY"

const workerProxyBridgeAddr = "127.0.0.1:17717"

// NormalizeWorkerProxy validates and canonicalizes the proxy URL used by
// external workers. Empty means that workers use their normal network route.
func NormalizeWorkerProxy(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("прокси персонала не разобран: %w", err)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", fmt.Errorf(
			"прокси персонала: схема %q не поддерживается; нужны http, https, socks5 или socks5h",
			u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("прокси персонала: не задан адрес сервера")
	}
	if u.User != nil {
		return "", fmt.Errorf(
			"прокси персонала: учётные данные в URL пока не поддерживаются; секрет нельзя раздавать каждому процессу как часть адреса")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("прокси персонала: путь %q в адресе прокси не поддерживается", u.Path)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("прокси персонала: query и fragment в адресе прокси не поддерживаются")
	}

	// A trailing slash carries no information for a proxy endpoint and makes
	// equality/settings display needlessly unstable.
	u.Path = ""
	return strings.TrimSuffix(u.String(), "/"), nil
}

// localWorkerProxyURL is what a worker sees inside its network namespace.
//
// The real upstream endpoint stays on the host side. For an HTTPS proxy the
// host relay performs TLS itself, so the worker speaks ordinary HTTP proxy
// protocol to loopback. For SOCKS we deliberately advertise socks5h: target
// DNS then goes to the upstream SOCKS server instead of leaking from the
// isolated worker namespace.
func localWorkerProxyURL(raw string) string {
	proxy, err := NormalizeWorkerProxy(raw)
	if err != nil || proxy == "" {
		return ""
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return ""
	}
	scheme := u.Scheme
	switch scheme {
	case "https":
		scheme = "http"
	case "socks5", "socks5h":
		scheme = "socks5h"
	}
	return scheme + "://" + workerProxyBridgeAddr
}

// workerProxyEnvironment exposes only the loopback bridge to a worker.
// Validation happens at startup; an impossible value nevertheless fails
// closed here by returning no route rather than the real endpoint.
func workerProxyEnvironment(raw string) []string {
	proxy := localWorkerProxyURL(raw)
	if proxy == "" {
		return nil
	}
	// localhost itself may bypass the proxy. That does not open the host: the
	// worker lives in a private network namespace whose loopback contains only
	// Barrymore's bridge.
	noProxy := "localhost,127.0.0.1,::1"
	return []string{
		"HTTP_PROXY=" + proxy,
		"HTTPS_PROXY=" + proxy,
		"ALL_PROXY=" + proxy,
		"http_proxy=" + proxy,
		"https_proxy=" + proxy,
		"all_proxy=" + proxy,
		"NO_PROXY=" + noProxy,
		"no_proxy=" + noProxy,
	}
}

// mergeEnv combines environment fragments with last-writer-wins semantics
// while preserving a deterministic order. This makes Barrymore's route
// authoritative over a proxy accidentally supplied by an adapter.
func mergeEnv(chunks ...[]string) []string {
	order := []string{}
	values := map[string]string{}
	for _, chunk := range chunks {
		for _, item := range chunk {
			name, _, ok := strings.Cut(item, "=")
			if !ok || name == "" {
				continue
			}
			if _, seen := values[name]; !seen {
				order = append(order, name)
			}
			values[name] = item
		}
	}
	out := make([]string, 0, len(order))
	for _, name := range order {
		out = append(out, values[name])
	}
	return out
}
