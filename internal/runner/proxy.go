package runner

import (
	"fmt"
	"net/url"
	"strings"
)

// WorkerProxyEnv is deliberately Barrymore-specific. barrymored may carry
// this marker in its own environment, but only runner translates it into
// standard HTTP proxy variables for an external worker process.
//
// This keeps the owner's worker route from becoming a network policy for
// Barrymore itself or for the local llama-server it supervises.
const WorkerProxyEnv = "BARRYMORE_WORKER_PROXY"

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

// workerProxyEnvironment turns Barrymore's private setting into the variables
// understood by most networked CLI tools. Validation happens at startup; the
// runner still fails closed here and returns no proxy on an impossible value.
func workerProxyEnvironment(raw string) []string {
	proxy, err := NormalizeWorkerProxy(raw)
	if err != nil || proxy == "" {
		return nil
	}
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
// while preserving a deterministic order. This makes the owner's worker proxy
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
