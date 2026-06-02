package domain

import "time"

// Tier represents a user's subscription tier.
type Tier string

// Tier constants — Phase-2 ships only free and pro.
const (
	TierFree Tier = "free"
	TierPro  Tier = "pro"
)

// User is a Telegram user known to the bot.
type User struct {
	ID        int64
	TGUserID  int64
	Username  string
	Tier      Tier
	TierUntil *time.Time
	TrialUsed bool
	Lang      string
	Blocked   bool
	CreatedAt time.Time
}
