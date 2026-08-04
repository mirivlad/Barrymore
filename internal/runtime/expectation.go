package runtime

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Виды ожиданий первого контура.
const (
	// KindRunStarts — процесс worker должен запуститься или дать диагностируемую ошибку.
	KindRunStarts = "worker_run.starts"
	// KindRunSignal — от запуска должен поступать наблюдаемый сигнал.
	// Отсутствие stdout само по себе зависанием не считается: сигналом является
	// любое наблюдение о запуске, включая подтверждение живости процесса.
	KindRunSignal = "worker_run.signal"
	// KindRunNoWrites — audit-only запуск не должен изменять workspace.
	KindRunNoWrites = "worker_run.no_writes"
	// KindRunReport — после завершения процесса должен быть собран обязательный отчёт.
	KindRunReport = "worker_run.report"
	// KindSnapshotFresh — снимок доступности должен быть свежим к моменту решения.
	KindSnapshotFresh = "snapshot.fresh"
	// KindRunCostPolicy — на бесплатной модели списаний быть не должно.
	//
	// Бесплатность определяется до запуска, по пометке провайдера в названии.
	// Это ожидание — страховка на случай, когда провайдер изменил условия:
	// появившееся списание означает нарушение договорённости, и работу
	// нужно прекращать, а не пересчитывать цену задним числом.
	KindRunCostPolicy = "worker_run.cost_policy"
	// KindLocalModelServing — локальный сервер модели должен обслуживать запросы.
	//
	// Это стоячее ожидание: оно не закрывается «выполненным», пока модель
	// нужна. Пока она отвечает или грузится, ожидание остаётся ожидающим;
	// исчезновение сервера сразу становится расхождением.
	KindLocalModelServing = "local_model.serving"
)

// Evaluator оценивает ожидание одного вида.
//
// Функция обязана быть чистой: те же вход и время дают тот же вердикт.
// Все побочные эффекты выполняет вызывающий код.
type Evaluator func(exp Expectation, obs []Observation, now time.Time) (Verdict, error)

// Kinds — реестр видов ожиданий.
//
// Ожидание неизвестного вида не оценивается «как-нибудь»: это ошибка,
// потому что молчаливый пропуск означает потерю контроля над запуском.
type Kinds struct{ m map[string]Evaluator }

// NewKinds создаёт реестр со стандартными видами.
func NewKinds() *Kinds {
	k := &Kinds{m: map[string]Evaluator{}}
	k.Register(KindRunStarts, evalRunStarts)
	k.Register(KindRunSignal, evalRunSignal)
	k.Register(KindRunNoWrites, evalRunNoWrites)
	k.Register(KindRunReport, evalRunReport)
	k.Register(KindSnapshotFresh, evalSnapshotFresh)
	k.Register(KindRunCostPolicy, evalRunCostPolicy)
	k.Register(KindLocalModelServing, evalLocalModelServing)
	return k
}

// Register добавляет вид ожидания.
func (k *Kinds) Register(kind string, fn Evaluator) {
	if _, dup := k.m[kind]; dup {
		panic("runtime: повторная регистрация вида ожидания " + kind)
	}
	k.m[kind] = fn
}

