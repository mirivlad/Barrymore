# Команды разработки Бэрримора.
#
# Каждая цель воспроизводима и не требует внешних сервисов.

GO ?= go
BIN := bin/barrymored
DATA_ROOT ?= $(CURDIR)/data/runtime
WORKSPACE_ROOTS ?= $(HOME)/git
ADDR ?= 127.0.0.1:7717

# Локальная модель для живого dev-прогона. Это не продуктовая зависимость:
# модель остаётся сменяемой через настройки. Сейчас пробуем компактную Ornith
# вместо прежней 35B MoE-модели, чтобы постоянно живущий дворецкий не требовал
# тяжёлого resident inference для обычного разговора и диспетчеризации.
LOCAL_MODEL ?= $(CURDIR)/data/local_models/Ornith-1.5-9B-AD-Q5_K-Q4_K.gguf
# Первый bring-up на 8 ГБ VRAM намеренно начинается с 8K контекста: сначала
# проверяем поведение и скорость, затем поднимаем окно по фактической памяти.
# История Barrymore хранится runtime'ом, а не обязана целиком жить в KV-cache.
MODEL_FLAGS ?= -local-model-context 8192 -local-model-threads 14 -local-model-gpu-layers 99

.PHONY: help build test test-race vet fmt lint run run-quiet dev install uninstall \
        clean host-audit rebuild ci e2e

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

e2e: build ## Проверить интерфейс настоящим браузером
	@command -v node >/dev/null || { echo "нужен node"; exit 1; }
	@test -d node_modules/playwright || npm install --no-save --no-audit --no-fund playwright
	node e2e/reception.mjs

ci: lint test-race build ## Локальный CI

run: build ## Запустить Бэрримора вместе с локальной моделью
	$(BIN) -addr $(ADDR) -data-root $(DATA_ROOT) -workspace-roots $(WORKSPACE_ROOTS) \
		-local-model $(LOCAL_MODEL) $(MODEL_FLAGS)

run-quiet: build ## Запустить без локальной модели: только нити, штат и поручения
	$(BIN) -addr $(ADDR) -data-root $(DATA_ROOT) -workspace-roots $(WORKSPACE_ROOTS)

dev: run ## Псевдоним run

install: build ## Поставить бинарник и пользовательскую службу systemd
	@install -Dm755 $(BIN) $(HOME)/.local/bin/barrymored
	@install -Dm644 packaging/barrymore.service \
		$(HOME)/.config/systemd/user/barrymore.service
	@systemctl --user daemon-reload
	@echo "Поставлено: ~/.local/bin/barrymored"
	@echo
	@echo "Дальше:"
	@echo "  systemctl --user enable --now barrymore"
	@echo "  xdg-open http://127.0.0.1:7717"
	@echo
	@echo "При первом запуске Бэрримор сам найдёт llama-server и модели."
	@echo "Разрешённые рабочие каталоги он себе не выдаёт: задайте их в"
	@echo "$(HOME)/.local/share/barrymore/settings.json полем workspace_roots."

uninstall: ## Убрать службу и бинарник; данные остаются на месте
	-@systemctl --user disable --now barrymore 2>/dev/null
	-@rm -f $(HOME)/.config/systemd/user/barrymore.service
	-@rm -f $(HOME)/.local/bin/barrymored
	-@systemctl --user daemon-reload
	@echo "Убрано. Данные и настройки не тронуты."

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
