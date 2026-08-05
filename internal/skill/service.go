package skill

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/ids"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/store"
)

// ErrUnknownSkill возвращается, когда умения нет или оно снято с применения.
var ErrUnknownSkill = errors.New("такого умения у Бэрримора нет")

// Watcher узнаёт об исходе применения умения.
//
// Опыт собирается тем же действием, что его порождает. Отдельный обход,
// который надо не забыть запустить, рано или поздно забывают запускать.
type Watcher interface {
	SkillApplied(ctx context.Context, run Run)
}

// WorkspacePolicy разрешает или запрещает каталог.
//
// Собственное умение читает каталоги владельца, и это ровно тот же вопрос
// доверия, что и у поручения. Значит и проверка та же: своя, более мягкая,
// означала бы, что запрет обходится сменой способа.
type WorkspacePolicy interface {
	AllowWorkspace(path string) error
}

// stepTimeout ограничивает шаг. Умение, идущее дольше, перестаёт быть
// собственным умением и становится работой — а работа делается поручением.
const stepTimeout = 15 * time.Second

// Service ведёт умения Бэрримора и их применение.
type Service struct {
	db      *store.DB
	journal *event.Journal
	clock   clock.Clock
	log     *slog.Logger
	policy  WorkspacePolicy

	mu      sync.RWMutex
	prims   map[string]Primitive
	skills  []Skill
	watcher Watcher
}

// Watch подключает наблюдателя за исходами.
func (s *Service) Watch(w Watcher) {
	s.mu.Lock()
	s.watcher = w
	s.mu.Unlock()
}

// Config — то, без чего умения не работают.
type Config struct {
	DB      *store.DB
	Journal *event.Journal
	Clock   clock.Clock
	Policy  WorkspacePolicy
	Logger  *slog.Logger
}

// New собирает службу умений со встроенным набором.
func New(cfg Config) *Service {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		db: cfg.DB, journal: cfg.Journal, clock: cfg.Clock,
		policy: cfg.Policy, log: log, prims: map[string]Primitive{},
	}
	for _, p := range Primitives() {
		s.prims[p.ID] = p
	}
	s.skills = Builtin()
	return s
}

// Skills возвращает умения в порядке показа.
func (s *Service) Skills() []Skill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Skill(nil), s.skills...)
}

// Live возвращает только применимые умения — те, что показываются модели.
func (s *Service) Live() []Skill {
	out := []Skill{}
	for _, sk := range s.Skills() {
		if sk.Live() {
			out = append(out, sk)
		}
	}
	return out
}

// Get находит умение по идентификатору.
func (s *Service) Get(id string) (Skill, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sk := range s.skills {
		if sk.ID == id {
			return sk, true
		}
	}
	return Skill{}, false
}

// Learn добавляет освоенное умение.
//
// Шаги проверяются целиком до записи: умение, собранное из несуществующего
// примитива, не должно попасть в список и обнаружиться при первом применении.
func (s *Service) Learn(ctx context.Context, sk Skill, actor event.Actor) (Skill, error) {
	sk.Origin = OriginLearned
	sk.Enabled = true
	if strings.TrimSpace(sk.ID) == "" {
		return Skill{}, fmt.Errorf("у умения нет имени")
	}
	if strings.TrimSpace(sk.Title) == "" || strings.TrimSpace(sk.Question) == "" {
		return Skill{}, fmt.Errorf("умение без названия и вопроса нельзя ни выбрать, ни объяснить")
	}
	if len(sk.Steps) == 0 {
		return Skill{}, fmt.Errorf("умение без шагов ничего не делает")
	}
	if _, exists := s.Get(sk.ID); exists {
		return Skill{}, fmt.Errorf("умение %q уже есть", sk.ID)
	}
	for i, st := range sk.Steps {
		if err := s.checkStep(st); err != nil {
			return Skill{}, fmt.Errorf("шаг %d: %w", i+1, err)
		}
	}

	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: sk.ID, ExpectedRevision: event.AnyRevision,
			EventType: EvSkillLearned, Actor: actor, Payload: sk,
		}); err != nil {
			return err
		}
		return applyLearned(ctx, tx, sk)
	})
	if err != nil {
		return Skill{}, err
	}

	s.mu.Lock()
	s.skills = append(s.skills, sk)
	s.mu.Unlock()
	return sk, nil
}

