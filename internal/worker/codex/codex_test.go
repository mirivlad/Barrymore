package codex_test

import (
	"testing"
	"time"

	"github.com/mirivlad/barrymore/internal/worker"
	"github.com/mirivlad/barrymore/internal/worker/codex"
)

// Строки взяты из настоящего вывода codex 0.146.0 на этом хосте.
// Схема принадлежит внешнему инструменту и может измениться, поэтому разбор
// проверяется на зафиксированных образцах, а не на догадках о формате.
const (
	lineThreadStarted = `{"type":"thread.started","thread_id":"019fcd80-c318-7e43-98e4-fc04505c487f"}`
	lineTurnStarted   = `{"type":"turn.started"}`
	lineForbidden     = `{"type":"error","message":"Reconnecting... 2/5 (unexpected status 403 Forbidden: 19e8\r\n<html>, url: wss://chatgpt.com/backend-api/codex/responses, cf-ray: a25ec5babe2ef10f-DME)"}`
	lineUsageLimit    = `{"type":"error","message":"You've hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again at Aug 8th, 2026 10:30 PM."}`
	lineTurnFailed    = `{"type":"turn.failed","error":{"message":"You've hit your usage limit."}}`
	lineAgentMessage  = `{"type":"item","msg":{"type":"agent_message","message":"Отчёт готов"}}`
	lineCommandBegin  = `{"type":"item","msg":{"type":"exec_command_begin","command":["git","status"],"cwd":"/w"}}`
	lineCommandEnd    = `{"type":"item","msg":{"type":"exec_command_end","exit_code":0}}`
)

func adapter() *codex.Adapter {
	a := codex.New()
	a.Now = func() time.Time { return time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC) }
	return a
}

func TestParseLineClassifiesRealOutput(t *testing.T) {
	cases := []struct {
		name string
		line string
		kind string
	}{
		{"начало нити", lineThreadStarted, worker.RunEventAction},
		// codex сообщает о старте нити и хода — это наблюдаемые действия.
		{"начало хода", lineTurnStarted, worker.RunEventAction},
		{"сообщение агента", lineAgentMessage, worker.RunEventMessage},
		{"начало команды", lineCommandBegin, worker.RunEventCommand},
		{"конец команды", lineCommandEnd, worker.RunEventCommand},
		{"отказ провайдера", lineForbidden, worker.RunEventError},
		{"исчерпан лимит", lineUsageLimit, worker.RunEventError},
	}
	a := adapter()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := a.ParseLine([]byte(tc.line))
			if !ok {
				t.Fatalf("строка не разобрана")
			}
			if ev.Kind != tc.kind {
				t.Fatalf("вид события %q, ожидался %q (сводка: %q)", ev.Kind, tc.kind, ev.Summary)
			}
			if ev.Raw == "" {
				t.Fatal("исходная строка потеряна: разбор мог упустить смысл")
			}
		})
	}
}

func TestUsageLimitIsRecognisedAsQuotaExhausted(t *testing.T) {
	// Сценарий E: исчерпанная квота — это состояние учётной записи, а не
	// поломка инструмента, и Бэрримор обязан его различать.
	a := adapter()

	ev, _ := a.ParseLine([]byte(lineUsageLimit))
	if flag, _ := ev.Detail["quota_exhausted"].(bool); !flag {
		t.Fatalf("сообщение об исчерпанном лимите не распознано: %+v", ev.Detail)
	}
	msg, _ := ev.Detail["quota_message"].(string)
	if msg == "" {
		t.Fatal("текст сообщения провайдера потерян")
	}

	// Тот же смысл, но в другом месте схемы.
	nested, _ := a.ParseLine([]byte(lineTurnFailed))
	if flag, _ := nested.Detail["quota_exhausted"].(bool); !flag {
		t.Fatalf("вложенное сообщение об исчерпанном лимите не распознано: %+v", nested.Detail)
	}
}

func TestForbiddenIsRefusalNotQuota(t *testing.T) {
	// 403 снаружи неотличим от исчерпанного лимита и проблем с учётной записью.
	// Выдавать его за точную причину нельзя.
	a := adapter()
	ev, _ := a.ParseLine([]byte(lineForbidden))

	if flag, _ := ev.Detail["quota_exhausted"].(bool); flag {
		t.Fatal("отказ 403 выдан за исчерпанный лимит; точная причина снаружи не видна")
	}
	if flag, _ := ev.Detail["provider_refused"].(bool); !flag {
		t.Fatalf("отказ провайдера не распознан: %+v", ev.Detail)
	}
}

func TestPlanRefusesWithoutScratchDir(t *testing.T) {
	a := adapter()
	_, err := a.Plan(t.Context(), worker.Installation{ExecutablePath: "/usr/bin/codex"},
		worker.RunRequest{RunID: "run_1", WorkDir: "/tmp/ws"})
	if err == nil {
		t.Fatal("план построен без писчего каталога: запуск завершился бы отказом изоляции")
	}
}
