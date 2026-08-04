package worker

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Класс исполнителя.
//
// Повседневную работу ведут routine-исполнители на бесплатных моделях.
// Specialist — мастер по вызову: он привлекается к трудной задаче осознанно,
// потому что расходует платную квоту.
const (
	ClassRoutine    = "routine"
	ClassSpecialist = "specialist"
)

// Стоимость модели.
const (
	// CostFree — модель бесплатна по прямому признаку (имя, ценник, отчёт запуска).
	CostFree = "free"
	// CostSubscription — расходует квоту подписки, отдельного счёта нет,
	// но ресурс конечен.
	CostSubscription = "subscription"
	// CostPaid — оплачивается за использование.
	CostPaid = "paid"
	// CostUnknown — сведений нет; оптимистичное допущение не делается.
	CostUnknown = "unknown"
)

// costRank задаёт порядок предпочтения: меньше — желаннее.
var costRank = map[string]int{
	CostFree:         0,
	CostSubscription: 1,
	CostUnknown:      2,
	CostPaid:         3,
}

// Model — модель, доступная внутри исполнителя.
type Model struct {
	ID string `json:"id"`
	// Ref — строка, которую понимает сам исполнитель ("opencode/ling-3.0-flash-free").
	Ref      string `json:"ref"`
	WorkerID string `json:"worker_id"`
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name,omitempty"`
	CostTier string `json:"cost_tier"`
	Source   string `json:"source"`
	Evidence string `json:"evidence,omitempty"`
	// Confidence отличает догадку по названию от подтверждённой стоимости.
	// Пометка "-free" в имени — слабое основание: провайдеры меняют модели,
	// переименовывают их и меняют цену.
	Confidence float64 `json:"confidence"`
	// LastCost — стоимость последнего наблюдённого запуска. Отрицательное
	// значение означает, что стоимость не наблюдалась.
	LastCost   float64    `json:"last_cost"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	IsDefault  bool       `json:"is_default"`
	ObservedAt time.Time  `json:"observed_at"`
}

// Charged сообщает, что за модель когда-либо списывали деньги.
//
// Признак постоянный и решающий: если списание было, модель не бесплатна,
// как бы она ни называлась. Снять отметку может только владелец вручную.
func (m Model) Charged() bool { return m.VerifiedAt != nil && m.LastCost > 0 }

// CostSummary объясняет стоимость человеческими словами.
func (m Model) CostSummary(now time.Time) string {
	if m.Charged() {
		return fmt.Sprintf("платная: наблюдалось списание %.6f (%s)",
			m.LastCost, m.VerifiedAt.Format("2006-01-02 15:04"))
	}
	switch m.CostTier {
	case CostFree:
		return "бесплатна: " + m.Evidence
	case CostSubscription:
		return "расходует квоту подписки"
	case CostPaid:
		return "платная"
	default:
		return "стоимость неизвестна"
	}
}

// Free сообщает, бесплатна ли модель.
func (m Model) Free() bool { return m.CostTier == CostFree }

// ModelPolicy задаёт, какие модели допустимы.
//
// Умолчание — только бесплатные: платный запуск не должен случаться
// как побочный эффект обычной работы (06_SECURITY §2.6).
type ModelPolicy struct {
	// AllowedTiers перечисляет разрешённые уровни стоимости.
	AllowedTiers []string `json:"allowed_tiers"`
	// PreferCheapest ставит бесплатные модели выше при прочих равных.
	PreferCheapest bool `json:"prefer_cheapest"`
	// AllowSpecialists разрешает привлекать мастеров по вызову.
	AllowSpecialists bool `json:"allow_specialists"`
}

// FreeOnly — политика по умолчанию: только бесплатные модели, без специалистов.
func FreeOnly() ModelPolicy {
	return ModelPolicy{
		AllowedTiers:     []string{CostFree},
		PreferCheapest:   true,
		AllowSpecialists: false,
	}
}

// PreferFree допускает подписку и неизвестную стоимость, но ставит бесплатное выше.
func PreferFree() ModelPolicy {
	return ModelPolicy{
		AllowedTiers:     []string{CostFree, CostSubscription, CostUnknown},
		PreferCheapest:   true,
		AllowSpecialists: true,
	}
}

// AnyCost разрешает всё, включая платные вызовы.
func AnyCost() ModelPolicy {
	return ModelPolicy{
		AllowedTiers:     []string{CostFree, CostSubscription, CostUnknown, CostPaid},
		PreferCheapest:   true,
		AllowSpecialists: true,
	}
}

// ParseModelPolicy разбирает название политики.
func ParseModelPolicy(name string) (ModelPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "free", "free-only":
		return FreeOnly(), nil
	case "prefer-free":
		return PreferFree(), nil
	case "any", "any-cost":
		return AnyCost(), nil
	default:
		return ModelPolicy{}, fmt.Errorf(
			"неизвестная политика стоимости %q: допустимы free, prefer-free, any", name)
	}
}

// Allows проверяет допустимость уровня стоимости.
func (p ModelPolicy) Allows(tier string) bool {
	for _, t := range p.AllowedTiers {
		if t == tier {
			return true
		}
	}
	return false
}

// Describe объясняет политику человеческими словами.
func (p ModelPolicy) Describe() string {
	switch {
	case len(p.AllowedTiers) == 1 && p.AllowedTiers[0] == CostFree:
		return "только бесплатные модели"
	case p.Allows(CostPaid):
		return "разрешены любые модели, включая платные; бесплатные предпочтительнее"
	default:
		return "бесплатные и входящие в подписку; платные запрещены"
	}
}

// SelectModel выбирает модель под политику.
//
// Возвращает объяснение выбора: пользователь должен видеть, почему взята
// именно эта модель, и иметь возможность переопределить.
func SelectModel(models []Model, policy ModelPolicy, preferred string, now time.Time) (Model, string, error) {
	if len(models) == 0 {
		return Model{}, "", fmt.Errorf(
			"у исполнителя нет известных моделей: сначала обновите их список")
	}

	if preferred != "" {
		for _, m := range models {
			if m.Ref != preferred {
				continue
			}
			if !policy.Allows(m.CostTier) {
				return Model{}, "", fmt.Errorf(
					"модель %s выбрана вручную, но её стоимость (%s) запрещена политикой: %s",
					m.Ref, m.CostTier, policy.Describe())
			}
			return m, fmt.Sprintf("модель %s выбрана владельцем вручную (стоимость: %s)",
				m.Ref, m.CostTier), nil
		}
		return Model{}, "", fmt.Errorf("модель %q не найдена среди известных", preferred)
	}

	allowed := make([]Model, 0, len(models))
	for _, m := range models {
		// Модель, за которую однажды списали деньги, к бесплатной работе
		// больше не допускается, даже если в названии осталась пометка free.
		if m.Charged() && !policy.Allows(CostPaid) {
			continue
		}
		if policy.Allows(m.CostTier) {
			allowed = append(allowed, m)
		}
	}
	if len(allowed) == 0 {
		return Model{}, "", fmt.Errorf(
			"ни одна из %d известных моделей не проходит по политике стоимости (%s)",
			len(models), policy.Describe())
	}

	sort.SliceStable(allowed, func(i, j int) bool {
		if policy.PreferCheapest {
			ri, rj := costRank[allowed[i].CostTier], costRank[allowed[j].CostTier]
			if ri != rj {
				return ri < rj
			}
		}
		if allowed[i].Confidence != allowed[j].Confidence {
			return allowed[i].Confidence > allowed[j].Confidence
		}
		// Модель, объявленная исполнителем по умолчанию, вероятнее настроена
		// и проверена владельцем.
		if allowed[i].IsDefault != allowed[j].IsDefault {
			return allowed[i].IsDefault
		}
		return allowed[i].Ref < allowed[j].Ref
	})

	best := allowed[0]
	reason := fmt.Sprintf("модель %s: %s", best.Ref, best.CostSummary(now))
	if best.Evidence != "" {
		reason += " (" + best.Evidence + ")"
	}
	if best.IsDefault {
		reason += "; объявлена исполнителем как модель по умолчанию"
	}
	freeCount := 0
	for _, m := range allowed {
		if m.Free() {
			freeCount++
		}
	}
	if freeCount > 0 {
		reason += fmt.Sprintf("; бесплатных вариантов доступно: %d", freeCount)
	}
	if best.Free() {
		// Бесплатность решается до запуска. Контроль по ходу выполнения —
		// не способ узнать цену, а страховка: появление списания означает,
		// что договорённость нарушена и работу надо прекращать.
		reason += "; при появлении любого списания запуск будет остановлен"
	}
	return best, reason, nil
}

// ClassifyModelRef определяет стоимость модели по её названию.
//
// Это решение принимается ДО запуска и служит основанием выбора: провайдеры
// помечают бесплатные модели в самом имени, и другого бесплатного способа
// узнать цену заранее нет. Модель без явной пометки бесплатной не считается.
func ClassifyModelRef(ref string) (tier, evidence string, confidence float64) {
	lower := strings.ToLower(ref)
	switch {
	case strings.HasPrefix(lower, "ollama/"), strings.HasPrefix(lower, "local/"),
		strings.HasPrefix(lower, "lmstudio/"):
		return CostFree, "локальная модель: внешних списаний быть не может", 1.0
	case strings.HasSuffix(lower, ":free"), strings.HasSuffix(lower, "-free"),
		strings.Contains(lower, ":free/"), strings.Contains(lower, "-free/"):
		return CostFree, "провайдер пометил модель как бесплатную в названии", 0.9
	default:
		return CostUnknown, "провайдер не пометил модель как бесплатную", 0.3
	}
}

// MarkCharged переводит модель в платные из-за фактического списания.
//
// Это не уточнение цены, а фиксация нарушения: модель считалась бесплатной,
// а списание произошло. Отметка постоянная — повторять ошибку нельзя.
func MarkCharged(m Model, cost float64, at time.Time) Model {
	m.CostTier = CostPaid
	m.Source = "run-charged"
	m.Evidence = fmt.Sprintf(
		"считалась бесплатной, но запуск обошёлся в %.6f: провайдер изменил условия", cost)
	m.Confidence = 1
	m.LastCost = cost
	charged := at.UTC()
	m.VerifiedAt = &charged
	return m
}

// CarryCharges переносит отметки о списаниях на обновлённый каталог.
//
// Обновление списка не должно обнулять память о нарушении: модель, за которую
// однажды списали деньги, обязана остаться платной и после того, как провайдер
// снова напишет в её названии "free".
func CarryCharges(fresh []Model, known []Model) []Model {
	byRef := make(map[string]Model, len(known))
	for _, k := range known {
		if k.Charged() {
			byRef[k.Ref] = k
		}
	}
	for i := range fresh {
		prev, ok := byRef[fresh[i].Ref]
		if !ok {
			continue
		}
		fresh[i].CostTier = CostPaid
		fresh[i].LastCost = prev.LastCost
		fresh[i].VerifiedAt = prev.VerifiedAt
		fresh[i].Confidence = 1
		fresh[i].Source = prev.Source
		fresh[i].Evidence = prev.Evidence +
			" (отметка сохранена при обновлении каталога)"
	}
	return fresh
}

// ModelCatalogTTL — срок годности каталога моделей.
//
// Провайдеры добавляют и убирают бесплатные модели, поэтому список,
// собранный вчера, сегодня уже может врать.
const ModelCatalogTTL = 6 * time.Hour

// ModelsStale сообщает, устарел ли каталог моделей исполнителя.
func (v View) ModelsStale(now time.Time) (bool, string) {
	if v.Worker.ModelsRefreshedAt == nil {
		return true, "каталог моделей ни разу не обновлялся"
	}
	age := now.Sub(*v.Worker.ModelsRefreshedAt)
	if age < ModelCatalogTTL {
		return false, ""
	}
	return true, fmt.Sprintf(
		"каталог моделей обновлялся %s назад: состав бесплатных моделей мог измениться",
		age.Round(time.Minute))
}
