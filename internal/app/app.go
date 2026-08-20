// Package app собирает Бэрримора из частей и владеет его жизненным циклом.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/conversation"
	"github.com/mirivlad/barrymore/internal/delegation"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/harness"
	"github.com/mirivlad/barrymore/internal/initiative"
	"github.com/mirivlad/barrymore/internal/learning"
	"github.com/mirivlad/barrymore/internal/localmodel"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/skill"
	"github.com/mirivlad/barrymore/internal/store"
	"github.com/mirivlad/barrymore/internal/thread"
	"github.com/mirivlad/barrymore/internal/worker"
	"github.com/mirivlad/barrymore/internal/worker/codex"
	"github.com/mirivlad/barrymore/internal/worker/hermes"
	"github.com/mirivlad/barrymore/internal/worker/opencode"
)

// Config — настройки экземпляра.
type Config struct {
	DataRoot string
	Addr     string
	// WorkspaceRoots — каталоги, которые разрешено отдавать исполнителям.
	// Пустой список означает запрет: доступ ко всему диску не является умолчанием
	// (01_PRODUCT_BOUNDARY §2.6).
	WorkspaceRoots []string
	TickInterval   time.Duration
	Logger         *slog.Logger
	Clock          clock.Clock
	// ModelPolicy задаёт допустимую стоимость моделей.
	ModelPolicy worker.ModelPolicy
	// ProviderEndpoint — адрес OpenAI-совместимого провайдера разговорного слоя.
	// Пустая строка означает честное отключённое состояние.
	ProviderEndpoint string
	ProviderModel    string
	ProviderLabel    string
	// ProviderAPIKey читается из окружения и нигде не журналируется.
	ProviderAPIKey string
	// Provider подставляет готового провайдера вместо создаваемого по адресу.
	//
	// Нужен сквозным тестам: путь «браузер → маршрут → служба → база» иначе
	// проверить нечем, а проверять его отдельно от разговорного слоя значит
	// не проверять главный сценарий продукта вовсе.
	Provider model.Provider
	// MemoryPolicy задаёт, что Бэрримор записывает сам.
	MemoryPolicy memory.Policy
	// InitiativePolicy задаёт рамки, в которых Бэрримор обращается первым.
	InitiativePolicy initiative.Policy
	// LocalModel описывает сервер локальной модели, если Бэрримор ведёт его сам.
	// Пустой ModelPath означает, что сервер поднимает владелец.
	LocalModel localmodel.Spec
	// ModelsDir — где искать модели для выбора в интерфейсе.
	ModelsDir string
	// Settings — сохранённый выбор владельца, прочитанный до запуска.
	Settings Settings
}

// App — собранный экземпляр Бэрримора.
type App struct {
	Config     Config
	DB         *store.DB
	Journal    *event.Journal
	Runtime    *runtime.Runtime
	Threads    *thread.Service
	Registry   *worker.Registry
	Delegation *delegation.Service
	Memory     *memory.Service
	Talk       *conversation.Service
	Skills     *skill.Service
	Learning   *learning.Service
	Harness    *harness.Service
	LocalModel *localmodel.Supervisor
	Initiative *initiative.Service
	Settings   *SettingsStore
	Policy     *Policy
	Projector  *projection.Registry
	Log        *slog.Logger
	Clock      clock.Clock

	// StartupNotes честно перечисляет ограничения текущего запуска.
	StartupNotes []string

	// lock не даёт второму экземпляру работать на том же каталоге данных.
	lock *lockfile

	stopOnce   sync.Once
	stopped    chan struct{}
	wg         sync.WaitGroup
	turnMu     sync.Mutex
	turnCtx    context.Context
	turnCancel context.CancelFunc
	closing    bool
}

// DefaultDataRoot возвращает каталог данных по умолчанию.
func DefaultDataRoot() string {
	if v := os.Getenv("BARRYMORE_DATA_ROOT"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "barrymore")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "./data"
	}
	return filepath.Join(home, ".local", "share", "barrymore")
}

