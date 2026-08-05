package skill

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Ambient — то, что Бэрримор видит о своей машине, не спрашивая.
//
// Это ответ на возражение, которое разрушает подход «умение на каждый вопрос».
// Если бы каждый факт о машине требовал умения, список умений рос бы вслед
// за списком вопросов: сегодня свободное место, завтра дата, послезавтра имя
// машины — и так до бесконечности, причём на любой не предусмотренный вопрос
// модель по-прежнему отвечала бы выдумкой.
//
// Граница проходит не по вопросу, а по цене и цели:
//
//   - факт стоит миллисекунды и не требует указать, о чём речь, — это
//     окружение. Runtime наблюдает его сам, кладёт в контекст, и модель
//     просто знает. Ни умения, ни нажатия, ни ожидания;
//   - факт требует цели — какой каталог, какой репозиторий, чей процесс —
//     это умение. Его надо выбрать и применить.
//
// Поэтому «сколько времени», «сколько места на дисках», «сколько памяти»
// умений не заслуживают и никогда их не получат. А «что творится с worktree
// вот этого репозитория» — заслуживает.
//
// Наблюдение никогда не отказывает целиком: недоступный источник просто
// не даёт своей строки. Пустое окружение хуже неполного.
func Ambient(_ context.Context) []Fact {
	var out []Fact

	if host, err := os.Hostname(); err == nil && host != "" {
		text := "машина: " + host
		if up := uptime(); up != "" {
			text += ", работает " + up
		}
		if load := loadAverage(); load != "" {
			text += ", загрузка " + load
		}
		out = append(out, Fact{Text: text})
	}

	if mem := memory(); mem != "" {
		out = append(out, Fact{Text: mem})
	}

	// Место на дисках — самый частый вопрос из тех, на которые модель отвечала
	// выдуманным числом. Здесь оно измерено, а не припомнено.
	if mounts, err := realMounts(); err == nil {
		var parts []string
		for _, m := range mounts {
			free, total, err := space(m)
			if err != nil || total == 0 {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s %s свободно из %s (%d%%)",
				m, humanBytes(free), humanBytes(total), percent(free, total)))
		}
		if len(parts) > 0 {
			out = append(out, Fact{Text: "место на дисках: " + strings.Join(parts, "; ")})
		}
	}

	return out
}

// Ambient на службе — чтобы разговор не зависел от пакета напрямую.
func (s *Service) Ambient(ctx context.Context) []Fact { return Ambient(ctx) }

func uptime() string {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return ""
	}
	secs, err := strconv.ParseFloat(strings.Fields(string(data))[0], 64)
	if err != nil {
		return ""
	}
	d := time.Duration(secs) * time.Second
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d мин", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d ч", int(d.Hours()))
	default:
		return fmt.Sprintf("%d сут", int(d.Hours()/24))
	}
}

func loadAverage() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return ""
	}
	return strings.Join(f[:3], " ")
}

// memory читает доступную память, а не свободную.
//
// «Свободная» на Linux почти всегда близка к нулю из-за кэша страниц, и
// показывать её значило бы пугать владельца там, где всё в порядке.
func memory() string {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return ""
	}
	var total, available int64
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, convErr := strconv.ParseInt(f[1], 10, 64)
		if convErr != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			available = v * 1024
		}
	}
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("память: доступно %s из %s", humanBytes(available), humanBytes(total))
}
