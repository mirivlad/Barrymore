// Package localmodel держит локальный сервер модели как обычный наблюдаемый
// процесс Бэрримора, а не как внешнее допущение.
//
// ADR 0012 принял llama.cpp как локального провайдера, но запускать сервер
// оставалось владельцу вручную. Это делало разговорный слой хрупким: Бэрримор
// честно показывал недоступность, но ничего не мог с ней сделать. Здесь сервер
// становится тем же, чем для Бэрримора являются исполнители — процессом с
// надёжной идентичностью (ADR 0006), наблюдением, ожиданием и ограниченной
// локальной реакцией на пропажу.
//
// Изоляции bubblewrap здесь нет намеренно. Сервер модели — собственный
// инструмент Бэрримора, а не чужой исполнитель: ему нужны видеокарта и
// слушающий порт, а `--unshare-net` отнял бы и то и другое. Ограничение,
// которое ломает работу и ничего не защищает, — самообман, а не безопасность.
package localmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/runner"
	"github.com/mirivlad/barrymore/internal/runtime"
)

// SubjectID — субъект, о котором ведутся наблюдения и ожидания.
const SubjectID = "local-model"

// PolicyRestart — идентификатор локальной реакции на пропажу модели.
const PolicyRestart = "local_model.restart"

// ActionClass — класс действия реакции. Отдельный класс, а не «read»:
// поднятие процесса — не чтение, и политика должна разрешать его осознанно.
const ActionClass = "local_model_start"

// Spec — как поднимать сервер модели.
type Spec struct {
	// Binary — путь к llama-server. Пустая строка означает поиск в PATH.
	Binary string
	// ModelPath — файл .gguf. Пустая строка означает, что надзор выключен.
	ModelPath string
	Host      string
	Port      int
	// ContextSize, Threads, GPULayers, CPUMoE — параметры, подтверждённые
	// спайком S1 на этом хосте.
	ContextSize int
	Threads     int
	GPULayers   int
	CPUMoE      int
	// Jinja включает шаблон чата модели: без него принуждение к схеме и
	// отключение размышлений работают не так, как проверялось.
	Jinja bool
	// ExtraArgs добавляются в конец как есть.
	ExtraArgs []string
	// LoadTimeout — сколько ждать готовности после запуска. Большая модель
	// поднимается минутами, и торопить её нечем.
	LoadTimeout time.Duration
	// ObserveEvery — как часто записывать наблюдение о состоянии.
	ObserveEvery time.Duration
}

// Configured сообщает, просили ли Бэрримора вести сервер модели.
func (s Spec) Configured() bool { return s.ModelPath != "" }

// Endpoint возвращает адрес сервера с учётом умолчаний.
func (s Spec) Endpoint() string {
	d := s.withDefaults()
	return "http://" + d.Host + ":" + strconv.Itoa(d.Port)
}

// withDefaults подставляет значения, проверенные спайком S1.
func (s Spec) withDefaults() Spec {
	if s.Host == "" {
		s.Host = "127.0.0.1"
	}
	if s.Port == 0 {
		s.Port = 18080
	}
	if s.ContextSize == 0 {
		s.ContextSize = 32768
	}
	if s.LoadTimeout <= 0 {
		s.LoadTimeout = 10 * time.Minute
	}
	if s.ObserveEvery <= 0 {
		s.ObserveEvery = 15 * time.Second
	}
	return s
}

// Argv собирает команду запуска.
func (s Spec) Argv(binary string) []string {
	argv := []string{
		binary,
		"-m", s.ModelPath,
		"--host", s.Host,
		"--port", strconv.Itoa(s.Port),
		"-c", strconv.Itoa(s.ContextSize),
	}
	if s.Threads > 0 {
		argv = append(argv, "-t", strconv.Itoa(s.Threads))
	}
	if s.GPULayers > 0 {
		argv = append(argv, "-ngl", strconv.Itoa(s.GPULayers))
	}
	if s.CPUMoE > 0 {
		argv = append(argv, "-ncmoe", strconv.Itoa(s.CPUMoE))
	}
	if s.Jinja {
		argv = append(argv, "--jinja")
	}
	return append(argv, s.ExtraArgs...)
}

