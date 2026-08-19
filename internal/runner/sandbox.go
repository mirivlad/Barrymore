// Package runner запускает исполнителей как отдельную границу побочных эффектов.
//
// ADR 0006: идентичность процесса — имя systemd-scope плюс пара
// (pid, время старта из /proc). ADR 0007: audit-only обеспечивается
// изоляцией ядра, а не просьбой исполнителю.
package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mirivlad/barrymore/internal/worker"
)

// Capabilities описывает, какие механизмы изоляции доступны на хосте.
type Capabilities struct {
	Bwrap      string `json:"bwrap,omitempty"`
	SystemdRun string `json:"systemd_run,omitempty"`
	// Reason объясняет отсутствие механизма.
	Reasons map[string]string `json:"reasons,omitempty"`
}

// DetectCapabilities проверяет не только наличие bwrap и systemd-run, но и
// возможность реально использовать bubblewrap. На контейнерных/CI-хостах
// бинарник может существовать, а создание user/network namespace быть
// запрещено ядром или политикой хоста. Считать такую установку изоляцией нельзя.
func DetectCapabilities() Capabilities {
	c := Capabilities{Reasons: map[string]string{}}

	if p, err := exec.LookPath("bwrap"); err == nil {
		if probeErr := probeBubblewrap(p); probeErr == nil {
			c.Bwrap = p
		} else {
			c.Reasons["bwrap"] = "найден, но пробная изоляция не работает: " + probeErr.Error()
		}
	} else {
		c.Reasons["bwrap"] = "не найден в PATH: " + err.Error()
	}

	p, err := exec.LookPath("systemd-run")
	switch {
	case err != nil:
		c.Reasons["systemd-run"] = "не найден в PATH: " + err.Error()
	case os.Getenv("XDG_RUNTIME_DIR") == "":
		c.Reasons["systemd-run"] = "XDG_RUNTIME_DIR не задан: пользовательский systemd недоступен"
	default:
		c.SystemdRun = p
	}
	return c
}

func probeBubblewrap(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Это минимальный реальный профиль из тех же примитивов, на которых
	// строится запуск worker: read-only root, отдельные /proc и /dev,
	// временный /tmp и отдельный network namespace. Если хотя бы один из них
	// запрещён хостом, audit-only/proxy-only обещать нельзя.
	cmd := exec.CommandContext(ctx, path,
		"--ro-bind", "/", "/",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--unshare-net",
		"--new-session",
		"--", "/bin/true",
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("пробный запуск не завершился за 3 секунды: %w", ctx.Err())
	}
	if err == nil {
		return nil
	}
	if detail := strings.TrimSpace(string(out)); detail != "" {
		return fmt.Errorf("%w: %s", err, detail)
	}
	return err
}

// SandboxProfile — итоговое описание изоляции конкретного запуска.
type SandboxProfile struct {
	// Isolation — bwrap либо none.
	Isolation string `json:"isolation"`
	// Supervision — systemd-scope либо process-group.
	Supervision string `json:"supervision"`
	ReadOnlyFS  bool   `json:"read_only_fs"`
	// Network означает обычный прямой network namespace хоста. При ProxyOnly
	// он всегда false: worker видит только loopback в собственной netns.
	Network   bool     `json:"network"`
	ProxyOnly bool     `json:"proxy_only,omitempty"`
	Writable  []string `json:"writable"`
	// Warnings перечисляет ослабления, о которых обязан знать владелец.
	Warnings []string `json:"warnings,omitempty"`
}

// ErrNoIsolation возвращается, когда audit-only невозможно обеспечить.
var ErrNoIsolation = fmt.Errorf(
	"audit-only запуск невозможен: bubblewrap недоступен, " +
		"а полагаться на добросовестность исполнителя нельзя (ADR 0007)")

// ErrNoProxyIsolation возвращается, когда владелец потребовал proxy-only, но
// kernel boundary построить нечем. В таком режиме прямой запуск запрещён.
var ErrNoProxyIsolation = fmt.Errorf(
	"запуск через обязательный прокси невозможен: bubblewrap недоступен; " +
		"без отдельного network namespace нельзя гарантировать отсутствие прямого выхода")

