package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/app"
	"github.com/mirivlad/barrymore/internal/memory"
	"github.com/mirivlad/barrymore/internal/testsupport"
	"github.com/mirivlad/barrymore/internal/worker"
)

// --- настройки ---

func TestSettingsAbsentFileIsFirstRun(t *testing.T) {
	s, err := app.LoadSettings(t.TempDir())
	if err != nil {
		t.Fatalf("отсутствие файла настроек — это первый запуск, а не ошибка: %v", err)
	}
	if s.LocalModel.Path != "" {
		t.Fatal("пустые настройки не должны ничего утверждать")
	}
}

// Испорченный файл не должен молча превращаться в умолчания: владелец там
// что-то настроил, и потеря этого обязана быть заметной.
func TestSettingsBrokenFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(app.SettingsPath(dir), []byte("{это не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := app.LoadSettings(dir)
	if err == nil {
		t.Fatal("испорченный файл настроек должен быть ошибкой")
	}
	if !strings.Contains(err.Error(), "испорчен") {
		t.Fatalf("причина неясна владельцу: %v", err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := app.Settings{
		Addr:           "127.0.0.1:7717",
		WorkspaceRoots: []string{"/home/x/git"},
		LocalModel: app.LocalModelSettings{
			Path: "/models/a.gguf", Port: 18080, Threads: 14, GPULayers: 99,
		},
	}
	if err := app.SaveSettings(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := app.LoadSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.LocalModel.Path != want.LocalModel.Path || got.LocalModel.Threads != 14 {
		t.Fatalf("настройки не пережили запись и чтение: %+v", got)
	}
	if len(got.WorkspaceRoots) != 1 || got.WorkspaceRoots[0] != "/home/x/git" {
		t.Fatalf("разрешённые каталоги потеряны: %+v", got.WorkspaceRoots)
	}
}

// Временный файл после записи оставаться не должен: он не настройки и
// только сбивает с толку того, кто заглянет в каталог данных.
func TestSaveSettingsLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	if err := app.SaveSettings(dir, app.Settings{Addr: "127.0.0.1:7717"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(app.SettingsPath(dir) + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("после записи остался временный файл")
	}
}

func TestSettingsStoreUpdatePersists(t *testing.T) {
	dir := t.TempDir()
	st := app.NewSettingsStore(dir, app.Settings{})
	if _, err := st.Update(func(s app.Settings) app.Settings {
		s.LocalModel.Path = "/models/b.gguf"
		return s
	}); err != nil {
		t.Fatal(err)
	}
	if st.Get().LocalModel.Path != "/models/b.gguf" {
		t.Fatal("изменение не отражено в памяти")
	}
	onDisk, err := app.LoadSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.LocalModel.Path != "/models/b.gguf" {
		t.Fatal("изменение не дошло до диска: обещать сохранённое, не сохранив, нельзя")
	}
}

// --- разрешённые каталоги ---

func TestPolicyRejectsPathOutsideRoots(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "проект")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	p := app.NewPolicy([]string{root})

	if err := p.AllowWorkspace(inside); err != nil {
		t.Fatalf("каталог внутри корня отклонён: %v", err)
	}
	if err := p.AllowWorkspace(t.TempDir()); err == nil {
		t.Fatal("каталог вне корней должен быть отклонён")
	}
	// Выход вверх через «..» не должен обходить проверку.
	if err := p.AllowWorkspace(filepath.Join(inside, "..", "..")); err == nil {
		t.Fatal("выход за корень через .. должен быть отклонён")
	}
}

func TestPolicyWithoutRootsForbidsEverything(t *testing.T) {
	p := app.NewPolicy(nil)
	err := p.AllowWorkspace(t.TempDir())
	if err == nil {
		t.Fatal("пустой список корней означает «ничего», а не «всё»")
	}
	if !strings.Contains(err.Error(), "не является значением по умолчанию") {
		t.Fatalf("причина не объясняет умолчание: %v", err)
	}
}

// Отзыв разрешения действует сразу: «я передумал» не должно ждать перезапуска.
func TestPolicySetRootsTakesEffectImmediately(t *testing.T) {
	root := t.TempDir()
	p := app.NewPolicy([]string{root})
	if err := p.AllowWorkspace(root); err != nil {
		t.Fatal(err)
	}
	p.SetRoots(nil)
	if err := p.AllowWorkspace(root); err == nil {
		t.Fatal("после отзыва разрешения каталог не должен быть доступен")
	}
}

func TestPolicySetRootsRemovesDuplicates(t *testing.T) {
	root := t.TempDir()
	got := app.NewPolicy(nil).SetRoots([]string{root, root, root})
	if len(got) != 1 {
		t.Fatalf("повторы не убраны: %v", got)
	}
}

func TestCheckRootRefusesDangerousChoices(t *testing.T) {
	if _, err := app.CheckRoot("/"); err == nil {
		t.Fatal("корень файловой системы разрешать нельзя")
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if _, err := app.CheckRoot(home); err == nil {
			t.Fatal("весь домашний каталог разрешать не следует")
		}
	}
	if _, err := app.CheckRoot(filepath.Join(t.TempDir(), "нет-такого")); err == nil {
		t.Fatal("несуществующий каталог разрешать нельзя")
	}
	file := filepath.Join(t.TempDir(), "файл")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CheckRoot(file); err == nil {
		t.Fatal("файл — не рабочий каталог")
	}
	dir := t.TempDir()
	if got, err := app.CheckRoot(dir); err != nil || got == "" {
		t.Fatalf("обычный каталог должен разрешаться: %v", err)
	}
}

// --- первый запуск ---

func TestBootstrapPicksSingleModel(t *testing.T) {
	dataRoot := t.TempDir()
	models := filepath.Join(dataRoot, "models")
	if err := os.MkdirAll(models, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(models, "one.gguf"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, notes := app.Bootstrap(dataRoot, app.Settings{})
	if filepath.Base(got.LocalModel.Path) != "one.gguf" {
		t.Fatalf("единственная модель не выбрана: %+v", got.LocalModel)
	}
	if !mentions(notes, "one.gguf") {
		t.Fatalf("выбор не объяснён владельцу: %v", notes)
	}
}

// Из нескольких моделей выбирать за владельца — самоуправство: они
// различаются размером, скоростью и характером ответов.
func TestBootstrapDoesNotChooseAmongSeveralModels(t *testing.T) {
	dataRoot := t.TempDir()
	models := filepath.Join(dataRoot, "models")
	if err := os.MkdirAll(models, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.gguf", "b.gguf"} {
		if err := os.WriteFile(filepath.Join(models, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, notes := app.Bootstrap(dataRoot, app.Settings{})
	if got.LocalModel.Path != "" {
		t.Fatalf("модель выбрана за владельца: %s", got.LocalModel.Path)
	}
	if got.LocalModel.ModelsDir == "" {
		t.Fatal("каталог моделей должен быть найден даже когда выбор не сделан")
	}
	if !mentions(notes, "выберите") {
		t.Fatalf("владельцу не сказано, что выбор за ним: %v", notes)
	}
}

// Разрешённые каталоги себе не выдаются: доступ к чужим каталогам не
// выписывают по догадке.
func TestBootstrapNeverGrantsWorkspaceAccess(t *testing.T) {
	got, _ := app.Bootstrap(t.TempDir(), app.Settings{})
	if len(got.WorkspaceRoots) != 0 {
		t.Fatalf("первый запуск выдал себе доступ к каталогам: %v", got.WorkspaceRoots)
	}
}

// Уже сделанный выбор не перетирается.
func TestBootstrapKeepsExistingChoice(t *testing.T) {
	dataRoot := t.TempDir()
	models := filepath.Join(dataRoot, "models")
	if err := os.MkdirAll(models, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(models, "one.gguf"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := app.Settings{LocalModel: app.LocalModelSettings{
		Path: "/выбрано/владельцем.gguf", Threads: 4, GPULayers: 7,
	}}
	got, _ := app.Bootstrap(dataRoot, before)
	if got.LocalModel.Path != before.LocalModel.Path {
		t.Fatalf("выбор владельца перетёрт: %s", got.LocalModel.Path)
	}
	if got.LocalModel.Threads != 4 || got.LocalModel.GPULayers != 7 {
		t.Fatalf("настройки владельца перетёрты: %+v", got.LocalModel)
	}
}

func TestFindModelsMarksCurrent(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.gguf", "b.gguf", "заметка.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := app.FindModels(dir, filepath.Join(dir, "b.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("посторонние файлы попали в список моделей: %+v", got)
	}
	current := 0
	for _, m := range got {
		if m.Current {
			current++
			if m.Name != "b.gguf" {
				t.Fatalf("выбранной отмечена не та модель: %s", m.Name)
			}
		}
	}
	if current != 1 {
		t.Fatalf("выбранных моделей %d, ожидалась одна", current)
	}
}

func TestFindModelsAbsentDirectoryIsNotAnError(t *testing.T) {
	got, err := app.FindModels(filepath.Join(t.TempDir(), "нет"), "")
	if err != nil {
		t.Fatalf("отсутствующий каталог моделей — не ошибка: %v", err)
	}
	if len(got) != 0 {
		t.Fatal("из несуществующего каталога не может быть моделей")
	}
}

func mentions(notes []string, substr string) bool {
	for _, n := range notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

// --- один экземпляр на каталог данных ---

// Двое на одном журнале расходятся тихо: две реакции на одно расхождение,
// два обращения об одном факте, оба считают сервер модели своим. Владелец
// увидел бы систему, которая спорит сама с собой. Найдено на этой машине:
// второй экземпляр запускался молча.
func TestSecondInstanceOnSameDataRootIsRefused(t *testing.T) {
	dataRoot := t.TempDir()
	ctx := context.Background()
	cfg := app.Config{
		DataRoot: dataRoot, Addr: "127.0.0.1:0",
		ModelPolicy: worker.FreeOnly(), MemoryPolicy: memory.DefaultPolicy(),
		Logger: testsupport.Logger(t),
	}

	first, err := app.New(ctx, cfg)
	if err != nil {
		t.Fatalf("первый экземпляр: %v", err)
	}

	_, err = app.New(ctx, cfg)
	if err == nil {
		t.Fatal("второй Бэрримор запустился на том же каталоге данных")
	}
	for _, want := range []string{"занят", "data-root"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("владельцу непонятно, что делать: %v", err)
		}
	}

	// После остановки каталог освобождается: иначе перезапуск требовал бы
	// удалять файл замка руками.
	if err := first.Close(); err != nil {
		t.Fatalf("остановка первого: %v", err)
	}
	second, err := app.New(ctx, cfg)
	if err != nil {
		t.Fatalf("после остановки каталог обязан освободиться: %v", err)
	}
	_ = second.Close()
}