// New собирает приложение и применяет миграции.
func New(ctx context.Context, cfg Config) (*App, error) {
	if cfg.DataRoot == "" {
		cfg.DataRoot = DefaultDataRoot()
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real{}
	}
	if len(cfg.ModelPolicy.AllowedTiers) == 0 {
		cfg.ModelPolicy = worker.FreeOnly()
	}
	for _, dir := range []string{"runs", "artifacts", "exports"} {
		if err := os.MkdirAll(filepath.Join(cfg.DataRoot, dir), 0o700); err != nil {
			return nil, fmt.Errorf("каталог данных %s: %w", dir, err)
		}
	}

	// Замок берётся до открытия базы: SQLite в режиме WAL пустит второй
	// экземпляр молча, и разойдутся они уже на ходу.
	lock, err := lockDataRoot(cfg.DataRoot)
	if err != nil {
		return nil, err
	}

	db, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(cfg.DataRoot, "barrymore.db"),
		Logger: cfg.Logger,
	})
	if err != nil {
		lock.release()
		return nil, err
	}

	turnCtx, turnCancel := context.WithCancel(ctx)
	a := &App{Config: cfg, DB: db, Log: cfg.Logger, Clock: cfg.Clock,
		turnCtx: turnCtx, turnCancel: turnCancel,
		lock: lock, stopped: make(chan struct{})}
	a.Settings = NewSettingsStore(cfg.DataRoot, cfg.Settings)
	a.Journal = event.NewJournal(db, cfg.Clock)
	a.Policy = NewPolicy(cfg.WorkspaceRoots)

	a.Runtime = runtime.New(runtime.Config{
		DB: db, Journal: a.Journal, Clock: cfg.Clock,
		Policy: a.Policy, Logger: cfg.Logger,
	})
	a.Threads = thread.NewService(db, a.Journal, cfg.Clock)
	a.Registry = worker.NewRegistry(db, a.Journal, cfg.Clock, a.Runtime)

	if err := a.registerAdapters(); err != nil {
		a.Close()
		return nil, err
	}

	a.Delegation = delegation.New(delegation.Config{
		DB: db, Journal: a.Journal, Clock: cfg.Clock, Runtime: a.Runtime,
		Registry: a.Registry, Threads: a.Threads, Logger: cfg.Logger,
		DataRoot: cfg.DataRoot, ModelPolicy: cfg.ModelPolicy,
	})
	if err := a.Delegation.RegisterReflexes(); err != nil {
		a.Close()
		return nil, err
	}

	a.Memory = memory.NewService(db, a.Journal, cfg.Clock, cfg.MemoryPolicy)

	// Заданная локальная модель сама определяет адрес провайдера: указывать
	// его отдельно значило бы допустить расхождение между тем, что Бэрримор
	// поднимает, и тем, куда он обращается.
	if cfg.ProviderEndpoint == "" && cfg.LocalModel.ModelPath != "" {
		cfg.ProviderEndpoint = cfg.LocalModel.Endpoint()
	}
	a.Config = cfg

	provider := cfg.Provider
	if provider == nil && cfg.ProviderEndpoint != "" {
		provider = model.NewOpenAICompatible(
			cfg.ProviderEndpoint, cfg.ProviderModel, cfg.ProviderAPIKey, cfg.ProviderLabel)
	}

	a.LocalModel = localmodel.New(localmodel.Config{
		Spec: cfg.LocalModel, Runtime: a.Runtime, Clock: cfg.Clock, Logger: cfg.Logger,
		StateDir: filepath.Join(cfg.DataRoot, "model"),
		Caps:     a.Delegation.Runner().Capabilities(),
		Provider: provider,
	})
	if a.LocalModel.Enabled() {
		if err := a.LocalModel.RegisterReflexes(); err != nil {
			a.Close()
			return nil, err
		}
	}
	// Умения поднимаются до разговора: разговор показывает их модели,
	// и пустой список означал бы, что Бэрримор о себе не знает.
	a.Skills = skill.New(skill.Config{
		DB: db, Journal: a.Journal, Clock: cfg.Clock,
		Policy: a.Policy, Logger: cfg.Logger,
	})
	if err := a.Skills.Restore(context.Background()); err != nil {
		a.Close()
		return nil, err
	}

	// Обучение подключается к умениям сразу: опыт собирается тем же действием,
	// что его порождает, а не отдельным обходом.
	a.Learning = learning.New(learning.Config{
		DB: db, Journal: a.Journal, Clock: cfg.Clock,
		Skills: a.Skills, Runs: a.Skills, Logger: cfg.Logger,
	})
	a.Skills.Watch(a.Learning)
	a.Delegation.Watch(a.Learning)

	identity := conversation.DefaultIdentity()
	identity.KeepsOwnModel = a.LocalModel.Enabled()
	identity.MemoryRule = a.Memory.Policy().Rule()
	a.Talk = conversation.New(conversation.Config{
		DB: db, Journal: a.Journal, Clock: cfg.Clock, Provider: provider,
		Threads: a.Threads, Memory: a.Memory, Runtime: a.Runtime, Logger: cfg.Logger,
		Skills: a.Skills, Practices: a.Learning, Identity: identity,
	})

	// Подключение незнакомых инструментов — тоже разговорный слой: без модели
	// справку разобрать нечем, и это честно сказано в отказе.
	a.Harness = harness.New(harness.Config{
		Provider: provider, Journal: a.Journal, Clock: cfg.Clock,
		Registry: a.Registry, Logger: cfg.Logger,
	})

	a.Initiative = initiative.NewService(db, a.Journal, cfg.Clock,
		cfg.InitiativePolicy, cfg.Logger)
	a.Initiative.AddSource(workSource{app: a})

	a.Projector = projection.NewRegistry()
	a.Runtime.Projections(a.Projector)
	a.Threads.Projections(a.Projector)
	a.Registry.Projections(a.Projector)
	a.Delegation.Projections(a.Projector)
	a.Memory.Projections(a.Projector)
	a.Talk.Projections(a.Projector)
	a.Skills.Projections(a.Projector)
	a.Learning.Projections(a.Projector)
	// События подключения — аудиторские: штат ведёт собственные проекции,
	// и вторая запись о том же исполнителе только расходилась бы с первой.
	a.Projector.OnAudit(harness.EvSurveyed, harness.EvAdopted, harness.EvRefused)
	a.Initiative.Projections(a.Projector)

	a.collectStartupNotes()
	return a, nil
}

