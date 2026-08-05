package delegation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Controlled write: исполнитель пишет в копию, а не в каталог владельца.
//
// 05_STAFF_AND_DELEGATION §10 требует зафиксировать исходный HEAD, не
// уничтожать существующую работу, запретить push и сделать слияние отдельным
// действием. Здесь это доведено до простого правила: до решения владельца его
// каталог не трогают вообще.
//
// Копия, а не `git worktree`, намеренно. Worktree создаёт записи внутри `.git`
// настоящего репозитория — то есть уже меняет каталог, неизменность которого
// Бэрримор потом проверяет, и делит с исполнителем объекты. Полная копия
// стоит дороже, но говорит правду: пробой изоляции не достанет до оригинала.
//
// Копируется всё, включая незакоммиченное. Иначе исполнитель работал бы не
// с тем, что видит владелец, а расхождение вскрылось бы в момент применения.

// WorkCopy — копия рабочего каталога, отданная исполнителю на запись.
type WorkCopy struct {
	// Path — куда скопировано.
	Path string `json:"path"`
	// Source — оригинал, которого никто не касался.
	Source string `json:"source"`
	// Branch — ветка внутри копии.
	Branch string `json:"branch"`
	// Baseline — коммит с точным состоянием до работы, включая незакоммиченное.
	// Всё, что отличается от него, сделал исполнитель.
	Baseline string `json:"baseline"`
	// GitBacked отличает копию репозитория от простого каталога.
	GitBacked bool      `json:"git_backed"`
	FileCount int       `json:"file_count"`
	CreatedAt time.Time `json:"created_at"`
}

// ErrNotGit возвращается, когда каталог не является репозиторием.
var ErrNotGit = errors.New(
	"каталог не под git: контролируемая запись без него невозможна — " +
		"нечем показать изменения и нечем их откатить")

// maxCopyFiles ограничивает копирование, чтобы огромный каталог не съел диск
// незаметно для владельца.
const maxCopyFiles = 200000

// PrepareWorkCopy делает копию каталога и фиксирует в ней исходное состояние.
func PrepareWorkCopy(ctx context.Context, source, dest, branch string) (WorkCopy, error) {
	abs, err := filepath.Abs(source)
	if err != nil {
		return WorkCopy{}, fmt.Errorf("рабочий каталог %q: %w", source, err)
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
		return WorkCopy{}, fmt.Errorf("%w: %s", ErrNotGit, abs)
	}

	wc := WorkCopy{Path: dest, Source: abs, Branch: branch, GitBacked: true,
		CreatedAt: time.Now().UTC()}

	n, err := copyTree(ctx, abs, dest)
	if err != nil {
		return WorkCopy{}, err
	}
	wc.FileCount = n

	// Ветка с говорящим именем: владелец должен понимать, откуда она взялась,
	// если решит оставить копию себе.
	if out, err := git(ctx, dest, "checkout", "-b", branch); err != nil {
		return WorkCopy{}, fmt.Errorf("ветка %s в копии: %v: %s", branch, err, out)
	}

	// Снимок «до» коммитом: всё, что отличается от него, сделал исполнитель.
	// Незакоммиченная работа владельца попадает сюда же и потому не появится
	// в изменениях как чужая.
	if out, err := git(ctx, dest, "add", "-A"); err != nil {
		return WorkCopy{}, fmt.Errorf("подготовка снимка: %v: %s", err, out)
	}
	if out, err := git(ctx, dest, "-c", "user.name=Бэрримор",
		"-c", "user.email=barrymore@localhost",
		"commit", "--allow-empty", "--no-verify", "-q",
		"-m", "состояние до работы исполнителя"); err != nil {
		return WorkCopy{}, fmt.Errorf("снимок до работы: %v: %s", err, out)
	}
	head, err := git(ctx, dest, "rev-parse", "HEAD")
	if err != nil {
		return WorkCopy{}, fmt.Errorf("чтение снимка: %w", err)
	}
	wc.Baseline = strings.TrimSpace(head)
	return wc, nil
}

