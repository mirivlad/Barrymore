package skill

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Примитивы Бэрримора. Все читают и ничего не меняют.
const (
	PrimFSInspect     = "fs.inspect"
	PrimGitStatus     = "git.status"
	PrimGitWorktrees  = "git.worktrees"
	PrimGitLog        = "git.log"
	PrimProcHolders   = "proc.holders"
	PrimHostFreeSpace = "host.free_space"
)

// maxInspectFiles ограничивает обход: осмотр каталога должен занимать
// мгновение, иначе смысл собственного умения теряется.
const maxInspectFiles = 50000

// Primitives возвращает то, чем Бэрримор действует сам.
func Primitives() []Primitive {
	return []Primitive{
		{
			ID: PrimFSInspect, Title: "осмотреть каталог",
			Args: []Arg{{Name: "path", Kind: ArgPath, Why: "что осматривать"}},
			Run:  fsInspect,
		},
		{
			ID: PrimGitStatus, Title: "посмотреть состояние репозитория",
			Args: []Arg{{Name: "path", Kind: ArgPath, Why: "какой репозиторий"}},
			Run:  gitStatus,
		},
		{
			ID: PrimGitWorktrees, Title: "перечислить рабочие копии",
			Args: []Arg{{Name: "path", Kind: ArgPath, Why: "какой репозиторий"}},
			Run:  gitWorktrees,
		},
		{
			ID: PrimGitLog, Title: "прочитать последние коммиты",
			Args: []Arg{
				{Name: "path", Kind: ArgPath, Why: "какой репозиторий"},
				{Name: "count", Kind: ArgCount, Why: "сколько коммитов"},
			},
			Run: gitLog,
		},
		{
			ID: PrimProcHolders, Title: "выяснить, кто держит каталог",
			Args: []Arg{{Name: "path", Kind: ArgPath, Why: "какой каталог"}},
			Run:  procHolders,
		},
		{
			ID: PrimHostFreeSpace, Title: "проверить свободное место",
			Args: []Arg{{
				Name: "path", Kind: ArgPath, Optional: true,
				Why: "на каком разделе; без него — по всем",
			}},
			Run: hostFreeSpace,
		},
	}
}

// git запускает git фиксированным набором аргументов.
//
// --no-optional-locks обязателен: без него `git status` трогает `.git/index`,
// то есть наблюдение меняет ровно то, что наблюдает. Однажды это уже
// испортило проверку неизменности каталога.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir, "--no-optional-locks"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	text := strings.TrimRight(string(out), "\n")
	if err != nil {
		return text, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

func isGitRepo(ctx context.Context, dir string) bool {
	out, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

func fsInspect(ctx context.Context, in Args) (Observation, error) {
	root := in["path"]
	var (
		files, dirs int
		bytes       int64
		truncated   bool
	)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Недоступный подкаталог не должен обрушить осмотр: дворецкий,
			// споткнувшийся о запертую дверь, докладывает об остальных комнатах.
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if files+dirs >= maxInspectFiles {
			truncated = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			if p != root && d.Name() == ".git" {
				return filepath.SkipDir
			}
			if p != root {
				dirs++
			}
			return nil
		}
		files++
		if fi, statErr := d.Info(); statErr == nil {
			bytes += fi.Size()
		}
		return nil
	})
	if err != nil {
		return Observation{}, err
	}

	top, _ := os.ReadDir(root)
	names := make([]string, 0, 12)
	for _, e := range top {
		if len(names) == 12 {
			names = append(names, "…")
			break
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	repo := isGitRepo(ctx, root)
	obs := Observation{Signals: map[string]string{
		"files": strconv.Itoa(files),
		"git":   strconv.FormatBool(repo),
	}}
	obs.Facts = append(obs.Facts, Fact{
		Text: fmt.Sprintf("файлов %d, каталогов %d, всего %s%s",
			files, dirs, humanBytes(bytes),
			map[bool]string{true: " (обход оборван на пределе)"}[truncated]),
		Detail: strings.Join(names, ", "),
	})
	if repo {
		obs.Facts = append(obs.Facts, Fact{Text: "это git-репозиторий"})
	} else {
		obs.Facts = append(obs.Facts, Fact{Text: "под git не заведён"})
	}
	return obs, nil
}

func gitStatus(ctx context.Context, in Args) (Observation, error) {
	dir := in["path"]
	if !isGitRepo(ctx, dir) {
		return Observation{
			Facts:   []Fact{{Text: "каталог не под git — состояния репозитория здесь нет"}},
			Signals: map[string]string{"git": "false"},
		}, nil
	}
	out, err := git(ctx, dir, "status", "--porcelain=v2", "--branch")
	if err != nil {
		return Observation{}, err
	}

	var (
		branch, head string
		changed      int
		untracked    int
	)
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.oid "):
			head = strings.TrimPrefix(line, "# branch.oid ")
		case strings.HasPrefix(line, "?"):
			untracked++
		case line != "" && !strings.HasPrefix(line, "#"):
			changed++
		}
	}
	if len(head) > 8 {
		head = head[:8]
	}

	obs := Observation{Signals: map[string]string{
		"branch":  branch,
		"changed": strconv.Itoa(changed),
		"dirty":   strconv.FormatBool(changed+untracked > 0),
	}}
	obs.Facts = append(obs.Facts, Fact{
		Text:   fmt.Sprintf("ветка %s", nonEmpty(branch, "не названа (detached HEAD)")),
		Detail: head,
	})
	if changed+untracked == 0 {
		obs.Facts = append(obs.Facts, Fact{Text: "незакоммиченного нет"})
	} else {
		obs.Facts = append(obs.Facts, Fact{
			Text:   fmt.Sprintf("незакоммичено: изменённых %d, новых %d", changed, untracked),
			Detail: out,
		})
	}
	return obs, nil
}

