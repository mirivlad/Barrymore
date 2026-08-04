// Command barrymored — основной процесс Бэрримора.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mirivlad/barrymore/internal/api"
	"github.com/mirivlad/barrymore/internal/app"
	"github.com/mirivlad/barrymore/internal/localmodel"
	"github.com/mirivlad/barrymore/internal/memory"
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

		lmModel = flag.String("local-model", "",
			"файл .gguf локальной модели; задан — Бэрримор сам поднимает и стережёт llama-server")
		lmBinary = flag.String("llama-server", "",
			"путь к llama-server; пусто — third_party/llama.cpp/build/bin, затем PATH")
		lmPort    = flag.Int("local-model-port", 18080, "порт локального сервера модели")
		lmContext = flag.Int("local-model-context", 32768, "размер контекста локальной модели")
		lmThreads = flag.Int("local-model-threads", 0, "потоков CPU; 0 — умолчание llama-server")
		lmGPU     = flag.Int("local-model-gpu-layers", 0, "слоёв на видеокарту (-ngl)")
		lmCPUMoE  = flag.Int("local-model-cpu-moe", 0, "экспертов MoE оставить на CPU (-ncmoe)")
		lmTimeout = flag.Duration("local-model-load-timeout", 10*time.Minute,
			"сколько ждать готовности модели после запуска")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(*logLevel)}))
	slog.SetDefault(logger)

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

	a, err := app.New(ctx, app.Config{
		ModelPolicy:      policy,
		MemoryPolicy:     memPolicy,
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
		DataRoot:       *dataRoot,
		Addr:           *addr,
		WorkspaceRoots: splitRoots(*roots),
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
		logger.Info("Бэрримор слушает", "addr", *addr, "data_root", *dataRoot)
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
