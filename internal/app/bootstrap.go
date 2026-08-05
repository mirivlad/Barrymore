package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Bootstrap заполняет то, что можно выяснить самому, при первом запуске.
//
// Смысл — простой запуск: `barrymored` без единого флага должен находить
// llama-server и модели там, где они обычно лежат. Найденное записывается
// в настройки и перечисляется владельцу: догадка, о которой не сказали,
// ничем не лучше скрытого решения.
//
// Разрешённые рабочие каталоги сюда не входят намеренно. Доступ к чужим
// каталогам — не та вещь, которую можно выдать себе по догадке
// (01_PRODUCT_BOUNDARY §2.6).
func Bootstrap(dataRoot string, s Settings) (Settings, []string) {
	notes := []string{}

	if s.LocalModel.Binary == "" {
		if p := findLlamaServer(); p != "" {
			s.LocalModel.Binary = p
			notes = append(notes, "llama-server найден: "+p)
		}
	}

	if s.LocalModel.ModelsDir == "" {
		if dir := findModelsDir(dataRoot); dir != "" {
			s.LocalModel.ModelsDir = dir
			notes = append(notes, "каталог моделей найден: "+dir)
		}
	}

	if s.LocalModel.Path == "" && s.LocalModel.ModelsDir != "" {
		found, err := FindModels(s.LocalModel.ModelsDir, "")
		switch {
		case err != nil:
			notes = append(notes, "каталог моделей прочитать не удалось: "+err.Error())
		case len(found) == 1:
			s.LocalModel.Path = found[0].Path
			notes = append(notes, "выбрана единственная найденная модель: "+found[0].Name)
		case len(found) > 1:
			// Выбирать за владельца из нескольких — самоуправство: модели
			// различаются размером, скоростью и характером ответов.
			notes = append(notes, fmt.Sprintf(
				"моделей найдено %d; какую поднимать — выберите в разделе «Настройки»", len(found)))
		}
	}

	if s.LocalModel.Threads == 0 {
		// Два ядра остаются системе: забрать все — верный способ сделать
		// машину неотзывчивой ровно тогда, когда владелец её ждёт.
		if n := runtime.NumCPU(); n > 2 {
			s.LocalModel.Threads = n - 2
			notes = append(notes, fmt.Sprintf(
				"потоков CPU для модели: %d из %d, два оставлены системе", n-2, n))
		}
	}

	if s.LocalModel.GPULayers == 0 && hasGPU() {
		// Видеокарта найдена. Если сборка llama.cpp собрана без её поддержки,
		// параметр просто игнорируется — навредить он не может.
		s.LocalModel.GPULayers = 99
		notes = append(notes, "найдена видеокарта: слои модели пойдут на неё")
	}

	// Распределение экспертов MoE (-ncmoe) намеренно не угадывается: оно
	// зависит от устройства конкретной модели, и число «на глаз» здесь
	// сделает хуже, а не лучше. Подобрать его можно в разделе «Настройки».

	return s, notes
}

// hasGPU проверяет наличие устройства видеокарты.
//
// Проверка нарочно грубая: точное определение возможностей требует запуска
// самой llama.cpp, а обещать ускорение до первого запуска всё равно нельзя.
func hasGPU() bool {
	entries, err := os.ReadDir("/dev/dri")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "render") || strings.HasPrefix(e.Name(), "card") {
			return true
		}
	}
	return false
}

// findLlamaServer ищет сервер там, где он обычно оказывается.
func findLlamaServer() string {
	candidates := []string{
		"third_party/llama.cpp/build/bin/llama-server",
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "llama-server"),
			filepath.Join(home, "llama.cpp", "build", "bin", "llama-server"),
		)
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if st, err := os.Stat(abs); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return abs
		}
	}
	if p, err := exec.LookPath("llama-server"); err == nil {
		return p
	}
	return ""
}

// findModelsDir ищет каталог с весами.
func findModelsDir(dataRoot string) string {
	candidates := []string{
		filepath.Join(dataRoot, "models"),
		"data/local_models",
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".local", "share", "barrymore", "models"),
			filepath.Join(home, "models"),
		)
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".gguf") {
				return abs
			}
		}
	}
	return ""
}
