package research_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mirivlad/barrymore/internal/model"
	"github.com/mirivlad/barrymore/internal/research"
	"github.com/mirivlad/barrymore/internal/testsupport"
)

type fakeProvider struct{ status model.Status }

func (p *fakeProvider) ID() string       { return "fake" }
func (p *fakeProvider) Describe() string { return "Ornith local" }
func (p *fakeProvider) Probe(context.Context) model.Status { return p.status }
func (p *fakeProvider) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}

func TestProviderInspectorIsRealtimeDirectEvidence(t *testing.T) {
	ctx := context.Background()
	clk := testsupport.Clock()
	p := &fakeProvider{status: model.Status{
		Status: model.StatusReady, Endpoint: "http://127.0.0.1:18080",
		Model: "Ornith-1.5-9B.gguf", Reason: "провайдер отвечает",
		ObservedAt: clk.Now(), SupportsSchema: true,
	}}
	r := research.New()
	if err := research.RegisterProviderInspector(r, p, clk); err != nil {
		t.Fatal(err)
	}

	caps := r.Catalog()
	if len(caps) != 1 || caps[0].ID != "runtime.provider.inspect" {
		t.Fatalf("неожиданный каталог: %+v", caps)
	}
	if caps[0].Stability != research.StabilityRealtime {
		t.Fatalf("состояние provider ошибочно признано долговечным: %+v", caps[0])
	}

	res, err := r.Execute(ctx, research.Request{CapabilityID: "runtime.provider.inspect"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stability != research.StabilityRealtime || res.ObservedAt != clk.Now() {
		t.Fatalf("неверная свежесть результата: %+v", res)
	}
	if !strings.Contains(res.Evidence, "Ornith-1.5-9B.gguf") {
		t.Fatalf("модель потерялась из evidence: %q", res.Evidence)
	}
	var data map[string]any
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["model"] != "Ornith-1.5-9B.gguf" {
		t.Fatalf("неверная модель в structured data: %#v", data)
	}
}

func TestRegistryRefusesInventedCapabilityAndBadArgs(t *testing.T) {
	r := research.New()
	if _, err := r.Execute(context.Background(), research.Request{CapabilityID: "git.magic"}); err == nil {
		t.Fatal("придуманная capability была принята")
	}

	if err := r.Register(research.Capability{
		ID: "test.read", Title: "Тест", Question: "Что видно?",
		Stability: research.StabilityStable,
	}, func(context.Context, json.RawMessage) (research.Result, error) {
		return research.Result{Evidence: "ok"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(context.Background(), research.Request{
		CapabilityID: "test.read", Args: json.RawMessage(`{сломано`),
	}); err == nil {
		t.Fatal("невалидные args приняты")
	}
	if err := r.Register(research.Capability{
		ID: "test.read", Title: "Дубликат", Question: "Что видно?",
		Stability: research.StabilityStable,
	}, func(context.Context, json.RawMessage) (research.Result, error) {
		return research.Result{Evidence: "other"}, nil
	}); err == nil {
		t.Fatal("дубликат capability молча заменил существующую")
	}
}

func TestRegistryRejectsHandlerWithoutEvidence(t *testing.T) {
	r := research.New()
	if err := r.Register(research.Capability{
		ID: "empty.read", Title: "Пусто", Question: "Что видно?",
		Stability: research.StabilityVolatile,
	}, func(context.Context, json.RawMessage) (research.Result, error) {
		return research.Result{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(context.Background(), research.Request{CapabilityID: "empty.read"}); err == nil {
		t.Fatal("результат без evidence принят")
	}
}
