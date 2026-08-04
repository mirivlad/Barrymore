// Package clock даёт доменному коду единственный законный источник времени.
//
// ADR 0009: прямой вызов time.Now() в internal/domain, internal/runtime и
// internal/adapters запрещён. Без этого правила нельзя детерминированно
// тестировать expiry ожиданий, cooldown реакций и freshness снапшотов.
package clock

import (
	"sync"
	"time"
)

// Clock — источник времени и ожидания.
type Clock interface {
	Now() time.Time
	// After ведёт себя как time.After, но у Fake управляется вручную.
	After(d time.Duration) <-chan time.Time
}

// Real — системное время. Единственное место в проекте, где вызывается time.Now.
type Real struct{}

func (Real) Now() time.Time                         { return time.Now().UTC() }
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Fake — управляемое время для тестов.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

// NewFake создаёт часы, стоящие на указанном моменте.
func NewFake(at time.Time) *Fake { return &Fake{now: at.UTC()} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	f.waiters = append(f.waiters, fakeWaiter{at: f.now.Add(d), ch: ch})
	return ch
}

// Advance двигает время вперёд и будит наступившие ожидания.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now
	kept := f.waiters[:0]
	var fire []chan time.Time
	for _, w := range f.waiters {
		if !w.at.After(now) {
			fire = append(fire, w.ch)
			continue
		}
		kept = append(kept, w)
	}
	f.waiters = kept
	f.mu.Unlock()

	for _, ch := range fire {
		ch <- now
	}
}

// Set устанавливает абсолютное время.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	d := t.UTC().Sub(f.now)
	f.mu.Unlock()
	if d > 0 {
		f.Advance(d)
		return
	}
	f.mu.Lock()
	f.now = t.UTC()
	f.mu.Unlock()
}
