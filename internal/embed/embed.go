// Package embed computes and stores message embeddings for semantic search.
//
// It is incremental and idempotent: only messages without an embedding for the
// configured model are embedded (keyed by stable message hash), so re-running
// after a fresh import embeds just the new messages. Embedding is the second
// network-egress step after import; it is a separate command so a plain import
// never makes LLM calls.
package embed

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/joestump/msgbrowse/internal/llm"
	"github.com/joestump/msgbrowse/internal/store"
)

// Options configures an embedding run.
type Options struct {
	// EmbedModel names the embedding model; recorded with each vector so a model
	// change re-embeds. Required.
	EmbedModel string
	// BatchSize is how many messages are sent per /embeddings request.
	BatchSize int
	// Prune removes embeddings whose message no longer exists before embedding.
	Prune bool
	// Logger receives progress; defaults to slog.Default().
	Logger *slog.Logger
}

// Summary reports what an embedding run did.
type Summary struct {
	Embedded   int
	Pruned     int64
	Batches    int
	DurationMS int64
}

// Run embeds every message that lacks an embedding for opts.EmbedModel, in
// batches, until none remain. It returns a summary. Individual batch failures
// abort the run (the next run resumes where this one stopped, since stored
// embeddings persist).
func Run(ctx context.Context, st *store.Store, client llm.Client, opts Options) (Summary, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	// Normalize the model string once. MessagesNeedingEmbedding and PutEmbedding
	// must agree on it exactly (the embeddings PK includes model); a stray
	// trailing space would otherwise make stored vectors never satisfy the
	// "needs embedding" query.
	model := strings.TrimSpace(opts.EmbedModel)
	if model == "" {
		return Summary{}, fmt.Errorf("embed: model not configured (set llm.embed_model)")
	}
	batch := opts.BatchSize
	if batch <= 0 || batch > 512 {
		batch = 64
	}
	start := time.Now()
	var sum Summary

	if opts.Prune {
		pruned, err := st.PruneOrphanEmbeddings(ctx)
		if err != nil {
			return sum, err
		}
		sum.Pruned = pruned
		if pruned > 0 {
			log.Info("pruned orphan embeddings", "count", pruned)
		}
	}

	total, err := st.CountMissingEmbeddings(ctx, model)
	if err != nil {
		return sum, err
	}
	if total == 0 {
		log.Info("embeddings up to date", "model", model)
		sum.DurationMS = time.Since(start).Milliseconds()
		return sum, nil
	}
	log.Info("embedding messages", "model", model, "to_embed", total, "batch_size", batch)

	// Bound the loop as a backstop against any future "no progress" seam (e.g. a
	// stored model string that never matches the query): embedding `total`
	// messages in batches of `batch` needs ceil(total/batch) iterations, so this
	// cap can never trip on a healthy run.
	maxBatches := total/batch + 2

	for {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		if sum.Batches >= maxBatches {
			return sum, fmt.Errorf("embed: aborting after %d batches with %d still pending — embeddings are not being recorded for model %q",
				sum.Batches, total-sum.Embedded, model)
		}
		targets, err := st.MessagesNeedingEmbedding(ctx, model, batch)
		if err != nil {
			return sum, err
		}
		if len(targets) == 0 {
			break
		}

		inputs := make([]string, len(targets))
		for i, t := range targets {
			inputs[i] = t.Text
		}
		vecs, err := client.Embed(ctx, inputs)
		if err != nil {
			return sum, fmt.Errorf("embed batch (%d msgs): %w", len(inputs), err)
		}
		if len(vecs) != len(targets) {
			return sum, fmt.Errorf("embed: provider returned %d vectors for %d inputs", len(vecs), len(targets))
		}
		for i, t := range targets {
			if err := st.PutEmbedding(ctx, t.Hash, model, vecs[i]); err != nil {
				return sum, err
			}
		}
		sum.Embedded += len(targets)
		sum.Batches++
		log.Debug("embedded batch", "batch", sum.Batches, "embedded", sum.Embedded, "of", total)
	}

	sum.DurationMS = time.Since(start).Milliseconds()
	log.Info("embedding complete", "embedded", sum.Embedded, "batches", sum.Batches, "duration_ms", sum.DurationMS)
	return sum, nil
}
