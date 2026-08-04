package delegation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WorkspaceState — слепок рабочего каталога до и после запуска.
//
// ADR 0007, третий слой: даже при работающей изоляции состояние сверяется
// фактически. Расхождение здесь означает пробой изоляции, а не рабочую заминку.
type WorkspaceState struct {
	Root string `json:"root"`
	// Digest — контрольная сумма перечня файлов с размерами и временами изменения.
	Digest string `json:"digest"`
	// FileCount и Truncated честно сообщают, полон ли обход.
	FileCount int    `json:"file_count"`
	Truncated bool   `json:"truncated"`
	GitHead   string `json:"git_head,omitempty"`
	GitStatus string `json:"git_status,omitempty"`
	// GitDirty перечисляет незакоммиченные изменения, существовавшие до запуска.
	// Они не должны пострадать (сценарий G).
	GitDirty  []string  `json:"git_dirty,omitempty"`
	ScannedAt time.Time `json:"scanned_at"`
	// entries хранится для сравнения слепков и не сериализуется.
	entries map[string]string
}

// maxScanFiles ограничивает обход, чтобы огромный каталог не вешал систему.
const maxScanFiles = 200000

// ScanWorkspace строит слепок каталога.
func ScanWorkspace(ctx context.Context, root string) (WorkspaceState, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return WorkspaceState{}, fmt.Errorf("рабочий каталог %q: %w", root, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return WorkspaceState{}, fmt.Errorf("рабочий каталог %q: %w", abs, err)
	}
	if !info.IsDir() {
		return WorkspaceState{}, fmt.Errorf("рабочий каталог %q не является каталогом", abs)
	}

	st := WorkspaceState{Root: abs, entries: map[string]string{}}
	var lines []string

	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Недоступный подкаталог не должен обрушить весь обход, но и молча
			// пропасть не должен: он попадает в перечень как отдельная запись.
			rel, _ := filepath.Rel(abs, path)
			lines = append(lines, rel+"\tERROR\t"+err.Error())
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if st.FileCount >= maxScanFiles {
			st.Truncated = true
			return filepath.SkipAll
		}
		rel, relErr := filepath.Rel(abs, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		fi, statErr := d.Info()
		if statErr != nil {
			lines = append(lines, rel+"\tERROR\t"+statErr.Error())
			return nil
		}
		entry := fmt.Sprintf("%d\t%d\t%s", fi.Size(), fi.ModTime().UnixNano(), fi.Mode().String())
		st.entries[rel] = entry
		lines = append(lines, rel+"\t"+entry)
		st.FileCount++
		return nil
	})
	if err != nil {
		return WorkspaceState{}, fmt.Errorf("обход рабочего каталога %q: %w", abs, err)
	}

	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	st.Digest = "sha256:" + hex.EncodeToString(sum[:])
	st.ScannedAt = time.Now().UTC()

	st.GitHead, st.GitStatus, st.GitDirty = gitState(ctx, abs)
	return st, nil
}

// gitState читает состояние репозитория, ничего в нём не меняя.
func gitState(ctx context.Context, root string) (head, status string, dirty []string) {
	run := func(args ...string) (string, bool) {
		// --no-optional-locks обязателен: обычный git status обновляет .git/index,
		// то есть проверка изменяла бы ровно то состояние, которое измеряет,
		// и любой audit-only запуск выглядел бы нарушившим неизменность каталога.
		full := append([]string{"--no-optional-locks", "-C", root}, args...)
		cmd := exec.CommandContext(ctx, "git", full...)
		out, err := cmd.Output()
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(string(out)), true
	}
	if v, ok := run("rev-parse", "HEAD"); ok {
		head = v
	}
	if v, ok := run("status", "--porcelain"); ok {
		status = v
		for _, line := range strings.Split(v, "\n") {
			if strings.TrimSpace(line) != "" {
				dirty = append(dirty, line)
			}
		}
	}
	return head, status, dirty
}

// Diff перечисляет отличия нового слепка от исходного.
type Diff struct {
	Added    []string `json:"added,omitempty"`
	Removed  []string `json:"removed,omitempty"`
	Modified []string `json:"modified,omitempty"`
}

// Empty сообщает, что каталог не изменился.
func (d Diff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Modified) == 0
}

// Paths возвращает все затронутые пути.
func (d Diff) Paths() []string {
	out := make([]string, 0, len(d.Added)+len(d.Removed)+len(d.Modified))
	out = append(out, d.Added...)
	out = append(out, d.Removed...)
	out = append(out, d.Modified...)
	sort.Strings(out)
	return out
}

// Compare сравнивает два слепка.
func Compare(before, after WorkspaceState) Diff {
	var d Diff
	for path, entry := range after.entries {
		prev, existed := before.entries[path]
		switch {
		case !existed:
			d.Added = append(d.Added, path)
		case prev != entry:
			d.Modified = append(d.Modified, path)
		}
	}
	for path := range before.entries {
		if _, ok := after.entries[path]; !ok {
			d.Removed = append(d.Removed, path)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Modified)
	return d
}
