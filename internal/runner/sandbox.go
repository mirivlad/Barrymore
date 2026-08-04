// Package runner запускает исполнителей как отдельную границу побочных эффектов.
//
// ADR 0006: идентичность процесса — имя systemd-scope плюс пара
// (pid, время старта из /proc). ADR 0007: audit-only обеспечивается
// изоляцией ядра, а не просьбой к исполнителю.
package runner

import (
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/mirivlad/barrymore/internal/worker"
)

// Capabilities описывает, какие механизмы изоляции доступны на хосте.
type Capabilities struct {
	Bwrap      string `json:"bwrap,omitempty"`
	SystemdRun string `json:"systemd_run,omitempty"`
	// Reason объясняет отсутствие механизма.
	Reasons map[string]string `json:"reasons,omitempty"`
}

// DetectCapabilities проверяет доступность bwrap и systemd-run.
func DetectCapabilities() Capabilities {
	c := Capabilities{Reasons: map[string]string{}}

	if p, err := exec.LookPath("bwrap"); err == nil {
		c.Bwrap = p
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

// SandboxProfile — итоговое описание изоляции конкретного запуска.
type SandboxProfile struct {
	// Isolation — bwrap либо none.
	Isolation string `json:"isolation"`
	// Supervision — systemd-scope либо process-group.
	Supervision string   `json:"supervision"`
	ReadOnlyFS  bool     `json:"read_only_fs"`
	Network     bool     `json:"network"`
	Writable    []string `json:"writable"`
	// Warnings перечисляет ослабления, о которых обязан знать владелец.
	Warnings []string `json:"warnings,omitempty"`
}

// ErrNoIsolation возвращается, когда audit-only невозможно обеспечить.
var ErrNoIsolation = fmt.Errorf(
	"audit-only запуск невозможен: bubblewrap недоступен, " +
		"а полагаться на добросовестность исполнителя нельзя (ADR 0007)")

// buildCommand оборачивает план запуска в изоляцию и супервизию.
//
// Порядок обёрток: systemd-run --scope → bwrap → сам исполнитель.
// Внешняя обёртка даёт устойчивую идентичность процесса, внутренняя — границы.
func buildCommand(caps Capabilities, plan worker.RunPlan, opts commandOptions) ([]string, SandboxProfile, error) {
	profile := SandboxProfile{
		Isolation: "none", Supervision: "process-group",
		Network: plan.Sandbox.Network,
	}

	inner := append([]string(nil), plan.Argv...)

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
			// Процесс умирает вместе с супервизором и не может перехватить
			// управляющий терминал.
			"--die-with-parent",
			"--new-session",
		}
		for _, dir := range sortedStrings(plan.Sandbox.TmpfsDirs) {
			bw = append(bw, "--tmpfs", dir)
		}
		// Рабочий каталог монтируется явно и после tmpfs. Полагаться на то,
		// что он «и так под ro-bind /», нельзя: каталог может лежать под
		// /tmp, который стал tmpfs, и тогда исполнитель его просто не увидит.
		if opts.Workspace != "" {
			if opts.WorkspaceWritable {
				bw = append(bw, "--bind", opts.Workspace, opts.Workspace)
				profile.Writable = append(profile.Writable, opts.Workspace)
			} else {
				bw = append(bw, "--ro-bind", opts.Workspace, opts.Workspace)
			}
		}
		// Файлы только для чтения монтируются после tmpfs: иначе tmpfs
		// перекрыл бы точку монтирования.
		for _, src := range sortedKeys(plan.Sandbox.ReadOnlyBinds) {
			bw = append(bw, "--ro-bind", src, plan.Sandbox.ReadOnlyBinds[src])
		}
		for _, src := range sortedKeys(plan.Sandbox.WritableBinds) {
			bw = append(bw, "--bind", src, plan.Sandbox.WritableBinds[src])
			profile.Writable = append(profile.Writable, plan.Sandbox.WritableBinds[src])
		}
		if !plan.Sandbox.Network {
			bw = append(bw, "--unshare-net")
		}
		bw = append(bw, "--")
		inner = append(bw, inner...)

		profile.Isolation = "bwrap"
		profile.ReadOnlyFS = true
	} else if opts.AuditOnly {
		return nil, profile, ErrNoIsolation
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

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