// Change — то, что исполнитель сделал в копии.
type Change struct {
	// Patch — полный дифф от снимка «до», пригодный для применения.
	Patch string `json:"patch"`
	// Files перечисляет затронутое с видом изменения.
	Files []ChangedFile `json:"files"`
	// Insertions и Deletions дают представление о размере до чтения диффа.
	Insertions int `json:"insertions"`
	Deletions  int `json:"deletions"`
	// Truncated честно сообщает, что дифф показан не целиком.
	Truncated   bool      `json:"truncated"`
	CollectedAt time.Time `json:"collected_at"`
}

// ChangedFile — один затронутый файл.
type ChangedFile struct {
	Path string `json:"path"`
	// Status: added, modified, deleted, renamed.
	Status string `json:"status"`
}

// Empty сообщает, что исполнитель ничего не изменил.
func (c Change) Empty() bool { return len(c.Files) == 0 }

// maxPatchBytes ограничивает размер диффа, который держится в памяти и
// показывается владельцу. Полный дифф остаётся файлом в каталоге запуска.
const maxPatchBytes = 512 * 1024

// CollectChange собирает изменения исполнителя относительно снимка «до».
func CollectChange(ctx context.Context, wc WorkCopy) (Change, error) {
	ch := Change{CollectedAt: time.Now().UTC()}
	if wc.Path == "" || wc.Baseline == "" {
		return ch, nil
	}

	// Индекс копии обновляется намеренно: копия принадлежит Бэрримору, и это
	// единственный способ увидеть новые файлы наравне с изменёнными.
	if out, err := git(ctx, wc.Path, "add", "-A"); err != nil {
		return ch, fmt.Errorf("сбор изменений: %v: %s", err, out)
	}

	names, err := git(ctx, wc.Path, "diff", "--cached", "--name-status", wc.Baseline)
	if err != nil {
		return ch, fmt.Errorf("перечень изменений: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(names), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		ch.Files = append(ch.Files, ChangedFile{
			Path: parts[len(parts)-1], Status: statusWord(parts[0]),
		})
	}
	if len(ch.Files) == 0 {
		return ch, nil
	}

	stat, err := git(ctx, wc.Path, "diff", "--cached", "--shortstat", wc.Baseline)
	if err == nil {
		ch.Insertions, ch.Deletions = parseShortstat(stat)
	}

	patch, err := git(ctx, wc.Path, "diff", "--cached", "--binary", wc.Baseline)
	if err != nil {
		return ch, fmt.Errorf("дифф: %w", err)
	}
	if len(patch) > maxPatchBytes {
		patch = patch[:maxPatchBytes]
		ch.Truncated = true
	}
	ch.Patch = patch
	return ch, nil
}

// statusWord переводит букву git в человеческое слово.
func statusWord(code string) string {
	switch {
	case strings.HasPrefix(code, "A"):
		return "добавлен"
	case strings.HasPrefix(code, "M"):
		return "изменён"
	case strings.HasPrefix(code, "D"):
		return "удалён"
	case strings.HasPrefix(code, "R"):
		return "переименован"
	case strings.HasPrefix(code, "C"):
		return "скопирован"
	default:
		return code
	}
}

func parseShortstat(s string) (ins, del int) {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		var n int
		switch {
		case strings.Contains(part, "insertion"):
			fmt.Sscanf(part, "%d", &n)
			ins = n
		case strings.Contains(part, "deletion"):
			fmt.Sscanf(part, "%d", &n)
			del = n
		}
	}
	return ins, del
}

// ApplyResult — итог применения изменений к каталогу владельца.
type ApplyResult struct {
	Applied bool     `json:"applied"`
	Files   []string `json:"files"`
	Detail  string   `json:"detail"`
}

