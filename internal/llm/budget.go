// Package llm provides the LLM provider abstraction and an OpenAI-compatible
// HTTP client used by the dedup, classifier and ranker workers. Includes a
// monthly USD budget guard shared across all calls so the pipeline cannot
// silently blow past its cap.
package llm

import (
	"context"
	"errors"
	"sync"
	"time"
)

// BudgetState classifies how close the running spend is to the monthly cap.
type BudgetState int

// Budget state values, ordered by severity.
const (
	BudgetOK       BudgetState = iota
	BudgetWarn                 // >= 80%
	BudgetDegraded             // >= 90%
	BudgetBlocked              // >= 100%
)

// ErrBudgetExceeded is returned by Charge when MonthlyUSD has been reached.
var ErrBudgetExceeded = errors.New("monthly LLM budget exceeded")

// BudgetConfig configures a BudgetGuard.
type BudgetConfig struct {
	MonthlyUSD float64
}

// BudgetGuard tracks cumulative LLM spend in USD and refuses Charge calls
// once the monthly cap is reached.
type BudgetGuard struct {
	mu       sync.Mutex
	cfg      BudgetConfig
	month    time.Month
	year     int
	spentUSD float64
	now      func() time.Time
}

// NewBudgetGuard constructs a guard with the given monthly USD cap.
func NewBudgetGuard(cfg BudgetConfig) *BudgetGuard {
	return &BudgetGuard{cfg: cfg, now: time.Now}
}

// Charge deducts costUSD from the remaining budget. Returns ErrBudgetExceeded
// when the cap is hit; the count keeps accumulating so State stays Blocked.
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

// State returns the bucket the running total currently falls into.
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

// SpentUSD returns the cumulative charges so far this month.
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