// Resolve находит исполняемый файл и проверяет наличие модели.
//
// Возвращает причину отказа человеческим языком: невозможность поднять модель
// должна быть видна как ограничение запуска, а не как загадочная тишина.
func (s Spec) Resolve() (binary string, err error) {
	if !s.Configured() {
		return "", errors.New("файл модели не задан")
	}
	if st, statErr := os.Stat(s.ModelPath); statErr != nil {
		return "", fmt.Errorf("файл модели %s недоступен: %w", s.ModelPath, statErr)
	} else if st.IsDir() {
		return "", fmt.Errorf("%s — каталог, а не файл модели", s.ModelPath)
	}

	candidates := []string{s.Binary}
	if s.Binary == "" {
		candidates = []string{
			"third_party/llama.cpp/build/bin/llama-server",
			"llama-server",
		}
	}
	var last error
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if filepath.Base(c) != c {
			abs, absErr := filepath.Abs(c)
			if absErr != nil {
				last = absErr
				continue
			}
			if st, statErr := os.Stat(abs); statErr == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
				return abs, nil
			} else if statErr != nil {
				last = statErr
			} else {
				last = fmt.Errorf("%s не является исполняемым файлом", abs)
			}
			continue
		}
		if p, lookErr := exec.LookPath(c); lookErr == nil {
			return p, nil
		} else {
			last = lookErr
		}
	}
	if last == nil {
		last = errors.New("не найден")
	}
	return "", fmt.Errorf("llama-server не найден: %w", last)
}

// State — что Бэрримор знает о сервере модели прямо сейчас.
type State struct {
	Configured bool   `json:"configured"`
	Serving    bool   `json:"serving"`
	Loading    bool   `json:"loading"`
	Endpoint   string `json:"endpoint,omitempty"`
	ModelPath  string `json:"model_path,omitempty"`
	// Managed отличает процесс, поднятый Бэрримором, от чужого на том же адресе.
	Managed   bool                   `json:"managed"`
	Identity  runner.ProcessIdentity `json:"identity,omitempty"`
	StartedAt *time.Time             `json:"started_at,omitempty"`
	LogPath   string                 `json:"log_path,omitempty"`
	// Reason объясняет текущее положение дел на человеческом языке.
	Reason     string    `json:"reason"`
	ObservedAt time.Time `json:"observed_at"`
}

// persisted — то, что переживает перезапуск Бэрримора.
//
// Сервер модели остаётся жить: загрузка больших весов занимает минуты, и
// убивать её из-за перезапуска управляющего процесса было бы расточительно.
type persisted struct {
	Identity  runner.ProcessIdentity `json:"identity"`
	StartedAt time.Time              `json:"started_at"`
	Endpoint  string                 `json:"endpoint"`
	ModelPath string                 `json:"model_path"`
	Argv      []string               `json:"argv"`
}

// Config — зависимости надзора.
type Config struct {
	Spec     Spec
	Runtime  *runtime.Runtime
	Clock    clock.Clock
	Logger   *slog.Logger
	StateDir string
	Caps     runner.Capabilities
	// Provider проверяет тот же endpoint, которым пользуется разговорный слой.
	// Это не совпадение, а требование: иначе надзор мог бы считать модель
	// готовой тогда, когда разговор её не видит.
	Provider model.Provider
}

// Supervisor ведёт сервер модели.
type Supervisor struct {
	spec     Spec
	binary   string
	resolve  error
	rt       *runtime.Runtime
	clk      clock.Clock
	log      *slog.Logger
	stateDir string
	caps     runner.Capabilities
	provider model.Provider

	mu      sync.Mutex
	last    State
	startMu sync.Mutex
}

