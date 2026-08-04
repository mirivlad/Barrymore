package event

import (
	"context"
	"sync"

	"github.com/mirivlad/barrymore/internal/store"
)

// Broker раздаёт события подписчикам.
//
// ADR 0010: подписка начинается с указанного seq. Сначала подписчик получает
// пропущенное из журнала, затем живой поток. Поэтому разрыв соединения не
// оставляет дыры в истории.
type Broker struct {
	db *store.DB

	mu   sync.RWMutex
	subs map[int]chan Envelope
	next int
}

// NewBroker создаёт шину.
func NewBroker(db *store.DB) *Broker {
	return &Broker{db: db, subs: map[int]chan Envelope{}}
}

func (b *Broker) publish(env Envelope) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- env:
		default:
			// Медленный подписчик не должен тормозить запись. Он обнаружит разрыв
			// по скачку seq и переподключится с Last-Event-ID.
		}
	}
}

// Subscription — живой поток событий.
type Subscription struct {
	C      <-chan Envelope
	broker *Broker
	id     int
	once   sync.Once
}

// Close отписывает подписчика.
func (s *Subscription) Close() {
	s.once.Do(func() {
		s.broker.mu.Lock()
		ch, ok := s.broker.subs[s.id]
		delete(s.broker.subs, s.id)
		s.broker.mu.Unlock()
		if ok {
			close(ch)
		}
	})
}

// Subscribe возвращает подписку на события с seq строго больше fromSeq.
//
// Порядок операций важен: сначала регистрируется живой канал, потом читается
// история. Событие, записанное между двумя шагами, попадёт в живой канал и будет
// отброшено дедупликацией по seq, но не потеряется.
func (b *Broker) Subscribe(ctx context.Context, j *Journal, fromSeq int64, buffer int) (*Subscription, []Envelope, error) {
	if buffer <= 0 {
		buffer = 256
	}
	ch := make(chan Envelope, buffer)

	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()

	sub := &Subscription{C: ch, broker: b, id: id}

	backlog, err := j.Since(ctx, fromSeq, 1000)
	if err != nil {
		sub.Close()
		return nil, nil, err
	}
	return sub, backlog, nil
}

// SubscriberCount используется тестами и диагностикой.
func (b *Broker) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
