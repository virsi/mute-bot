package domain

import (
	"crypto/sha256"
	"time"
)

// RawPost is the unprocessed Telegram message that arrives from MTProto.
type RawPost struct {
	ChannelID int64
	TGMsgID   int64
	Text      string
	PostedAt  time.Time
}

// NormalizedPost is the post after the normalizer pipeline has cleaned and
// language-detected its content.
type NormalizedPost struct {
	ID         int64
	ChannelID  int64
	TGMsgID    int64
	TextRaw    string
	TextClean  string
	Lang       string
	PostedAt   time.Time
	IngestedAt time.Time
	ClusterID  *int64
}

// Hash returns a deterministic SHA-256 digest of the cleaned text.
// Used as a fast first-pass deduplication key.
func (p NormalizedPost) Hash() [32]byte {
	return sha256.Sum256([]byte(p.TextClean))
}
