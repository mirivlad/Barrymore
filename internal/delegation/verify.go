package delegation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/mirivlad/barrymore/internal/event"
	"github.com/mirivlad/barrymore/internal/ids"
	"github.com/mirivlad/barrymore/internal/runner"
	"github.com/mirivlad/barrymore/internal/runtime"
	"github.com/mirivlad/barrymore/internal/worker"
)

// Finalize собирает результат и проверяет его.
//
// «Готово» исполнителя не является доказательством готовности: состояние
// поручения определяют проверки, а не отчёт (00_PRODUCT_VISION §9.7).
func (s *Service) Finalize(ctx context.Context, orderID, runID string) error {
	order, err := s.Get(ctx, orderID)
	if err != nil {
		return err
	}
	if order.State == StateCompleted || order.State == StateCancelled {
		return nil
	}
	run, err := s.runByID(ctx, runID)
	if err != nil {
		return err
	}

	if err := s.setState(ctx, orderID, StateVerifying, "", event.Actor{Type: event.ActorRuntime}); err != nil {
		return err
	}

	// Adapter приводит вывод к обязательным артефактам: у одних инструментов
	// отчёт уже лежит файлом, у других его надо извлечь из потока.
	if w, err := s.registry.Get(ctx, order.WorkerID); err == nil {
		if adapter, ok := s.registry.Adapter(w.AdapterID); ok {
			if err := adapter.Collect(ctx, run.RunDir); err != nil {
				s.log.Error("сбор артефактов adapter'ом не удался",
					"run", run.ID, "error", err)
			}
		}
	}

	artifacts, err := s.collectArtifacts(ctx, order, run)
	if err != nil {
		return err
	}

	after, err := ScanWorkspace(ctx, order.WorkspaceRoot)
	if err != nil {
		return err
	}
	diff := Diff{}
	if order.WorkspaceBaseline != "" && after.Digest != order.WorkspaceBaseline {
		// Слепки различаются; подробный перечень получаем повторным обходом.
		diff = s.diffAgainstBaseline(ctx, order, after)
	}

	// Наблюдение о состоянии каталога записывается всегда: на него опирается
	// ожидание «audit-only не пишет».
	if _, err := s.rt.RecordObservation(ctx, runtime.ObservationRequest{
		Kind:          runtime.ObsWorkspaceScan,
		SubjectType:   runtime.SubjectWorkerRun,
		SubjectID:     run.ID,
		Source:        "verification",
		SourceQuality: runtime.QualityDirect,
		CorrelationID: orderID,
		Payload: runtime.WorkspaceScanPayload{
			Digest: after.Digest, ChangedPaths: diff.Paths(), GitStatus: after.GitStatus,
		},
	}); err != nil {
		return err
	}

	// Сообщение об исчерпанной квоте — сведение о состоянии учётной записи,
	// а не рядовая ошибка запуска. Оно обновляет снимок доступности, иначе
	// Бэрримор продолжал бы предлагать исполнителя, который заведомо не сможет
	// взяться за работу (сценарий E).
	if status, msg, found := s.providerSignal(ctx, run.ID); found {
		if err := s.markProviderState(ctx, order.WorkerID, status, msg); err != nil {
			s.log.Error("снимок доступности не обновлён",
				"worker", order.WorkerID, "error", err)
		}
	}

	// Изменения из копии собираются до проверок: одна из них смотрит, появились
	// ли они там, где должны были появиться.
	if !order.AuditOnly && order.WorkCopyPath != "" {
		change, err := CollectChange(ctx, WorkCopy{
			Path: order.WorkCopyPath, Source: order.WorkspaceRoot,
			Branch: order.WorkCopyBranch, Baseline: order.WorkCopyBaseline,
		})
		if err != nil {
			s.log.Error("изменения из копии не собраны", "order", orderID, "error", err)
		} else if err := s.recordChange(ctx, orderID, change); err != nil {
			return err
		} else {
			order.ChangeSummary = change
			if !change.Empty() {
				order.ChangeState = ChangeCollected
			}
		}
	}

	checks := s.runChecks(ctx, order, run, artifacts, after, diff)

	passed := true
	var failures []string
	for _, c := range checks {
		if c.Status == VerifyFailed {
			passed = false
			failures = append(failures, c.Name+": "+c.Detail)
		}
	}

	state, reason := StateCompleted, ""
	if !passed {
		state = StateFailed
		reason = strings.Join(failures, "; ")
	}
	if err := s.setState(ctx, orderID, state, reason, event.Actor{Type: event.ActorRuntime}); err != nil {
		return err
	}

	// Нить узнаёт итог сама. Иначе владельцу пришлось бы переносить результат
	// туда руками — то есть обслуживать связь, которую система знает и без него.
	order.State = state
	order.FailureReason = reason
	s.reflectFinish(ctx, order, checks)

	// Исход уходит в опыт: исполнитель — такой же способ работы, как и
	// собственное умение, и сравнивать их можно только по одинаковой мерке.
	if s.watcher != nil && order.WorkerID != "" {
		result, evidence := "good", fmt.Sprintf("проверок пройдено %d из %d",
			len(checks)-len(failures), len(checks))
		if !passed {
			result, evidence = "bad", reason
		}
		took := int64(0)
		if order.StartedAt != nil {
			took = s.clock.Now().Sub(*order.StartedAt).Milliseconds()
		}
		s.watcher.OrderFinished(ctx, order.WorkerID, order.WorkerID, result, evidence, took)
	}
	return nil
}

