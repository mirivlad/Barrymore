package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/app"
)

func TestChooseFirstRunModelConfirmsSingleByDefault(t *testing.T) {
	m := app.AvailableModel{Path: "/models/ornith.gguf", Name: "ornith.gguf", SizeBytes: 5 << 30}
	var out bytes.Buffer
	path, completed, note := chooseFirstRunModel([]app.AvailableModel{m}, m.Path, true,
		strings.NewReader("\n"), &out)
	if path != m.Path || !completed {
		t.Fatalf("единственная модель не подтверждена: path=%q completed=%v note=%q", path, completed, note)
	}
	if !strings.Contains(out.String(), "ornith.gguf") {
		t.Fatalf("владелец не увидел имя модели: %q", out.String())
	}
}

func TestChooseFirstRunModelLetsOwnerPickAmongSeveral(t *testing.T) {
	models := []app.AvailableModel{
		{Path: "/models/a.gguf", Name: "a.gguf"},
		{Path: "/models/b.gguf", Name: "b.gguf"},
	}
	path, completed, _ := chooseFirstRunModel(models, "", true,
		strings.NewReader("2\n"), &bytes.Buffer{})
	if path != models[1].Path || !completed {
		t.Fatalf("выбран не второй вариант: path=%q completed=%v", path, completed)
	}
}

func TestChooseFirstRunModelCanRunWithoutConversation(t *testing.T) {
	models := []app.AvailableModel{{Path: "/models/a.gguf", Name: "a.gguf"}}
	path, completed, _ := chooseFirstRunModel(models, "", true,
		strings.NewReader("n\n"), &bytes.Buffer{})
	if path != "" || !completed {
		t.Fatalf("отказ от локальной модели не сохранён как решение: path=%q completed=%v", path, completed)
	}
}

func TestChooseFirstRunModelDoesNotGuessWithoutTTY(t *testing.T) {
	models := []app.AvailableModel{{Path: "/models/a.gguf", Name: "a.gguf"}}
	path, completed, _ := chooseFirstRunModel(models, "", false,
		strings.NewReader("\n"), &bytes.Buffer{})
	if path != "" || completed {
		t.Fatalf("неинтерактивный запуск выбрал модель за владельца: path=%q completed=%v", path, completed)
	}
}

func TestModelChoiceMarker(t *testing.T) {
	dir := t.TempDir()
	if modelChoiceDone(dir) {
		t.Fatal("новый каталог не должен считаться настроенным")
	}
	if err := markModelChoiceDone(dir); err != nil {
		t.Fatal(err)
	}
	if !modelChoiceDone(dir) {
		t.Fatal("решение владельца не пережило запуск")
	}
	info, err := os.Stat(filepath.Join(dir, "local-model-choice.done"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("маркер first-run слишком открыт: %o", info.Mode().Perm())
	}
}
