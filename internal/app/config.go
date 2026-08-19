package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Settings — то, что владелец настраивает и что переживает перезапуск.
//
// Файл настроек существует ради простого запуска: `barrymored` без единого
// флага должен работать так же, как вчера. Флаг, заданный явно, всё равно
// сильнее файла — иначе разовый запуск с другими параметрами был бы невозможен.
type Settings struct {
	// Addr, WorkspaceRoots и прочее, что нельзя поменять на ходу.
	Addr           string   `json:"addr,omitempty"`
	WorkspaceRoots []string `json:"workspace_roots,omitempty"`
	ModelPolicy    string   `json:"model_policy,omitempty"`
	MemoryPolicy   string   `json:"memory_policy,omitempty"`
	// Initiative — когда Бэрримор обращается первым: on, urgent-only, off.
	Initiative string `json:"initiative,omitempty"`
	// WorkerProxy — сетевой маршрут только для внешнего персонала. Он не
	// превращается в HTTP_PROXY самого Бэрримора или локальной модели.
	WorkerProxy string `json:"worker_proxy,omitempty"`

	// LocalModel — какую локальную модель поднимать.
	LocalModel LocalModelSettings `json:"local_model"`

	// Provider задаётся, только если владелец хочет внешнего провайдера
	// вместо локальной модели.
	ProviderEndpoint string `json:"provider_endpoint,omitempty"`
	ProviderModel    string `json:"provider_model,omitempty"`
	ProviderLabel    string `json:"provider_label,omitempty"`
}

// Участие исполнителя здесь намеренно не хранится. Это доменное решение с
// событием в журнале (`worker.enabled.changed`), у него есть время, автор и
// причина. Дубликат в файле настроек завёл бы второй источник правды, и рано
// или поздно они разошлись бы.

// LocalModelSettings — выбор локальной модели.
type LocalModelSettings struct {
	// Path — файл .gguf. Пусто означает, что модель Бэрримор не поднимает.
	Path   string `json:"path,omitempty"`
	Binary string `json:"binary,omitempty"`
	Port   int    `json:"port,omitempty"`
	// ModelsDir — где искать модели для выбора в интерфейсе.
	ModelsDir   string `json:"models_dir,omitempty"`
	ContextSize int    `json:"context_size,omitempty"`
	Threads     int    `json:"threads,omitempty"`
	GPULayers   int    `json:"gpu_layers,omitempty"`
	CPUMoE      int    `json:"cpu_moe,omitempty"`
}

// SettingsPath возвращает путь к файлу настроек.
func SettingsPath(dataRoot string) string {
	return filepath.Join(dataRoot, "settings.json")
}

// LoadSettings читает настройки. Отсутствие файла — не ошибка: это первый запуск.
func LoadSettings(dataRoot string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(SettingsPath(dataRoot))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("чтение настроек: %w", err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		// Испорченный файл настроек не должен молча превращаться в умолчания:
		// владелец что-то там настроил, и потеря этого должна быть заметна.
		return s, fmt.Errorf("файл настроек %s испорчен: %w", SettingsPath(dataRoot), err)
	}
	return s, nil
}

// SaveSettings записывает настройки атомарно.
//
// Через временный файл: обрыв записи не должен оставить владельца с
// полупустым файлом настроек, из-за которого Бэрримор перестанет запускаться.
func SaveSettings(dataRoot string, s Settings) error {
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return fmt.Errorf("каталог данных: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := SettingsPath(dataRoot)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("запись настроек: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("замена настроек: %w", err)
	}
	return nil
}

// SettingsStore хранит настройки и следит, чтобы запись была одна за раз.
type SettingsStore struct {
	dataRoot string
	mu       sync.RWMutex
	current  Settings
}

// NewSettingsStore создаёт хранилище с уже прочитанными настройками.
func NewSettingsStore(dataRoot string, s Settings) *SettingsStore {
	return &SettingsStore{dataRoot: dataRoot, current: s}
}

// Get возвращает копию текущих настроек.
func (st *SettingsStore) Get() Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.current
}

// Update применяет изменение и сохраняет его на диск.
//
// Настройки записываются до того, как о них сообщено вызывающему: обещать
// сохранённое, не сохранив, — ровно то, чего Бэрримор не делает.
func (st *SettingsStore) Update(fn func(Settings) Settings) (Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	next := fn(st.current)
	if err := SaveSettings(st.dataRoot, next); err != nil {
		return st.current, err
	}
	st.current = next
	return next, nil
}

// Path возвращает путь к файлу настроек для показа владельцу.
func (st *SettingsStore) Path() string { return SettingsPath(st.dataRoot) }

// AvailableModel — найденный файл модели.
type AvailableModel struct {
	Path string `json:"path"`
	Name string `json:"name"`
	// SizeBytes помогает понять, влезет ли модель в память.
	SizeBytes  int64     `json:"size_bytes"`
	ModifiedAt time.Time `json:"modified_at"`
	// Current отмечает выбранную сейчас модель.
	Current bool `json:"current"`
}

// FindModels перечисляет файлы .gguf в каталоге моделей.
//
// Только перечисляет: пригодность файла проверяется запуском, и обещать
// работоспособность по одному расширению было бы гаданием.
func FindModels(dir, current string) ([]AvailableModel, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("каталог моделей %s: %w", dir, err)
	}
	out := []AvailableModel{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".gguf") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		full := filepath.Join(dir, e.Name())
		out = append(out, AvailableModel{
			Path: full, Name: e.Name(), SizeBytes: info.Size(),
			ModifiedAt: info.ModTime().UTC(),
			Current:    sameFile(full, current),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// sameFile сравнивает пути после приведения к абсолютным.
func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	return absA == absB
}
