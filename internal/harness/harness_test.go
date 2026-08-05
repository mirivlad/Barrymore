package harness_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/harness"
	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/worker"
)

// fakeTool кладёт в каталог исполняемый файл, печатающий заданную справку.
//
// Настоящая программа, а не подделка вывода: проверяется в том числе то, что
// Бэрримор действительно её запускает и читает напечатанное.
func fakeTool(t *testing.T, name, help string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  --version) echo '" + name + " 1.4.2' ;;\n" +
		"  *) cat <<'EOF'\n" + help + "\nEOF\n" +
		"  ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func looker(path string) harness.Look {
	return func(name string) (string, error) {
		if filepath.Base(path) == name {
			return path, nil
		}
		return "", os.ErrNotExist
	}
}

const helpText = `crush — агент для работы с кодом

Использование:
  crush run [флаги] <задание>

Флаги:
  --version        показать версию
  -h, --help       показать эту справку
  -q, --quiet      не печатать ничего лишнего
  --read-only      ничего не менять на диске
  --model строка   какую модель использовать
  --yolo           разрешить всё без вопросов
`

// scripted отдаёт заранее подготовленный ответ модели.
type scripted struct {
	content string
	lastReq model.Request
}

func (p *scripted) ID() string       { return "scripted" }
func (p *scripted) Describe() string { return "подготовленный ответ" }
func (p *scripted) Probe(context.Context) model.Status {
	return model.Status{Status: model.StatusReady, SupportsSchema: true}
}
func (p *scripted) Complete(_ context.Context, req model.Request) (model.Response, error) {
	p.lastReq = req
	return model.Response{Content: p.content, Model: "тестовая"}, nil
}

func draftJSON(d map[string]any) string {
	base := map[string]any{
		"display_name": "Crush", "version_args": []string{"--version"},
		"run_args": []string{"run", "--quiet", "{prompt}"}, "prompt_via": "argv",
		"audit_args": []string{"--read-only"}, "model_flag": "--model",
		"auth_paths": []string{}, "capabilities": []string{"repository-audit"},
		"why":      "справка описывает подкоманду run с заданием аргументом",
		"evidence": []string{"crush run [флаги] <задание>"},
	}
	for k, v := range d {
		base[k] = v
	}
	raw, _ := json.Marshal(base)
	return string(raw)
}

type registry struct {
	adopted []worker.Adapter
	known   map[string]bool
}

func (r *registry) Register(a worker.Adapter) error {
	r.adopted = append(r.adopted, a)
	return nil
}

func (r *registry) Known(id string) bool { return r.known[id] }

func newService(t *testing.T, content string) (*harness.Service, *registry) {
	t.Helper()
	path := fakeTool(t, "crush", helpText)
	reg := &registry{known: map[string]bool{}}
	return harness.New(harness.Config{
		Provider: &scripted{content: content}, Registry: reg, Look: looker(path),
	}), reg
}

// --- наблюдение своими средствами ---

func TestObserveReadsWhatTheToolSaysAboutItself(t *testing.T) {
	path := fakeTool(t, "crush", helpText)
	sv, err := harness.Observe(context.Background(), "crush", looker(path))
	if err != nil {
		t.Fatal(err)
	}
	if sv.Version != "crush 1.4.2" {
		t.Fatalf("версия не получена: %q", sv.Version)
	}
	if !strings.Contains(sv.Help, "--read-only") {
		t.Fatal("справка не прочитана")
	}
	for _, want := range []string{"--read-only", "--model", "--quiet"} {
		if !sv.Knows(want) {
			t.Fatalf("флаг %q не разобран из справки", want)
		}
	}
	if sv.Knows("--чего-нет") {
		t.Fatal("справка признала флаг, которого в ней нет")
	}
}

// Имя команды — не строка команды.
func TestObserveRefusesSomethingThatIsNotACommandName(t *testing.T) {
	for _, bad := range []string{"rm -rf /", "crush; whoami", "../../bin/sh", "crush|tee"} {
		if _, err := harness.Observe(context.Background(), bad, looker("/bin/true")); err == nil {
			t.Fatalf("принято за имя команды: %q", bad)
		}
	}
}

func TestObserveRefusesWhatIsNotInstalled(t *testing.T) {
	_, err := harness.Observe(context.Background(), "notinstalled", looker("/tmp/crush"))
	if err == nil {
		t.Fatal("изучен инструмент, которого нет")
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("отказ не объясняет, чего не хватает: %v", err)
	}
}

// --- граница доверия к выводу модели ---

// Главное свойство всего среза: способ запуска не сочиняется. Всё, чего нет
// в справке, отвергается — даже если оно выглядит правдоподобно.
func TestInventedFlagIsRefused(t *testing.T) {
	svc, _ := newService(t, draftJSON(map[string]any{
		"run_args": []string{"run", "--dangerously-skip-permissions", "{prompt}"},
	}))
	_, d, err := svc.Study(context.Background(), "crush")
	if err != nil {
		t.Fatal(err)
	}
	if d.Refused == "" {
		t.Fatal("принят флаг, которого инструмент о себе не говорил")
	}
	if !strings.Contains(d.Refused, "--dangerously-skip-permissions") {
		t.Fatalf("отказ не называет виновника: %q", d.Refused)
	}
}

func TestShellStringIsRefused(t *testing.T) {
	svc, _ := newService(t, draftJSON(map[string]any{
		"run_args": []string{"run", "{prompt}", "> /tmp/out"},
	}))
	_, d, err := svc.Study(context.Background(), "crush")
	if err != nil {
		t.Fatal(err)
	}
	if d.Refused == "" {
		t.Fatal("принята строка команды вместо аргумента")
	}
}