func (a *App) registerAdapters() error {
	// Порядок отражает расстановку: повседневные исполнители впереди,
	// мастера по вызову — следом.
	adapters := []worker.Adapter{
		opencode.New(),
		hermes.New(),
		codex.New(),
	}
	for _, ad := range adapters {
		if err := a.Registry.Register(ad); err != nil {
			return err
		}
	}
	for _, m := range worker.BuiltinManifests() {
		if err := a.Registry.Register(worker.NewManifestAdapter(m)); err != nil {
			return err
		}
	}
	return nil
}

// collectStartupNotes фиксирует ограничения, о которых владелец должен знать.
//
// Деградация показывается честно и не маскируется (00_PRODUCT_VISION §9.13).
func (a *App) collectStartupNotes() {
	caps := a.Delegation.Runner().Capabilities()
	if caps.Bwrap == "" {
		a.StartupNotes = append(a.StartupNotes,
			"bubblewrap недоступен: audit-only поручения запускаться не будут ("+
				caps.Reasons["bwrap"]+")")
	}
	if caps.SystemdRun == "" {
		a.StartupNotes = append(a.StartupNotes,
			"пользовательский systemd недоступен: идентичность процессов менее надёжна ("+
				caps.Reasons["systemd-run"]+")")
	}
	if len(a.Config.WorkspaceRoots) == 0 {
		a.StartupNotes = append(a.StartupNotes,
			"не задан ни один разрешённый рабочий каталог: поручения будут отклоняться политикой")
	}
	a.StartupNotes = append(a.StartupNotes,
		"политика стоимости: "+a.Config.ModelPolicy.Describe())
	a.StartupNotes = append(a.StartupNotes,
		"память: "+a.Memory.Policy().Describe())
	a.StartupNotes = append(a.StartupNotes,
		"инициатива: "+a.Initiative.Policy().Describe())
	if !a.Talk.Available() {
		a.StartupNotes = append(a.StartupNotes,
			"разговорный слой не настроен: Бэрримор не разговаривает, "+
				"но нити, штат, поручения и предиктивный контур работают")
	}
	if note := a.LocalModel.StartupNote(); note != "" {
		a.StartupNotes = append(a.StartupNotes, note)
	}
}