// gitWorktrees отвечает на вопрос, ради которого раньше звали исполнителя.
//
// `prunable` и `locked` — ровно то, что владелец называет «завис в worktree»:
// каталога уже нет или он заперт, а репозиторий продолжает его считать своим.
func gitWorktrees(ctx context.Context, in Args) (Observation, error) {
	dir := in["path"]
	if !isGitRepo(ctx, dir) {
		return Observation{
			Facts:   []Fact{{Text: "каталог не под git — рабочих копий нет"}},
			Signals: map[string]string{"git": "false"},
		}, nil
	}
	out, err := git(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return Observation{}, err
	}

	type wt struct{ path, branch, locked, prunable string }
	var (
		list []wt
		cur  *wt
	)
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "worktree "):
			list = append(list, wt{path: strings.TrimPrefix(line, "worktree ")})
			cur = &list[len(list)-1]
		case cur == nil:
		case strings.HasPrefix(line, "branch "):
			cur.branch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "detached":
			cur.branch = "без ветки"
		case strings.HasPrefix(line, "locked"):
			cur.locked = nonEmpty(strings.TrimSpace(strings.TrimPrefix(line, "locked")),
				"причина не указана")
		case strings.HasPrefix(line, "prunable"):
			cur.prunable = nonEmpty(strings.TrimSpace(strings.TrimPrefix(line, "prunable")),
				"причина не указана")
		}
	}

	stuck := 0
	obs := Observation{}
	for _, w := range list {
		text := fmt.Sprintf("%s — %s", w.path, nonEmpty(w.branch, "ветка не названа"))
		switch {
		case w.prunable != "":
			stuck++
			text += fmt.Sprintf("; висит впустую: %s", w.prunable)
		case w.locked != "":
			stuck++
			text += fmt.Sprintf("; заперта: %s", w.locked)
		}
		obs.Facts = append(obs.Facts, Fact{Text: text})
	}
	if len(list) == 0 {
		obs.Facts = append(obs.Facts, Fact{Text: "рабочих копий не заведено"})
	}
	obs.Signals = map[string]string{
		"worktrees": strconv.Itoa(len(list)),
		"stuck":     strconv.Itoa(stuck),
	}
	return obs, nil
}

func gitLog(ctx context.Context, in Args) (Observation, error) {
	dir := in["path"]
	if !isGitRepo(ctx, dir) {
		return Observation{
			Facts:   []Fact{{Text: "каталог не под git — истории здесь нет"}},
			Signals: map[string]string{"git": "false"},
		}, nil
	}
	n := in["count"]
	if n == "" {
		n = "5"
	}
	out, err := git(ctx, dir, "log", "-n", n, "--date=short",
		"--format=%h · %ad · %an · %s")
	if err != nil {
		// Пустой репозиторий — не поломка: сказать «истории пока нет» честнее,
		// чем показать ошибку git.
		return Observation{
			Facts:   []Fact{{Text: "коммитов пока нет"}},
			Signals: map[string]string{"commits": "0"},
		}, nil
	}
	lines := []string{}
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	obs := Observation{Signals: map[string]string{"commits": strconv.Itoa(len(lines))}}
	for _, l := range lines {
		obs.Facts = append(obs.Facts, Fact{Text: l})
	}
	if len(lines) == 0 {
		obs.Facts = append(obs.Facts, Fact{Text: "коммитов пока нет"})
	}
	return obs, nil
}

