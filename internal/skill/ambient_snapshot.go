package skill

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// AmbientSnapshot — те же дешёвые наблюдения о машине, которые Barrymore
// получает в разговорный контекст, но в типизированном виде для доверенного UI.
//
// Это не отдельный мониторинг и не второй источник истины: значения читаются
// прямо из тех же Linux-источников (/proc и statfs), что и Ambient. Модель к
// построению карточки отношения не имеет.
type AmbientSnapshot struct {
	ObservedAt      time.Time     `json:"observed_at"`
	Hostname        string        `json:"hostname,omitempty"`
	UptimeSeconds   float64       `json:"uptime_seconds,omitempty"`
	CPUs            int           `json:"cpus"`
	Load1           float64       `json:"load_1,omitempty"`
	Load5           float64       `json:"load_5,omitempty"`
	Load15          float64       `json:"load_15,omitempty"`
	MemoryTotal     int64         `json:"memory_total,omitempty"`
	MemoryAvailable int64         `json:"memory_available,omitempty"`
	Disks           []AmbientDisk `json:"disks,omitempty"`
}

// AmbientDisk — наблюдаемое свободное место на реальной точке монтирования.
type AmbientDisk struct {
	Mount string `json:"mount"`
	Free  int64  `json:"free"`
	Total int64  `json:"total"`
}

// SnapshotAmbient читает текущее состояние машины без побочных действий.
// Отдельный источник может отказать независимо: отсутствие одной цифры не
// превращает всю карточку в «неизвестно».
func SnapshotAmbient() AmbientSnapshot {
	s := AmbientSnapshot{ObservedAt: time.Now().UTC(), CPUs: runtime.NumCPU()}
	s.Hostname, _ = os.Hostname()
	s.UptimeSeconds = ambientUptimeSeconds()
	s.Load1, s.Load5, s.Load15 = ambientLoads()
	s.MemoryTotal, s.MemoryAvailable = ambientMemoryBytes()

	if mounts, err := realMounts(); err == nil {
		for _, mount := range mounts {
			free, total, err := space(mount)
			if err != nil || total == 0 {
				continue
			}
			s.Disks = append(s.Disks, AmbientDisk{Mount: mount, Free: free, Total: total})
		}
	}
	return s
}

func ambientUptimeSeconds() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return v
}

func ambientLoads() (float64, float64, float64) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	one, _ := strconv.ParseFloat(fields[0], 64)
	five, _ := strconv.ParseFloat(fields[1], 64)
	fifteen, _ := strconv.ParseFloat(fields[2], 64)
	return one, five, fifteen
}

func ambientMemoryBytes() (total, available int64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			available = v * 1024
		}
	}
	return total, available
}
