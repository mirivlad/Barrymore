package learning

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mirivlad/barrymore/internal/clock"
	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/projection"
	"github.com/mirivlad/barrymore/internal/skill"
	"github.com/mirivlad/barrymore/internal/store"
)

// SkillRetirer снимает умение с применения.
//
// Обучение не удаляет умение и не правит его тайком: единственное, что оно
// может, — сказать «этим способом я больше не пользуюсь» и назвать причину.
type SkillRetirer interface {
	Retire(ctx context.Context, id, why string, actor event.Actor) error
}

// RunSource отдаёт последние применения умений.
type RunSource interface {
	Runs(ctx context.Context, limit int) ([]skill.Run, error)
}

// Service ведёт практики.
type Service struct {
	db      *store.DB
	journal *event.Journal
	clock   clock.Clock
	log     *slog.Logger
	skills  SkillRetirer
	runs    RunSource
}

// Config — что нужно обучению.
type Config struct {
	DB      *store.DB
	Journal *event.Journal
	Clock   clock.Clock
	Skills  SkillRetirer
	Runs    RunSource
	Logger  *slog.Logger
}

// New создаёт службу обучения.
func New(cfg Config) *Service {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		db: cfg.DB, journal: cfg.Journal, clock: cfg.Clock,
		skills: cfg.Skills, runs: cfg.Runs, log: log,
	}
}

// SkillApplied принимает исход применения умения.
//
// Вызывается самим применением, а не отдельным обходом: опыт, который надо
// специально собирать, рано или поздно собирать перестают.
func (s *Service) SkillApplied(ctx context.Context, run skill.Run) {
	result := OutcomeGood
	note := run.Answer
	if run.Status != skill.StatusDone {
		result = OutcomeBad
		note = run.Failure
	}
	if err := s.Note(ctx, Outcome{
		Kind: KindOwn, Ref: run.SkillID, Title: run.SkillTitle,
		Result: result, Evidence: note, TookMS: run.TookMS,
		ThreadID: run.ThreadID, At: run.FinishedAt,
	}); err != nil {
		s.log.Error("исход применения умения не записан", "skill", run.SkillID, "error", err)
	}
}

// OrderFinished принимает исход поручения.
//
// Исполнитель — такой же способ работы, как собственное умение, и мерка
// у них одна: сколько раз применялся, сколько раз вышло, во что обошёлся.
func (s *Service) OrderFinished(ctx context.Context, workerID, title, outcome,
	evidence string, tookMS int64) {
	if err := s.Note(ctx, Outcome{
		Kind: KindDelegated, Ref: workerID, Title: nonEmpty(title, workerID),
		Result: outcome, Evidence: evidence, TookMS: tookMS,
	}); err != nil {
		s.log.Error("исход поручения не записан", "worker", workerID, "error", err)
	}
}

// Note записывает исход и обновляет практику.
func (s *Service) Note(ctx context.Context, o Outcome) error {
	if strings.TrimSpace(o.Ref) == "" {
		return fmt.Errorf("исход без способа: учиться не на чем")
	}
	if strings.TrimSpace(o.Evidence) == "" {
		// Практика без основания — мнение, а мнение не должно менять поведение.
		return fmt.Errorf("исход без основания: %s", o.Ref)
	}
	if o.At.IsZero() {
		o.At = s.clock.Now()
	}
	if o.Result == "" {
		o.Result = OutcomeGood
	}

	if _, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: key(o.Kind, o.Ref),
			ExpectedRevision: event.AnyRevision, EventType: EvOutcomeNoted,
			Actor: event.Actor{Type: event.ActorRuntime}, CorrelationID: o.ThreadID,
			Payload: o,
		}); err != nil {
			return err
		}
		return applyOutcome(ctx, tx, o)
	}); err != nil {
		return err
	}

	return s.reconsider(ctx, key(o.Kind, o.Ref))
}

// reconsider решает, не пора ли перестать пользоваться способом.
//
// Это и есть то место, где опыт меняет поведение. Без него практика осталась
// бы статистикой: цифры растут, а Бэрримор упорно делает то же самое.
func (s *Service) reconsider(ctx context.Context, id string) error {
	p, err := s.Practice(ctx, id)
	if err != nil || p.Stale || p.Streak < failureStreakLimit {
		return err
	}

	why := fmt.Sprintf("%d неудачи подряд, последняя — %s",
		p.Streak, nonEmpty(p.LastNote, "без объяснения"))
	stale := stalePayload{PracticeID: id, Why: why, At: s.clock.Now()}
	if _, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: StreamType, StreamID: id, ExpectedRevision: event.AnyRevision,
			EventType: EvPracticeStale, Actor: event.Actor{Type: event.ActorBarrymore},
			Payload: stale,
		}); err != nil {
			return err
		}
		return applyStale(ctx, tx, stale)
	}); err != nil {
		return err
	}

	// Умение снимается сразу: оставить его в списке значило бы предлагать
	// владельцу способ, который на его же работе трижды подряд не сработал.
	if p.Kind == KindOwn && s.skills != nil {
		if err := s.skills.Retire(ctx, p.Ref, why,
			event.Actor{Type: event.ActorBarrymore}); err != nil {
			s.log.Error("негодное умение не снято", "skill", p.Ref, "error", err)
		}
	}
	s.log.Warn("способ признан негодным", "practice", id, "why", why)
	return nil
}