// Retire снимает умение с применения, называя причину.
//
// Умение не удаляется. «Прежний способ больше не годится» — это знание,
// и стирать его вместе со способом значило бы освоить тот же способ заново.
func (s *Service) Retire(ctx context.Context, id, why string, actor event.Actor) error {
	if strings.TrimSpace(why) == "" {
		return fmt.Errorf("умение снимают с причиной, иначе его нечем оспорить")
	}
	sk, ok := s.Get(id)
	if !ok {
		return ErrUnknownSkill
	}
	p := retiredPayload{SkillID: sk.ID, Why: why, At: s.clock.Now()}
	_, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: sk.ID, ExpectedRevision: event.AnyRevision,
			EventType: EvSkillRetired, Actor: actor, Payload: p,
		}); err != nil {
			return err
		}
		return applyRetired(ctx, tx, p)
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	for i := range s.skills {
		if s.skills[i].ID == id {
			s.skills[i].RetiredWhy = why
		}
	}
	s.mu.Unlock()
	return nil
}

// Apply применяет умение и записывает, что вышло.
func (s *Service) Apply(ctx context.Context, req Request) (Run, error) {
	sk, ok := s.Get(req.SkillID)
	if !ok {
		return Run{}, ErrUnknownSkill
	}
	if !sk.Live() {
		return Run{}, fmt.Errorf("%w: %s", ErrUnknownSkill, nonEmpty(sk.RetiredWhy, "умение выключено"))
	}

	target := ""
	if sk.NeedsTarget {
		abs, err := filepath.Abs(strings.TrimSpace(req.Target))
		if err != nil || strings.TrimSpace(req.Target) == "" {
			return Run{}, fmt.Errorf("умению %q нужен каталог", sk.Title)
		}
		if resolved, rErr := filepath.EvalSymlinks(abs); rErr == nil {
			abs = resolved
		}
		if s.policy != nil {
			if err := s.policy.AllowWorkspace(abs); err != nil {
				return Run{}, err
			}
		}
		target = abs
	}

	run := Run{
		ID: ids.New(ids.SkillRun), SkillID: sk.ID, SkillTitle: sk.Title,
		Target: target, ThreadID: req.ThreadID, ConversationID: req.ConversationID,
		StartedAt: s.clock.Now(),
	}

	failed := 0
	for _, st := range sk.Steps {
		res := s.runStep(ctx, st, target)
		if res.Failure != "" {
			failed++
		}
		run.Steps = append(run.Steps, res)
	}

	run.FinishedAt = s.clock.Now()
	run.TookMS = run.FinishedAt.Sub(run.StartedAt).Milliseconds()
	switch {
	case failed == len(sk.Steps):
		run.Status = StatusFailed
		run.Failure = "ни один шаг не удался"
		run.Answer = "Посмотреть не вышло: " + run.Steps[0].Failure
	default:
		run.Status = StatusDone
		run.Answer = answer(sk, run.Steps)
	}

	if _, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: run.ID, ExpectedRevision: 0,
			EventType: EvRunCompleted, Actor: event.Actor{Type: event.ActorBarrymore},
			CorrelationID: run.ThreadID, Payload: run,
		}); err != nil {
			return err
		}
		return applyRun(ctx, tx, run)
	}); err != nil {
		return Run{}, err
	}

	s.mu.RLock()
	w := s.watcher
	s.mu.RUnlock()
	if w != nil {
		w.SkillApplied(ctx, run)
	}
	return run, nil
}

