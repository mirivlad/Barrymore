package harness

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// probeTimeout ограничивает одну пробу. Инструмент, который на `--help`
// думает дольше, для неинтерактивной работы не годится в любом случае.
const probeTimeout = 10 * time.Second

// maxHelp ограничивает объём справки, уходящей в контекст модели.
const maxHelp = 12000

// safeProbes — единственное, что Бэрримор позволяет себе с незнакомой
// программой.
//
// Все пробы одинаковы по смыслу: «расскажи о себе». Ни одна не выполняет
// работы, не ходит в сеть по заданию и не может ничего изменить. Список
// закрыт намеренно: пробы, которую можно дописать снаружи, достаточно,
// чтобы обойти всё остальное.
var safeProbes = [][]string{
	{"--version"},
	{"--help"},
	{"-h"},
	{"help"},
}

var reFlag = regexp.MustCompile(`(^|[\s,\[(|"'` + "`" + `])(--?[A-Za-z][A-Za-z0-9-]*)`)

// Look ищет инструмент в PATH.
type Look func(string) (string, error)

// Observe изучает незнакомый инструмент безопасными пробами.
//
// Имя проверяется до запуска: команда с пробелом, слэшем или служебным
// символом — не имя программы, а попытка выполнить что-то ещё.
func Observe(ctx context.Context, name string, look Look) (Survey, error) {
	clean := strings.TrimSpace(name)
	if err := checkName(clean); err != nil {
		return Survey{}, err
	}
	if look == nil {
		look = exec.LookPath
	}
	path, err := look(clean)
	if err != nil {
		return Survey{}, fmt.Errorf("в PATH нет команды %q: поставьте её или назовите точнее", clean)
	}

	s := Survey{Name: clean, ExecutablePath: path, SurveyedAt: time.Now().UTC()}
	var help strings.Builder
	for _, args := range safeProbes {
		p := runProbe(ctx, path, args)
		s.Probes = append(s.Probes, p)
		if !p.OK {
			continue
		}
		if len(args) == 1 && args[0] == "--version" && s.Version == "" {
			s.Version = firstLine(p.Output)
			continue
		}
		if help.Len() < maxHelp {
			help.WriteString(p.Output)
			help.WriteString("\n")
		}
	}

	s.Help = truncate(help.String(), maxHelp)
	s.Flags = collectFlags(s.Help)
	if s.Help == "" && s.Version == "" {
		return s, fmt.Errorf("%s ничего о себе не рассказал: ни --version, ни --help "+
			"не отработали, а гадать о способе запуска я не стану", clean)
	}
	return s, nil
}

func runProbe(ctx context.Context, path string, args []string) Probe {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, path, args...)
	// Ни stdin, ни терминала: интерактивный инструмент должен упасть сразу,
	// а не ждать ввода, которого не будет.
	cmd.Env = append(cmd.Environ(), "TERM=dumb", "NO_COLOR=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	text := stripANSI(strings.TrimRight(string(out), "\n"))

	p := Probe{Args: args, Output: truncate(text, maxHelp), OK: err == nil}
	if err != nil {
		p.Note = err.Error()
		// Многие CLI печатают справку и выходят с ненулевым кодом. Текст
		// в таком случае не выбрасывается: он и есть то, что нужно.
		if len(text) > 200 {
			p.OK = true
			p.Note = "код возврата ненулевой, но справка напечатана: " + err.Error()
		}
	}
	return p
}

// collectFlags выбирает из справки флаги. Это и есть граница дозволенного:
// предложить можно только то, что инструмент сам о себе напечатал.
func collectFlags(help string) []string {
	seen := map[string]bool{}
	for _, m := range reFlag.FindAllStringSubmatch(help, -1) {
		seen[m[2]] = true
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Knows сообщает, встречался ли аргумент в справке.
func (s Survey) Knows(arg string) bool {
	for _, f := range s.Flags {
		if f == arg {
			return true
		}
	}
	// Подкоманда — не флаг, и в списке флагов её нет. Она принимается,
	// если встречается в справке отдельным словом.
	return containsWord(s.Help, arg)
}

func containsWord(text, word string) bool {
	if word == "" {
		return false
	}
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == ',' || r == '|' ||
			r == '[' || r == ']' || r == '(' || r == ')' || r == '"' || r == '\''
	}) {
		if field == word {
			return true
		}
	}
	return false
}

var reName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]{0,63}$`)

func checkName(name string) error {
	if !reName.MatchString(name) {
		return fmt.Errorf("%q не похоже на имя команды: имя состоит из букв, цифр, "+
			"точки, дефиса и подчёркивания", name)
	}
	return nil
}

var reANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func stripANSI(s string) string { return reANSI.ReplaceAllString(s, "") }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\n…(справка обрезана)"
}