// procHolders ищет процессы, работающие внутри каталога.
//
// Читается только /proc: никаких lsof и fuser, которых на хосте может не быть.
// Чужие процессы недоступны — и это не скрывается: перечень честно называется
// «из тех, что видны».
func procHolders(ctx context.Context, in Args) (Observation, error) {
	root := strings.TrimRight(in["path"], "/")
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return Observation{}, fmt.Errorf("/proc недоступен: %w", err)
	}

	self := os.Getpid()
	type holder struct{ pid, comm, where string }
	var found []holder
	for _, e := range entries {
		if ctx.Err() != nil {
			return Observation{}, ctx.Err()
		}
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil || pid == self {
			continue
		}
		cwd, linkErr := os.Readlink(filepath.Join("/proc", e.Name(), "cwd"))
		if linkErr != nil || !under(cwd, root) {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		found = append(found, holder{
			pid:   e.Name(),
			comm:  strings.TrimSpace(string(comm)),
			where: cwd,
		})
		if len(found) == 50 {
			break
		}
	}

	obs := Observation{Signals: map[string]string{"holders": strconv.Itoa(len(found))}}
	if len(found) == 0 {
		obs.Facts = append(obs.Facts, Fact{
			Text: "из видимых мне процессов в этом каталоге не работает никто",
		})
		return obs, nil
	}
	for _, h := range found {
		obs.Facts = append(obs.Facts, Fact{
			Text:   fmt.Sprintf("в каталоге работает %s (pid %s)", nonEmpty(h.comm, "процесс"), h.pid),
			Detail: h.where,
		})
	}
	return obs, nil
}

// pseudoFS — файловые системы, которые не занимают места на диске.
//
// Показывать владельцу, что в `/dev` свободно 16 ГБ, — не ответ на вопрос
// «сколько места на диске», а шум, в котором тонет настоящий ответ.
var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true,
	"cgroup": true, "cgroup2": true, "securityfs": true, "pstore": true,
	"bpf": true, "debugfs": true, "tracefs": true, "configfs": true,
	"fusectl": true, "mqueue": true, "hugetlbfs": true, "efivarfs": true,
	"autofs": true, "binfmt_misc": true, "ramfs": true, "squashfs": true,
	"nsfs": true, "overlay": true, "tmpfs": true,
}

// hostFreeSpace отвечает на тот же вопрос, что и `df -h`.
//
// Без каталога перечисляются все настоящие файловые системы: владелец
// спрашивает «сколько места на диске», а не «сколько на разделе с этим
// путём». Раньше умение требовало каталог — и на общий вопрос его просто
// не звали, а модель отвечала выдуманным числом.
func hostFreeSpace(_ context.Context, in Args) (Observation, error) {
	if p := in["path"]; p != "" {
		free, total, err := space(p)
		if err != nil {
			return Observation{}, err
		}
		return Observation{
			Facts:   []Fact{{Text: describeSpace(p, free, total)}},
			Signals: map[string]string{"free_pct": strconv.Itoa(percent(free, total))},
		}, nil
	}

	mounts, err := realMounts()
	if err != nil {
		return Observation{}, err
	}
	obs := Observation{Signals: map[string]string{}}
	var tightest = 101
	for _, m := range mounts {
		free, total, err := space(m)
		if err != nil || total == 0 {
			continue
		}
		obs.Facts = append(obs.Facts, Fact{Text: describeSpace(m, free, total)})
		if p := percent(free, total); p < tightest {
			tightest = p
			obs.Signals["tightest"] = m
		}
	}
	if len(obs.Facts) == 0 {
		return Observation{}, fmt.Errorf("ни одна файловая система не опрошена")
	}
	obs.Signals["free_pct"] = strconv.Itoa(tightest)
	obs.Signals["mounts"] = strconv.Itoa(len(obs.Facts))
	return obs, nil
}

func space(path string) (free, total int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, fmt.Errorf("раздел %s недоступен: %w", path, err)
	}
	return int64(st.Bavail) * st.Bsize, int64(st.Blocks) * st.Bsize, nil
}

func percent(free, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(free * 100 / total)
}

func describeSpace(mount string, free, total int64) string {
	return fmt.Sprintf("%s — свободно %s из %s (%d%% свободно)",
		mount, humanBytes(free), humanBytes(total), percent(free, total))
}

// realMounts читает /proc/self/mounts и оставляет то, что занимает диск.
func realMounts() ([]string, error) {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil, fmt.Errorf("перечень файловых систем недоступен: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || pseudoFS[f[2]] {
			continue
		}
		// Пробелы в точке монтирования экранируются восьмеричным \040.
		mount := strings.ReplaceAll(f[1], `\040`, " ")
		if seen[mount] {
			continue
		}
		seen[mount] = true
		out = append(out, mount)
	}
	sort.Strings(out)
	return out, nil
}

func under(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+"/")
}

func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d Б", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), [...]string{"КБ", "МБ", "ГБ", "ТБ"}[exp])
}