// ApplyChange переносит изменения в каталог владельца.
//
// Проверка идёт до записи (`git apply --check`): применить наполовину хуже,
// чем не применить вовсе. Ничего не коммитится — изменения остаются
// незакоммиченными, чтобы владелец посмотрел их своими инструментами и решил
// сам. Откат при этом остаётся обычным `git checkout`.
func ApplyChange(ctx context.Context, target string, patch string) (ApplyResult, error) {
	res := ApplyResult{}
	if strings.TrimSpace(patch) == "" {
		return res, errors.New("применять нечего: изменений нет")
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		return res, fmt.Errorf("%w: %s", ErrNotGit, target)
	}

	if out, err := gitStdin(ctx, target, patch, "apply", "--check", "--3way", "-"); err != nil {
		return res, fmt.Errorf(
			"изменения не накладываются на текущее состояние каталога: %s; "+
				"вероятно, он изменился с момента запуска исполнителя", strings.TrimSpace(out))
	}
	out, err := gitStdin(ctx, target, patch, "apply", "--3way", "-")
	if err != nil {
		return res, fmt.Errorf("применение не удалось: %s", strings.TrimSpace(out))
	}

	res.Applied = true
	if changed, err := git(ctx, target, "--no-optional-locks", "status", "--porcelain"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(changed), "\n") {
			if len(line) > 3 {
				res.Files = append(res.Files, strings.TrimSpace(line[2:]))
			}
		}
	}
	res.Detail = "изменения наложены и остались незакоммиченными: " +
		"посмотрите их своими инструментами и решите, что с ними делать"
	return res, nil
}

// RemoveWorkCopy убирает копию с диска.
func RemoveWorkCopy(wc WorkCopy) error {
	if wc.Path == "" {
		return nil
	}
	if err := os.RemoveAll(wc.Path); err != nil {
		return fmt.Errorf("удаление копии %s: %w", wc.Path, err)
	}
	return nil
}

// copyTree копирует каталог целиком.
func copyTree(ctx context.Context, src, dst string) (int, error) {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return 0, fmt.Errorf("каталог копии: %w", err)
	}
	count := 0
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if count >= maxCopyFiles {
			return fmt.Errorf(
				"в каталоге больше %d файлов: копия для контролируемой записи "+
					"была бы слишком дорогой", maxCopyFiles)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)

		switch {
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case d.Type()&os.ModeSymlink != 0:
			// Ссылки копируются ссылками: разыменование могло бы вытащить
			// в копию то, что лежит вне рабочего каталога.
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			count++
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			count++
			return copyFile(path, target)
		default:
			// Сокеты и устройства не копируются: они не часть работы.
			return nil
		}
	})
	if err != nil {
		os.RemoveAll(dst)
		return 0, fmt.Errorf("копирование каталога: %w", err)
	}
	return count, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// git выполняет команду в каталоге и возвращает вывод.
//
// core.quotePath=false обязателен: иначе git экранирует не-ASCII пути в
// восьмеричные последовательности, и файл «новый.go» приходит как
// "\320\275\320\276...". Разбор такого перечня молча терял бы файлы
// с русскими именами — то есть Бэрримор показал бы владельцу неполный
// список изменений и умолчал бы об этом.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir, "-c", "core.quotePath=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return out.String() + errBuf.String(), err
	}
	return out.String(), nil
}

func gitStdin(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	full := append([]string{"-C", dir, "-c", "core.quotePath=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return out.String() + errBuf.String(), err
	}
	return out.String(), nil
}

// shortID делает из идентификатора поручения кусок имени ветки.
//
// Имя должно быть узнаваемым: увидев `barrymore/06fwz2pg` в своём репозитории,
// владелец обязан понять, откуда она взялась.
func shortID(id string) string {
	if i := strings.IndexByte(id, '_'); i >= 0 && i+1 < len(id) {
		id = id[i+1:]
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}
