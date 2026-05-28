// Package queue provides NATS JetStream primitives — stream names, subject
// names, connection management, publisher and subscriber helpers — for the
// mute-bot pipeline (ingest → cluster → delivery).
package queue

// Stream names. One stream per pipeline domain so retention and consumer
// settings can be tuned independently.
const (
	StreamIngest   = "INGEST"
	StreamClusters = "CLUSTERS"
	StreamDelivery = "DELIVERY"
)

// Subject names. Subjects are flat strings (no wildcards) so producers and
// consumers can be wired together by constant reference.
const (
	SubjectRaw           = "ingest.raw"
	SubjectNormalized    = "ingest.normalized"
	SubjectClusterUpdate = "cluster.updated"
	SubjectClusterScored = "cluster.scored"
	SubjectDeliverySched = "delivery.scheduled"
	SubjectDeliveryPro   = "delivery.pro"
	SubjectDeliveryFree  = "delivery.free"
)

// DLQSuffix is appended to a subject when a message exhausts MaxDeliver and
// must be parked for human inspection.
const DLQSuffix = ".dlq"
