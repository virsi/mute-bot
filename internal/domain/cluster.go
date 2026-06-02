// Package domain holds the pure types shared by the pipeline — posts,
// clusters, users, topics. No I/O, no third-party imports beyond stdlib.
package domain

import "time"

// Cluster is a group of duplicate/related posts after dedup. The ranker
// fills the Score field; the classifier fills Headline/Summary/Topics/Severity.
type Cluster struct {
	ID            int64
	Headline      string
	Summary       string
	Topics        []string
	Severity      int
	Coverage      int
	Score         float32
	FirstSeenAt   time.Time
	LastUpdatedAt time.Time
	Status        string
}

// Age returns how long the cluster has existed since its first post.
func (c Cluster) Age() time.Duration {
	return time.Since(c.FirstSeenAt)
}
