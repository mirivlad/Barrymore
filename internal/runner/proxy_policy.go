package runner

import "os"

// CloseWorkerProxyRelays закрывает все host-side relay текущей сетевой
// политики персонала.
//
// Вызывается только после остановки всех внешних worker-процессов. После
// возврата старый proxy route больше не принимает новые соединения и его Unix
// sockets удалены. Новая политика при следующем запуске worker поднимет ровно
// тот relay, который ей нужен.
func CloseWorkerProxyRelays() {
	workerRelayState.Lock()
	relays := workerRelayState.relays
	workerRelayState.relays = map[string]*hostProxyRelay{}
	workerRelayState.Unlock()

	for _, r := range relays {
		if r == nil {
			continue
		}
		_ = r.listener.Close()
		_ = os.Remove(r.socket)
	}
}