// Suggest ищет повторяющийся порядок действий.
//
// Умение не выдумывается: предлагается имя для того, что владелец уже делал
// сам, и не однажды. Шаги берутся из тех же умений, значит они уже проверены —
// новых полномочий предложение не даёт.
func (s *Service) Suggest(ctx context.Context) ([]Suggestion, error) {
	if s.runs == nil {
		return nil, nil
	}
	runs, err := s.runs.Runs(ctx, 200)
	if err != nil {
		return nil, err
	}

	// Заход — несколько умений, применённых к одному каталогу подряд.
	// Разрыв больше получаса — уже другой разговор, а не один приём работы.
	const gap = 30 * time.Minute
	type session struct {
		order  []string
		titles map[string]string
		last   time.Time
	}
	sessions := map[string]*session{}
	var ordered []*session

	// Runs отдаёт свежие первыми; для порядка шагов нужен обратный ход.
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		if r.Status != skill.StatusDone || r.Target == "" {
			continue
		}
		// Заход кончается двумя способами: долгим перерывом или повтором уже
		// применённого умения. Второе и означает «начал сначала» — без него
		// повторение одного и того же порядка на одном каталоге слилось бы
		// в один длинный заход и осталось незамеченным.
		cur, ok := sessions[r.Target]
		started := ok && cur.titles[r.SkillID] != ""
		if !ok || started || r.StartedAt.Sub(cur.last) > gap {
			cur = &session{titles: map[string]string{}}
			sessions[r.Target] = cur
			ordered = append(ordered, cur)
		}
		if _, dup := cur.titles[r.SkillID]; !dup {
			cur.order = append(cur.order, r.SkillID)
		}
		cur.titles[r.SkillID] = r.SkillTitle
		cur.last = r.StartedAt
	}

	type group struct {
		order  []string
		titles []string
		times  int
	}
	groups := map[string]*group{}
	for _, sess := range ordered {
		if len(sess.order) < 2 {
			continue
		}
		sorted := append([]string(nil), sess.order...)
		sort.Strings(sorted)
		k := strings.Join(sorted, "+")
		g, ok := groups[k]
		if !ok {
			g = &group{order: sess.order}
			for _, id := range sess.order {
				g.titles = append(g.titles, sess.titles[id])
			}
			groups[k] = g
		}
		g.times++
	}

	out := []Suggestion{}
	for k, g := range groups {
		if g.times < 2 {
			continue
		}
		out = append(out, Suggestion{
			ID: "seq:" + k, Skills: g.order, Titles: g.titles, SeenTimes: g.times,
			Title:    strings.Join(g.titles, " и "),
			Question: "то, что вы уже " + plural(g.times) + " выясняли этим порядком",
			Why: fmt.Sprintf("этот порядок повторился %d раза; из него получится "+
				"одно умение вместо %d нажатий", g.times, len(g.order)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeenTimes > out[j].SeenTimes })
	return out, nil
}

// Compose собирает предложенное умение из шагов уже существующих.
//
// Собирается runtime, а не моделью: шаги берутся дословно из умений, которые
// уже есть. Владельцу остаётся согласиться или нет.
func Compose(sg Suggestion, known map[string]skill.Skill) (skill.Skill, error) {
	out := skill.Skill{
		ID: "learned." + strings.NewReplacer("+", ".", ":", ".").Replace(
			strings.TrimPrefix(sg.ID, "seq:")),
		Title: sg.Title, Question: sg.Question, Origin: skill.OriginLearned,
		Enabled: true,
	}
	for _, id := range sg.Skills {
		src, ok := known[id]
		if !ok {
			return skill.Skill{}, fmt.Errorf("умения %q больше нет", id)
		}
		if src.NeedsTarget {
			out.NeedsTarget = true
		}
		out.Steps = append(out.Steps, src.Steps...)
	}
	if len(out.Steps) == 0 {
		return skill.Skill{}, fmt.Errorf("складывать нечего")
	}
	if len(out.ID) > 80 {
		out.ID = out.ID[:80]
	}
	return out, nil
}

// Projections подключает проекторы обучения.
func (s *Service) Projections(reg *projection.Registry) {
	reg.On(EvOutcomeNoted, projectOutcome)
	reg.On(EvPracticeStale, projectStale)
	reg.Tables("practices")
}

func key(kind, ref string) string { return kind + ":" + ref }

func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func itoa(n int) string { return strconv.Itoa(n) }

func plural(n int) string {
	switch {
	case n%10 == 1 && n%100 != 11:
		return itoa(n) + " раз"
	default:
		return itoa(n) + " раза"
	}
}

func duration(ms int64) string {
	if ms < 1000 {
		return itoa(int(ms)) + " мс"
	}
	return fmt.Sprintf("%.1f с", float64(ms)/1000)
}

func outcomeWord(v string) string {
	if v == OutcomeBad {
		return "неудачно"
	}
	return "удачно"
}