// Start выполняет восстановление и запускает планировщик.
func (a *App) Start(ctx context.Context) error {
	if err := a.DB.Integrity(ctx); err != nil {
		return err
	}
	interrupted, err := a.Talk.InterruptUnfinished(ctx)
	if err != nil {
		return fmt.Errorf("восстановление незавершённых разговорных ходов: %w", err)
	}
	if interrupted > 0 {
		a.Log.Info("незавершённые разговорные ходы отмечены прерванными",
			"количество", interrupted)
	}
	res, err := a.Delegation.Reconcile(ctx)
	if err != nil {
		return fmt.Errorf("восстановление после запуска: %w", err)
	}
	if res.Checked > 0 {
		a.Log.Info("сверка незавершённых запусков",
			"проверено", res.Checked, "продолжено", res.Resumed,
			"потеряно", res.Orphaned, "принято", res.Finalized)
		for _, note := range res.Notes {
			a.Log.Info("восстановление: " + note)
		}
	}

	if err := a.LocalModel.EnsureExpectation(ctx); err != nil {
		return fmt.Errorf("ожидание по локальной модели: %w", err)
	}

	a.wg.Add(1)
	go a.tickLoop(ctx)

	if a.LocalModel.Configured() {
		a.wg.Add(1)
		go a.observeLocalModel(ctx)
	}

	if a.LocalModel.Enabled() {
		// Загрузка больших весов занимает минуты, поэтому запуск сервера не
		// задерживает готовность Бэрримора: интерфейс сразу честно показывает
		// «модель поднимается», а не делает вид, что её нет.
		a.wg.Add(1)
		go a.ensureLocalModel(ctx)
	}
	return nil
}

var ErrShuttingDown = errors.New("Barrymore завершает работу и не принимает новые ходы")

// BeginTurn atomically accepts an owner message, registers its runner before
// returning, and executes it under the application lifetime rather than the
// HTTP request lifetime.
func (a *App) BeginTurn(ctx context.Context, conversationID, text string) (conversation.TurnRun, error) {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.closing {
		return conversation.TurnRun{}, ErrShuttingDown
	}
	run, err := a.Talk.BeginTurn(ctx, conversationID, text)
	if err != nil {
		return conversation.TurnRun{}, err
	}
	a.wg.Add(1)
	go a.executeTurn(run.ID)
	return run, nil
}

func (a *App) executeTurn(turnID string) {
	defer a.wg.Done()
	defer func() {
		if recovered := recover(); recovered != nil {
			a.Log.Error("паника при выполнении разговорного хода",
				"turn", turnID, "panic", recovered, "stack", string(debug.Stack()))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := a.Talk.FailTurn(ctx, turnID, "turn_panic",
				fmt.Sprintf("внутренняя паника: %v", recovered)); err != nil {
				a.Log.Error("паника разговорного хода не записана", "turn", turnID, "error", err)
			}
		}
	}()
	if _, err := a.Talk.ExecuteTurn(a.turnCtx, turnID); err != nil &&
		!errors.Is(err, context.Canceled) {
		a.Log.Error("разговорный ход завершился ошибкой", "turn", turnID, "error", err)
	}
}

