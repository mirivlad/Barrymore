package runner

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// ProcessIdentity — надёжная идентичность процесса (ADR 0006).
//
// Голого PID недостаточно: номера переиспользуются, и после рестарта
// Бэрримора чужой процесс может быть принят за живой запуск.
type ProcessIdentity struct {
	UnitName string `json:"unit_name,omitempty"`
	PID      int    `json:"pid"`
	// StartTicks — поле 22 /proc/<pid>/stat: момент старта в тиках с загрузки.
	// Вместе с PID образует пару, уникальную в пределах загрузки системы.
	StartTicks uint64 `json:"start_ticks"`
}

// Liveness — вывод о состоянии процесса с основанием.
type Liveness struct {
	Alive bool `json:"alive"`
	// Certain отличает уверенный вывод от предположения.
	Certain bool   `json:"certain"`
	Source  string `json:"source"`
	Detail  string `json:"detail"`
}

// Probe возвращает вывод о состоянии процесса без экземпляра Runner.
//
// Нужен тем частям Бэрримора, которые ведут собственный процесс, а не запуск
// исполнителя: правило идентичности (ADR 0006) для них то же самое.
func Probe(caps Capabilities, id ProcessIdentity) Liveness { return checkLiveness(caps, id) }

// Terminate останавливает процесс по его идентичности.
func Terminate(caps Capabilities, id ProcessIdentity, hard bool) error {
	return terminate(caps, id, hard)
}

// StartTicks читает момент старта процесса из /proc.
func StartTicks(pid int) (uint64, error) { return readStartTicks(pid) }

// readStartTicks читает момент старта процесса из /proc.
func readStartTicks(pid int) (uint64, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, fmt.Errorf("чтение /proc/%d/stat: %w", pid, err)
	}
	// Имя процесса в скобках может содержать пробелы, поэтому разбор начинается
	// после последней закрывающей скобки.
	line := string(data)
	idx := strings.LastIndexByte(line, ')')
	if idx < 0 || idx+2 >= len(line) {
		return 0, fmt.Errorf("непонятный формат /proc/%d/stat", pid)
	}
	fields := strings.Fields(line[idx+2:])
	// После закрывающей скобки идёт поле 3, значит поле 22 — это индекс 19.
	const startTimeIndex = 19
	if len(fields) <= startTimeIndex {
		return 0, fmt.Errorf("в /proc/%d/stat нет поля времени старта", pid)
	}
	ticks, err := strconv.ParseUint(fields[startTimeIndex], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("разбор времени старта процесса %d: %w", pid, err)
	}
	return ticks, nil
}

// checkLiveness определяет, жив ли именно наш процесс.
//
// Активный unit — достаточное доказательство жизни. Неактивный доказательством
// смерти не является: между fork и регистрацией scope есть окно, в котором
// systemd о процессе ещё не знает и отвечает «inactive» про живой процесс.
// Поэтому отрицательный ответ systemd проверяется по /proc, а решает точная
// пара (pid, время старта) — она же защищает от переиспользованного номера.
func checkLiveness(caps Capabilities, id ProcessIdentity) Liveness {
	unitState := ""
	if id.UnitName != "" && caps.SystemdRun != "" {
		out, _ := exec.Command("systemctl", "--user", "is-active", id.UnitName).Output()
		unitState = strings.TrimSpace(string(out))
		if unitState == "active" || unitState == "activating" {
			return Liveness{Alive: true, Certain: true, Source: "systemd",
				Detail: "unit " + id.UnitName + " в состоянии " + unitState}
		}
	}

	// unitNote добавляет мнение systemd туда, где оно расходится с /proc:
	// молчаливо предпочесть один источник другому значило бы скрыть разногласие.
	unitNote := ""
	if unitState != "" {
		unitNote = fmt.Sprintf(" (unit %s в состоянии %s)", id.UnitName, unitState)
	}

	if id.PID <= 0 {
		if unitState != "" {
			return Liveness{Alive: false, Certain: true, Source: "systemd",
				Detail: "unit " + id.UnitName + " в состоянии " + unitState +
					", номер процесса неизвестен"}
		}
		return Liveness{Alive: false, Certain: false, Source: "none",
			Detail: "идентификатор процесса неизвестен"}
	}

	if err := syscall.Kill(id.PID, 0); err != nil {
		return Liveness{Alive: false, Certain: true, Source: "signal-0",
			Detail: fmt.Sprintf("процесс %d не существует: %v%s", id.PID, err, unitNote)}
	}

	ticks, err := readStartTicks(id.PID)
	if err != nil {
		return Liveness{Alive: false, Certain: false, Source: "proc",
			Detail: "процесс с таким номером есть, но время старта не прочитано: " + err.Error()}
	}
	if id.StartTicks != 0 && ticks != id.StartTicks {
		// Номер занят другим процессом. Считать его нашим было бы ошибкой,
		// из-за которой завершившийся запуск выглядел бы живым.
		return Liveness{Alive: false, Certain: true, Source: "proc",
			Detail: fmt.Sprintf(
				"номер %d занят другим процессом: время старта %d вместо %d%s",
				id.PID, ticks, id.StartTicks, unitNote)}
	}
	return Liveness{Alive: true, Certain: true, Source: "proc",
		Detail: fmt.Sprintf("процесс %d жив, время старта совпадает%s", id.PID, unitNote)}
}

// terminate останавливает процесс мягко, затем жёстко.
func terminate(caps Capabilities, id ProcessIdentity, hard bool) error {
	if id.UnitName != "" && caps.SystemdRun != "" {
		verb := "stop"
		if hard {
			verb = "kill"
		}
		args := []string{"--user", verb, id.UnitName}
		if hard {
			args = append(args, "--signal=SIGKILL")
		}
		if err := exec.Command("systemctl", args...).Run(); err == nil {
			return nil
		}
		// systemd не справился — переходим к сигналу группе процессов.
	}
	if id.PID <= 0 {
		return fmt.Errorf("останов невозможен: идентификатор процесса неизвестен")
	}
	sig := syscall.SIGTERM
	if hard {
		sig = syscall.SIGKILL
	}
	// Отрицательный PID — вся группа процессов: дочерние процессы исполнителя
	// не должны пережить остановку.
	if err := syscall.Kill(-id.PID, sig); err != nil {
		if err := syscall.Kill(id.PID, sig); err != nil {
			return fmt.Errorf("останов процесса %d: %w", id.PID, err)
		}
	}
	return nil
}
