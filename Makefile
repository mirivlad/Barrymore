# Команды разработки Бэрримора.
#
# Каждая цель воспроизводима и не требует внешних сервисов.

GO ?= go
BIN := bin/barrymored
DATA_ROOT ?= $(CURDIR)/data/runtime
WORKSPACE_ROOTS ?= $(HOME)/git
ADDR ?= 127.0.0.1:7717
DIST_DIR ?= $(CURDIR)/dist/barrymore

# Локальная модель для живого dev-прогона. Это не продуктовая зависимость:
# модель остаётся сменяемой через настройки. Сейчас пробуем компактную Ornith
# вместо прежней 35B MoE-модели, чтобы постоянно живущий дворецкий не требовал
# тяжёлого resident inference для обычного разговора и диспетчеризации.
LOCAL_MODEL ?= $(CURDIR)/data/local_models/Ornith-1.5-9B-AD-Q5_K-Q4_K.gguf

# Для воспроизводимого live-run путь выбирает Makefile и передаёт его явно.
# Standalone bundle кладёт свой wrapper в libexec/llama-server; dev-путь
# дополнительно принимает обычную локальную сборку llama.cpp.
LLAMA_SERVER ?= $(shell \
	for p in \
		"$(CURDIR)/libexec/llama-server" \
		"$(CURDIR)/third_party/llama.cpp/build/bin/llama-server" \
		"$(HOME)/.local/bin/llama-server" \
		"$(HOME)/llama.cpp/build/bin/llama-server"; do \
		if test -x "$$p"; then printf '%s\n' "$$p"; exit 0; fi; \
	done; \
	command -v llama-server 2>/dev/null || true)

# Первый bring-up на 8 ГБ VRAM намеренно начинается с 8K контекста: сначала
# проверяем поведение и скорость, затем поднимаем окно по фактической памяти.
# История Barrymore хранится runtime'ом, а не обязана целиком жить в KV-cache.
# Значения заданы явно, включая нули: старый settings.json от 35B MoE-модели
# не должен незаметно вернуть cpu_moe или внешний provider в тест Ornith.
MODEL_FLAGS ?= -local-model-context 8192 -local-model-threads 14 \
	-local-model-gpu-layers 99 -local-model-cpu-moe 0 -local-model-port 18080 \
	-llama-server="$(LLAMA_SERVER)" -provider= -provider-model=local -provider-label=Ornith

.PHONY: help build test test-race vet fmt lint run run-quiet dev install uninstall \
        clean host-audit rebuild ci e2e ornith-ready bundle

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

ornith-ready: ## Проверить окружение первого живого запуска Ornith
	@test -f "$(LOCAL_MODEL)" || { \
		echo "Не найден файл модели:"; \
		echo "  $(LOCAL_MODEL)"; \
		echo "Ожидается Ornith-1.5-9B-AD-Q5_K-Q4_K.gguf в data/local_models/"; \
		echo "Если установлен hf: hf download AtomicChat/Ornith-1.5-9B-GGUF Ornith-1.5-9B-AD-Q5_K-Q4_K.gguf --local-dir data/local_models"; \
		exit 1; \
	}
	@printf "модель: "; ls -lh "$(LOCAL_MODEL)" | awk '{print $$5, $$9}'
	@test -n "$(LLAMA_SERVER)" || { \
		echo "llama-server не найден ни в libexec, ни в third_party, ни в ~/.local/bin, ни в ~/llama.cpp/build/bin, ни в PATH"; \
		exit 1; \
	}
	@test -x "$(LLAMA_SERVER)" || { echo "llama-server не исполняемый: $(LLAMA_SERVER)"; exit 1; }
	@echo "llama-server: $(LLAMA_SERVER)"
	@printf "bubblewrap: "; command -v bwrap 2>/dev/null || echo "не найден — сам Barrymore работает, proxy-only персонал запускаться не будет"

run: build ornith-ready ## Запустить Бэрримора вместе с локальной Ornith
	$(BIN) -addr $(ADDR) -data-root $(DATA_ROOT) -workspace-roots $(WORKSPACE_ROOTS) \
		-local-model $(LOCAL_MODEL) $(MODEL_FLAGS)

run-quiet: build ## Запустить без локальной модели: runtime и интерфейс без разговора
	$(BIN) -addr $(ADDR) -data-root $(DATA_ROOT) -workspace-roots $(WORKSPACE_ROOTS)

dev: run ## Псевдоним run

# Standalone bundle содержит Barrymore и ровно тот llama-server, который был
# проверен разработчиком. Веса модели намеренно не копируются: пользователь
# кладёт выбранный GGUF в data/local_models. Shared libraries из каталога
# llama-server едут рядом, а wrapper добавляет их в LD_LIBRARY_PATH.
bundle: build ## Собрать переносимый каталог с Barrymore и llama-server
	@test -n "$(LLAMA_SERVER)" || { echo "llama-server не найден; сначала соберите llama.cpp"; exit 1; }
	@test -x "$(LLAMA_SERVER)" || { echo "llama-server не исполняемый: $(LLAMA_SERVER)"; exit 1; }
	rm -rf "$(DIST_DIR)"
	install -Dm755 "$(BIN)" "$(DIST_DIR)/barrymore"
	install -Dm755 "$(LLAMA_SERVER)" "$(DIST_DIR)/libexec/llama-server.bin"
	@srcdir="$$(dirname "$(LLAMA_SERVER)")"; \
	for lib in "$$srcdir"/*.so "$$srcdir"/*.so.*; do \
		test -e "$$lib" || continue; cp -a "$$lib" "$(DIST_DIR)/libexec/"; \
	done
	@printf '%s\n' \
		'#!/bin/sh' \
		'HERE=$$(CDPATH= cd -- "$$(dirname -- "$$0")" && pwd)' \
		'export LD_LIBRARY_PATH="$$HERE$${LD_LIBRARY_PATH:+:$$LD_LIBRARY_PATH}"' \
		'exec "$$HERE/llama-server.bin" "$$@"' \
		> "$(DIST_DIR)/libexec/llama-server"
	@chmod 755 "$(DIST_DIR)/libexec/llama-server"
	@mkdir -p "$(DIST_DIR)/data/local_models"
	@printf '%s\n' \
		'Положите сюда одну или несколько моделей *.gguf.' \
		'Затем из корня bundle запустите: ./barrymore' \
		'При первом интерактивном запуске Бэрримор попросит подтвердить или выбрать модель.' \
		> "$(DIST_DIR)/data/local_models/README.txt"
	@"$(DIST_DIR)/libexec/llama-server" --version >/dev/null
	@echo "Standalone bundle готов: $(DIST_DIR)"
	@echo "Дальше: положите GGUF в $(DIST_DIR)/data/local_models/ и запустите $(DIST_DIR)/barrymore"

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