func (s *Service) diffAgainstBaseline(ctx context.Context, order WorkOrder, after WorkspaceState) Diff {
	// Исходный слепок хранится только контрольной суммой: держать в базе
	// перечень из сотен тысяч файлов было бы дорого. Поэтому подробности
	// берём из git, который знает, что изменилось.
	var d Diff
	for _, line := range after.GitDirty {
		if len(line) > 3 {
			d.Modified = append(d.Modified, strings.TrimSpace(line[2:]))
		}
	}
	if len(d.Modified) == 0 {
		d.Modified = append(d.Modified,
			"состав каталога изменился, но git не показывает изменений: "+
				"возможны изменения вне репозитория или обновление времён доступа")
	}
	return d
}

// collectArtifacts переносит файлы запуска в реестр артефактов.
func (s *Service) collectArtifacts(ctx context.Context, order WorkOrder, run WorkerRun) ([]Artifact, error) {
	outDir := filepath.Join(run.RunDir, runner.OutputDir)
	entries, err := os.ReadDir(outDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("чтение каталога артефактов: %w", err)
	}

	// Сырой вывод — тоже артефакт: без него нельзя разобрать неудачу.
	candidates := []struct{ name, path string }{
		{runner.StdoutFile, filepath.Join(run.RunDir, runner.StdoutFile)},
		{runner.StderrFile, filepath.Join(run.RunDir, runner.StderrFile)},
		{"context-pack.json", filepath.Join(run.RunDir, "context-pack.json")},
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		candidates = append(candidates, struct{ name, path string }{e.Name(), filepath.Join(outDir, e.Name())})
	}

	var out []Artifact
	now := s.clock.Now()
	for _, c := range candidates {
		info, err := os.Stat(c.path)
		if err != nil {
			continue
		}
		sum, err := checksumFile(c.path)
		if err != nil {
			s.log.Warn("контрольная сумма артефакта не вычислена", "path", c.path, "error", err)
		}
		a := Artifact{
			ID: ids.New(ids.Artifact), RunID: run.ID, WorkOrderID: order.ID,
			Name: c.name, Path: c.path, Size: info.Size(), Checksum: sum,
			Kind: "file", CollectedAt: now,
		}
		if _, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
			if _, err := w.Append(ctx, event.Request{
				StreamType: StreamType, StreamID: order.ID, ExpectedRevision: event.AnyRevision,
				EventType: EvArtifactCollected, Actor: event.Actor{Type: event.ActorRuntime},
				Payload: a,
			}); err != nil {
				return err
			}
			return applyArtifact(ctx, tx, a)
		}); err != nil {
			return nil, err
		}
		if _, err := s.rt.RecordObservation(ctx, runtime.ObservationRequest{
			Kind:          runtime.ObsArtifactCollected,
			SubjectType:   runtime.SubjectWorkerRun,
			SubjectID:     run.ID,
			Source:        "verification",
			SourceQuality: runtime.QualityDirect,
			DedupeKey:     "artifact:" + run.ID + ":" + c.name,
			Payload:       runtime.ArtifactPayload{Name: a.Name, Path: a.Path, Size: a.Size},
		}); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// runChecks выполняет детерминированные проверки.
