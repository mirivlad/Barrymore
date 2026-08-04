package event_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

func newJournal(t *testing.T) *event.Journal {
	t.Helper()
	return event.NewJournal(testsupport.OpenDB(t), testsupport.Clock())
}

func appendOne(t *testing.T, j *event.Journal, req event.Request) event.Envelope {
	t.Helper()
	var got event.Envelope
	_, err := j.Write(context.Background(), func(tx *sql.Tx, w *event.TxWriter) error {
		var err error
		got, err = w.Append(context.Background(), req)
		return err
	})
	if err != nil {
		t.Fatalf("запись события %s: %v", req.EventType, err)
	}
	return got
}

func TestAppendAssignsMonotonicSeqAndRevision(t *testing.T) {
	j := newJournal(t)
	actor := event.Actor{Type: event.ActorPerson, ID: "person_owner"}

	first := appendOne(t, j, event.Request{
		StreamType: "thread", StreamID: "thr_1", ExpectedRevision: 0,
		EventType: "thread.created", Actor: actor,
		Payload: map[string]string{"title": "Первая нить"},
	})
	second := appendOne(t, j, event.Request{
		StreamType: "thread", StreamID: "thr_1", ExpectedRevision: 1,
		EventType: "thread.updated", Actor: actor,
		Payload: map[string]string{"title": "Первая нить, уточнённая"},
	})
	other := appendOne(t, j, event.Request{
		StreamType: "thread", StreamID: "thr_2", ExpectedRevision: 0,
		EventType: "thread.created", Actor: actor,
	})

	if first.StreamRevision != 1 || second.StreamRevision != 2 {
		t.Fatalf("ревизии потока: %d, %d", first.StreamRevision, second.StreamRevision)
	}
	if other.StreamRevision != 1 {
		t.Fatalf("новый поток должен начинаться с ревизии 1, получено %d", other.StreamRevision)
	}
	if !(first.Seq < second.Seq && second.Seq < other.Seq) {
		t.Fatalf("seq не монотонен: %d, %d, %d", first.Seq, second.Seq, other.Seq)
	}
}

func TestAppendRejectsStaleRevision(t *testing.T) {
	j := newJournal(t)
	ctx := context.Background()
	actor := event.Actor{Type: event.ActorPerson}

	appendOne(t, j, event.Request{
		StreamType: "thread", StreamID: "thr_1", ExpectedRevision: 0,
		EventType: "thread.created", Actor: actor,
	})

	_, err := j.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		_, err := w.Append(ctx, event.Request{
			StreamType: "thread", StreamID: "thr_1", ExpectedRevision: 0,
			EventType: "thread.updated", Actor: actor,
		})
		return err
	})
	if !errors.Is(err, event.ErrConcurrency) {
		t.Fatalf("ожидался ErrConcurrency, получено: %v", err)
	}

	// Отказ не должен оставлять следов.
	rev, err := j.Revision(ctx, "thread", "thr_1")
	if err != nil {
		t.Fatalf("чтение ревизии: %v", err)
	}
	if rev != 1 {
		t.Fatalf("ревизия изменилась после отказа: %d", rev)
	}
}

func TestIdempotencyKeyReturnsSameEvent(t *testing.T) {
	j := newJournal(t)
	actor := event.Actor{Type: event.ActorPerson}
	req := event.Request{
		StreamType: "thread", StreamID: "thr_1", ExpectedRevision: 0,
		EventType: "thread.created", Actor: actor, IdempotencyKey: "create-thr_1",
	}

	first := appendOne(t, j, req)
	// Повтор с той же ревизией: без идемпотентности это был бы конфликт.
	second := appendOne(t, j, req)

	if first.ID != second.ID || first.Seq != second.Seq {
		t.Fatalf("повторный запрос создал новое событие: %s/%d против %s/%d",
			first.ID, first.Seq, second.ID, second.Seq)
	}
	head, err := j.Head(context.Background())
	if err != nil {
		t.Fatalf("голова журнала: %v", err)
	}
	if head != first.Seq {
		t.Fatalf("в журнале появилось лишнее событие: голова %d", head)
	}
}

func TestFailedTransactionPublishesNothing(t *testing.T) {
	db := testsupport.OpenDB(t)
	j := event.NewJournal(db, testsupport.Clock())
	ctx := context.Background()

	sub, _, err := j.Broker().Subscribe(ctx, j, 0, 8)
	if err != nil {
		t.Fatalf("подписка: %v", err)
	}
	defer sub.Close()

	boom := errors.New("сбой после записи события")
	_, err = j.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
		if _, err := w.Append(ctx, event.Request{
			StreamType: "thread", StreamID: "thr_1", ExpectedRevision: 0,
			EventType: "thread.created", Actor: event.Actor{Type: event.ActorPerson},
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("ожидалась исходная ошибка, получено: %v", err)
	}

	select {
	case env := <-sub.C:
		t.Fatalf("подписчик получил событие незафиксированной транзакции: %s", env.EventType)
	default:
	}

	head, err := j.Head(ctx)
	if err != nil {
		t.Fatalf("голова журнала: %v", err)
	}
	if head != 0 {
		t.Fatalf("откатившаяся транзакция оставила события: голова %d", head)
	}
}

func TestSubscribeDeliversBacklogThenLive(t *testing.T) {
	db := testsupport.OpenDB(t)
	j := event.NewJournal(db, testsupport.Clock())
	ctx := context.Background()
	actor := event.Actor{Type: event.ActorPerson}

	appendOne(t, j, event.Request{
		StreamType: "thread", StreamID: "thr_1", ExpectedRevision: 0,
		EventType: "thread.created", Actor: actor,
	})

	sub, backlog, err := j.Broker().Subscribe(ctx, j, 0, 8)
	if err != nil {
		t.Fatalf("подписка: %v", err)
	}
	defer sub.Close()

	if len(backlog) != 1 || backlog[0].EventType != "thread.created" {
		t.Fatalf("ожидался backlog из одного события, получено %d", len(backlog))
	}

	appendOne(t, j, event.Request{
		StreamType: "thread", StreamID: "thr_1", ExpectedRevision: 1,
		EventType: "thread.updated", Actor: actor,
	})

	select {
	case env := <-sub.C:
		if env.EventType != "thread.updated" {
			t.Fatalf("в живом потоке получено %q", env.EventType)
		}
		if env.Seq <= backlog[0].Seq {
			t.Fatalf("seq живого события %d не больше backlog %d", env.Seq, backlog[0].Seq)
		}
	default:
		t.Fatal("живое событие не доставлено подписчику")
	}
}