// New создаёт надзор. Ошибка разрешения путей не мешает созданию: она
// становится честной причиной в состоянии, а не отказом запуска Бэрримора.
func New(cfg Config) *Supervisor {
	spec := cfg.Spec.withDefaults()
	s := &Supervisor{
		spec: spec, rt: cfg.Runtime, clk: cfg.Clock, log: cfg.Logger,
		stateDir: cfg.StateDir, caps: cfg.Caps, provider: cfg.Provider,
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if spec.Configured() {
		s.binary, s.resolve = spec.Resolve()
	}
	s.last = State{
		Configured: spec.Configured(),
		Endpoint:   spec.Endpoint(),
		ModelPath:  spec.ModelPath,
		Reason:     s.initialReason(),
		ObservedAt: s.now(),
	}
	return s
}

func (s *Supervisor) initialReason() string {
	switch {
	case !s.spec.Configured():
		return "надзор за локальной моделью выключен: файл модели не задан"
	case s.resolve != nil:
		return "локальную модель поднять нечем: " + s.resolve.Error()
	default:
		return "состояние ещё не проверялось"
	}
}

func (s *Supervisor) now() time.Time {
	if s.clk == nil {
		return time.Now().UTC()
	}
	return s.clk.Now()
}

// Enabled сообщает, может ли Бэрримор поднимать сервер модели сам.
func (s *Supervisor) Enabled() bool { return s.spec.Configured() && s.resolve == nil }

// Configured сообщает, ждёт ли Бэрримор локальную модель вообще.
//
// Отличается от Enabled: модель может быть нужна, а поднять её нечем — и тогда
// наблюдать за ней всё равно надо, чтобы сказать владельцу правду.
func (s *Supervisor) Configured() bool { return s.spec.Configured() }

// ObserveInterval — как часто следует записывать наблюдение о состоянии.
func (s *Supervisor) ObserveInterval() time.Duration { return s.spec.ObserveEvery }

// Endpoint возвращает адрес сервера.
func (s *Supervisor) Endpoint() string { return s.spec.Endpoint() }

// State возвращает последнее известное состояние без обращения к сети.
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// StartupNote возвращает ограничение запуска, если оно есть.
func (s *Supervisor) StartupNote() string {
	switch {
	case !s.spec.Configured():
		return ""
	case s.resolve != nil:
		return "локальная модель не будет подниматься Бэрримором: " + s.resolve.Error() +
			"; сервер придётся запускать вручную"
	default:
		return ""
	}
}

// identityPath — где хранится идентичность живого процесса.
func (s *Supervisor) identityPath() string {
	return filepath.Join(s.stateDir, "process.json")
}

// LogPath — куда пишется вывод сервера.
func (s *Supervisor) LogPath() string {
	return filepath.Join(s.stateDir, "llama-server.log")
}

func (s *Supervisor) loadIdentity() (persisted, bool) {
	data, err := os.ReadFile(s.identityPath())
	if err != nil {
		return persisted{}, false
	}
	var p persisted
	if err := json.Unmarshal(data, &p); err != nil {
		return persisted{}, false
	}
	return p, p.Identity.PID > 0 || p.Identity.UnitName != ""
}

func (s *Supervisor) saveIdentity(p persisted) error {
	if err := os.MkdirAll(s.stateDir, 0o700); err != nil {
		return fmt.Errorf("каталог состояния модели: %w", err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.identityPath(), data, 0o600)
}

func (s *Supervisor) forgetIdentity() {
	if err := os.Remove(s.identityPath()); err != nil && !os.IsNotExist(err) {
		s.log.Warn("не удалось забыть идентичность сервера модели", "error", err)
	}
}

// Observe определяет текущее состояние и записывает наблюдение.
//
// Ничего не запускает: наблюдение и действие разделены намеренно, чтобы
// решение о перезапуске принимал предиктивный контур, а не тот, кто смотрит.
func (s *Supervisor) Observe(ctx context.Context) (State, error) {
	st := s.look(ctx)
	s.mu.Lock()
	s.last = st
	s.mu.Unlock()

	if !st.Configured || s.rt == nil {
		return st, nil
	}
	_, err := s.rt.RecordObservation(ctx, runtime.ObservationRequest{
		Kind:          runtime.ObsLocalModel,
		SubjectType:   runtime.SubjectProvider,
		SubjectID:     SubjectID,
		ObservedAt:    st.ObservedAt,
		Source:        "localmodel",
		SourceQuality: runtime.QualityDirect,
		Confidence:    1,
		Payload: runtime.LocalModelStatePayload{
			Serving: st.Serving, Loading: st.Loading, Endpoint: st.Endpoint,
			Reason: st.Reason, Managed: st.Managed,
		},
	})
	return st, err
}

// look выясняет состояние: сначала endpoint, потом процесс.
//
// Порядок важен. Отвечающий endpoint — прямое доказательство работоспособности,
// живой процесс — только основание надеяться. Считать модель готовой по живому
// процессу нельзя: она может ещё грузиться или уже сломаться.
func (s *Supervisor) look(ctx context.Context) State {
	st := State{
		Configured: s.spec.Configured(),
		Endpoint:   s.spec.Endpoint(),
		ModelPath:  s.spec.ModelPath,
		LogPath:    s.LogPath(),
		ObservedAt: s.now(),
	}
	if !st.Configured {
		st.Reason = s.initialReason()
		return st
	}

	saved, known := s.loadIdentity()
	if known {
		st.Identity = saved.Identity
		st.Managed = true
		started := saved.StartedAt
		st.StartedAt = &started
	}

	if s.provider != nil {
		if probe := s.provider.Probe(ctx); probe.Ready() {
			st.Serving = true
			if known {
				st.Reason = "сервер модели отвечает; поднят Бэрримором"
			} else {
				st.Reason = "сервер модели отвечает; Бэрримор его не поднимал"
			}
			return st
		} else if probe.Reason != "" {
			st.Reason = probe.Reason
		}
	}

	if known {
		live := runner.Probe(s.caps, saved.Identity)
		if live.Alive {
			st.Loading = true
			st.Reason = "процесс сервера жив, но модель ещё не отвечает: " +
				"скорее всего идёт загрузка весов"
			return st
		}
		// Процесс мёртв — запись об идентичности больше ничего не значит и
		// только вводила бы в заблуждение при следующей проверке.
		s.forgetIdentity()
		st.Identity = runner.ProcessIdentity{}
		st.Managed = false
		st.StartedAt = nil
		st.Reason = "сервер модели не работает: " + live.Detail
		return st
	}

	if st.Reason == "" {
		st.Reason = "сервер модели не отвечает и Бэрримором не запускался"
	}
	return st
}

// Ensure добивается того, чтобы модель обслуживала запросы.
//
// Возвращает состояние на момент окончания ожидания. Загрузка больших весов
// занимает минуты, поэтому вызывать это синхронно на пути запроса нельзя.
func (s *Supervisor) Ensure(ctx context.Context) (State, error) {
	// Одновременных запусков быть не должно: два llama-server на одном порту
	// дали бы непредсказуемое поведение вместо честной ошибки.
	s.startMu.Lock()
	defer s.startMu.Unlock()

	st, err := s.Observe(ctx)
	if err != nil {
		return st, err
	}
	if st.Serving || st.Loading {
		return s.awaitReady(ctx, st)
	}
	if !s.Enabled() {
		return st, nil
	}
	if err := s.launch(ctx); err != nil {
		return s.State(), err
	}
	st, err = s.Observe(ctx)
	if err != nil {
		return st, err
	}
	return s.awaitReady(ctx, st)
}

// awaitReady ждёт готовности, пока процесс жив и время не вышло.
func (s *Supervisor) awaitReady(ctx context.Context, st State) (State, error) {
	if st.Serving {
		return st, nil
	}
	deadline := s.now().Add(s.spec.LoadTimeout)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-ticker.C:
		}
		var err error
		st, err = s.Observe(ctx)
		if err != nil {
			return st, err
		}
		if st.Serving {
			return st, nil
		}
		if !st.Loading {
			return st, fmt.Errorf("сервер модели не поднялся: %s", st.Reason)
		}
		if !s.now().Before(deadline) {
			return st, fmt.Errorf(
				"модель не ответила за %s; процесс жив, вывод сервера в %s",
				s.spec.LoadTimeout, s.LogPath())
		}
	}
}