//
// ADR 0011: набор проверок задаётся Бэрримором. Отчёт исполнителя влияет только
// на данные и не может ни добавить проверку, ни отключить её.
func (s *Service) runChecks(ctx context.Context, order WorkOrder, run WorkerRun,
	artifacts []Artifact, after WorkspaceState, diff Diff) []Verification {

	var checks []Verification

	record := func(name, status, detail string) {
		v := Verification{
			ID: ids.New(ids.Verification), WorkOrderID: order.ID, RunID: run.ID,
			Kind: "deterministic", Name: name, Status: status, Detail: detail,
			StartedAt: s.clock.Now(),
		}
		finished := s.clock.Now()
		v.FinishedAt = &finished
		if _, err := s.journal.Write(ctx, func(tx *sql.Tx, w *event.TxWriter) error {
			if _, err := w.Append(ctx, event.Request{
				StreamType: StreamType, StreamID: order.ID, ExpectedRevision: event.AnyRevision,
				EventType: EvVerifyCompleted, Actor: event.Actor{Type: event.ActorRuntime},
				Payload: v,
			}); err != nil {
				return err
			}
			return applyVerification(ctx, tx, v)
		}); err != nil {
			s.log.Error("проверка не записана", "order", order.ID, "check", name, "error", err)
		}
		checks = append(checks, v)
	}

	// 1. Процесс завершился без ошибки запуска.
	switch {
	case run.ExitCode == nil:
		record("завершение процесса", VerifyFailed, "код завершения неизвестен")
	case *run.ExitCode != 0:
		record("завершение процесса", VerifyFailed,
			fmt.Sprintf("исполнитель завершился с кодом %d", *run.ExitCode))
	default:
		record("завершение процесса", VerifyPassed, "код завершения 0")
	}

	// 2. Обязательные артефакты на месте.
	present := map[string]Artifact{}
	for _, a := range artifacts {
		present[a.Name] = a
	}
	var missing []string
	for _, name := range order.RequiredArtifacts {
		if a, ok := present[name]; !ok || a.Size == 0 {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		record("обязательные артефакты", VerifyFailed,
			"не собраны или пусты: "+strings.Join(missing, ", "))
	} else {
		record("обязательные артефакты", VerifyPassed,
			fmt.Sprintf("собрано артефактов: %d", len(artifacts)))
	}

	// 3. Отчёт соответствует схеме.
	if a, ok := present["last-message.txt"]; ok && a.Size > 0 {
		if detail, err := validateReport(a.Path); err != nil {
			record("схема отчёта", VerifyFailed, err.Error())
		} else {
			record("схема отчёта", VerifyPassed, detail)
		}
	} else {
		record("схема отчёта", VerifySkipped, "отчёт не собран, проверять нечего")
	}

	// 4. Рабочий каталог не изменён.
	if order.AuditOnly {
		if order.WorkspaceBaseline == "" {
			record("неизменность рабочего каталога", VerifySkipped,
				"исходный слепок не сохранён")
		} else if after.Digest == order.WorkspaceBaseline {
			record("неизменность рабочего каталога", VerifyPassed,
				"слепок каталога совпадает с исходным")
		} else {
			record("неизменность рабочего каталога", VerifyFailed,
				"каталог изменён: "+strings.Join(diff.Paths(), ", "))
		}
	}

	// 4b. Поручение с записью: изменения собраны и ждут решения владельца.
	//
	// Каталог владельца при этом обязан остаться нетронутым — исполнитель
	// работал в копии. Проверка та же, что и для audit-only, потому что
	// требование то же самое.
	if !order.AuditOnly {
		switch {
		case order.WorkCopyPath == "":
			record("изменения исполнителя", VerifyFailed,
				"копия рабочего каталога не подготовлена, изменениям взяться неоткуда")
		case order.ChangeSummary.Empty():
			record("изменения исполнителя", VerifySkipped,
				"исполнитель ничего не изменил")
		default:
			record("изменения исполнителя", VerifyPassed, fmt.Sprintf(
				"изменено файлов: %d (+%d/−%d); ждут вашего решения",
				len(order.ChangeSummary.Files),
				order.ChangeSummary.Insertions, order.ChangeSummary.Deletions))
		}
		if order.WorkspaceBaseline != "" {
			if after.Digest == order.WorkspaceBaseline {
				record("каталог владельца не тронут", VerifyPassed,
					"исполнитель работал в копии; оригинал совпадает с исходным слепком")
			} else {
				record("каталог владельца не тронут", VerifyFailed,
					"каталог изменён в обход копии: "+strings.Join(diff.Paths(), ", "))
			}
		}
	}

	// 5. Незакоммиченные изменения не пострадали (сценарий G).
	if after.GitHead != "" && order.WorkspaceGitHead != "" {
		if after.GitHead == order.WorkspaceGitHead {
			record("HEAD репозитория", VerifyPassed, "HEAD не изменился: "+after.GitHead)
		} else {
			record("HEAD репозитория", VerifyFailed,
				fmt.Sprintf("HEAD изменился: было %s, стало %s",
					order.WorkspaceGitHead, after.GitHead))
		}
	}
	return checks
}

// Report — разобранный отчёт исполнителя.
type Report struct {
	Summary      string          `json:"summary"`
	Findings     []ReportFinding `json:"findings"`
	CheckedPaths []string        `json:"checked_paths,omitempty"`
	Limitations  string          `json:"limitations"`
}

// ReportFinding — одна находка.
type ReportFinding struct {
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Evidence string `json:"evidence"`
	Path     string `json:"path,omitempty"`
}

// validateReport проверяет отчёт по схеме Бэрримора.
func validateReport(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("отчёт не прочитан: %w", err)
	}
	trimmed := strings.TrimSpace(string(data))
	// Исполнитель мог обернуть JSON в блок кода — это отклонение от формата,
	// но не повод терять содержательный отчёт.
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	var doc any
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return "", fmt.Errorf("отчёт не является корректным JSON: %v", err)
	}

	compiler := jsonschema.NewCompiler()
	schemaDoc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(ReportSchema())))
	if err != nil {
		return "", fmt.Errorf("схема отчёта не разобрана: %w", err)
	}
	if err := compiler.AddResource("report.schema.json", schemaDoc); err != nil {
		return "", fmt.Errorf("схема отчёта не загружена: %w", err)
	}
	schema, err := compiler.Compile("report.schema.json")
	if err != nil {
		return "", fmt.Errorf("схема отчёта не скомпилирована: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		return "", fmt.Errorf("отчёт не соответствует схеме: %v", err)
	}

	var r Report
	if err := json.Unmarshal([]byte(trimmed), &r); err != nil {
		return "", fmt.Errorf("отчёт не разобран: %v", err)
	}
	detail := fmt.Sprintf("отчёт соответствует схеме, находок: %d", len(r.Findings))
	if r.Limitations == "" {
		detail += "; ограничения не указаны"
	}
	return detail, nil
}

