package harness

import (
	"fmt"
	"strings"

	"github.com/mirivlad/barrymore/internal/worker"
)

// unsafeChars — символы, которых в аргументе быть не может.
//
// Аргументы уходят в argv напрямую, без оболочки, так что сами по себе они
// не опасны. Но их появление означает, что модель сочинила строку команды,
// а не выбрала флаг из справки, — и такое предложение нельзя принимать
// независимо от того, сработает ли оно.
const unsafeChars = ";|&$><`\n\r\t*?!\\"

// Validate проверяет предложение по тому, что инструмент сам о себе сказал.
//
// Это единственное место, где решается, можно ли верить выводу модели.
// Правило одно и без исключений: аргумент, которого нет в справке, не
// принимается. Модель здесь не изобретает способ запуска, а читает его.
func Validate(d Draft, s Survey) error {
	if d.Name != s.Name {
		return fmt.Errorf("предложение про %q, а изучался %q", d.Name, s.Name)
	}
	if strings.TrimSpace(d.DisplayName) == "" {
		return fmt.Errorf("у исполнителя нет человеческого имени")
	}
	if strings.TrimSpace(d.Why) == "" {
		return fmt.Errorf("не сказано, почему способ запуска именно такой")
	}

	switch d.PromptVia {
	case PromptViaArgv, PromptViaStdin:
	default:
		return fmt.Errorf("непонятно, как передавать задание: %q", d.PromptVia)
	}

	prompts := 0
	for _, a := range d.RunArgs {
		if a == PromptToken {
			prompts++
		}
	}
	if d.PromptVia == PromptViaArgv && prompts != 1 {
		return fmt.Errorf("задание передаётся аргументом, но %s встречается %d раз",
			PromptToken, prompts)
	}
	if d.PromptVia == PromptViaStdin && prompts != 0 {
		return fmt.Errorf("задание передаётся через stdin, а %s всё равно стоит в аргументах",
			PromptToken)
	}

	groups := map[string][]string{
		"аргументах запуска":   d.RunArgs,
		"аргументах версии":    d.VersionArgs,
		"аргументах просмотра": d.AuditArgs,
	}
	for where, args := range groups {
		for _, a := range args {
			if err := checkArg(a, s, where); err != nil {
				return err
			}
		}
	}
	if d.ModelFlag != "" {
		if err := checkArg(d.ModelFlag, s, "флаге модели"); err != nil {
			return err
		}
	}
	if len(d.VersionArgs) == 0 {
		return fmt.Errorf("не назван способ узнать версию: без него инструмент " +
			"нельзя даже опросить")
	}
	if len(d.RunArgs) == 0 {
		return fmt.Errorf("не назван способ неинтерактивного запуска")
	}
	return nil
}

func checkArg(a string, s Survey, where string) error {
	if strings.TrimSpace(a) == "" {
		return fmt.Errorf("пустой аргумент в %s", where)
	}
	if a == PromptToken {
		return nil
	}
	if strings.ContainsAny(a, unsafeChars) {
		return fmt.Errorf("аргумент %q в %s похож на строку команды, а не на флаг", a, where)
	}
	if strings.Contains(a, " ") {
		return fmt.Errorf("аргумент %q в %s содержит пробел: это два аргумента, а не один",
			a, where)
	}
	if !s.Knows(a) {
		return fmt.Errorf("аргумента %q нет в справке %s — я не принимаю то, "+
			"чего инструмент о себе не сказал", a, s.Name)
	}
	return nil
}

// ToManifest превращает проверенное предложение в манифест исполнителя.
//
// Доверие новичку выдаётся наименьшее из возможных: только чтение рабочего
// каталога. Инструмент, о котором известна одна справка, не получает права
// менять файлы — это решение владельца и отдельный разговор.
func ToManifest(d Draft) worker.Manifest {
	caps := append([]string{worker.CapNonInteractive}, d.Capabilities...)
	return worker.Manifest{
		ID:                   d.Name,
		DisplayName:          d.DisplayName,
		Executables:          []string{d.Name},
		VersionArgs:          d.VersionArgs,
		DefaultTrust:         worker.TrustWorkspaceRead,
		CostPolicy:           "provider-account",
		AuthPaths:            d.AuthPaths,
		DeclaredCapabilities: dedupe(caps),
		SupportsAuditOnly:    len(d.AuditArgs) > 0,
		Class:                worker.ClassRoutine,
		Notes: "подключён Бэрримором по собственной справке инструмента; " +
			"возможности заявлены, а не проверены работой",
		Run: worker.RunSpec{
			Args:      d.RunArgs,
			PromptVia: d.PromptVia,
			AuditArgs: d.AuditArgs,
			ModelFlag: d.ModelFlag,
			Network:   true,
		},
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