// observeLocalModel регулярно записывает наблюдение о состоянии модели.
//
// Наблюдение отделено от действия: этот цикл ничего не запускает, он только
// сообщает предиктивному контуру, что видит. Решение о перезапуске принимает
// ожидание и его реакция с бюджетом попыток.
func (a *App) observeLocalModel(ctx context.Context) {
	defer a.wg.Done()
	ticker := time.NewTicker(a.LocalModel.ObserveInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopped:
			return
		case <-ticker.C:
			if _, err := a.LocalModel.Observe(ctx); err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				a.Log.Error("наблюдение за локальной моделью прервано", "error", err)
			}
		}
	}
}

// ensureLocalModel поднимает локальную модель в фоне.
func (a *App) ensureLocalModel(ctx context.Context) {
	defer a.wg.Done()
	st, err := a.LocalModel.Ensure(ctx)
	switch {
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		a.Log.Warn("локальная модель не поднялась", "error", err,
			"log", a.LocalModel.LogPath())
	case st.Serving:
		a.Log.Info("локальная модель отвечает", "endpoint", st.Endpoint,
			"поднята_бэрримором", st.Managed)
	case st.Loading:
		// Отведённое время вышло, а веса всё грузятся. Это не отказ, но и не
		// повод молчать: владелец должен знать, почему разговор недоступен.
		a.Log.Warn("локальная модель всё ещё грузится", "endpoint", st.Endpoint,
			"log", a.LocalModel.LogPath())
	}
}

// tickLoop крутит предиктивный контур.
//
// Это единственный цикл, которому не нужна LLM: он проверяет ожидания,
// фиксирует расхождения и запускает разрешённые локальные реакции.
func (a *App) tickLoop(ctx context.Context) {
	defer a.wg.Done()
	ticker := time.NewTicker(a.Config.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopped:
			return
		case <-ticker.C:
			res, err := a.Runtime.Tick(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				a.Log.Error("проход предиктивного контура прерван", "error", err)
				continue
			}
			if res.Discrepancies > 0 || res.Reflexes > 0 || res.Escalations > 0 {
				a.Log.Info("предиктивный контур",
					"проверено", res.Checked, "расхождений", res.Discrepancies,
					"реакций", res.Reflexes, "эскалаций", res.Escalations)
			}

			// Инициатива — не отдельный демон, а следствие того, что Бэрримор
			// и так наблюдает: поводы берутся из уже установленных фактов.
			created, delivered, err := a.Initiative.Tick(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				a.Log.Error("инициатива прервана", "error", err)
				continue
			}
			if created > 0 || delivered > 0 {
				a.Log.Info("инициатива", "поводов", created, "показано", delivered)
			}
		}
	}
}

// Close останавливает приложение.
//
// Процессы исполнителей не убиваются: они живут в собственных scope и
// переживают перезапуск (сценарий H).
func (a *App) Close() error {
	a.turnMu.Lock()
	a.closing = true
	if a.turnCancel != nil {
		a.turnCancel()
	}
	a.stopOnce.Do(func() { close(a.stopped) })
	a.turnMu.Unlock()
	a.wg.Wait()
	// Close вызывается и при неудачной сборке, когда собрано ещё не всё:
	// падать на уборке — худший способ сообщить о проблеме в другом месте.
	if a.Delegation != nil {
		a.Delegation.Runner().Shutdown()
	}
	var err error
	if a.DB != nil {
		err = a.DB.Close()
	}
	// Замок снимается последним: пока база открыта, каталог занят.
	a.lock.release()
	return err
}

// Rebuild пересобирает проекции из журнала.
func (a *App) Rebuild(ctx context.Context) error {
	return projection.Rebuild(ctx, a.DB, a.Journal, a.Projector)
}

// Policy проверяет доступ к каталогам и классам действий.
//
// 06_SECURITY §2.1: безопасность реализуется runtime, а не инструкцией модели.
type Policy struct {
	mu    sync.RWMutex
	roots []string
}

