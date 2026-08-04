# Команды разработки Бэрримора.
#
# Каждая цель воспроизводима и не требует внешних сервисов.

GO ?= go
BIN := bin/barrymored
DATA_ROOT ?= $(CURDIR)/data/runtime
WORKSPACE_ROOTS ?= $(HOME)/git
ADDR ?= 127.0.0.1:7717

# Локальная модель. Параметры подтверждены спайком S1 на этом хосте:
# эксперты MoE на CPU, остальные слои на видеокарту — 94/18 токенов в секунду.
LOCAL_MODEL ?= $(CURDIR)/data/local_models/Qwen3.6-35B-A3B-UD-Q4_K_M.gguf
MODEL_FLAGS ?= -local-model-threads 14 -local-model-gpu-layers 99 -local-model-cpu-moe 40

.PHONY: help build test test-race vet fmt lint run run-quiet dev clean host-audit rebuild ci

help: ## Показать список целей
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

build: ## Собрать основной бинарник
	$(GO) build -o $(BIN) ./cmd/barrymored

test: ## Прогнать тесты
	$(GO) test ./...

test-race: ## Прогнать тесты с детектором гонок
	$(GO) test -race ./...

vet: ## Статические проверки
	$(GO) vet ./...

fmt: ## Форматирование
	gofmt -l -w ./cmd ./internal

lint: fmt vet ## Форматирование и проверки

ci: lint test-race build ## Локальный CI

run: build ## Запустить Бэрримора вместе с локальной моделью
	$(BIN) -addr $(ADDR) -data-root $(DATA_ROOT) -workspace-roots $(WORKSPACE_ROOTS) \
		-local-model $(LOCAL_MODEL) $(MODEL_FLAGS)

run-quiet: build ## Запустить без локальной модели: только нити, штат и поручения
	$(BIN) -addr $(ADDR) -data-root $(DATA_ROOT) -workspace-roots $(WORKSPACE_ROOTS)

dev: run ## Псевдоним run

rebuild: build ## Пересобрать проекции из журнала
	$(BIN) -data-root $(DATA_ROOT) -rebuild-projections

host-audit: ## Пересобрать сведения о хосте
	@echo "== платформа =="; uname -sr; . /etc/os-release 2>/dev/null && echo "$$PRETTY_NAME"
	@echo "== toolchain =="; $(GO) version; node --version 2>/dev/null; git --version
	@echo "== изоляция =="; command -v bwrap || echo "bwrap отсутствует"; \
		command -v systemd-run || echo "systemd-run отсутствует"; \
		stat -fc %T /sys/fs/cgroup
	@echo "== исполнители =="; for t in codex opencode pi qwen hermes claude; do \
		printf "%-10s " $$t; command -v $$t || echo "-"; done
	@echo "== диск =="; df -h $(CURDIR) | tail -1

clean: ## Убрать артефакты сборки
	rm -rf bin