func TestArgumentWithSpaceIsRefused(t *testing.T) {
	svc, _ := newService(t, draftJSON(map[string]any{
		"run_args": []string{"run --quiet", "{prompt}"},
	}))
	_, d, err := svc.Study(context.Background(), "crush")
	if err != nil {
		t.Fatal(err)
	}
	if d.Refused == "" {
		t.Fatal("два аргумента приняты за один")
	}
}

func TestMissingPromptPlaceholderIsRefused(t *testing.T) {
	svc, _ := newService(t, draftJSON(map[string]any{
		"run_args": []string{"run", "--quiet"},
	}))
	_, d, err := svc.Study(context.Background(), "crush")
	if err != nil {
		t.Fatal(err)
	}
	if d.Refused == "" {
		t.Fatal("принят запуск, которому некуда положить задание")
	}
}

// Модели дают справку, а не просят вспомнить инструмент.
func TestModelIsGivenTheHelpItMustReadFrom(t *testing.T) {
	path := fakeTool(t, "crush", helpText)
	prov := &scripted{content: draftJSON(nil)}
	svc := harness.New(harness.Config{
		Provider: prov, Registry: &registry{known: map[string]bool{}}, Look: looker(path),
	})
	if _, _, err := svc.Study(context.Background(), "crush"); err != nil {
		t.Fatal(err)
	}
	whole := prov.lastReq.System + prov.lastReq.Messages[0].Content
	if !strings.Contains(whole, "--read-only") {
		t.Fatal("модели не показали справку: выводить ей будет неоткуда")
	}
	if !strings.Contains(prov.lastReq.System, "дословно встречаться") {
		t.Fatal("модели не сказано, что придумывать нельзя")
	}
}

// --- подключение ---

func TestAdoptedHarnessIsRunnableAndTrustedLeast(t *testing.T) {
	ctx := context.Background()
	svc, reg := newService(t, draftJSON(nil))
	sv, d, err := svc.Study(ctx, "crush")
	if err != nil {
		t.Fatal(err)
	}
	if d.Refused != "" {
		t.Fatalf("годное предложение отвергнуто: %s", d.Refused)
	}

	m, err := svc.Adopt(ctx, d, sv)
	if err != nil {
		t.Fatal(err)
	}
	// Инструмент, о котором известна одна справка, не получает права
	// менять файлы владельца.
	if m.DefaultTrust != worker.TrustWorkspaceRead {
		t.Fatalf("новичку выдано доверие %q", m.DefaultTrust)
	}
	if len(reg.adopted) != 1 {
		t.Fatal("исполнитель не попал в штат")
	}

	adapter := reg.adopted[0]
	if !adapter.Descriptor().Runnable {
		t.Fatal("подключённый исполнитель не умеет запускаться")
	}
	plan, err := adapter.Plan(ctx, worker.Installation{ExecutablePath: "/usr/bin/crush"},
		worker.RunRequest{
			WorkDir: "/tmp/w", ScratchDir: "/tmp/s", Prompt: "разберись",
			AuditOnly: true, Model: "free/model",
		})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/crush", "run", "--quiet", "разберись",
		"--read-only", "--model", "free/model"}
	if strings.Join(plan.Argv, " ") != strings.Join(want, " ") {
		t.Fatalf("запуск собран не так:\n  вышло: %v\n  ждали: %v", plan.Argv, want)
	}
	// Задание уходит аргументом, а не через оболочку: никакой строки команды.
	for _, a := range plan.Argv {
		if strings.ContainsAny(a, ";|&$><") && a != "разберись" {
			t.Fatalf("в argv попал служебный символ: %q", a)
		}
	}
}

func TestAuditOnlyIsRefusedWhenToolDoesNotSupportIt(t *testing.T) {
	ctx := context.Background()
	svc, reg := newService(t, draftJSON(map[string]any{"audit_args": []string{}}))
	sv, d, err := svc.Study(ctx, "crush")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Adopt(ctx, d, sv); err != nil {
		t.Fatal(err)
	}
	_, err = reg.adopted[0].Plan(ctx, worker.Installation{ExecutablePath: "/usr/bin/crush"},
		worker.RunRequest{WorkDir: "/tmp/w", ScratchDir: "/tmp/s", AuditOnly: true})
	if err == nil {
		t.Fatal("запуск «только чтение» разрешён инструменту, который такого не умеет")
	}
}

func TestKnownHarnessIsNotStudiedAgain(t *testing.T) {
	path := fakeTool(t, "crush", helpText)
	reg := &registry{known: map[string]bool{"crush": true}}
	svc := harness.New(harness.Config{
		Provider: &scripted{content: draftJSON(nil)}, Registry: reg, Look: looker(path),
	})
	if _, _, err := svc.Study(context.Background(), "crush"); err == nil {
		t.Fatal("уже подключённый инструмент изучен заново")
	}
}

// Манифест без раздела запуска остаётся честно неподключённым.
func TestManifestWithoutRunSpecStillRefusesToLaunch(t *testing.T) {
	a := worker.NewManifestAdapter(worker.Manifest{
		ID: "pi", Executables: []string{"pi"},
	})
	if a.Descriptor().Runnable {
		t.Fatal("манифест без способа запуска выдаёт себя за готового исполнителя")
	}
	if _, err := a.Plan(context.Background(),
		worker.Installation{ExecutablePath: "/usr/bin/pi"},
		worker.RunRequest{WorkDir: "/tmp", ScratchDir: "/tmp"}); err == nil {
		t.Fatal("запуск подделан")
	}
}