// ModelPolicyChoices перечисляет то, из чего выбирает владелец.
//
// Три варианта, а не тонкая настройка по родам задач: «платные для кодинга,
// бесплатные для остального» требует решить, что такое род задачи и кто его
// определяет, — а это отдельный разговор, а не поле в форме.
func ModelPolicyChoices() []map[string]string {
	return []map[string]string{
		{"name": "free", "title": "только бесплатные",
			"why": "ни одного платного вызова; часть исполнителей будет отказываться"},
		{"name": "prefer-free", "title": "бесплатные и по подписке",
			"why": "платные деньгами запрещены, квота подписки разрешена"},
		{"name": "any", "title": "разрешить платные",
			"why": "бесплатные всё равно предпочтительнее; платный вызов возможен"},
	}
}

// NewPolicy создаёт политику с разрешёнными корнями.
func NewPolicy(roots []string) *Policy {
	return &Policy{roots: cleanRoots(roots)}
}

// cleanRoots приводит пути к каноническому виду и убирает повторы.
func cleanRoots(roots []string) []string {
	clean := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		clean = append(clean, abs)
	}
	return clean
}

// Roots возвращает разрешённые корни.
func (p *Policy) Roots() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.roots...)
}

// SetRoots заменяет список разрешённых корней.
//
// Меняется на ходу намеренно: разрешение каталога — то, ради чего владелец
// не должен перезапускать систему. Отзыв разрешения тоже действует сразу,
// иначе «я передумал» ничего не значило бы до перезагрузки.
//
// На уже идущие запуски это не влияет: их изоляция задана при старте процесса,
// и утверждать обратное было бы обманом.
func (p *Policy) SetRoots(roots []string) []string {
	clean := cleanRoots(roots)
	p.mu.Lock()
	p.roots = clean
	p.mu.Unlock()
	return append([]string(nil), clean...)
}

// CheckRoot проверяет, что каталог годится в разрешённые.
//
// Отказ до записи: разрешить несуществующий путь значит поселить в настройках
// строку, о которую всё будет спотыкаться потом.
func CheckRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("путь не задан")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("путь %q: %w", path, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("каталог %s недоступен: %w", abs, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s — не каталог", abs)
	}
	if abs == "/" {
		return "", errors.New(
			"корень файловой системы разрешать нельзя: это не ограничение, а его отсутствие")
	}
	if home, err := os.UserHomeDir(); err == nil && abs == home {
		return "", errors.New(
			"весь домашний каталог разрешать не стоит: укажите каталог с работой, " +
				"иначе исполнители увидят почту, ключи и учётные записи")
	}
	return abs, nil
}

// AllowWorkspace проверяет, что путь лежит внутри разрешённого корня.
//
// Путь приводится к каноническому виду до сравнения, поэтому "..", символьные
// ссылки и относительные пути не позволяют выйти за пределы корня.
func (p *Policy) AllowWorkspace(path string) error {
	if path == "" {
		return errors.New("рабочий каталог не задан")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("путь %q: %w", path, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	roots := p.Roots()
	if len(roots) == 0 {
		return errors.New(
			"не задан ни один разрешённый рабочий каталог; " +
				"доступ ко всему диску не является значением по умолчанию")
	}
	for _, root := range roots {
		if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("каталог %q находится вне разрешённых корней %v", abs, roots)
}

// Check реализует runtime.PolicyGate.
func (p *Policy) Check(_ context.Context, req runtime.PolicyRequest) runtime.PolicyResult {
	switch req.ActionClass {
	case "read", "process_probe":
		// Чтение состояния и проверка живости обратимы и локальны.
		return runtime.PolicyResult{Allowed: true, Rule: "reflex.read.allow"}
	case localmodel.ActionClass:
		// Поднятие сервера модели — не чтение, поэтому у него отдельный класс.
		// Разрешено потому, что действие затрагивает только собственный
		// инструмент Бэрримора, ничего не пишет в рабочие каталоги владельца
		// и ограничено бюджетом попыток: три неудачи уводят вопрос к человеку.
		return runtime.PolicyResult{Allowed: true, Rule: "reflex.local_model.allow"}
	default:
		return runtime.PolicyResult{
			Allowed: false, Rule: "reflex.default.deny",
			Reason: "класс действия " + req.ActionClass +
				" не разрешён локальной реакции без отдельной политики",
		}
	}
}
