package api_test

import (
	"net/http"
	"testing"
)

func TestDeskAmbientIsTypedRuntimeObservation(t *testing.T) {
	s := newServer(t)
	out := s.mustDo(http.MethodGet, "/api/v1/desk/ambient", nil, http.StatusOK)
	if out["kind"] != "machine" {
		t.Fatalf("вид предмета Стола = %v", out["kind"])
	}
	snapshot, ok := out["snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot не объект: %#v", out["snapshot"])
	}
	if cpus, _ := snapshot["cpus"].(float64); cpus <= 0 {
		t.Fatalf("число CPU не наблюдено: %#v", snapshot)
	}
	if snapshot["observed_at"] == nil || snapshot["observed_at"] == "" {
		t.Fatalf("время наблюдения отсутствует: %#v", snapshot)
	}
}