// buildCommand оборачивает план запуска в сетевую generation, изоляцию и
// супервизию.
//
// Для сетевого worker порядок внутренних обёрток такой:
//
//   systemd-run → bwrap → [proxy bridge] → network guard → worker.
//
// Guard делает глобальную смену сетевой policy атомарной даже относительно
// одновременно стартующего процесса. В proxy-only режиме bwrap закрывает
// прямой IP-egress, а bridge даёт единственный предусмотренный сетевой маршрут.
// Полная изоляция от произвольных file-backed Unix-сокетов хоста этим слоем
// пока не заявляется (ADR 0023).
func buildCommand(caps Capabilities, plan worker.RunPlan, opts commandOptions) ([]string, SandboxProfile, error) {
	proxyRaw := os.Getenv(WorkerProxyEnv)
	proxyOnly := proxyRaw != "" && plan.Sandbox.Network
	profile := SandboxProfile{
		Isolation: "none", Supervision: "process-group",
		Network:   plan.Sandbox.Network && !proxyOnly,
		ProxyOnly: proxyOnly,
	}

	inner := append([]string(nil), plan.Argv...)

	// Любой worker, которому нужна сеть, получает generation guard — не только
	// proxy-only. Иначе переход direct → proxy мог бы оставить старый direct
	// процесс жить после того, как интерфейс уже сообщил о новой политике.
	if plan.Sandbox.Network {
		if workerNetworkPolicyChanging.Load() {
			return nil, profile, ErrNetworkPolicyChanging
		}
		policyPath, epoch, err := workerNetworkEpochSnapshot()
		if err != nil {
			return nil, profile, err
		}
		self, err := os.Executable()
		if err != nil {
			return nil, profile, fmt.Errorf("runner: собственный бинарник для network guard не найден: %w", err)
		}
		inner = append([]string{
			self, internalWorkerNetworkGuardMode, policyPath, epoch, "--",
		}, inner...)
	}

	if proxyOnly {
		if caps.Bwrap == "" {
			return nil, profile, ErrNoProxyIsolation
		}
		socketPath, err := ensureWorkerProxyRelay(proxyRaw)
		if err != nil {
			return nil, profile, err
		}
		self, err := os.Executable()
		if err != nil {
			return nil, profile, fmt.Errorf("runner: собственный бинарник для proxy bridge не найден: %w", err)
		}
		// Bridge снаружи guard: он даёт loopback transport, а guard владеет
		// непосредственно полезным worker и его дочерней process group.
		inner = append([]string{self, internalWorkerProxyBridgeMode, socketPath, "--"}, inner...)
	}

	// Кто владеет временем жизни запуска: systemd-scope или процесс Бэрримора.
	//
	// Со scope запуск переживает перезапуск Бэрримора — так и задумано
	// (сценарий H): работа исполнителя не должна пропадать оттого, что
	// управляющий процесс перезапустили. Без systemd такой владелец только
	// один — сам Бэрримор, и тогда `--die-with-parent` необходим, иначе
	// песочница останется жить бесконтрольно.
	ownedByScope := caps.SystemdRun != "" && opts.UnitName != ""

	if caps.Bwrap != "" {
		bw := []string{
			caps.Bwrap,
			// Вся файловая система только для чтения. Рабочий каталог попадает
			// под это правило автоматически: отдельного ro-bind не требуется,
			// а сужать корень нельзя — исполнителю нужны его библиотеки.
			"--ro-bind", "/", "/",
			"--proc", "/proc",
			"--dev", "/dev",
			"--tmpfs", "/tmp",
			// Процесс не может перехватить управляющий терминал.
			"--new-session",
		}
		if proxyOnly {
			// Главное свойство proxy-only: в namespace нет IP-маршрута к хосту
			// или интернету, только loopback. Даже worker, игнорирующий
			// HTTP_PROXY, физически не сможет сделать прямой TCP/UDP connect.
			bw = append(bw,
				"--unshare-net",
				// Стандартные host runtime sockets (DBus, ssh-agent, Docker и
				// подобные) не должны становиться случайным обходным каналом.
				"--tmpfs", "/run",
				"--tmpfs", "/var/tmp",
			)
			// Read-only bind корня не запрещает connect() к уже существующему
			// файловому Unix socket. /run, /tmp и /var/tmp закрывают обычные
			// runtime-точки, но нестандартный socket, лежащий, например, в
			// домашнем каталоге, этим профилем пока не исключён. Не прячем
			// ограничение за словом proxy-only: владелец должен видеть его.
			profile.Warnings = append(profile.Warnings,
				"proxy-only запрещает прямой IP-egress и скрывает стандартные runtime-сокеты; "+
					"нестандартные файловые Unix-сокеты в доступном read-only дереве пока не изолированы")
		}
		if !ownedByScope {
			// Умирает вместе с Бэрримором: иначе за песочницей некому следить.
			bw = append(bw, "--die-with-parent")
			profile.Warnings = append(profile.Warnings,
				"запуск не переживёт перезапуск Бэрримора: без systemd им некому владеть")
		}
		// Рабочий каталог монтируется первым и явно. Полагаться на то, что
		// он «и так под ro-bind /», нельзя: каталог может лежать под /tmp,
		// который стал tmpfs, и тогда исполнитель его просто не увидит.
		if opts.Workspace != "" {
			if opts.WorkspaceWritable {
				bw = append(bw, "--bind", opts.Workspace, opts.Workspace)
				profile.Writable = append(profile.Writable, opts.Workspace)
			} else {
				bw = append(bw, "--ro-bind", opts.Workspace, opts.Workspace)
			}
		}

		// Дальше — монтирования adapter'а строго в объявленном им порядке.
		for _, m := range plan.Sandbox.Mounts {
			switch m.Kind {
			case worker.MountTmpfs:
				bw = append(bw, "--tmpfs", m.Dst)
				profile.Writable = append(profile.Writable, m.Dst)
			case worker.MountReadOnly:
				bw = append(bw, "--ro-bind", m.Src, m.Dst)
			case worker.MountWritable:
				bw = append(bw, "--bind", m.Src, m.Dst)
				profile.Writable = append(profile.Writable, m.Dst)
			default:
				return nil, profile, fmt.Errorf(
					"runner: неизвестный вид монтирования %q для %s", m.Kind, m.Dst)
			}
		}

		if !plan.Sandbox.Network && !proxyOnly {
			bw = append(bw, "--unshare-net")
		}
		bw = append(bw, "--")
		inner = append(bw, inner...)

		profile.Isolation = "bwrap"
		profile.ReadOnlyFS = true
	} else if opts.AuditOnly {
		return nil, profile, ErrNoIsolation
	} else if proxyOnly {
		return nil, profile, ErrNoProxyIsolation
	} else {
		profile.Warnings = append(profile.Warnings,
			"bubblewrap недоступен: файловая система не ограничена изоляцией")
	}

	if caps.SystemdRun != "" && opts.UnitName != "" {
		sd := []string{
			caps.SystemdRun, "--user", "--scope", "--quiet", "--collect",
			"--unit=" + opts.UnitName,
		}
		for _, prop := range opts.ScopeProperties {
			sd = append(sd, "--property="+prop)
		}
		sd = append(sd, "--")
		inner = append(sd, inner...)
		profile.Supervision = "systemd-scope"
	} else {
		profile.Warnings = append(profile.Warnings,
			"пользовательский systemd недоступен: идентичность процесса опирается "+
				"на пару (pid, время старта), ограничения ресурсов не применяются")
	}

	return inner, profile, nil
}

type commandOptions struct {
	AuditOnly         bool
	Workspace         string
	WorkspaceWritable bool
	UnitName          string
	ScopeProperties   []string
}
