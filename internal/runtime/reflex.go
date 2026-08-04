package runtime

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ReflexInput — то, что реакция знает о ситуации.
type ReflexInput struct {
	Discrepancy Discrepancy
	AttemptNo   int
	Now         time.Time
}

// ReflexOutcome — результат попытки.
//
// Succeeded означает не «команда выполнилась», а «ожидаемый результат достигнут
// и проверен». Реакция без проверки результата запрещена (06_SECURITY §2.12).
type ReflexOutcome struct {
	Succeeded bool
	Detail    string
	// Observations — что реакция увидела. Записывается как обычные наблюдения,
	// поэтому следующая оценка ожидания увидит их наравне с остальными.
	Observations []ObservationRequest
	// Resolution заполняется, когда расхождение закрыто.
	Resolution string
}

// ReflexAction выполняет ограниченное локальное действие.
type ReflexAction func(ctx context.Context, in ReflexInput) (ReflexOutcome, error)

// ReflexPolicy — детерминированная реакция на класс расхождений.
//
// Свободная команда из текста модели сюда попасть не может: реакция существует
// только как заранее зарегистрированная функция с бюджетом и областью действия.
type ReflexPolicy struct {
	// ID участвует в ключе бюджета, поэтому переименование сбрасывает счётчик попыток.
	ID string
	// DiscrepancyKinds — какие классы расхождений обслуживает реакция.
	DiscrepancyKinds []string
	// MaxAttempts — предел попыток на одно расхождение. Ноль недопустим.
	MaxAttempts int
	// Cooldown — минимальный интервал между попытками.
	Cooldown time.Duration
	// ActionClass — класс действия для проверки политикой.
	ActionClass string
	// EscalateTo описывает, что происходит при исчерпании бюджета.
	EscalateTo string
	// Act — само действие.
	Act ReflexAction
}

// Reflexes — реестр реакций.
type Reflexes struct {
	byKind map[string][]*ReflexPolicy
	all    map[string]*ReflexPolicy
}

// NewReflexes создаёт пустой реестр.
func NewReflexes() *Reflexes {
	return &Reflexes{byKind: map[string][]*ReflexPolicy{}, all: map[string]*ReflexPolicy{}}
}

// Register добавляет политику.
func (r *Reflexes) Register(p *ReflexPolicy) error {
	if p.ID == "" {
		return fmt.Errorf("runtime: у reflex-политики нет идентификатора")
	}
	if _, dup := r.all[p.ID]; dup {
		return fmt.Errorf("runtime: reflex-политика %q уже зарегистрирована", p.ID)
	}
	if p.MaxAttempts <= 0 {
		return fmt.Errorf("runtime: reflex-политика %q без предела попыток", p.ID)
	}
	if p.Act == nil {
		return fmt.Errorf("runtime: reflex-политика %q без действия", p.ID)
	}
	if len(p.DiscrepancyKinds) == 0 {
		return fmt.Errorf("runtime: reflex-политика %q не привязана ни к одному классу расхождений", p.ID)
	}
	r.all[p.ID] = p
	for _, k := range p.DiscrepancyKinds {
		r.byKind[k] = append(r.byKind[k], p)
	}
	return nil
}

// MustRegister регистрирует политику или паникует. Для инициализации.
func (r *Reflexes) MustRegister(p *ReflexPolicy) {
	if err := r.Register(p); err != nil {
		panic(err)
	}
}

// For возвращает политики, обслуживающие класс расхождений.
func (r *Reflexes) For(kind string) []*ReflexPolicy { return r.byKind[kind] }

// Get возвращает политику по идентификатору.
func (r *Reflexes) Get(id string) (*ReflexPolicy, bool) {
	p, ok := r.all[id]
	return p, ok
}

// IDs перечисляет зарегистрированные политики.
func (r *Reflexes) IDs() []string {
	out := make([]string, 0, len(r.all))
	for id := range r.all {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// BudgetState описывает состояние бюджета попыток.
type BudgetState struct {
	Used          int
	Max           int
	LastAttemptAt *time.Time
}

// Exhausted сообщает, что попытки кончились.
func (b BudgetState) Exhausted() bool { return b.Used >= b.Max }

// CooldownUntil возвращает момент, раньше которого новая попытка недопустима.
func (b BudgetState) CooldownUntil(cooldown time.Duration) *time.Time {
	if b.LastAttemptAt == nil || cooldown <= 0 {
		return nil
	}
	t := b.LastAttemptAt.Add(cooldown)
	return &t
}
