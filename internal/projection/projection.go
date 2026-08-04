// Package projection применяет события к таблицам чтения.
//
// ADR 0003: сервисы не пишут проекции напрямую. Сервис формирует событие,
// журнал его записывает, проектор применяет — всё в одной транзакции.
// Благодаря этому проекции полностью восстановимы из журнала, и это
// проверяется тестом, а не декларируется.
package projection

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/store"
)

// Applier применяет одно событие к проекциям.
type Applier func(ctx context.Context, tx *sql.Tx, env event.Envelope) error

// Ignore регистрирует событие как чисто аудиторское, без проекции.
func Ignore(context.Context, *sql.Tx, event.Envelope) error { return nil }

// Registry хранит соответствие «тип события → проектор».
type Registry struct {
	handlers map[string]Applier
	// tables перечислены в порядке создания; очистка идёт в обратном порядке,
	// чтобы не нарушать внешние ключи.
	tables []string
}

// NewRegistry создаёт пустой реестр.
func NewRegistry() *Registry {
	return &Registry{handlers: map[string]Applier{}}
}

// On регистрирует проектор для типа события.
func (r *Registry) On(eventType string, fn Applier) *Registry {
	if _, dup := r.handlers[eventType]; dup {
		panic("projection: повторная регистрация проектора для " + eventType)
	}
	r.handlers[eventType] = fn
	return r
}

// OnAudit помечает тип события как не влияющий на проекции.
func (r *Registry) OnAudit(eventTypes ...string) *Registry {
	for _, t := range eventTypes {
		r.On(t, Ignore)
	}
	return r
}

// Tables объявляет таблицы проекций, очищаемые при пересборке.
func (r *Registry) Tables(names ...string) *Registry {
	r.tables = append(r.tables, names...)
	return r
}

// Apply применяет событие.
//
// Незарегистрированный тип — ошибка, а не тихий пропуск: иначе забытый проектор
// превращается в расхождение между журналом и состоянием, которое обнаружится
// только после рестарта.
func (r *Registry) Apply(ctx context.Context, tx *sql.Tx, env event.Envelope) error {
	fn, ok := r.handlers[env.EventType]
	if !ok {
		return fmt.Errorf("projection: нет проектора для события %q (%s); "+
			"зарегистрируйте обработчик или пометьте тип через OnAudit", env.EventType, env.ID)
	}
	if err := fn(ctx, tx, env); err != nil {
		return fmt.Errorf("projection %s (%s): %w", env.EventType, env.ID, err)
	}
	return nil
}

// KnownTypes возвращает зарегистрированные типы событий.
func (r *Registry) KnownTypes() []string {
	out := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Rebuild очищает проекции и заново проигрывает журнал.
//
// Используется при восстановлении и в тестах инварианта «состояние выводится
// из журнала». Выполняется одной транзакцией: частично пересобранная база хуже,
// чем непересобранная.
func Rebuild(ctx context.Context, db *store.DB, j *event.Journal, r *Registry) error {
	events, err := j.All(ctx)
	if err != nil {
		return err
	}
	return db.Tx(ctx, func(tx *sql.Tx) error {
		for i := len(r.tables) - 1; i >= 0; i-- {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+quoteIdent(r.tables[i])); err != nil {
				return fmt.Errorf("projection: очистка %s: %w", r.tables[i], err)
			}
		}
		for _, env := range events {
			if err := r.Apply(ctx, tx, env); err != nil {
				return err
			}
		}
		return nil
	})
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
