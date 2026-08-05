package delegation_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/delegation"
)

// repo создаёт настоящий маленький репозиторий: контролируемая запись держится
// на git, и проверять её на подделке бессмысленно.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Тест", "GIT_AUTHOR_EMAIL=t@localhost",
			"GIT_COMMITTER_NAME=Тест", "GIT_COMMITTER_EMAIL=t@localhost")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	write(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	write(t, dir, "README.md", "# проект\n")
	run("add", "-A")
	run("commit", "-q", "-m", "первый коммит")
	return dir
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func prepare(t *testing.T, src string) delegation.WorkCopy {
	t.Helper()
	wc, err := delegation.PrepareWorkCopy(context.Background(), src,
		filepath.Join(t.TempDir(), "copy"), "barrymore/test")
	if err != nil {
		t.Fatalf("копия не подготовлена: %v", err)
	}
	return wc
}

// Копия — именно копия: оригинал не делит с ней ни файлов, ни объектов git.
func TestWorkCopyIsIndependentOfTheOriginal(t *testing.T) {
	src := repo(t)
	wc := prepare(t, src)

	if wc.Path == src {
		t.Fatal("копия совпала с оригиналом")
	}
	if read(t, wc.Path, "main.go") != read(t, src, "main.go") {
		t.Fatal("содержимое не скопировано")
	}

	// Пишем в копию — оригинал не должен измениться.
	write(t, wc.Path, "main.go", "испорчено исполнителем\n")
	if strings.Contains(read(t, src, "main.go"), "испорчено") {
		t.Fatal("запись в копию достала до каталога владельца")
	}

	// И объекты git не общие: удаление копии не должно трогать оригинал.
	if err := delegation.RemoveWorkCopy(wc); err != nil {
		t.Fatal(err)
	}
	if read(t, src, "main.go") == "" {
		t.Fatal("удаление копии повредило оригинал")
	}
}

// Незакоммиченная работа владельца попадает в снимок «до» и не выдаётся
// потом за изменения исполнителя (сценарий G).
func TestOwnersUncommittedWorkIsNotAttributedToTheWorker(t *testing.T) {
	src := repo(t)
	write(t, src, "черновик.txt", "мои заметки\n")
	write(t, src, "main.go", "package main\n\n// моя правка\nfunc main() {}\n")

	wc := prepare(t, src)
	if read(t, wc.Path, "черновик.txt") != "мои заметки\n" {
		t.Fatal("незакоммиченная работа владельца не попала в копию: " +
			"исполнитель работал бы не с тем, что видит владелец")
	}

	ch, err := delegation.CollectChange(context.Background(), wc)
	if err != nil {
		t.Fatal(err)
	}
	if !ch.Empty() {
		t.Fatalf("работа владельца записана как изменения исполнителя: %+v", ch.Files)
	}
}

func TestCollectChangeSeesEveryKindOfEdit(t *testing.T) {
	src := repo(t)
	wc := prepare(t, src)

	write(t, wc.Path, "main.go", "package main\n\nfunc main() { println(\"привет\") }\n")
	write(t, wc.Path, "новый.go", "package main\n")
	if err := os.Remove(filepath.Join(wc.Path, "README.md")); err != nil {
		t.Fatal(err)
	}

	ch, err := delegation.CollectChange(context.Background(), wc)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range ch.Files {
		got[f.Path] = f.Status
	}
	for path, want := range map[string]string{
		"main.go": "изменён", "новый.go": "добавлен", "README.md": "удалён",
	} {
		if got[path] != want {
			t.Errorf("%s: %q, ожидалось %q", path, got[path], want)
		}
	}
	if ch.Patch == "" {
		t.Fatal("дифф пуст: владельцу нечего смотреть")
	}
	if ch.Insertions == 0 && ch.Deletions == 0 {
		t.Fatal("размер изменений не посчитан")
	}
}

// Ничего не сделал — так и сказано, а не выдумано.
func TestCollectChangeOnUntouchedCopyIsEmpty(t *testing.T) {
	wc := prepare(t, repo(t))
	ch, err := delegation.CollectChange(context.Background(), wc)
	if err != nil {
		t.Fatal(err)
	}
	if !ch.Empty() || ch.Patch != "" {
		t.Fatalf("на нетронутой копии найдены изменения: %+v", ch)
	}
}

// Применение доходит до каталога владельца — но только по отдельному решению.
func TestApplyChangeReachesTheOwnersDirectory(t *testing.T) {
	src := repo(t)
	wc := prepare(t, src)

	write(t, wc.Path, "main.go", "package main\n\nfunc main() { println(\"готово\") }\n")
	write(t, wc.Path, "новый.go", "package main\n")
	ch, err := delegation.CollectChange(context.Background(), wc)
	if err != nil {
		t.Fatal(err)
	}

	// До применения каталог владельца нетронут.
	if strings.Contains(read(t, src, "main.go"), "готово") {
		t.Fatal("изменения дошли до владельца без его решения")
	}

	res, err := delegation.ApplyChange(context.Background(), src, ch.Patch)
	if err != nil {
		t.Fatalf("применение: %v", err)
	}
	if !res.Applied {
		t.Fatal("применение не состоялось")
	}
	if !strings.Contains(read(t, src, "main.go"), "готово") {
		t.Fatal("правка не дошла до файла")
	}
	if read(t, src, "новый.go") != "package main\n" {
		t.Fatal("новый файл не создан")
	}
	// Ничего не закоммичено: решать, что делать дальше, владельцу.
	if !strings.Contains(res.Detail, "незакоммиченными") {
		t.Fatalf("владельцу не сказано, в каком виде остались изменения: %q", res.Detail)
	}
}

// Применить наполовину хуже, чем не применить: проверка идёт до записи.
func TestApplyChangeRefusesWhenDirectoryMovedOn(t *testing.T) {
	src := repo(t)
	wc := prepare(t, src)
	write(t, wc.Path, "main.go", "package main\n\nfunc main() { println(\"из копии\") }\n")
	ch, err := delegation.CollectChange(context.Background(), wc)
	if err != nil {
		t.Fatal(err)
	}

	// Владелец тем временем переписал тот же файл целиком.
	write(t, src, "main.go", "совсем другое содержимое, ни строчки прежнего\n")
	before := read(t, src, "main.go")

	_, err = delegation.ApplyChange(context.Background(), src, ch.Patch)
	if err == nil {
		t.Fatal("изменения наложены поверх чужой правки без предупреждения")
	}
	if !strings.Contains(err.Error(), "изменился") {
		t.Fatalf("причина отказа неясна: %v", err)
	}
	if read(t, src, "main.go") != before {
		t.Fatal("отказавшись применять, Бэрримор всё же тронул файл")
	}
}

func TestApplyEmptyPatchIsRefused(t *testing.T) {
	if _, err := delegation.ApplyChange(context.Background(), repo(t), "   "); err == nil {
		t.Fatal("применение пустого набора изменений должно быть отказом")
	}
}

// Без git контролируемой записи не бывает: нечем показать изменения и нечем
// их откатить. Это сказано прямо, а не превращается в молчаливый провал.
func TestWorkCopyRequiresGit(t *testing.T) {
	plain := t.TempDir()
	write(t, plain, "файл.txt", "содержимое\n")

	_, err := delegation.PrepareWorkCopy(context.Background(), plain,
		filepath.Join(t.TempDir(), "copy"), "barrymore/test")
	if !errors.Is(err, delegation.ErrNotGit) {
		t.Fatalf("ошибка %v, ожидалась ErrNotGit", err)
	}
	if !strings.Contains(err.Error(), "откатить") {
		t.Fatalf("причина не объясняет, почему без git нельзя: %v", err)
	}
}

// Символьные ссылки копируются ссылками: разыменование вытащило бы в копию
// то, что лежит вне рабочего каталога.
func TestWorkCopyDoesNotFollowSymlinksOutside(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "секрет.txt")
	if err := os.WriteFile(secret, []byte("не для исполнителя\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	src := repo(t)
	if err := os.Symlink(secret, filepath.Join(src, "ссылка.txt")); err != nil {
		t.Fatal(err)
	}

	wc := prepare(t, src)
	info, err := os.Lstat(filepath.Join(wc.Path, "ссылка.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("ссылка разыменована: содержимое извне каталога попало в копию")
	}
}