// Request — просьба применить умение.
type Request struct {
	SkillID        string `json:"skill_id"`
	Target         string `json:"target,omitempty"`
	ThreadID       string `json:"thread_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

func (s *Service) runStep(ctx context.Context, st Step, target string) StepResult {
	s.mu.RLock()
	p, ok := s.prims[st.Primitive]
	s.mu.RUnlock()
	if !ok {
		return StepResult{Primitive: st.Primitive,
			Failure: "примитива " + st.Primitive + " не существует"}
	}

	args, err := resolveArgs(p, st, target)
	if err != nil {
		return StepResult{Primitive: p.ID, Title: p.Title, Failure: err.Error()}
	}

	stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	obs, err := p.Run(stepCtx, args)
	if err != nil {
		return StepResult{Primitive: p.ID, Title: p.Title, Failure: err.Error()}
	}
	return StepResult{Primitive: p.ID, Title: p.Title, Facts: obs.Facts, Signals: obs.Signals}
}

// checkStep проверяет шаг до того, как умение попадёт в список.
func (s *Service) checkStep(st Step) error {
	s.mu.RLock()
	p, ok := s.prims[st.Primitive]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("примитива %q не существует", st.Primitive)
	}
	for _, a := range p.Args {
		v, given := st.Args[a.Name]
		if !given || strings.TrimSpace(v) == "" {
			if a.Kind == ArgCount {
				continue
			}
			return fmt.Errorf("примитиву %s нужен аргумент %q (%s)", p.ID, a.Name, a.Why)
		}
		if v == Target {
			continue
		}
		if a.Kind == ArgCount {
			if _, err := strconv.Atoi(v); err != nil {
				return fmt.Errorf("аргумент %q должен быть числом", a.Name)
			}
		}
	}
	for name := range st.Args {
		if !declared(p, name) {
			return fmt.Errorf("примитив %s не принимает аргумент %q", p.ID, name)
		}
	}
	return nil
}

func declared(p Primitive, name string) bool {
	for _, a := range p.Args {
		if a.Name == name {
			return true
		}
	}
	return false
}

// resolveArgs подставляет каталог и проверяет остальное.
//
// Подставляется ровно одно значение — каталог применения. Всё прочее задано
// самим умением и не приходит снаружи: аргумент, который можно продиктовать,
// рано или поздно диктует не владелец.
func resolveArgs(p Primitive, st Step, target string) (Args, error) {
	out := Args{}
	for _, a := range p.Args {
		v := st.Args[a.Name]
		if v == Target {
			if target == "" {
				return nil, fmt.Errorf("шагу нужен каталог, а он не задан")
			}
			v = target
		}
		switch a.Kind {
		case ArgPath:
			if v == "" {
				return nil, fmt.Errorf("шагу нужен каталог")
			}
			if !filepath.IsAbs(v) {
				return nil, fmt.Errorf("каталог должен быть абсолютным путём")
			}
		case ArgCount:
			if v == "" {
				continue
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 100 {
				return nil, fmt.Errorf("число %q вне разумных пределов", v)
			}
		}
		out[a.Name] = v
	}
	return out, nil
}

func answer(sk Skill, steps []StepResult) string {
	if sk.Summarize != nil {
		if a := strings.TrimSpace(sk.Summarize(steps)); a != "" {
			return a
		}
	}
	var facts []string
	for _, st := range steps {
		for _, f := range st.Facts {
			facts = append(facts, f.Text)
		}
	}
	if len(facts) == 0 {
		return "Посмотрел, но сказать нечего."
	}
	return strings.Join(facts, "; ") + "."
}

// Report превращает применение умения в текст для разговора.
//
// Первая строка — ответ, дальше наблюдения. Владелец должен видеть не только
// вывод, но и то, из чего он сделан: иначе «похоже, нашёл» ничем не лучше
// «готово» в отчёте исполнителя.
func (r Run) Report() string {
	var b strings.Builder
	b.WriteString("Посмотрел сам. ")
	b.WriteString(r.Answer)
	for _, st := range r.Steps {
		if st.Failure != "" {
			fmt.Fprintf(&b, "\n· %s: не вышло — %s", st.Title, st.Failure)
			continue
		}
		for _, f := range st.Facts {
			fmt.Fprintf(&b, "\n· %s", f.Text)
		}
	}
	return b.String()
}

// Projections подключает проекторы событий умений.
func (s *Service) Projections(reg *projection.Registry) {
	reg.On(EvRunCompleted, projectRun)
	reg.On(EvSkillLearned, projectLearned)
	reg.On(EvSkillRetired, projectRetired)
	reg.Tables("skill_runs", "skills")
}

// Restore поднимает освоенные умения из проекции при старте.
func (s *Service) Restore(ctx context.Context) error {
	learned, err := s.storedSkills(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	known := map[string]int{}
	for i, sk := range s.skills {
		known[sk.ID] = i
	}
	for _, sk := range learned {
		if i, dup := known[sk.ID]; dup {
			// Встроенное умение могло быть снято владельцем: причина живёт
			// в проекции и должна пережить перезапуск.
			s.skills[i].RetiredWhy = sk.RetiredWhy
			s.skills[i].Enabled = sk.Enabled
			continue
		}
		s.skills = append(s.skills, sk)
	}
	return nil
}
