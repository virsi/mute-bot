// Command processor hosts the stateless pipeline workers as a single
// binary:
//
//	ingest.raw          → normalizer        → ingest.normalized
//	ingest.normalized   → dedup matcher     → cluster.updated
//	cluster.updated     → classifier        → cluster.scored
//	cluster.scored      → ranker            → (score persisted)
//	delivery.scheduled  → digest assembler  → bot send
//
// Each consumer runs in its own goroutine driven by queue.Subscriber, which
// applies the standard retry-with-backoff + DLQ semantics around the
// per-worker Handle methods. The classifier additionally runs a debouncer
// goroutine because cluster.updated events arrive in bursts as a story
// spreads — see internal/classify/worker.go for the rationale.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/virsi/mute-bot/internal/bot"
	"github.com/virsi/mute-bot/internal/classify"
	"github.com/virsi/mute-bot/internal/config"
	"github.com/virsi/mute-bot/internal/dedup"
	"github.com/virsi/mute-bot/internal/digest"
	"github.com/virsi/mute-bot/internal/llm"
	"github.com/virsi/mute-bot/internal/normalize"
	"github.com/virsi/mute-bot/internal/obs"
	"github.com/virsi/mute-bot/internal/queue"
	"github.com/virsi/mute-bot/internal/rank"
	"github.com/virsi/mute-bot/internal/storage/postgres"
	rdb "github.com/virsi/mute-bot/internal/storage/redis"
)

