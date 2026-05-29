package llm

import (
	"context"
	"errors"
	"sync"
	"time"
)

type BudgetState int

const (
	BudgetOK       BudgetState = iota
	BudgetWarn                 // >= 80%
	BudgetDegraded             // >= 90%
	BudgetBlocked              // >= 100%
)

var ErrBudgetExceeded = errors.New("monthly LLM budget exceeded")

type BudgetConfig struct {
	MonthlyUSD float64
}

type BudgetGuard struct {
	mu       sync.Mutex
	cfg      BudgetConfig
	month    time.Month
	year     int
	spentUSD float64
	now      func() time.Time
}

func NewBudgetGuard(cfg BudgetConfig) *BudgetGuard {
	return &BudgetGuard{cfg: cfg, now: time.Now}
}

func (b *BudgetGuard) Charge(_ context.Context, costUSD float64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollIfNeededLocked()
	b.spentUSD += costUSD
	if b.spentUSD > b.cfg.MonthlyUSD {
		return ErrBudgetExceeded
	}
	return nil
}

func (b *BudgetGuard) State() BudgetState {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollIfNeededLocked()
	ratio := b.spentUSD / b.cfg.MonthlyUSD
	switch {
	case ratio >= 1.0:
		return BudgetBlocked
	case ratio >= 0.99:
		return BudgetDegraded
	case ratio >= 0.95:
		return BudgetWarn
	default:
		return BudgetOK
	}
}

func (b *BudgetGuard) SpentUSD() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollIfNeededLocked()
	return b.spentUSD
}

func (b *BudgetGuard) rollIfNeededLocked() {
	t := b.now().UTC()
	if t.Year() != b.year || t.Month() != b.month {
		b.year, b.month = t.Year(), t.Month()
		b.spentUSD = 0
	}
}