// ParseReport читает отчёт запуска, если он есть.
func ParseReport(runDir string) (Report, bool) {
	path := filepath.Join(runDir, runner.OutputDir, "last-message.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, false
	}
	trimmed := strings.TrimSpace(string(data))
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")

	var r Report
	if err := json.Unmarshal([]byte(strings.TrimSpace(trimmed)), &r); err != nil {
		return Report{}, false
	}
	return r, true
}

// providerSignal ищет в наблюдениях сведения о состоянии учётной записи.
//
// Исчерпанный лимит имеет приоритет над общим отказом: он точнее.
func (s *Service) providerSignal(ctx context.Context, runID string) (string, string, bool) {
	obs, err := s.rt.Observations(ctx, runtime.SubjectWorkerRun, runID, 500)
	if err != nil {
		s.log.Error("наблюдения запуска недоступны", "run", runID, "error", err)
		return "", "", false
	}
	var refusedMsg string
	for _, o := range obs {
		if o.Kind != runtime.ObsRunEvent {
			continue
		}
		var ev worker.RunEvent
		if err := o.Decode(&ev); err != nil {
			continue
		}
		if flag, ok := ev.Detail["quota_exhausted"].(bool); ok && flag {
			msg, _ := ev.Detail["quota_message"].(string)
			if msg == "" {
				msg = ev.Summary
			}
			return worker.StatusQuotaExhausted, msg, true
		}
		if flag, ok := ev.Detail["provider_refused"].(bool); ok && flag && refusedMsg == "" {
			refusedMsg, _ = ev.Detail["provider_message"].(string)
			if refusedMsg == "" {
				refusedMsg = ev.Summary
			}
		}
	}
	if refusedMsg != "" {
		return worker.StatusBroken, refusedMsg, true
	}
	return "", "", false
}

// markProviderState записывает наблюдаемое состояние учётной записи.
func (s *Service) markProviderState(ctx context.Context, workerID, status, message string) error {
	if workerID == "" {
		return nil
	}
	reason := "провайдер отказал в соединении: " + message
	note := "причина отказа снаружи не видна: это может быть лимит или учётная запись"
	if status == worker.StatusQuotaExhausted {
		reason = "провайдер сообщил об исчерпании лимита: " + message
		note = "лимит исчерпан по сообщению провайдера; обход лимитов не выполняется"
	}
	// Срок действия снимка короткий: квота восстанавливается, и Бэрримор
	// обязан это перепроверить, а не считать исполнителя мёртвым навсегда.
	validUntil := s.clock.Now().Add(30 * time.Minute)
	_, err := s.rt.UpdateSnapshot(ctx, runtime.SnapshotRequest{
		Scope:      worker.SnapshotScope(workerID),
		Status:     status,
		Confidence: 1,
		ValidUntil: &validUntil,
		Source:     "run-event",
		Reason:     reason,
		Payload: worker.Availability{
			Status: status, Confidence: 1,
			ObservedAt: s.clock.Now(), ValidUntil: &validUntil,
			Source: "run-event", Reason: reason,
			QuotaKnown: status == worker.StatusQuotaExhausted,
			QuotaNote:  note,
		},
		Actor: event.Actor{Type: event.ActorRuntime},
	})
	return err
}

func checksumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
