// Command barrymored — основной процесс Бэрримора.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mirivlad/barrymore/internal/api"
	"github.com/mirivlad/barrymore/internal/app"
	"github.com/mirivlad/barrymore/internal/initiative"
	"github.com/mirivlad/barrymore/internal/localmodel"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/runner"
	"github.com/mirivlad/barrymore/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "барримор не запустился:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr     = flag.String("addr", "127.0.0.1:7717", "адрес прослушивания; по умолчанию только петля")
		dataRoot = flag.String("data-root", app.DefaultDataRoot(), "каталог данных")
		roots    = flag.String("workspace-roots", "", "разрешённые рабочие каталоги через запятую")
		logLevel = flag.String("log-level", "info", "уровень журналирования: debug, info, warn, error")
		rebuild  = flag.Bool("rebuild-projections", false, "пересобрать проекции из журнала и выйти")
		tick     = flag.Duration("tick", 5*time.Second, "интервал предиктивного контура")
		costs    = flag.String("model-policy", "free",
			"допустимая стоимость моделей: free, prefer-free, any")
		provider = flag.String("provider", "",
			"адрес OpenAI-совместимого провайдера разговорного слоя, например http://127.0.0.1:18080")
		providerModel = flag.String("provider-model", "local", "имя модели у провайдера")
		providerLabel = flag.String("provider-label", "локальная модель", "как называть провайдера")
		memoryMode    = flag.String("memory-policy", "auto-safe",
			"что Бэрримор записывает сам: ask, auto-safe, auto")
		initMode = flag.String("initiative", "on",
			"когда Бэрримор обращается первым: on, urgent-only, off")
		workerProxy = flag.String("worker-proxy", "",
			"прокси только для внешнего персонала: http(s)://host:port или socks5(h)://host:port")

		lmModel = flag.String("local-model", "",
			"файл .gguf локальной модели; задан — Бэрримор сам поднимает и стережёт llama-server")
		lmDir = flag.String("local-models-dir", "",
			"каталог, где искать модели для выбора в интерфейсе")
		lmBinary = flag.String("llama-server", "",
			"путь к llama-server; пусто — комплектный libexec, затем обычные места и PATH")
		lmPort    = flag.Int("local-model-port", 18080, "порт локального сервера модели")
		lmContext = flag.Int("local-model-context", 32768, "размер контекста локальной модели")
		lmThreads = flag.Int("local-model-threads", 0, "потоков CPU; 0 — умолчание llama-server")
		lmGPU     = flag.Int("local-model-gpu-layers", 0, "слоёв на видеокарту (-ngl)")
		lmCPUMoE  = flag.Int("local-model-cpu-moe", 0, "экспертов MoE оставить на CPU (-ncmoe)")
		lmTimeout = flag.Duration("local-model-load-timeout", 10*time.Minute,
			"сколько ждать готовности модели после запуска")
	)
	flag.Parse()

	// Явно заданный флаг сильнее файла настроек: разовый запуск с другими
	// параметрами должен быть возможен, не переписывая сохранённый выбор.
	given := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { given[f.Name] = true })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(*logLevel)}))
	slog.SetDefault(logger)

	settings, err := app.LoadSettings(*dataRoot)
	if err != nil {
		return err
	}

	// Bootstrap повторяется, пока локальный first-run не закончен: это важно
	// для сценария «сначала распаковал Barrymore, потом положил GGUF в data».
	// При этом явный -local-model остаётся разовым флагом и не записывается как
	// молчаливый постоянный выбор.
	localSetupNeeded := settings.ProviderEndpoint == "" &&
		settings.LocalModel.Path == "" && !modelChoiceDone(*dataRoot)
	bootstrapNeeded := settings.LocalModel.Binary == "" || localSetupNeeded
	choiceCompleted := false
	if bootstrapNeeded {
		oldPath := settings.LocalModel.Path
		found, notes := app.Bootstrap(*dataRoot, settings)
		settings = found
		for _, n := range notes {
			logger.Info("первый запуск: " + n)
		}

		canChooseLocal := localSetupNeeded && !given["local-model"] &&
			!(given["provider"] && strings.TrimSpace(*provider) != "")
		if canChooseLocal {
			proposed := settings.LocalModel.Path
			// Bootstrap может предложить единственную модель, но до решения
			// владельца это ещё не сохранённый выбор.
			settings.LocalModel.Path = ""
			models, modelErr := app.FindModels(settings.LocalModel.ModelsDir, proposed)
			if modelErr != nil {
				logger.Warn("первый запуск: модели не перечислены", "error", modelErr)
			} else {
				selected, completed, note := chooseFirstRunModel(
					models, proposed, stdinInteractive(), os.Stdin, os.Stdout)
				settings.LocalModel.Path = selected
				choiceCompleted = completed
				if note != "" {
					logger.Info("первый запуск: " + note)
				}
			}
		} else {
			settings.LocalModel.Path = oldPath
		}

		if err := app.SaveSettings(*dataRoot, settings); err != nil {
			// Не смертельно: Бэрримор работает и без сохранённых настроек,
			// просто будет выяснять их заново при следующем запуске.
			logger.Warn("настройки первого запуска не сохранены", "error", err)
		} else if choiceCompleted {
			if err := markModelChoiceDone(*dataRoot); err != nil {
				logger.Warn("решение о модели не отмечено", "error", err)
			}
		}
	}

	pick := func(name, flagValue, saved string) string {
		if given[name] || saved == "" {
			return flagValue
		}
		return saved
	}
	pickInt := func(name string, flagValue, saved int) int {
		if given[name] || saved == 0 {
			return flagValue
		}
		return saved
	}

	*addr = pick("addr", *addr, settings.Addr)
	*costs = pick("model-policy", *costs, settings.ModelPolicy)
	*memoryMode = pick("memory-policy", *memoryMode, settings.MemoryPolicy)
	*initMode = pick("initiative", *initMode, settings.Initiative)
	*provider = pick("provider", *provider, settings.ProviderEndpoint)
	*providerModel = pick("provider-model", *providerModel, settings.ProviderModel)
	*providerLabel = pick("provider-label", *providerLabel, settings.ProviderLabel)
	*workerProxy = pick("worker-proxy", *workerProxy, settings.WorkerProxy)

	// Прокси персонала не становится HTTP_PROXY самого процесса. В окружении
	// barrymored остаётся только частная переменная, которую runner превращает
	// в стандартные proxy variables уже для конкретного worker-процесса.
	normalizedProxy, err := runner.NormalizeWorkerProxy(*workerProxy)
	if err != nil {
		return err
	}
	*workerProxy = normalizedProxy
	if normalizedProxy == "" {
		_ = os.Unsetenv(runner.WorkerProxyEnv)
	} else {
		if err := os.Setenv(runner.WorkerProxyEnv, normalizedProxy); err != nil {
			return fmt.Errorf("прокси персонала не установлен: %w", err)
		}
		// Адрес намеренно не журналируется: маршрут может раскрывать внутреннюю
		// сеть. Для диагностики достаточно знать, что настройка включена.
		logger.Info("прокси для внешнего персонала включён")
	}

	lm := settings.LocalModel
	*lmModel = pick("local-model", *lmModel, lm.Path)
	*lmBinary = pick("llama-server", *lmBinary, lm.Binary)
	*lmPort = pickInt("local-model-port", *lmPort, lm.Port)
	*lmContext = pickInt("local-model-context", *lmContext, lm.ContextSize)
	*lmThreads = pickInt("local-model-threads", *lmThreads, lm.Threads)
	*lmGPU = pickInt("local-model-gpu-layers", *lmGPU, lm.GPULayers)
	*lmCPUMoE = pickInt("local-model-cpu-moe", *lmCPUMoE, lm.CPUMoE)
	*lmDir = pick("local-models-dir", *lmDir, lm.ModelsDir)
	if *lmDir == "" && *lmModel != "" {
		// Каталог рядом с выбранной моделью — разумное умолчание: соседние
		// файлы почти всегда и есть остальные модели владельца.
		*lmDir = filepath.Dir(*lmModel)
	}

	workspaceRoots := splitRoots(*roots)
	if !given["workspace-roots"] && len(settings.WorkspaceRoots) > 0 {
		workspaceRoots = settings.WorkspaceRoots
	}

	// Внешний bind — отдельное решение владельца, а не побочный эффект запуска.
	if !strings.HasPrefix(*addr, "127.0.0.1:") && !strings.HasPrefix(*addr, "localhost:") {
		logger.Warn("сервер слушает не только петлевой интерфейс; "+
			"удалённый доступ должен идти через защищённый канал", "addr", *addr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	policy, err := worker.ParseModelPolicy(*costs)
	if err != nil {
		return err
	}

	memPolicy, err := memory.ParsePolicy(*memoryMode)
	if err != nil {
		return err
	}

	initPolicy, err := initiative.ParsePolicy(*initMode)
	if err != nil {
		return err
	}

	a, err := app.New(ctx, app.Config{
		ModelPolicy:      policy,
		MemoryPolicy:     memPolicy,
		InitiativePolicy: initPolicy,
		ProviderEndpoint: *provider,
		ProviderModel:    *providerModel,
		ProviderLabel:    *providerLabel,
		// Ключ приходит только из окружения: в командной строке он был бы
		// виден в списке процессов.
		ProviderAPIKey: os.Getenv("BARRYMORE_PROVIDER_API_KEY"),
		LocalModel: localmodel.Spec{
			Binary:      *lmBinary,
			ModelPath:   *lmModel,
			Port:        *lmPort,
			ContextSize: *lmContext,
			Threads:     *lmThreads,
			GPULayers:   *lmGPU,
			CPUMoE:      *lmCPUMoE,
			// Шаблон чата модели нужен всегда: без него принуждение к схеме и
			// отключение размышлений работают не так, как проверялось спайком.
			Jinja:       true,
			LoadTimeout: *lmTimeout,
		},
		ModelsDir:      *lmDir,
		Settings:       settings,
		DataRoot:       *dataRoot,
		Addr:           *addr,
		WorkspaceRoots: workspaceRoots,
		TickInterval:   *tick,
		Logger:         logger,
	})
	if err != nil {
		return err
	}
	defer a.Close()

	if *rebuild {
		logger.Info("пересборка проекций из журнала")
		if err := a.Rebuild(ctx); err != nil {
			return err
		}
		logger.Info("проекции пересобраны")
		return nil
	}

	if err := a.Start(ctx); err != nil {
		return err
	}
	for _, note := range a.StartupNotes {
		logger.Warn("ограничение запуска: " + note)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.NewServer(a).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Таймаут записи не задаётся: SSE держит соединение открытым долго.
		IdleTimeout: 120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("Бэрримор слушает", "addr", "http://"+*addr, "data_root", *dataRoot)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("получен сигнал остановки; процессы исполнителей продолжают работу")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// chooseFirstRunModel превращает найденные GGUF в явное решение владельца.
// Никакой модели при первом обычном запуске нельзя назначить молча: даже две
// одинаковые по размеру модели отличаются поведением, а единственная на диске
// всё равно могла оказаться там для теста.
func chooseFirstRunModel(models []app.AvailableModel, proposed string, interactive bool, in io.Reader, out io.Writer) (string, bool, string) {
	if len(models) == 0 {
		return "", false, "GGUF-модели пока не найдены; положите файл в data/local_models и запустите снова"
	}
	if !interactive {
		return "", false, "модель найдена, но выбор требует владельца; запустите Barrymore интерактивно или выберите модель в настройках"
	}

	reader := bufio.NewReader(in)
	if len(models) == 1 {
		fmt.Fprintf(out, "\nНайдена локальная модель:\n  %s (%s)\n", models[0].Name, humanBytes(models[0].SizeBytes))
		fmt.Fprint(out, "Использовать её для Бэрримора? [Y/n]: ")
		answer, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", false, "выбор модели не прочитан: " + err.Error()
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		switch answer {
		case "", "y", "yes", "д", "да":
			return models[0].Path, true, "владелец подтвердил модель " + models[0].Name
		case "n", "no", "н", "нет", "0":
			return "", true, "владелец решил пока работать без локальной разговорной модели"
		default:
			return "", false, "ответ не распознан; выбор модели не сохранён"
		}
	}

	defaultChoice := 1
	for i, m := range models {
		if sameModelPath(m.Path, proposed) {
			defaultChoice = i + 1
			break
		}
	}
	fmt.Fprintln(out, "\nНайдены локальные модели:")
	for i, m := range models {
		fmt.Fprintf(out, "  %d. %s (%s)\n", i+1, m.Name, humanBytes(m.SizeBytes))
	}
	fmt.Fprintln(out, "  0. Пока без локальной разговорной модели")
	fmt.Fprintf(out, "Выберите модель [%d]: ", defaultChoice)
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, "выбор модели не прочитан: " + err.Error()
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		answer = strconv.Itoa(defaultChoice)
	}
	choice, err := strconv.Atoi(answer)
	if err != nil || choice < 0 || choice > len(models) {
		return "", false, "номер модели не распознан; выбор не сохранён"
	}
	if choice == 0 {
		return "", true, "владелец решил пока работать без локальной разговорной модели"
	}
	selected := models[choice-1]
	return selected.Path, true, "владелец выбрал модель " + selected.Name
}

func modelChoiceMarker(dataRoot string) string {
	return filepath.Join(dataRoot, "local-model-choice.done")
}

func modelChoiceDone(dataRoot string) bool {
	_, err := os.Stat(modelChoiceMarker(dataRoot))
	return err == nil
}

func markModelChoiceDone(dataRoot string) error {
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return err
	}
	return os.WriteFile(modelChoiceMarker(dataRoot), []byte("confirmed\n"), 0o600)
}

func stdinInteractive() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func sameModelPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}

func humanBytes(n int64) string {
	const unit = int64(1024)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, suffix := range units {
		value /= 1024
		if value < 1024 || suffix == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%d B", n)
}

func splitRoots(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseLevel(v string) slog.Level {
	switch strings.ToLower(v) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
