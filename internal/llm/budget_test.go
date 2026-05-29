package llm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBudget_AllowAndDeny(t *testing.T) {
	b := NewBudgetGuard(BudgetConfig{MonthlyUSD: 1.00})

	ctx := context.Background()
	require.NoError(t, b.Charge(ctx, 0.40))
	require.NoError(t, b.Charge(ctx, 0.50))
	require.Equal(t, BudgetOK, b.State())

	require.NoError(t, b.Charge(ctx, 0.05)) // 95% -> still ok
	require.Equal(t, BudgetWarn, b.State())

	require.NoError(t, b.Charge(ctx, 0.04)) // 99% -> degraded
	require.Equal(t, BudgetDegraded, b.State())

	err := b.Charge(ctx, 0.10) // exceed -> blocked
	require.ErrorIs(t, err, ErrBudgetExceeded)
	require.Equal(t, BudgetBlocked, b.State())
}

func TestBudget_MonthRollover(t *testing.T) {
	b := NewBudgetGuard(BudgetConfig{MonthlyUSD: 1.00})
	b.now = func() time.Time { return time.Date(2026, 6, 30, 23, 59, 0, 0, time.UTC) }
	_ = b.Charge(context.Background(), 0.99)
	require.Equal(t, BudgetDegraded, b.State())

	b.now = func() time.Time { return time.Date(2026, 7, 1, 0, 0, 1, 0, time.UTC) }
	require.Equal(t, BudgetOK, b.State()) // resets on new month
}
