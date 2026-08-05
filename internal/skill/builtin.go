package skill

import (
	"fmt"
	"strconv"
	"strings"
)

// Встроенные умения.
const (
	SkillSurvey        = "workspace.survey"
	SkillWorktree      = "git.worktree.diagnose"
	SkillFreeSpace     = "host.space"
	SkillWhoIsWorking  = "workspace.who"
	skillDefaultCommit = "5"
)

// Builtin возвращает то, что Бэрримор умеет с самого начала.
//
// Список короткий намеренно. Умение попадает сюда не потому, что его можно
// написать, а потому, что за ним раньше звали исполнителя: каждое из них
// закрывает вопрос, ради которого запускался внешний процесс на минуту.
func Builtin() []Skill {
	return []Skill{
		{
			ID:          SkillWorktree,
			Title:       "разобраться с рабочими копиями",
			Question:    "что происходит с worktree этого репозитория и не завис ли он",
			NeedsTarget: true,
			Origin:      OriginBuiltin,
			Enabled:     true,
			Steps: []Step{
				{Primitive: PrimGitWorktrees, Args: map[string]string{"path": Target},
					Why: "перечислить рабочие копии и найти висящие"},
				{Primitive: PrimGitStatus, Args: map[string]string{"path": Target},
					Why: "понять, есть ли незакоммиченная работа"},
				{Primitive: PrimProcHolders, Args: map[string]string{"path": Target},
					Why: "проверить, не держит ли каталог чей-то процесс"},
			},
			Summarize: summarizeWorktree,
		},
		{
			ID:          SkillSurvey,
			Title:       "осмотреть каталог",
			Question:    "что это за каталог, под git ли он и что там в последнее время делали",
			NeedsTarget: true,
			Origin:      OriginBuiltin,
			Enabled:     true,
			Steps: []Step{
				{Primitive: PrimFSInspect, Args: map[string]string{"path": Target},
					Why: "понять размер и состав"},
				{Primitive: PrimGitStatus, Args: map[string]string{"path": Target},
					Why: "узнать ветку и незакоммиченное"},
				{Primitive: PrimGitLog,
					Args: map[string]string{"path": Target, "count": skillDefaultCommit},
					Why:  "посмотреть, что делали в последнее время"},
			},
			Summarize: summarizeSurvey,
		},
		{
			ID:          SkillWhoIsWorking,
			Title:       "выяснить, кто держит каталог",
			Question:    "почему каталог занят и какой процесс в нём сидит",
			NeedsTarget: true,
			Origin:      OriginBuiltin,
			Enabled:     true,
			Steps: []Step{
				{Primitive: PrimProcHolders, Args: map[string]string{"path": Target},
					Why: "найти процессы, работающие внутри"},
			},
			Summarize: summarizeHolders,
		},
		{
			ID:    SkillFreeSpace,
			Title: "проверить свободное место на дисках",
			Question: "сколько свободного места на дисках и не кончается ли оно " +
				"где-нибудь",
			NeedsTarget: false,
			Origin:      OriginBuiltin,
			Enabled:     true,
			Steps: []Step{
				{Primitive: PrimHostFreeSpace, Args: map[string]string{"path": Target},
					Why: "измерить свободное место по всем файловым системам"},
			},
			Summarize: summarizeSpace,
		},
	}
}

func signal(steps []StepResult, primitive, name string) string {
	for _, s := range steps {
		if s.Primitive == primitive {
			return s.Signals[name]
		}
	}
	return ""
}

func num(v string) int {
	n, _ := strconv.Atoi(v)
	return n
}

// summarizeWorktree отвечает на заданный вопрос, а не пересказывает шаги.
//
// «Ничего не висит» — такой же ответ, как «висят две копии»: владелец спросил,
// завис ли репозиторий, и должен получить да или нет, а не таблицу.
func summarizeWorktree(steps []StepResult) string {
	if signal(steps, PrimGitWorktrees, "git") == "false" {
		return "Каталог не под git: рабочих копий у него не бывает, и зависать нечему."
	}
	total := num(signal(steps, PrimGitWorktrees, "worktrees"))
	stuck := num(signal(steps, PrimGitWorktrees, "stuck"))
	holders := num(signal(steps, PrimProcHolders, "holders"))
	dirty := signal(steps, PrimGitStatus, "dirty") == "true"

	var b strings.Builder
	switch {
	case stuck > 0:
		fmt.Fprintf(&b, "Похоже, нашёл: висящих рабочих копий %d из %d.", stuck, total)
	case total <= 1:
		b.WriteString("Рабочая копия одна, лишних нет.")
	default:
		fmt.Fprintf(&b, "Рабочих копий %d, все на месте.", total)
	}
	if holders > 0 {
		fmt.Fprintf(&b, " В каталоге работает процессов: %d — каталог занят именно ими.", holders)
	} else if stuck == 0 {
		b.WriteString(" Процессов в каталоге не видно.")
	}
	if dirty {
		b.WriteString(" Незакоммиченная работа есть — убирать копии, не разобравшись, не стоит.")
	}
	return b.String()
}

func summarizeSurvey(steps []StepResult) string {
	files := signal(steps, PrimFSInspect, "files")
	if signal(steps, PrimFSInspect, "git") != "true" {
		return fmt.Sprintf("Обычный каталог, файлов %s, под git не заведён.", files)
	}
	branch := signal(steps, PrimGitStatus, "branch")
	state := "без незакоммиченного"
	if signal(steps, PrimGitStatus, "dirty") == "true" {
		state = "с незакоммиченной работой"
	}
	return fmt.Sprintf("Репозиторий на ветке %s, файлов %s, %s.",
		nonEmpty(branch, "без имени"), files, state)
}

// summarizeSpace называет самое узкое место, а не среднее по больнице.
func summarizeSpace(steps []StepResult) string {
	free := signal(steps, PrimHostFreeSpace, "free_pct")
	where := signal(steps, PrimHostFreeSpace, "tightest")
	n := num(signal(steps, PrimHostFreeSpace, "mounts"))
	if where == "" {
		return ""
	}
	pct := num(free)
	tone := "места хватает"
	if pct < 10 {
		tone = "места почти нет"
	} else if pct < 20 {
		tone = "места остаётся немного"
	}
	return fmt.Sprintf("Проверил %d файловых систем: %s. Теснее всего на %s — "+
		"свободно %d%%. Подробности ниже.", n, tone, where, pct)
}

func summarizeHolders(steps []StepResult) string {
	n := num(signal(steps, PrimProcHolders, "holders"))
	if n == 0 {
		return "В каталоге не работает ни один процесс из тех, что мне видны."
	}
	return fmt.Sprintf("Каталог держат %d процесса(ов) — они перечислены ниже.", n)
}