// Names перечисляет зарегистрированные виды.
func (k *Kinds) Names() []string {
	out := make([]string, 0, len(k.m))
	for n := range k.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Evaluate вычисляет вердикт по ожиданию.
func (k *Kinds) Evaluate(exp Expectation, obs []Observation, now time.Time) (Verdict, error) {
	fn, ok := k.m[exp.Kind]
	if !ok {
		return Verdict{}, fmt.Errorf("runtime: неизвестный вид ожидания %q (ожидание %s)", exp.Kind, exp.ID)
	}
	// Окно закрылось — ожидание больше не проверяется, чем бы ни закончилось.
	if exp.WindowUntil != nil && !now.Before(*exp.WindowUntil) {
		v, err := fn(exp, obs, now)
		if err != nil {
			return Verdict{}, err
		}
		if v.Outcome == OutcomeSatisfied {
			return v, nil
		}
		v.Outcome = OutcomeExpired
		if v.Expected == "" {
			v.Expected = "ожидание должно было разрешиться до " + exp.WindowUntil.Format(time.RFC3339)
		}
		return v, nil
	}
	return fn(exp, obs, now)
}

// ParamsRunStarts — параметры ожидания запуска.
type ParamsRunStarts struct {
	// StartDeadline — сколько ждать появления процесса.
	StartDeadline time.Duration `json:"start_deadline"`
}

func evalRunStarts(exp Expectation, obs []Observation, now time.Time) (Verdict, error) {
	var p ParamsRunStarts
	if err := exp.DecodeParams(&p); err != nil {
		return Verdict{}, fmt.Errorf("%s: разбор параметров: %w", exp.ID, err)
	}
	if p.StartDeadline <= 0 {
		p.StartDeadline = 30 * time.Second
	}
	if latest(obs, ObsRunStarted) != nil {
		return Verdict{Outcome: OutcomeSatisfied, Expected: "процесс запущен", Observed: "процесс запущен"}, nil
	}
	// Завершение без старта — тоже диагностируемый исход, а не «ожидание не сбылось».
	if o := latest(obs, ObsRunExited); o != nil {
		return Verdict{
			Outcome:  OutcomeMissed,
			Expected: "процесс запущен",
			Observed: "процесс завершился, не сообщив о запуске",
			Severity: SeverityCritical,
		}, nil
	}
	deadline := exp.WindowFrom.Add(p.StartDeadline)
	if now.Before(deadline) {
		return Verdict{Outcome: OutcomePending, NextCheckAt: ptr(deadline)}, nil
	}
	return Verdict{
		Outcome:  OutcomeMissed,
		Expected: fmt.Sprintf("процесс запускается в течение %s", p.StartDeadline),
		Observed: "наблюдений о запуске нет",
	}, nil
}

// ParamsRunSignal — параметры ожидания сигнала от запуска.
type ParamsRunSignal struct {
	// MaxSilence — допустимая тишина. Зависит от режима worker и текущего действия.
	MaxSilence time.Duration `json:"max_silence"`
	// SignalKinds — какие наблюдения считаются признаком жизни.
	SignalKinds []string `json:"signal_kinds,omitempty"`
}

func evalRunSignal(exp Expectation, obs []Observation, now time.Time) (Verdict, error) {
	var p ParamsRunSignal
	if err := exp.DecodeParams(&p); err != nil {
		return Verdict{}, fmt.Errorf("%s: разбор параметров: %w", exp.ID, err)
	}
	if p.MaxSilence <= 0 {
		p.MaxSilence = 2 * time.Minute
	}
	kinds := p.SignalKinds
	if len(kinds) == 0 {
		kinds = []string{ObsRunEvent, ObsRunHeartbeat, ObsRunStarted, ObsProcessLiveness, ObsRunAttached}
	}

	// Завершившийся процесс молчит законно: ожидание сигнала снимается.
	if o := latest(obs, ObsRunExited); o != nil {
		return Verdict{
			Outcome:  OutcomeSatisfied,
			Expected: "запуск подаёт признаки жизни",
			Observed: "процесс завершился, ожидание сигнала более не применимо",
		}, nil
	}

	last := exp.WindowFrom
	var lastKind string
	for _, k := range kinds {
		if o := latest(obs, k); o != nil && o.ObservedAt.After(last) {
			last = o.ObservedAt
			lastKind = o.Kind
		}
	}
	silence := now.Sub(last)
	if silence < p.MaxSilence {
		return Verdict{
			Outcome:     OutcomePending,
			Observed:    lastKind,
			NextCheckAt: ptr(last.Add(p.MaxSilence)),
		}, nil
	}
	return Verdict{
		Outcome:  OutcomeMissed,
		Expected: fmt.Sprintf("сигнал не реже чем раз в %s", p.MaxSilence),
		Observed: fmt.Sprintf("тишина %s, последний сигнал: %s", silence.Round(time.Second), signalLabel(lastKind)),
		// Тишина сама по себе не критична: сначала нужен probe, а не тревога.
		Severity:  SeverityWarning,
		DedupeKey: "run_silence:" + exp.SubjectID,
	}, nil
}

func signalLabel(kind string) string {
	if kind == "" {
		return "отсутствует"
	}
	return kind
}

// ParamsRunNoWrites — параметры ожидания неизменности workspace.
type ParamsRunNoWrites struct {
	// BaselineDigest — слепок состояния workspace до запуска.
	BaselineDigest string `json:"baseline_digest"`
}

// WorkspaceScanPayload — результат сканирования workspace.
type WorkspaceScanPayload struct {
	Digest       string   `json:"digest"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	GitStatus    string   `json:"git_status,omitempty"`
}

func evalRunNoWrites(exp Expectation, obs []Observation, now time.Time) (Verdict, error) {
	var p ParamsRunNoWrites
	if err := exp.DecodeParams(&p); err != nil {
		return Verdict{}, fmt.Errorf("%s: разбор параметров: %w", exp.ID, err)
	}
	scan := latest(obs, ObsWorkspaceScan)
	if scan == nil {
		// Отсутствие сканирования — не доказательство чистоты. Ожидание остаётся открытым.
		return Verdict{Outcome: OutcomePending, Expected: "workspace не изменяется"}, nil
	}
	var got WorkspaceScanPayload
	if err := scan.Decode(&got); err != nil {
		return Verdict{}, fmt.Errorf("%s: разбор наблюдения %s: %w", exp.ID, scan.ID, err)
	}
	if got.Digest == p.BaselineDigest {
		return Verdict{
			Outcome:  OutcomeSatisfied,
			Expected: "workspace не изменяется",
			Observed: "слепок workspace совпадает с исходным",
		}, nil
	}
	return Verdict{
		Outcome:  OutcomeMissed,
		Expected: "audit-only запуск не изменяет workspace",
		Observed: fmt.Sprintf("workspace изменён, затронуто путей: %d", len(got.ChangedPaths)),
		// Запись при audit-only означает пробой изоляции, а не рабочую заминку.
		Severity:  SeverityCritical,
		DedupeKey: "audit_write:" + exp.SubjectID,
	}, nil
}

// ParamsRunReport — параметры ожидания отчёта.
type ParamsRunReport struct {
	// RequiredArtifacts — имена артефактов, без которых результат не принимается.
	RequiredArtifacts []string `json:"required_artifacts"`
	// CollectDeadline — сколько ждать сбора после завершения процесса.
	CollectDeadline time.Duration `json:"collect_deadline"`
}

// ArtifactPayload — наблюдение о собранном артефакте.
type ArtifactPayload struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func evalRunReport(exp Expectation, obs []Observation, now time.Time) (Verdict, error) {
	var p ParamsRunReport
	if err := exp.DecodeParams(&p); err != nil {
		return Verdict{}, fmt.Errorf("%s: разбор параметров: %w", exp.ID, err)
	}
	if p.CollectDeadline <= 0 {
		p.CollectDeadline = time.Minute
	}
	exited := latest(obs, ObsRunExited)
	if exited == nil {
		return Verdict{Outcome: OutcomePending, Expected: "после завершения появится отчёт"}, nil
	}

	collected := map[string]bool{}
	for _, o := range obs {
		if o.Kind != ObsArtifactCollected {
			continue
		}
		var a ArtifactPayload
		if err := o.Decode(&a); err != nil {
			return Verdict{}, fmt.Errorf("%s: разбор наблюдения %s: %w", exp.ID, o.ID, err)
		}
		collected[a.Name] = true
	}
	var missing []string
	for _, name := range p.RequiredArtifacts {
		if !collected[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return Verdict{
			Outcome:  OutcomeSatisfied,
			Expected: "обязательные артефакты собраны",
			Observed: fmt.Sprintf("собрано артефактов: %d", len(collected)),
		}, nil
	}

	deadline := exited.ObservedAt.Add(p.CollectDeadline)
	if now.Before(deadline) {
		return Verdict{Outcome: OutcomePending, NextCheckAt: ptr(deadline)}, nil
	}
	return Verdict{
		Outcome:   OutcomeMissed,
		Expected:  fmt.Sprintf("обязательные артефакты: %v", p.RequiredArtifacts),
		Observed:  fmt.Sprintf("не собраны: %v", missing),
		Severity:  SeverityCritical,
		DedupeKey: "missing_report:" + exp.SubjectID,
	}, nil
}

// ParamsSnapshotFresh — параметры ожидания свежести снимка.
type ParamsSnapshotFresh struct {
	Scope string `json:"scope"`
}

// SnapshotFreshnessPayload — наблюдение о свежести снимка.
type SnapshotFreshnessPayload struct {
	Scope      string     `json:"scope"`
	ObservedAt time.Time  `json:"observed_at"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
}

func evalSnapshotFresh(exp Expectation, obs []Observation, now time.Time) (Verdict, error) {
	var p ParamsSnapshotFresh
	if err := exp.DecodeParams(&p); err != nil {
		return Verdict{}, fmt.Errorf("%s: разбор параметров: %w", exp.ID, err)
	}
	o := latest(obs, ObsProbeResult)
	if o == nil {
		return Verdict{
			Outcome:   OutcomeMissed,
			Expected:  "снимок " + p.Scope + " свежий",
			Observed:  "свежих наблюдений нет",
			Severity:  SeverityWarning,
			DedupeKey: "stale_snapshot:" + p.Scope,
		}, nil
	}
	var got SnapshotFreshnessPayload
	if err := o.Decode(&got); err != nil {
		return Verdict{}, fmt.Errorf("%s: разбор наблюдения %s: %w", exp.ID, o.ID, err)
	}
	if got.ValidUntil != nil && !now.Before(*got.ValidUntil) {
		return Verdict{
			Outcome:   OutcomeMissed,
			Expected:  "снимок " + p.Scope + " свежий",
			Observed:  "срок действия снимка истёк " + got.ValidUntil.Format(time.RFC3339),
			Severity:  SeverityWarning,
			DedupeKey: "stale_snapshot:" + p.Scope,
		}, nil
	}
	v := Verdict{Outcome: OutcomeSatisfied, Expected: "снимок свежий", Observed: "снимок действителен"}
	if got.ValidUntil != nil {
		v.NextCheckAt = got.ValidUntil
	}
	return v, nil
}

// ParamsRunCostPolicy — параметры ожидания по стоимости.
type ParamsRunCostPolicy struct {
	// MaxCost — предел суммарного списания. Ноль означает «списаний быть не должно».
	MaxCost float64 `json:"max_cost"`
	// Model — модель, которой разрешён запуск.
	Model string `json:"model,omitempty"`
	// PolicyName объясняет, откуда взялся предел.
	PolicyName string `json:"policy_name,omitempty"`
}

// evalRunCostPolicy ловит списание там, где его быть не должно.
func evalRunCostPolicy(exp Expectation, obs []Observation, now time.Time) (Verdict, error) {
	var p ParamsRunCostPolicy
	if err := exp.DecodeParams(&p); err != nil {
		return Verdict{}, fmt.Errorf("%s: разбор параметров: %w", exp.ID, err)
	}

	total, seen := observedCost(obs)
	if total > p.MaxCost {
		return Verdict{
			Outcome: OutcomeMissed,
			Expected: fmt.Sprintf("списание не превышает %.6f (%s, модель %s)",
				p.MaxCost, p.PolicyName, p.Model),
			Observed: fmt.Sprintf(
				"исполнитель сообщил о списании %.6f: модель, выбранная как бесплатная, "+
					"больше таковой не является", total),
			Severity:  SeverityCritical,
			DedupeKey: "cost_violation:" + exp.SubjectID,
		}, nil
	}

	// Завершившийся запуск в пределах бюджета закрывает ожидание.
	if latest(obs, ObsRunExited) != nil {
		observedNote := "списаний не наблюдалось"
		if seen {
			observedNote = fmt.Sprintf("суммарное списание %.6f", total)
		}
		return Verdict{
			Outcome:  OutcomeSatisfied,
			Expected: fmt.Sprintf("списание не превышает %.6f", p.MaxCost),
			Observed: observedNote,
		}, nil
	}
	return Verdict{Outcome: OutcomePending}, nil
}

// observedCost суммирует стоимость, сообщённую исполнителем.
func observedCost(obs []Observation) (total float64, seen bool) {
	for _, o := range obs {
		if o.Kind != ObsRunEvent {
			continue
		}
		var ev struct {
			Detail map[string]any `json:"detail"`
		}
		if err := o.Decode(&ev); err != nil {
			continue
		}
		v, ok := ev.Detail["observed_cost"]
		if !ok {
			continue
		}
		cost, ok := v.(float64)
		if !ok {
			continue
		}
		seen = true
		total += cost
	}
	return total, seen
}

// ParamsLocalModelServing — параметры стоячего ожидания по локальной модели.
type ParamsLocalModelServing struct {
	// Endpoint — адрес, на котором модель должна отвечать.
	Endpoint string `json:"endpoint"`
	// CheckEvery — как часто пересматривать ожидание.
	CheckEvery time.Duration `json:"check_every"`
	// SilenceAfter — через сколько отсутствие свежих наблюдений само становится
	// расхождением. Молчание наблюдателя — это не «всё хорошо»: не зная
	// состояния, утверждать исправность нельзя.
	SilenceAfter time.Duration `json:"silence_after"`
}

// evalLocalModelServing проверяет, что локальная модель на месте.
//
// Загрузка не считается отказом: большая модель поднимается минутами, и
// перезапускать её в это время означало бы не дать ей запуститься никогда.
func evalLocalModelServing(exp Expectation, obs []Observation, now time.Time) (Verdict, error) {
	var p ParamsLocalModelServing
	if err := exp.DecodeParams(&p); err != nil {
		return Verdict{}, fmt.Errorf("%s: разбор параметров: %w", exp.ID, err)
	}
	checkEvery := p.CheckEvery
	if checkEvery <= 0 {
		checkEvery = 30 * time.Second
	}

	o := latest(obs, ObsLocalModel)
	if o == nil {
		// Наблюдений ещё не было. Это не отказ: наблюдатель мог не успеть.
		return Verdict{Outcome: OutcomePending, NextCheckAt: ptr(now.Add(checkEvery))}, nil
	}

	var got LocalModelStatePayload
	if err := o.Decode(&got); err != nil {
		return Verdict{}, fmt.Errorf("%s: разбор наблюдения %s: %w", exp.ID, o.ID, err)
	}

	if p.SilenceAfter > 0 && now.Sub(o.ObservedAt) > p.SilenceAfter {
		return Verdict{
			Outcome:  OutcomeMissed,
			Expected: "состояние модели на " + p.Endpoint + " наблюдается регулярно",
			Observed: fmt.Sprintf("последнее наблюдение %s назад: состояние модели неизвестно",
				now.Sub(o.ObservedAt).Round(time.Second)),
			Severity:  SeverityWarning,
			DedupeKey: "local_model_unobserved:" + exp.SubjectID,
		}, nil
	}

	switch {
	case got.Serving:
		return Verdict{Outcome: OutcomePending, NextCheckAt: ptr(now.Add(checkEvery))}, nil
	case got.Loading:
		return Verdict{Outcome: OutcomePending, NextCheckAt: ptr(now.Add(checkEvery))}, nil
	default:
		observed := got.Reason
		if observed == "" {
			observed = "сервер не отвечает"
		}
		return Verdict{
			Outcome:   OutcomeMissed,
			Expected:  "локальная модель отвечает на " + p.Endpoint,
			Observed:  observed,
			Severity:  SeverityWarning,
			DedupeKey: "local_model_down:" + exp.SubjectID,
		}, nil
	}
}

// latest возвращает последнее по времени наблюдение указанного вида.
func latest(obs []Observation, kind string) *Observation {
	var found *Observation
	for i := range obs {
		if obs[i].Kind != kind {
			continue
		}
		if found == nil || obs[i].ObservedAt.After(found.ObservedAt) {
			found = &obs[i]
		}
	}
	return found
}

func ptr[T any](v T) *T { return &v }

// MarshalParams упаковывает параметры ожидания.
func MarshalParams(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("{}"), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("runtime: сериализация параметров ожидания: %w", err)
	}
	return b, nil
}
