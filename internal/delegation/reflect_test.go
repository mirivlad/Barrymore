package delegation_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/delegation"
)

// После поручения владелец не должен переносить результат в нить руками:
// система знает и то, и другое.
func TestFinishedOrderUpdatesThreadState(t *testing.T) {
	script := `
		cat > "$OUT_DIR/last-message.txt" <<'REPORT'
{"summary":"репозиторий состоит из одного файла","findings":[],"limitations":"только чтение"}
REPORT
		echo '{"summary":"закончил"}'`
	h := newHarness(t, &fakeAdapter{script: script, model: freeModel()})

	p := h.propose(t)
	h.approveAndStart(t, p)

	ctx := context.Background()
	waitFor(t, "поручение завершилось", func() bool {
		o, err := h.deleg.Get(ctx, p.Order.ID)
		return err == nil &&
			(o.State == delegation.StateCompleted || o.State == delegation.StateFailed)
	})

	order, err := h.deleg.Get(ctx, p.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if order.State != delegation.StateCompleted {
		t.Fatalf("поручение %s: %s", order.State, order.FailureReason)
	}

	th, err := h.threads.Get(ctx, order.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if th.Canon.Situation == "" {
		t.Fatal("нить не узнала об итоге поручения")
	}
	if !strings.Contains(th.Canon.Situation, order.Title) {
		t.Fatalf("в нити не сказано, о каком поручении речь: %q", th.Canon.Situation)
	}
	// Проверки — факт; «готово» из отчёта исполнителя фактом не является
	// и в каноническое состояние нити попасть не может.
	if !strings.Contains(th.Canon.Situation, "проверок пройдено") {
		t.Fatalf("итог не опирается на проверки: %q", th.Canon.Situation)
	}
	if strings.Contains(th.Canon.Situation, "репозиторий состоит") {
		t.Fatalf("в нить попал непроверенный текст отчёта: %q", th.Canon.Situation)
	}
	if th.Canon.NextStep == "" {
		t.Fatal("не сказано, какой шаг следующий")
	}
	if len(th.Canon.Waiting) != 0 {
		t.Fatalf("нить всё ещё чего-то ждёт после завершения: %v", th.Canon.Waiting)
	}
	if th.Canon.Source != "поручение" {
		t.Fatalf("источник записи %q: владелец должен видеть, откуда она", th.Canon.Source)
	}
}

// Пока исполнитель работает, нить честно говорит, чего ждёт.
func TestRunningOrderMarksThreadAsWaiting(t *testing.T) {
	// Исполнитель молчит достаточно долго, чтобы состояние успело считаться.
	h := newHarness(t, &fakeAdapter{script: `sleep 5`, model: freeModel()})

	p := h.propose(t)
	h.approveAndStart(t, p)

	ctx := context.Background()
	th, err := h.threads.Get(ctx, p.Order.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Canon.Waiting) != 1 {
		t.Fatalf("нить не отмечена как ожидающая: %+v", th.Canon)
	}
	if !strings.Contains(th.Canon.Waiting[0], p.Order.Title) {
		t.Fatalf("не сказано, чего именно ждём: %q", th.Canon.Waiting[0])
	}
}