func main() {
	if err := run(); err != nil {
		slog.Error("processor: fatal", slog.Any("err", err))
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "configs/config.yaml", "path to config yaml")
	flag.Parse()

	slog.SetDefault(obs.NewLogger(slog.LevelInfo, "processor"))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Metrics endpoint on the processor slot. Started before any worker
	// goroutine spins up so /healthz returns 200 the moment systemd checks.
	metricsSrv := obs.ServeMetrics(":9102")
	defer func() { _ = metricsSrv.Close() }()

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownTracing, err := obs.SetupTracing(rootCtx, "processor", cfg.OTLPEndpoint)
	if err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}
	defer func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		_ = shutdownTracing(shutdownCtx)
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stop
		slog.Info("processor: shutdown signal", slog.String("sig", sig.String()))
		cancel()
	}()

	pool, err := postgres.NewPool(rootCtx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("pg pool: %w", err)
	}
	defer pool.Close()

	rclient, err := rdb.NewClient(rootCtx, cfg.RedisAddr)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer func() { _ = rclient.Close() }()

	nc, err := queue.Connect(rootCtx, cfg.NATSURL)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer nc.Close()
	if err := nc.EnsureStreams(rootCtx); err != nil {
		return fmt.Errorf("nats ensure streams: %w", err)
	}
	pub := queue.NewPublisher(nc)
	sub := queue.NewSubscriber(nc)

	// Storage repositories. Each is bound to the shared pool; the repos are
	// safe to share across worker goroutines because the underlying pgx
	// pool handles per-call connection acquisition.
	postsRepo := postgres.NewPostsRepo(pool)
	channelsRepo := postgres.NewChannelsRepo(pool)
	clustersRepo := postgres.NewClustersRepo(pool)
	embeddingsRepo := postgres.NewEmbeddingsRepo(pool)
	deliveriesRepo := postgres.NewDeliveriesRepo(pool)
	settingsRepo := postgres.NewSettingsRepo(pool)

	// LLM client + budget guard. One BudgetGuard is shared between the
	// embedder and the classifier so the monthly cap covers the whole
	// pipeline rather than per-component sub-caps.
	budget := llm.NewBudgetGuard(llm.BudgetConfig{MonthlyUSD: cfg.LLM.MonthlyBudgetUSD})
	llmClient := llm.NewOpenAI(llm.OpenAIConfig{APIKey: cfg.OpenAIAPIKey, BaseURL: cfg.LLM.BaseURL, Budget: budget})

	// Dedup pipeline.
	minhashIdx := rdb.NewMinHashIndex(rclient, rdb.MinHashIndexConfig{Bands: 16, RowsPerBand: 8})
	embCache := rdb.NewEmbeddingCache(rclient, 7*24*3600)
	embedder := dedup.NewEmbedder(dedup.EmbedderDeps{
		LLM:   llmClient,
		Cache: embCache,
		Model: cfg.LLM.EmbeddingModel,
	})
	borderlineQueue := rdb.NewBorderlineQueue(rclient, 5000)
	matcher := dedup.NewMatcher(dedup.MatcherDeps{
		MinHashIndex: minhashIdx,
		Embedder:     embedder,
		Embeddings:   embeddingsRepo,
		Clusters:     clustersRepo,
		Posts:        postsRepo,
		Publisher:    pub,
		Model:        cfg.LLM.EmbeddingModel,
		Borderline:   borderlineQueue,
	})
	dedupWorker := dedup.NewWorker(dedup.WorkerDeps{Matcher: matcher})

	// Classifier. ClustersUpdaterFunc adapts the storage repo's
	// (id, ClusterMeta) signature to the classify-side MetaUpdate.
	classifier := classify.NewClassifier(classify.ClassifierDeps{
		LLM:   llmClient,
		Model: cfg.LLM.ClassifierModel,
	})
	clustersAdapter := classify.ClustersUpdaterFunc(func(ctx context.Context, id int64, m classify.MetaUpdate) error {
		return clustersRepo.UpdateMeta(ctx, id, postgres.ClusterMeta{
			Headline: m.Headline,
			Summary:  m.Summary,
			Topics:   m.Topics,
			Severity: m.Severity,
		})
	})
	classifyWorker := classify.NewWorker(classify.WorkerDeps{
		Classifier: classifier,
		Posts:      postsRepo,
		Clusters:   clustersAdapter,
		Publisher:  pub,
	})

	// Ranker. The storage snapshot type and the rank-side snapshot type
	// are intentionally distinct (rank does not import postgres) so we
	// bridge them with a small adapter that re-shapes the struct fields.
	ranker := rank.NewRanker(rank.RankerDeps{Clusters: rankerClustersAdapter{repo: clustersRepo}})
	rankWorker := rank.NewWorker(ranker)

	// Bot sender (delivery side; same Bot API token as cmd/bot-api).
	// Two processes hold the token simultaneously — for Phase 1 this is
	// acceptable because the bot-api process only does long-poll on the
	// updates endpoint and the processor only calls SendMessage; they do
	// not contend on a single API resource. SendOnly intentionally hides
	// the long-polling surface so the processor cannot accidentally call
	// getUpdates and contend with cmd/bot-api.
	sendOnly, err := bot.NewSendOnly(cfg.BotToken)
	if err != nil {
		return fmt.Errorf("new bot sender: %w", err)
	}
	sender := bot.NewSender(bot.SenderDeps{API: sendOnly, PerChatPerSec: 1})

	assembler := digest.NewAssembler(digest.AssemblerDeps{
		Settings:   settingsRepo,
		Clusters:   clustersRepo,
		Deliveries: deliveriesRepo,
		Sources:    channelsRepo,
		Sender:     sender,
	})
	deliveryWorker := digest.NewDeliveryWorker(assembler)

	// Normalizer. The storage-bound PostsRepoFunc closure keeps the
	// normalize package free of postgres types: NormalizedPostInsert is
	// the stable boundary, PostInsert is internal to storage.
	normalizeWorker := normalize.NewWorker(normalize.WorkerDeps{
		Publisher: pub,
		Posts: normalize.PostsRepoFunc(func(ctx context.Context, p normalize.NormalizedPostInsert) (int64, error) {
			return postsRepo.Insert(ctx, postgres.PostInsert{
				ChannelID: p.ChannelID,
				TGMsgID:   p.TGMsgID,
				TextRaw:   p.TextRaw,
				TextClean: p.TextClean,
				TextHash:  p.TextHash[:],
				Lang:      p.Lang,
				PostedAt:  p.PostedAt,
			})
		}),
		Channels: channelsRepo,
	})

	var wg sync.WaitGroup

	// Classifier debouncer runs alongside the consumer so cluster.updated
	// events can be coalesced. Without this, every cluster.updated would
	// trigger an immediate LLM call.
	wg.Add(1)
	go func() {
		defer wg.Done()
		classifyWorker.Run(rootCtx)
	}()

	// Borderline reconciler: drains the dedup:borderline list every 5
	// minutes, asks LLMJudge per pair, and merges clusters that the model
	// rules same-event with high confidence. Runs out of band so a slow LLM
	// never blocks the dedup hot path.
	judge := dedup.NewLLMJudge(dedup.LLMJudgeDeps{LLM: llmClient, Model: cfg.LLM.ClassifierModel})
	reconciler := dedup.NewReconciler(dedup.ReconcilerDeps{
		Queue:    borderlineQueue,
		Posts:    postsRepo,
		Clusters: clustersRepo,
		Judge:    judge,
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = reconciler.Run(rootCtx)
	}()

	// consume launches a JetStream consumer goroutine and tracks it on wg
	// so run() blocks until every consumer returns after rootCtx is done.
	consume := func(stream, subject, durable string, h queue.Handler) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sub.Run(rootCtx, queue.SubscribeConfig{
				Stream:     stream,
				Subject:    subject,
				Durable:    durable,
				MaxDeliver: 5,
				Handler:    h,
			}); err != nil && rootCtx.Err() == nil {
				slog.Error("subscriber exit",
					slog.String("subject", subject),
					slog.String("durable", durable),
					slog.Any("err", err),
				)
			}
		}()
	}

	consume(queue.StreamIngest, queue.SubjectRaw, "normalizer", normalizeWorker.Handle)
	consume(queue.StreamIngest, queue.SubjectNormalized, "dedup", dedupWorker.Handle)
	consume(queue.StreamClusters, queue.SubjectClusterUpdate, "classify", classifyWorker.Handle)
	consume(queue.StreamClusters, queue.SubjectClusterScored, "rank", rankWorker.Handle)
	consume(queue.StreamDelivery, queue.SubjectDeliverySched, "delivery", deliveryWorker.Handle)

	slog.Info("processor: pipeline running")
	wg.Wait()
	slog.Info("processor: stopped")
	return nil
}

// rankerClustersAdapter bridges *postgres.ClustersRepo to rank.ClustersRanker.
// The two packages each declare their own Snapshot struct (rank does not
// import postgres to stay storage-agnostic), so we copy fields explicitly.
type rankerClustersAdapter struct{ repo *postgres.ClustersRepo }

func (a rankerClustersAdapter) Snapshot(ctx context.Context, clusterID int64) (rank.Snapshot, error) {
	s, err := a.repo.Snapshot(ctx, clusterID)
	if err != nil {
		return rank.Snapshot{}, err
	}
	return rank.Snapshot{
		Coverage:     s.Coverage,
		Severity:     s.Severity,
		MaxAuthority: s.MaxAuthority,
	}, nil
}

func (a rankerClustersAdapter) SetScore(ctx context.Context, clusterID int64, score float32) error {
	return a.repo.SetScore(ctx, clusterID, score)
}
