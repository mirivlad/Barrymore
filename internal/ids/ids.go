// Package ids выдаёт префиксованные идентификаторы.
//
// Префикс делает читаемыми логи, журнал событий и сообщения об ошибках:
// по одному идентификатору видно, о какой сущности идёт речь.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
	"time"
)

// Префиксы доменных сущностей.
const (
	Event         = "evt"
	Thread        = "thr"
	Position      = "pos"
	Decision      = "dec"
	Question      = "qst"
	Link          = "lnk"
	Worker        = "wrk"
	Snapshot      = "snp"
	Observation   = "obs"
	Expectation   = "exp"
	Discrepancy   = "dsc"
	Probe         = "prb"
	ReflexAttempt = "rfx"
	WorkOrder     = "wo"
	WorkerRun     = "run"
	ContextPack   = "ctx"
	Artifact      = "art"
	Verification  = "ver"
	Approval      = "apr"
	Correlation   = "corr"
	Message       = "msg"
	TurnRun       = "trn"
	SkillRun      = "act"
)

var enc = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// New возвращает идентификатор вида "thr_01j8x…": префикс, время и случайная часть.
// Лексикографический порядок совпадает с порядком создания.
func New(prefix string) string {
	var b [16]byte
	ms := uint64(time.Now().UTC().UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	if _, err := rand.Read(b[6:]); err != nil {
		// crypto/rand на Linux не отказывает; если отказал — продолжать нельзя.
		panic("ids: источник случайности недоступен: " + err.Error())
	}
	return prefix + "_" + enc.EncodeToString(b[:])
}

// Prefix возвращает префикс идентификатора или пустую строку.
func Prefix(id string) string {
	if i := strings.IndexByte(id, '_'); i > 0 {
		return id[:i]
	}
	return ""
}