// launch запускает сервер под собственным systemd-scope.
func (s *Supervisor) launch(ctx context.Context) error {
	if !s.Enabled() {
		return fmt.Errorf("сервер модели поднять нечем: %s", s.initialReason())
	}
	if err := os.MkdirAll(s.stateDir, 0o700); err != nil {
		return fmt.Errorf("каталог состояния модели: %w", err)
	}

	argv := s.spec.Argv(s.binary)
	unit := "barrymore-model.scope"
	if s.caps.SystemdRun != "" {
		// Свежий scope нужен даже если старый остался после павшего процесса:
		// systemd не даст переиспользовать имя, пока unit не убран.
		_ = exec.Command("systemctl", "--user", "reset-failed", unit).Run()
		argv = append([]string{
			s.caps.SystemdRun, "--user", "--scope", "--quiet", "--collect",
			"--unit=" + unit, "--",
		}, argv...)
	} else {
		unit = ""
	}

	logFile, err := os.OpenFile(s.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("журнал сервера модели: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Отдельная группа процессов: остановка должна забирать всё дерево.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	startedAt := s.now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("запуск сервера модели: %w", err)
	}

	id := runner.ProcessIdentity{UnitName: unit, PID: cmd.Process.Pid}
	if ticks, err := runner.StartTicks(id.PID); err == nil {
		id.StartTicks = ticks
	} else {
		s.log.Warn("время старта сервера модели не прочитано; идентичность менее надёжна",
			"pid", id.PID, "error", err)
	}

	if err := s.saveIdentity(persisted{
		Identity: id, StartedAt: startedAt, Endpoint: s.spec.Endpoint(),
		ModelPath: s.spec.ModelPath, Argv: argv,
	}); err != nil {
		return err
	}

	// Процесс намеренно не ожидается: он переживает перезапуск Бэрримора, как
	// и запуски исполнителей. Ошибку старта покажет наблюдение, а не Wait.
	go func() { _ = cmd.Wait() }()

	s.log.Info("поднимаю локальную модель",
		"endpoint", s.spec.Endpoint(), "pid", id.PID, "unit", unit, "log", s.LogPath())

	if s.rt != nil {
		if _, err := s.rt.RecordObservation(ctx, runtime.ObservationRequest{
			Kind:          runtime.ObsProcessLiveness,
			SubjectType:   runtime.SubjectProvider,
			SubjectID:     SubjectID,
			ObservedAt:    startedAt,
			Source:        "localmodel",
			SourceQuality: runtime.QualityDirect,
			Confidence:    1,
			Payload: map[string]any{
				"action": "start", "pid": id.PID, "unit": unit,
				"argv": argv, "log_path": s.LogPath(),
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

// Stop останавливает сервер, поднятый Бэрримором.
//
// Чужой процесс на том же адресе не трогается: Бэрримор не распоряжается тем,
// чего не запускал.
func (s *Supervisor) Stop(ctx context.Context, hard bool) error {
	saved, known := s.loadIdentity()
	if !known {
		return errors.New("Бэрримор не запускал сервер модели и не может его остановить")
	}
	if err := runner.Terminate(s.caps, saved.Identity, hard); err != nil {
		return err
	}
	s.forgetIdentity()
	if s.rt != nil {
		if _, err := s.rt.RecordObservation(ctx, runtime.ObservationRequest{
			Kind:          runtime.ObsProcessLiveness,
			SubjectType:   runtime.SubjectProvider,
			SubjectID:     SubjectID,
			Source:        "localmodel",
			SourceQuality: runtime.QualityDirect,
			Confidence:    1,
			Payload:       map[string]any{"action": "stop", "hard": hard, "pid": saved.Identity.PID},
		}); err != nil {
			return err
		}
	}
	_, err := s.Observe(ctx)
	return err
}
