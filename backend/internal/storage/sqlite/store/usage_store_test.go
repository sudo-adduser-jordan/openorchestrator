package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sudo-adduser-jordan/open-agents/backend/internal/cdc"
	"github.com/sudo-adduser-jordan/open-agents/backend/internal/domain"
	"github.com/sudo-adduser-jordan/open-agents/backend/internal/storage/sqlite"
	"github.com/sudo-adduser-jordan/open-agents/backend/internal/storage/sqlite/sqlitetest"
)

func TestUsageBindingAndSourceIdempotency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()

	binding := mustUpsertUsageBinding(t, s, sess, now, domain.UsageBindingRecord{
		NativeRootID:   "root-thread",
		InitialModelID: "gpt-5",
		ProviderHint:   "openai",
		State:          domain.UsageBindingDiscovering,
	})
	again := mustUpsertUsageBinding(t, s, sess, now, domain.UsageBindingRecord{
		NativeRootID:   "root-thread",
		InitialModelID: "gpt-5.1",
		State:          domain.UsageBindingActive,
		UpdatedAt:      now.Add(time.Hour),
	})
	if again.ID != binding.ID {
		t.Fatalf("idempotent binding = %+v, want id %d", again, binding.ID)
	}
	if again.InitialModelID != "gpt-5.1" || again.State != domain.UsageBindingActive {
		t.Fatalf("refreshed binding = %+v", again)
	}
	if again.ProviderHint != "openai" {
		t.Fatalf("provider hint = %q, want retained openai", again.ProviderHint)
	}

	src := mustInsertUsageSource(t, s, now, domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceKind("kimi_wire"),
		NativeSessionID: "child-thread",
		ArtifactPath:    "/tmp/kimi/wire.jsonl",
		FileIdentity:    "dev:ino",
		State:           domain.UsageSourcePending,
	})
	srcAgain := mustInsertUsageSource(t, s, now.Add(time.Hour), domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceKind("kimi_wire"),
		NativeSessionID: "child-thread-updated",
		ArtifactPath:    "/tmp/kimi/wire.jsonl",
		FileIdentity:    "dev:ino:updated",
		ParserStateJSON: `{"version":1,"source_kind":"kimi_wire","kimi":{}}`,
		State:           domain.UsageSourcePending,
	})
	if srcAgain.ID != src.ID || srcAgain.NativeSessionID != "child-thread-updated" ||
		srcAgain.FileIdentity != "dev:ino" || srcAgain.ParserStateJSON != "{}" {
		t.Fatalf("idempotent source = %+v", srcAgain)
	}

	watchable, err := s.ListWatchableUsageSources(ctx)
	mustNoError(t, err, "watchable sources")
	if len(watchable) != 1 || watchable[0].ID != src.ID {
		t.Fatalf("watchable sources = %+v, want source %d", watchable, src.ID)
	}

	bindings, err := s.ListUsageBindingsForSession(ctx, sess.ID)
	mustNoError(t, err, "bindings")
	sources, err := s.ListUsageSourcesForBinding(ctx, binding.ID)
	mustNoError(t, err, "sources")
	aggregates, err := s.ListUsageModelAggregates(ctx, sess.ID)
	mustNoError(t, err, "aggregates")
	if len(bindings) != 1 || len(sources) != 1 || len(aggregates) != 0 {
		t.Fatalf("rows = bindings:%d sources:%d aggregates:%d, want 1/1/0", len(bindings), len(sources), len(aggregates))
	}
	pending, err := s.HasPendingUsageDiscovery(ctx)
	mustNoError(t, err, "check healthy discovery state")
	if pending {
		t.Fatal("healthy active binding requested discovery retry")
	}
	if _, err := s.UpdateUsageBindingState(
		ctx,
		binding.ID,
		domain.UsageBindingDiscovering,
		domain.UsageErrorSourceDiscoveryPending,
		now.Add(2*time.Hour),
	); err != nil {
		t.Fatalf("mark discovery pending: %v", err)
	}
	pending, err = s.HasPendingUsageDiscovery(ctx)
	mustNoError(t, err, "check pending discovery state")
	if !pending {
		t.Fatal("discovering binding did not request targeted retry")
	}
}

func TestUsageBindingUpsertDoesNotRegressSettledLifecycle(t *testing.T) {
	s := newTestStore(t)
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	binding := mustUpsertUsageBinding(t, s, sess, now, domain.UsageBindingRecord{
		NativeRootID:  "root-thread",
		State:         domain.UsageBindingFinalizing,
		LastErrorCode: "finalizing-warning",
	})
	got := mustUpsertUsageBinding(t, s, sess, now, domain.UsageBindingRecord{
		NativeRootID: "root-thread",
		State:        domain.UsageBindingActive,
		UpdatedAt:    now.Add(time.Second),
	})
	if got.ID != binding.ID || got.State != domain.UsageBindingFinalizing || got.LastErrorCode != "finalizing-warning" {
		t.Fatalf("stale upsert regressed binding: %+v", got)
	}
}

func TestFinalizeUsageBindingsForSessionLaunchIsGenerationAndRevisionFenced(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	sess.Metadata.RuntimeLaunchID = "launch-current"
	mustNoError(t, s.UpdateSession(ctx, sess))
	now := time.Unix(1700000000, 0).UTC()
	binding := mustUpsertUsageBinding(t, s, sess, now, domain.UsageBindingRecord{
		NativeRootID: "root-thread",
		State:        domain.UsageBindingActive,
	})
	sess, found, err := s.GetSession(ctx, sess.ID)
	if err != nil || !found {
		t.Fatalf("reload session before finalization: %v %v", found, err)
	}
	expectedRevision := sess.Revision

	finalized, err := s.FinalizeUsageBindingsForSessionLaunch(
		ctx,
		sess.ID,
		"launch-stale",
		expectedRevision,
		now.Add(time.Second),
	)
	if err != nil || len(finalized) != 0 {
		t.Fatalf("stale finalization rows=%+v err=%v", finalized, err)
	}
	got, ok, err := s.GetUsageBinding(ctx, sess.ID, sess.Harness, binding.NativeRootID)
	if err != nil || !ok || got.State != domain.UsageBindingActive {
		t.Fatalf("binding after stale finalization=%+v ok=%v err=%v", got, ok, err)
	}

	finalized, err = s.FinalizeUsageBindingsForSessionLaunch(
		ctx,
		sess.ID,
		"launch-current",
		expectedRevision-1,
		now.Add(2*time.Second),
	)
	if err != nil || len(finalized) != 0 {
		t.Fatalf("stale-revision finalization rows=%+v err=%v", finalized, err)
	}
	got, ok, err = s.GetUsageBinding(ctx, sess.ID, sess.Harness, binding.NativeRootID)
	if err != nil || !ok || got.State != domain.UsageBindingActive {
		t.Fatalf("binding after stale-revision finalization=%+v ok=%v err=%v", got, ok, err)
	}
	// A no-op timestamp write still invalidates an observer's session snapshot.
	mustNoError(t, s.UpdateSession(ctx, sess))
	finalized, err = s.FinalizeUsageBindingsForSessionLaunch(ctx, sess.ID, "launch-current", expectedRevision, now)
	if err != nil || len(finalized) != 0 {
		t.Fatalf("unchanged timestamp admitted stale usage finalization: rows=%+v err=%v", finalized, err)
	}
	sess, ok, err = s.GetSession(ctx, sess.ID)
	if err != nil || !ok {
		t.Fatalf("reload session: %v %v", ok, err)
	}
	expectedRevision = sess.Revision

	finalized, err = s.FinalizeUsageBindingsForSessionLaunch(
		ctx,
		sess.ID,
		"launch-current",
		expectedRevision,
		now.Add(3*time.Second),
	)
	if err != nil || len(finalized) != 1 || finalized[0].State != domain.UsageBindingFinalizing {
		t.Fatalf("current finalization rows=%+v err=%v", finalized, err)
	}
	if _, err := s.UpdateUsageBindingState(ctx, binding.ID, domain.UsageBindingActive, "", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	sess.IsTerminated = true
	mustNoError(t, s.UpdateSession(ctx, sess))
	finalized, err = s.FinalizeUsageBindingsForSessionLaunch(
		ctx,
		sess.ID,
		"launch-current",
		expectedRevision,
		now.Add(5*time.Second),
	)
	if err != nil || len(finalized) != 0 {
		t.Fatalf("terminated finalization rows=%+v err=%v", finalized, err)
	}
	got, ok, err = s.GetUsageBinding(ctx, sess.ID, sess.Harness, binding.NativeRootID)
	if err != nil || !ok || got.State != domain.UsageBindingActive {
		t.Fatalf("binding after terminated finalization=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestInsertUsageSourceErrorRedactsArtifactPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	source := seedUsageSource(t, s, sess, now)
	secretPath := "/private/transcripts/customer-session.jsonl"

	_, err := s.InsertUsageSource(ctx, domain.UsageSourceRecord{
		BindingID:    source.BindingID,
		Kind:         domain.UsageSourceKind("invalid"),
		ArtifactPath: secretPath,
		State:        domain.UsageSourcePending,
		UpdatedAt:    now,
	})
	if err == nil {
		t.Fatal("expected invalid source insert to fail")
	}
	if strings.Contains(err.Error(), secretPath) {
		t.Fatalf("store error exposed artifact path: %v", err)
	}
}

func TestInsertUsageSourceRejectsNonObjectParserState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	binding := mustUpsertUsageBinding(t, s, sess, now, domain.UsageBindingRecord{
		NativeRootID: "root-thread",
		State:        domain.UsageBindingActive,
	})
	_, err := s.InsertUsageSource(ctx, domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceKind("kimi_wire"),
		ArtifactPath:    "/tmp/kimi/wire.jsonl",
		ParserStateJSON: `[]`,
		State:           domain.UsageSourcePending,
		UpdatedAt:       now,
	})
	if err == nil || !strings.Contains(err.Error(), "parser state") {
		t.Fatalf("insert error = %v, want parser state object error", err)
	}
}

func TestReplaceUsageSourceRollsBackRetirementWhenInsertFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	source := seedUsageSource(t, s, sess, now)

	_, err := s.ReplaceUsageSource(ctx, source.ID, domain.UsageErrorArtifactReplaced, domain.UsageSourceRecord{
		BindingID:       source.BindingID,
		Kind:            domain.UsageSourceKind("invalid"),
		NativeSessionID: source.NativeSessionID,
		ArtifactPath:    "/tmp/kimi/replacement.jsonl",
		FileIdentity:    "replacement",
		Generation:      source.Generation + 1,
		State:           domain.UsageSourcePending,
		UpdatedAt:       now.Add(time.Second),
	}, now.Add(time.Second))
	if err == nil {
		t.Fatal("expected replacement insert to fail")
	}

	sources, err := s.ListUsageSourcesForBinding(ctx, source.BindingID)
	mustNoError(t, err)
	if len(sources) != 1 {
		t.Fatalf("sources = %+v, want only original source", sources)
	}
	if sources[0].ID != source.ID || sources[0].State != domain.UsageSourcePending || sources[0].LastErrorCode != "" {
		t.Fatalf("original source was retired despite rollback: %+v", sources[0])
	}
}

func TestUsageMutationsEmitSessionUpdatedCDC(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	base, err := s.LatestSeq(ctx)
	mustNoError(t, err)

	binding := mustUpsertUsageBinding(t, s, sess, now, domain.UsageBindingRecord{
		NativeRootID: "root-thread",
		State:        domain.UsageBindingActive,
	})
	assertUsageSessionUpdatedEvents(t, s, base, sess, 1)

	base, err = s.LatestSeq(ctx)
	mustNoError(t, err)
	source := mustInsertUsageSource(t, s, now, domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceKind("kimi_wire"),
		NativeSessionID: "child-thread",
		ArtifactPath:    "/tmp/kimi/wire.jsonl",
		FileIdentity:    "dev:ino",
		State:           domain.UsageSourcePending,
	})
	assertUsageSessionUpdatedEvents(t, s, base, sess, 0)

	base, err = s.LatestSeq(ctx)
	mustNoError(t, err)
	err = s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10,
		State:      domain.UsageSourceActive,
		UpdatedAt:  now,
	}, []domain.ModelUsageEvent{usageEvent(
		"event-1",
		canonicalUsageTokens(10, 0, 10, 2),
	)})
	mustNoError(t, err)
	// New totals invalidate usage once after the transaction commits.
	assertUsageSessionUpdatedEvents(t, s, base, sess, 1)

	base, err = s.LatestSeq(ctx)
	mustNoError(t, err)
	current, ok, err := s.GetUsageSourceForIngestion(ctx, source.ID)
	mustNoError(t, err)
	if !ok {
		t.Fatal("usage source disappeared")
	}
	err = s.ApplyUsageChunk(ctx, source.ID, 10, current.Source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 20,
		State:      domain.UsageSourceActive,
		UpdatedAt:  now.Add(time.Second),
	}, nil)
	mustNoError(t, err)
	assertUsageSessionUpdatedEvents(t, s, base, sess, 0)
}

func assertUsageSessionUpdatedEvents(
	t *testing.T,
	s *sqlite.Store,
	after int64,
	session domain.SessionRecord,
	want int,
) {
	t.Helper()
	events, err := s.EventsAfter(context.Background(), after, 100)
	mustNoError(t, err)
	if len(events) != want {
		t.Fatalf("events = %+v, want %d", events, want)
	}
	for _, event := range events {
		if event.Type != cdc.EventSessionUpdated ||
			event.ProjectID != string(session.ProjectID) ||
			event.SessionID != string(session.ID) {
			t.Fatalf("decoded usage event = %+v", event)
		}
		var payload map[string]any
		mustNoError(t, json.Unmarshal(event.Payload, &payload))
		if payload["id"] != string(session.ID) || len(payload) != 1 {
			t.Fatalf("usage event payload = %v, want id-only payload", payload)
		}
	}
}

func TestApplyUsageChunkAtomicReplayAndTokenAggregates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	source := seedUsageSource(t, s, sess, now)

	event := usageEvent("event-1", canonicalUsageTokens(100, 50, 50, 20))
	event.ProviderUsageJSON = kimiProviderUsage(10, 3)

	err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset:      100,
		State:           domain.UsageSourceActive,
		ParserStateJSON: `{"version":1,"source_kind":"kimi_wire","kimi":{"baseline":{"input_tokens":100}}}`,
		UpdatedAt:       now,
	}, []domain.ModelUsageEvent{event})
	if err != nil {
		t.Fatalf("apply chunk: %v", err)
	}
	err = s.ApplyUsageChunk(ctx, source.ID, 100, now, domain.SourceCursorState{
		ByteOffset:      120,
		State:           domain.UsageSourceActive,
		ParserStateJSON: `{"version":1,"source_kind":"kimi_wire","kimi":{"baseline":{"input_tokens":100},"model_id":"gpt-5.6"}}`,
		UpdatedAt:       now.Add(time.Second),
	}, []domain.ModelUsageEvent{event})
	if err != nil {
		t.Fatalf("apply duplicate chunk: %v", err)
	}
	aggs, err := s.ListUsageModelAggregates(ctx, sess.ID)
	mustNoError(t, err, "aggregates")
	if len(aggs) != 1 {
		t.Fatalf("aggregates = %+v, want one row", aggs)
	}
	got := aggs[0]
	if usageTokenValue(got.Tokens.InputTokens) != 100 || usageTokenValue(got.Tokens.OutputTokens) != 20 {
		t.Fatalf("aggregate tokens = %+v", got.Tokens)
	}

	ctxRow, ok, err := s.GetUsageSourceForIngestion(ctx, source.ID)
	if err != nil || !ok {
		t.Fatalf("get source context: ok=%v err=%v", ok, err)
	}
	if ctxRow.Source.ByteOffset != 120 || ctxRow.NativeRootID != "root-thread" ||
		!strings.Contains(ctxRow.Source.ParserStateJSON, `"model_id":"gpt-5.6"`) ||
		ctxRow.InitialModelID != "gpt-5" || ctxRow.BindingState != domain.UsageBindingActive {
		t.Fatalf("source context = %+v", ctxRow)
	}
}

func TestApplyUsageChunkPersistsProviderSplits(t *testing.T) {
	dataDir := t.TempDir()
	s := sqlitetest.MustOpenAt(t, dataDir)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	binding := mustUpsertUsageBinding(t, s, sess, now, domain.UsageBindingRecord{
		NativeRootID:   "root-thread",
		InitialModelID: " gpt-4o ",
		ProviderHint:   " openai ",
		State:          domain.UsageBindingActive,
	})
	source := mustInsertUsageSource(t, s, now, domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceKind("kimi_wire"),
		NativeSessionID: "root-thread",
		ArtifactPath:    "/tmp/agent/transcript.jsonl",
		State:           domain.UsageSourcePending,
	})
	fiveMinutes, oneHour := int64(7), int64(3)
	providerUsage := openaiProviderUsage(5, 10, &fiveMinutes, &oneHour)
	event := domain.ModelUsageEvent{
		ProviderID:        domain.UsageProviderOpenAI,
		BillingProviderID: "source-provider", BillingProviderSource: domain.UsageBillingProviderObserved,
		ModelID:           " source-model ",
		MeasurementKind:   domain.UsageMeasurementNativeReported,
		Tokens:            canonicalUsageTokens(20, 5, 15, 4),
		ProviderUsageJSON: providerUsage,
		SourceEventKey:    "event-cost",
	}
	if err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10, State: domain.UsageSourceActive, UpdatedAt: now,
	}, []domain.ModelUsageEvent{event}); err != nil {
		t.Fatalf("apply priced source event: %v", err)
	}

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "open-agents.db"))
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	var providerHint, billingProviderID, modelID, measurementKind, storedUsage string
	if err := raw.QueryRow(`
SELECT ub.provider_hint, mue.billing_provider_id, mue.model_id,
       mue.usage_measurement_kind, mue.provider_usage_json
FROM model_usage_events mue
JOIN usage_bindings ub ON ub.id = mue.binding_id
WHERE mue.source_event_key = 'event-cost'`).Scan(
		&providerHint, &billingProviderID, &modelID, &measurementKind, &storedUsage,
	); err != nil {
		t.Fatalf("read persisted source facts: %v", err)
	}
	if providerHint != "openai" || billingProviderID != "source-provider" || modelID != "source-model" ||
		measurementKind != string(domain.UsageMeasurementNativeReported) || storedUsage != providerUsage {
		t.Fatalf("persisted facts = hint:%q billing:%q model:%q kind:%q usage:%s",
			providerHint, billingProviderID, modelID, measurementKind, storedUsage)
	}
	// The cost columns still exist in the table, but nothing writes them, so a
	// fresh event must leave the estimates NULL and the version at its default.
	var gotInput, gotCachedInput, gotOutput, gotTotal sql.NullInt64
	var pricingVersion string
	if err := raw.QueryRow(`
SELECT input_cost_nanos, cached_input_cost_nanos, output_cost_nanos,
       estimated_cost_nanos, pricing_version
FROM model_usage_events WHERE source_event_key = 'event-cost'`).Scan(
		&gotInput, &gotCachedInput, &gotOutput, &gotTotal, &pricingVersion,
	); err != nil {
		t.Fatalf("read cost columns: %v", err)
	}
	if gotInput.Valid || gotCachedInput.Valid || gotOutput.Valid || gotTotal.Valid || pricingVersion != "" {
		t.Fatalf("cost columns were written: %v/%v/%v/%v version=%q",
			gotInput, gotCachedInput, gotOutput, gotTotal, pricingVersion)
	}
	contextRow, ok, err := s.GetUsageSourceForIngestion(ctx, source.ID)
	if err != nil || !ok || contextRow.ProviderHint != "openai" || contextRow.InitialModelID != "gpt-4o" {
		t.Fatalf("source context = %+v, ok=%v err=%v", contextRow, ok, err)
	}
}

// Break caught: provider backfill could miss source-exact case/alias variants,
// revisit the active version, include immutable zero totals, or process an
// unbounded/non-deterministic page.
func TestApplyUsageChunkReplayComparesNewSourceFacts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	source := seedUsageSource(t, s, sess, now)
	fiveMinutes, oneHour := int64(7), int64(3)
	event := openaiUsageEvent("event-1", 5, 10, 5, 4)
	event.BillingProviderID = "openai"
	event.BillingProviderSource = domain.UsageBillingProviderObserved
	event.ProviderUsageJSON = openaiProviderUsage(5, 10, &fiveMinutes, &oneHour)
	if err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{ByteOffset: 10, State: domain.UsageSourceActive, UpdatedAt: now}, []domain.ModelUsageEvent{event}); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	providerConflict := event
	providerConflict.BillingProviderID = "zai"
	if err := s.ApplyUsageChunk(ctx, source.ID, 10, now, domain.SourceCursorState{ByteOffset: 30, State: domain.UsageSourceActive, UpdatedAt: now.Add(2 * time.Second)}, []domain.ModelUsageEvent{providerConflict}); !errors.Is(err, domain.ErrUsageSourceEventConflict) {
		t.Fatalf("provider replay err = %v, want source conflict", err)
	}
	otherFiveMinutes, otherOneHour := int64(6), int64(4)
	splitConflict := event
	splitConflict.ProviderUsageJSON = openaiProviderUsage(5, 10, &otherFiveMinutes, &otherOneHour)
	if err := s.ApplyUsageChunk(ctx, source.ID, 10, now, domain.SourceCursorState{ByteOffset: 30, State: domain.UsageSourceActive, UpdatedAt: now.Add(2 * time.Second)}, []domain.ModelUsageEvent{splitConflict}); !errors.Is(err, domain.ErrUsageSourceEventConflict) {
		t.Fatalf("split replay err = %v, want source conflict", err)
	}

	// A stored NULL predates the capture, so a replay carrying the object is an
	// enrichment rather than a conflict.
	kindConflict := event
	kindConflict.MeasurementKind = domain.UsageMeasurementUnknown
	if err := s.ApplyUsageChunk(ctx, source.ID, 10, now, domain.SourceCursorState{ByteOffset: 30, State: domain.UsageSourceActive, UpdatedAt: now.Add(2 * time.Second)}, []domain.ModelUsageEvent{kindConflict}); !errors.Is(err, domain.ErrUsageSourceEventConflict) {
		t.Fatalf("measurement kind replay err = %v, want source conflict", err)
	}
}

func TestApplyUsageChunkLegacyNullProviderReplayUsesGenericTokenFacts(t *testing.T) {
	dataDir := t.TempDir()
	s := sqlitetest.MustOpenAt(t, dataDir)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	source := seedUsageSource(t, s, sess, now)
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "open-agents.db"))
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.Exec(`
INSERT INTO model_usage_events (
    binding_id, usage_source_id, provider_id, billing_provider_id, model_id,
    usage_measurement_kind, input_tokens, cached_input_tokens,
    uncached_input_tokens, output_tokens, provider_usage_json, source_event_key
) VALUES (?, ?, 'openai', NULL, 'gpt-5', 'native_reported', 20, 5, 15, 4, NULL, 'legacy-event')`, source.BindingID, source.ID); err != nil {
		t.Fatalf("seed legacy event: %v", err)
	}
	replay := usageEvent("legacy-event", canonicalUsageTokens(20, 5, 15, 4))
	replay.BillingProviderID = "openai"
	replay.BillingProviderSource = domain.UsageBillingProviderObserved
	if err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10, State: domain.UsageSourceActive, UpdatedAt: now,
	}, []domain.ModelUsageEvent{replay}); err != nil {
		t.Fatalf("legacy replay conflict: %v", err)
	}
}

func TestApplyUsageChunkRejectsBlankProviderOnNewEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	source := seedUsageSource(t, s, sess, now)
	event := usageEvent("event-blank-provider", canonicalUsageTokens(1, 0, 1, 1))
	event.ProviderID = "  "
	if err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10, State: domain.UsageSourceActive, UpdatedAt: now,
	}, []domain.ModelUsageEvent{event}); err == nil {
		t.Fatal("blank provider event was persisted")
	}
	assertUsageSourceOffset(t, s, source.ID, 0)
}

func TestApplyUsageChunkRejectsConflictsAndPreservesCursor(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	source := seedUsageSource(t, s, sess, now)

	if err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{ByteOffset: 50, State: domain.UsageSourceActive, UpdatedAt: now}, []domain.ModelUsageEvent{
		usageEvent("event-1", canonicalUsageTokens(10, 0, 10, 1)),
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	conflict := usageEvent("event-1", canonicalUsageTokens(11, 0, 11, 1))
	err := s.ApplyUsageChunk(ctx, source.ID, 50, now, domain.SourceCursorState{ByteOffset: 80, State: domain.UsageSourceActive, UpdatedAt: now}, []domain.ModelUsageEvent{
		usageEvent("event-2", canonicalUsageTokens(4, 0, 4, 1)),
		conflict,
	})
	if !errors.Is(err, domain.ErrUsageSourceEventConflict) {
		t.Fatalf("conflict err = %v, want ErrUsageSourceEventConflict", err)
	}
	assertUsageSourceOffset(t, s, source.ID, 50)
	aggregates, err := s.ListUsageModelAggregates(ctx, sess.ID)
	mustNoError(t, err)
	if len(aggregates) != 1 || usageTokenValue(aggregates[0].Tokens.InputTokens)+usageTokenValue(aggregates[0].Tokens.OutputTokens) != 11 {
		t.Fatalf("rolled-back chunk persisted events: %+v", aggregates)
	}

	bad := usageEvent("event-2", canonicalUsageTokens(10, 11, 10, 1))
	if err := s.ApplyUsageChunk(ctx, source.ID, 50, now, domain.SourceCursorState{ByteOffset: 90, State: domain.UsageSourceActive, UpdatedAt: now}, []domain.ModelUsageEvent{bad}); err == nil {
		t.Fatal("expected invalid event insert to fail")
	}
	assertUsageSourceOffset(t, s, source.ID, 50)

	if err := s.ApplyUsageChunk(ctx, source.ID, 0, now, domain.SourceCursorState{ByteOffset: 60, State: domain.UsageSourceActive, UpdatedAt: now}, nil); !errors.Is(err, domain.ErrUsageSourceOffsetConflict) {
		t.Fatalf("offset err = %v, want ErrUsageSourceOffsetConflict", err)
	}
	assertUsageSourceOffset(t, s, source.ID, 50)
}

func TestApplyUsageChunkProviderUsageConflictsRollback(t *testing.T) {
	tests := []struct {
		name        string
		harness     domain.AgentHarness
		wantInput   int64
		wantOutput  int64
		baseEvent   func(string) domain.ModelUsageEvent
		conflicting func(string) domain.ModelUsageEvent
	}{
		{
			name: "OpenAI", harness: domain.HarnessOpenCode, wantInput: 10, wantOutput: 1,
			baseEvent: func(key string) domain.ModelUsageEvent {
				return usageEvent(key, canonicalUsageTokens(10, 0, 10, 1))
			},
			conflicting: func(key string) domain.ModelUsageEvent {
				event := usageEvent(key, canonicalUsageTokens(10, 0, 10, 1))
				event.ProviderUsageJSON = kimiProviderUsage(0, 1)
				return event
			},
		},
		{
			name: "OpenAI", harness: domain.HarnessOpenCode, wantInput: 20, wantOutput: 4,
			baseEvent: func(key string) domain.ModelUsageEvent {
				return openaiUsageEvent(key, 10, 3, 7, 4)
			},
			conflicting: func(key string) domain.ModelUsageEvent {
				event := openaiUsageEvent(key, 10, 3, 7, 4)
				event.ProviderUsageJSON = openaiProviderUsage(10, 4, nil, nil)
				return event
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			sess := seedUsageSession(t, s, test.harness)
			now := time.Unix(1700000000, 0).UTC()
			source := seedUsageSource(t, s, sess, now)
			if err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
				ByteOffset: 50, State: domain.UsageSourceActive, UpdatedAt: now,
			}, []domain.ModelUsageEvent{test.baseEvent("event-1")}); err != nil {
				t.Fatalf("seed provider event: %v", err)
			}

			err := s.ApplyUsageChunk(ctx, source.ID, 50, now, domain.SourceCursorState{
				ByteOffset: 80, State: domain.UsageSourceActive, UpdatedAt: now.Add(time.Second),
			}, []domain.ModelUsageEvent{test.baseEvent("event-2"), test.conflicting("event-1")})
			if !errors.Is(err, domain.ErrUsageSourceEventConflict) {
				t.Fatalf("provider conflict err = %v, want ErrUsageSourceEventConflict", err)
			}
			assertUsageSourceOffset(t, s, source.ID, 50)
			aggregates, err := s.ListUsageModelAggregates(ctx, sess.ID)
			mustNoError(t, err)
			if len(aggregates) != 1 || usageTokenValue(aggregates[0].Tokens.InputTokens) != test.wantInput ||
				usageTokenValue(aggregates[0].Tokens.OutputTokens) != test.wantOutput {
				t.Fatalf("provider conflict persisted partial chunk: %+v", aggregates)
			}
		})
	}
}

// An event stored before the bounded object was captured has provider_usage_json
// NULL. Replaying its durable prefix must fill that in rather than either
// rejecting the event or inserting a duplicate.
func TestApplyUsageChunkProviderUsageEnrichmentAdvancesCursorWithoutDuplicate(t *testing.T) {
	tests := []struct {
		name       string
		harness    domain.AgentHarness
		wantInput  int64
		wantOutput int64
		richer     func() domain.ModelUsageEvent
	}{
		{
			name: "OpenAI", harness: domain.HarnessOpenCode, wantInput: 10, wantOutput: 1,
			richer: func() domain.ModelUsageEvent {
				event := usageEvent("event-1", canonicalUsageTokens(10, 0, 10, 1))
				event.ProviderUsageJSON = kimiProviderUsage(0, 1)
				return event
			},
		},
		{
			name: "OpenAI", harness: domain.HarnessOpenCode, wantInput: 20, wantOutput: 4,
			richer: func() domain.ModelUsageEvent {
				fiveM, oneH := int64(2), int64(1)
				event := openaiUsageEvent("event-1", 10, 3, 7, 4)
				event.ProviderUsageJSON = openaiProviderUsage(10, 3, &fiveM, &oneH)
				return event
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			s := sqlitetest.MustOpenAt(t, dataDir)
			ctx := context.Background()
			sess := seedUsageSession(t, s, test.harness)
			now := time.Unix(1700000000, 0).UTC()
			source := seedUsageSource(t, s, sess, now)

			base := test.richer()
			base.ProviderUsageJSON = ""
			if err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
				ByteOffset: 50, State: domain.UsageSourceActive, UpdatedAt: now,
			}, []domain.ModelUsageEvent{base}); err != nil {
				t.Fatalf("seed provider event: %v", err)
			}
			if err := s.ApplyUsageChunk(ctx, source.ID, 50, now, domain.SourceCursorState{
				ByteOffset: 80, State: domain.UsageSourceActive, UpdatedAt: now.Add(time.Second),
			}, []domain.ModelUsageEvent{test.richer()}); err != nil {
				t.Fatalf("enrich provider event: %v", err)
			}
			assertUsageSourceOffset(t, s, source.ID, 80)
			aggregates, err := s.ListUsageModelAggregates(ctx, sess.ID)
			mustNoError(t, err)
			if len(aggregates) != 1 || usageTokenValue(aggregates[0].Tokens.InputTokens) != test.wantInput ||
				usageTokenValue(aggregates[0].Tokens.OutputTokens) != test.wantOutput {
				t.Fatalf("enrichment duplicated provider event: %+v", aggregates)
			}
			if stored := readStoredProviderUsage(t, dataDir, "event-1"); stored != test.richer().ProviderUsageJSON {
				t.Fatalf("stored provider usage = %q, want the replayed object", stored)
			}
		})
	}
}

func readStoredProviderUsage(t *testing.T, dataDir, sourceEventKey string) string {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "open-agents.db"))
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	var stored sql.NullString
	if err := raw.QueryRow(
		`SELECT provider_usage_json FROM model_usage_events WHERE source_event_key = ?`, sourceEventKey,
	).Scan(&stored); err != nil {
		t.Fatalf("read stored provider usage: %v", err)
	}
	return stored.String
}

func TestUsageBindingIgnoresChildrenFromSupersededKimiGeneration(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	const (
		rootID  = "11111111-1111-4111-8111-111111111111"
		childID = "22222222-2222-4222-8222-222222222222"
	)
	binding := mustUpsertUsageBinding(t, s, sess, now, domain.UsageBindingRecord{
		NativeRootID: rootID,
		State:        domain.UsageBindingFinalizing,
	})
	oldState := `{"version":1,"source_kind":"kimi_wire","kimi":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":["` + childID + `"]}}`
	emptyState := `{"version":1,"source_kind":"kimi_wire","kimi":{"baseline":{},"pending_spawn_call_ids":[],"discovered_child_ids":[]}}`
	for generation, state := range []string{oldState, emptyState} {
		mustInsertUsageSource(t, s, now, domain.UsageSourceRecord{
			BindingID:       binding.ID,
			Kind:            domain.UsageSourceKind("kimi_wire"),
			NativeSessionID: rootID,
			ArtifactPath:    "/tmp/kimi/root.jsonl",
			FileIdentity:    fmt.Sprintf("root-%d", generation),
			Generation:      int64(generation),
			ParserStateJSON: state,
			State:           domain.UsageSourceComplete,
		})
	}

	completed, err := s.CompleteUsageBindingIfSettled(ctx, binding.ID, now)
	if err != nil || !completed {
		sources, _ := s.ListUsageSourcesForBinding(ctx, binding.ID)
		got, _, _ := s.GetUsageBinding(ctx, sess.ID, sess.Harness, rootID)
		t.Fatalf("completion with superseded child edge = %v, err=%v, binding=%+v, sources=%+v", completed, err, got, sources)
	}
}

func TestUsageRowsCascadeWhenSeedSessionDeleted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "usage")
	now := time.Unix(1700000000, 0).UTC()
	sess, err := s.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "usage",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessOpenCode,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: now},
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create seed session: %v", err)
	}
	source := seedUsageSource(t, s, sess, now)
	if err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{ByteOffset: 10, State: domain.UsageSourceComplete, UpdatedAt: now}, []domain.ModelUsageEvent{
		usageEvent("event-1", canonicalUsageTokens(1, 0, 1, 1)),
	}); err != nil {
		t.Fatalf("apply event: %v", err)
	}

	deleted, err := s.DeleteSession(ctx, sess.ID)
	if err != nil || !deleted {
		t.Fatalf("delete seed session = %v, %v; want true nil", deleted, err)
	}
	bindings, err := s.ListUsageBindingsForSession(ctx, sess.ID)
	mustNoError(t, err, "bindings after delete")
	aggregates, err := s.ListUsageModelAggregates(ctx, sess.ID)
	mustNoError(t, err, "aggregates after delete")
	if len(bindings) != 0 || len(aggregates) != 0 {
		t.Fatalf("rows after delete = bindings:%d aggregates:%d, want zero", len(bindings), len(aggregates))
	}
}

func TestUsageAggregatesMergeProvidersPerModel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	source := seedUsageSource(t, s, sess, now)
	events := []domain.ModelUsageEvent{
		{
			ProviderID: domain.UsageProviderOpenAI, BillingProviderID: "openai",
			BillingProviderSource: domain.UsageBillingProviderObserved,
			ModelID:               "shared-model", SourceEventKey: "complete",
			MeasurementKind: domain.UsageMeasurementNativeReported,
			Tokens:          canonicalUsageTokens(10, 4, 6, 2),
		},
		{
			ProviderID: domain.UsageProviderOpenAI, BillingProviderID: "zai",
			BillingProviderSource: domain.UsageBillingProviderInferred,
			ModelID:               "shared-model", SourceEventKey: "partial",
			MeasurementKind: domain.UsageMeasurementNativeReported,
			Tokens:          canonicalUsageTokens(5, 0, 5, 1),
		},
	}
	if err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10, State: domain.UsageSourceComplete, UpdatedAt: now,
	}, events); err != nil {
		t.Fatalf("apply cost events: %v", err)
	}

	// One model is one row even when two providers served it; splitting the row
	// only ever exposed Open Agents's own attribution state as a duplicate model.
	models, err := s.ListUsageModelAggregates(ctx, sess.ID)
	mustNoError(t, err)
	if len(models) != 1 || models[0].ModelID != "shared-model" {
		t.Fatalf("model rows = %+v, want one merged row", models)
	}
	tokens := models[0].Tokens
	if usageTokenValue(tokens.InputTokens) != 15 || usageTokenValue(tokens.CachedInputTokens) != 4 ||
		usageTokenValue(tokens.UncachedInputTokens) != 11 || usageTokenValue(tokens.OutputTokens) != 3 {
		t.Fatalf("merged tokens = %+v", tokens)
	}

	compact, err := s.ListCompactSessionUsageAggregates(ctx, sess.ProjectID)
	mustNoError(t, err)
	if len(compact) != 1 || usageTokenValue(compact[0].ProcessedTokens) != 18 {
		t.Fatalf("compact rows = %+v", compact)
	}
}

func TestUsageAggregatesReturnSQLiteIntegerOverflow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	source := seedUsageSource(t, s, sess, now)
	if err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10, State: domain.UsageSourceComplete, UpdatedAt: now,
	}, []domain.ModelUsageEvent{
		usageEvent("overflow-1", canonicalUsageTokens(math.MaxInt64, 0, math.MaxInt64, 0)),
		usageEvent("overflow-2", canonicalUsageTokens(math.MaxInt64, 0, math.MaxInt64, 0)),
	}); err != nil {
		t.Fatalf("apply overflow events: %v", err)
	}
	if _, err := s.ListUsageModelAggregates(ctx, sess.ID); err == nil || !strings.Contains(err.Error(), "integer overflow") {
		t.Fatalf("detail overflow error = %v", err)
	}
	if _, err := s.ListCompactSessionUsageAggregates(ctx, sess.ProjectID); err == nil || !strings.Contains(err.Error(), "integer overflow") {
		t.Fatalf("compact overflow error = %v", err)
	}
}

func TestListCompactSessionUsageAggregatesAndFiltersByProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	seedProject(t, s, "usage")
	seedProject(t, s, "other")

	usageRec := sampleRecord("usage")
	usageRec.Harness = domain.HarnessOpenCode
	usageSession, err := s.CreateSession(ctx, usageRec)
	mustNoError(t, err, "create usage session")
	otherSession, err := s.CreateSession(ctx, sampleRecord("other"))
	mustNoError(t, err, "create other session")
	source := seedUsageSource(t, s, usageSession, now)
	if err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10,
		State:      domain.UsageSourceComplete,
		UpdatedAt:  now,
	}, []domain.ModelUsageEvent{
		usageEvent("event-1", canonicalUsageTokens(100, 50, 50, 20)),
		usageEvent("event-2", canonicalUsageTokens(50, 20, 30, 10)),
	}); err != nil {
		t.Fatalf("apply usage events: %v", err)
	}
	if _, err := s.MarkUsageSourceState(ctx, source.ID, domain.UsageSourceComplete, domain.UsageErrorArtifactReplaced, nil, now); err != nil {
		t.Fatalf("retire original source: %v", err)
	}
	mustInsertUsageSource(t, s, now, domain.UsageSourceRecord{
		BindingID:       source.BindingID,
		Kind:            source.Kind,
		NativeSessionID: source.NativeSessionID,
		ArtifactPath:    source.ArtifactPath,
		FileIdentity:    "replacement",
		Generation:      1,
		State:           domain.UsageSourceComplete,
	})
	if _, err := s.UpdateUsageBindingState(ctx, source.BindingID, domain.UsageBindingComplete, "", now); err != nil {
		t.Fatalf("complete replacement binding: %v", err)
	}

	got, err := s.ListCompactSessionUsageAggregates(ctx, "usage")
	mustNoError(t, err, "list compact usage")
	if len(got) != 1 || got[0].SessionID != usageSession.ID {
		t.Fatalf("filtered rows = %+v, want only %s (not %s)", got, usageSession.ID, otherSession.ID)
	}
	row := got[0]
	if usageTokenValue(row.ProcessedTokens) != 180 || row.Incomplete {
		t.Fatalf("aggregate = %+v", row)
	}
	all, err := s.ListCompactSessionUsageAggregates(ctx, "")
	mustNoError(t, err, "list all compact usage")
	if len(all) != 1 {
		t.Fatalf("all rows = %d, want only sessions with usage", len(all))
	}
}

func TestListCompactSessionUsageSeparatesRetriesFromIntegrityFailures(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	transientSession := seedUsageSession(t, s, domain.HarnessOpenCode)
	transientSource := seedUsageSource(t, s, transientSession, now)
	if err := s.ApplyUsageChunk(ctx, transientSource.ID, 0, transientSource.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 1,
		State:      domain.UsageSourceActive,
		UpdatedAt:  now,
	}, []domain.ModelUsageEvent{
		usageEvent("transient-event", canonicalUsageTokens(1, 0, 1, 0)),
	}); err != nil {
		t.Fatalf("seed transient usage: %v", err)
	}
	if _, err := s.MarkUsageSourceState(
		ctx,
		transientSource.ID,
		domain.UsageSourceError,
		domain.UsageErrorSourceReadFailed,
		nil,
		now,
	); err != nil {
		t.Fatalf("mark transient failure: %v", err)
	}

	incompleteSession := seedUsageSession(t, s, domain.HarnessOpenCode)
	incompleteSource := seedUsageSource(t, s, incompleteSession, now)
	if err := s.ApplyUsageChunk(ctx, incompleteSource.ID, 0, incompleteSource.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 1,
		State:      domain.UsageSourceActive,
		UpdatedAt:  now,
	}, []domain.ModelUsageEvent{
		usageEvent("incomplete-event", canonicalUsageTokens(1, 0, 1, 0)),
	}); err != nil {
		t.Fatalf("seed incomplete usage: %v", err)
	}
	if _, err := s.MarkUsageSourceState(
		ctx,
		incompleteSource.ID,
		domain.UsageSourceComplete,
		domain.UsageErrorSourceEventConflict,
		nil,
		now,
	); err != nil {
		t.Fatalf("mark integrity failure: %v", err)
	}

	rows, err := s.ListCompactSessionUsageAggregates(ctx, transientSession.ProjectID)
	mustNoError(t, err)
	bySession := make(map[domain.SessionID]domain.CompactSessionUsageAggregate, len(rows))
	for _, row := range rows {
		bySession[row.SessionID] = row
	}
	if got := bySession[transientSession.ID]; got.Incomplete {
		t.Fatalf("transient aggregate = %+v, want retry without integrity warning", got)
	}
	if got := bySession[incompleteSession.ID]; !got.Incomplete {
		t.Fatalf("incomplete aggregate = %+v, want integrity warning", got)
	}
}

func TestUsageSessionAggregatesParentChildAndMultipleBindingsExactlyOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)

	newBinding := func(nativeRootID string) domain.UsageBindingRecord {
		t.Helper()
		return mustUpsertUsageBinding(t, s, sess, now, domain.UsageBindingRecord{
			NativeRootID:   nativeRootID,
			InitialModelID: "gpt-5",
			State:          domain.UsageBindingActive,
		})
	}
	newSource := func(binding domain.UsageBindingRecord, nativeID, subagentID, path string) domain.UsageSourceRecord {
		t.Helper()
		return mustInsertUsageSource(t, s, now, domain.UsageSourceRecord{
			BindingID:       binding.ID,
			Kind:            domain.UsageSourceKind("kimi_wire"),
			NativeSessionID: nativeID,
			SubagentID:      subagentID,
			ArtifactPath:    path,
			FileIdentity:    nativeID,
			State:           domain.UsageSourceActive,
		})
	}
	apply := func(source domain.UsageSourceRecord, key string, input, output int64, observedAt time.Time) {
		t.Helper()
		err := s.ApplyUsageChunk(ctx, source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
			ByteOffset: 10,
			State:      domain.UsageSourceComplete,
			UpdatedAt:  observedAt,
		}, []domain.ModelUsageEvent{usageEvent(key, canonicalUsageTokens(input, 0, input, output))})
		if err != nil {
			t.Fatalf("apply source %d: %v", source.ID, err)
		}
	}

	rootBinding := newBinding("root-thread")
	parent := newSource(rootBinding, "root-thread", "", "/tmp/kimi/root.jsonl")
	child := newSource(rootBinding, "child-thread", "child-thread", "/tmp/kimi/child.jsonl")
	secondBinding := newBinding("resumed-thread")
	resumed := newSource(secondBinding, "resumed-thread", "", "/tmp/kimi/resumed.jsonl")
	apply(parent, "parent-event", 100, 20, now)
	apply(child, "child-event", 30, 5, now.Add(time.Second))
	apply(resumed, "resumed-event", 50, 10, now.Add(2*time.Second))
	for _, binding := range []domain.UsageBindingRecord{rootBinding, secondBinding} {
		if _, err := s.UpdateUsageBindingState(ctx, binding.ID, domain.UsageBindingComplete, "", now); err != nil {
			t.Fatalf("complete binding %d: %v", binding.ID, err)
		}
	}

	models, err := s.ListUsageModelAggregates(ctx, sess.ID)
	mustNoError(t, err, "list model aggregates")
	if len(models) != 1 ||
		usageTokenValue(models[0].Tokens.InputTokens) != 180 || usageTokenValue(models[0].Tokens.OutputTokens) != 35 {
		t.Fatalf("model aggregates = %+v, want input=180 output=35 events=3", models)
	}

	compact, err := s.ListCompactSessionUsageAggregates(ctx, sess.ProjectID)
	mustNoError(t, err, "list compact usage")
	if len(compact) != 1 {
		t.Fatalf("compact rows = %+v, want one", compact)
	}
	row := compact[0]
	if usageTokenValue(row.ProcessedTokens) != 215 || row.Incomplete {
		t.Fatalf("compact aggregate = %+v", row)
	}
}

func seedUsageSession(t *testing.T, s *sqlite.Store, harness domain.AgentHarness) domain.SessionRecord {
	t.Helper()
	ctx := context.Background()
	seedProject(t, s, "usage")
	rec := sampleRecord("usage")
	rec.Harness = harness
	got, err := s.CreateSession(ctx, rec)
	mustNoError(t, err, "create usage session")
	return got
}

// TestKimiUsageEventRoundTrip catches schema or validation changes that accept
// Kimi sessions in the app but reject their certified usage source or canonical
// OpenAI-shaped token event at the storage boundary.
func TestKimiUsageEventRoundTrip(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	session := seedUsageSession(t, s, domain.HarnessOpenCode)
	binding := mustUpsertUsageBinding(t, s, session, now, domain.UsageBindingRecord{
		NativeRootID: "kimi-session",
		State:        domain.UsageBindingActive,
	})
	source := mustInsertUsageSource(t, s, now, domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            domain.UsageSourceKind("kimi_wire"),
		NativeSessionID: "kimi-session",
		ArtifactPath:    "/tmp/kimi/sessions/kimi-session/agents/main/wire.jsonl",
		FileIdentity:    "dev:ino",
		State:           domain.UsageSourcePending,
	})
	event := openaiUsageEvent("kimi-event", 13, 8, 21, 5)
	event.ModelID = "kimi-for-coding"
	if err := s.ApplyUsageChunk(context.Background(), source.ID, 0, source.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 100, State: domain.UsageSourceActive, ParserStateJSON: `{}`,
		UpdatedAt: now,
	}, []domain.ModelUsageEvent{event}); err != nil {
		t.Fatalf("apply Kimi usage: %v", err)
	}

	models, err := s.ListUsageModelAggregates(context.Background(), session.ID)
	mustNoError(t, err)
	if len(models) != 1 || models[0].Harness != domain.HarnessOpenCode || models[0].ModelID != "kimi-for-coding" ||
		usageTokenValue(models[0].Tokens.InputTokens) != 42 ||
		usageTokenValue(models[0].Tokens.CachedInputTokens) != 21 ||
		usageTokenValue(models[0].Tokens.UncachedInputTokens) != 21 ||
		usageTokenValue(models[0].Tokens.OutputTokens) != 5 {
		t.Fatalf("Kimi aggregate = %+v", models)
	}
}

func seedUsageSource(t *testing.T, s *sqlite.Store, sess domain.SessionRecord, now time.Time) domain.UsageSourceRecord {
	t.Helper()
	initialModelID := "gpt-5"
	sourceKind := domain.UsageSourceKind("kimi_wire")
	artifactPath := "/tmp/kimi/wire.jsonl"
	binding := mustUpsertUsageBinding(t, s, sess, now, domain.UsageBindingRecord{
		NativeRootID:   "root-thread",
		InitialModelID: initialModelID,
		State:          domain.UsageBindingActive,
	})
	return mustInsertUsageSource(t, s, now, domain.UsageSourceRecord{
		BindingID:       binding.ID,
		Kind:            sourceKind,
		NativeSessionID: "child-thread",
		ArtifactPath:    artifactPath,
		FileIdentity:    "dev:ino",
		State:           domain.UsageSourcePending,
	})
}

func mustUpsertUsageBinding(
	t *testing.T,
	s *sqlite.Store,
	session domain.SessionRecord,
	now time.Time,
	record domain.UsageBindingRecord,
) domain.UsageBindingRecord {
	t.Helper()
	record.SessionID = session.ID
	record.Harness = session.Harness
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	binding, err := s.UpsertUsageBinding(context.Background(), record)
	mustNoError(t, err)
	return binding
}

func mustInsertUsageSource(
	t *testing.T,
	s *sqlite.Store,
	now time.Time,
	record domain.UsageSourceRecord,
) domain.UsageSourceRecord {
	t.Helper()
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	source, err := s.InsertUsageSource(context.Background(), record)
	mustNoError(t, err)
	return source
}

func usageEvent(key string, tokens domain.UsageTokenMetrics) domain.ModelUsageEvent {
	return domain.ModelUsageEvent{
		ProviderID:        domain.UsageProviderOpenAI,
		ModelID:           "gpt-5",
		MeasurementKind:   domain.UsageMeasurementNativeReported,
		Tokens:            tokens,
		ProviderUsageJSON: kimiProviderUsage(0, 0),
		SourceEventKey:    key,
	}
}

// kimiProviderUsage is payload.info reduced to the per-event vector the store
// round-trips and pricing later reads.
func kimiProviderUsage(cacheWrite, reasoning int64) string {
	return fmt.Sprintf(
		`{"last_token_usage":{"cache_write_input_tokens":%d,"reasoning_output_tokens":%d}}`,
		cacheWrite, reasoning,
	)
}

func openaiProviderUsage(directInput, cacheCreationInput int64, fiveM, oneH *int64) string {
	usage := map[string]any{
		"input_tokens":                directInput,
		"cache_creation_input_tokens": cacheCreationInput,
	}
	if fiveM != nil && oneH != nil {
		usage["cache_creation"] = map[string]any{
			"ephemeral_5m_input_tokens": *fiveM, "ephemeral_1h_input_tokens": *oneH,
		}
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func pricedCandidateEvent(key string) domain.ModelUsageEvent {
	event := usageEvent(key, canonicalUsageTokens(5, 1, 4, 3))
	event.BillingProviderID = "openai"
	event.BillingProviderSource = domain.UsageBillingProviderObserved
	event.ModelID = "gpt-test"
	return event
}

func openaiUsageEvent(key string, directInput, cacheCreationInput, cachedInput, output int64) domain.ModelUsageEvent {
	input := directInput + cacheCreationInput + cachedInput
	uncachedInput := directInput + cacheCreationInput
	return domain.ModelUsageEvent{
		ProviderID:        domain.UsageProviderOpenAI,
		ModelID:           "gpt-test",
		MeasurementKind:   domain.UsageMeasurementNativeReported,
		Tokens:            canonicalUsageTokens(input, cachedInput, uncachedInput, output),
		ProviderUsageJSON: openaiProviderUsage(directInput, cacheCreationInput, nil, nil),
		SourceEventKey:    key,
	}
}

func canonicalUsageTokens(input, cachedInput, uncachedInput, output int64) domain.UsageTokenMetrics {
	return domain.UsageTokenMetrics{
		InputTokens: &input, CachedInputTokens: &cachedInput, UncachedInputTokens: &uncachedInput,
		OutputTokens: &output,
	}
}

func usageTokenValue(value *int64) int64 {
	if value == nil {
		return -1
	}
	return *value
}

func assertUsageSourceOffset(t *testing.T, s *sqlite.Store, sourceID int64, want int64) {
	t.Helper()
	got, ok, err := s.GetUsageSourceForIngestion(context.Background(), sourceID)
	if err != nil || !ok {
		t.Fatalf("get usage source: ok=%v err=%v", ok, err)
	}
	if got.Source.ByteOffset != want {
		t.Fatalf("source offset = %d, want %d", got.Source.ByteOffset, want)
	}
}

func mustNoError(t testing.TB, err error, context ...string) {
	t.Helper()
	if err != nil {
		if len(context) > 0 {
			t.Fatalf("%s: %v", context[0], err)
		}
		t.Fatal(err)
	}
}

// Break caught: a physically replaced transcript re-emits the same logical event
// under the same stable key, so replay deduplicates against the row the retired
// generation left behind and the row keeps pointing at that generation. The
// legacy repairer skips a source retired as artifact_replaced and the
// replacement owns no row for the event, so an attribution that never landed
// could never land — the cost stayed NULL for the life of the row.
func TestApplyUsageChunkRehomesAnOpenDuplicateToTheReplacementSource(t *testing.T) {
	dataDir := t.TempDir()
	s := sqlitetest.MustOpenAt(t, dataDir)
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "open-agents.db"))
	mustNoError(t, err, "open raw sqlite")
	t.Cleanup(func() { _ = raw.Close() })
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	retired := seedUsageSource(t, s, sess, now)

	// Unattributed on the original generation, exactly what the repairer exists
	// to finish.
	event := openaiUsageEvent("replaced-event", 5, 10, 5, 4)
	mustNoError(t, s.ApplyUsageChunk(ctx, retired.ID, 0, retired.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10, State: domain.UsageSourceActive, UpdatedAt: now,
	}, []domain.ModelUsageEvent{event}), "seed unattributed event")

	replacement, err := s.ReplaceUsageSource(ctx, retired.ID, domain.UsageErrorArtifactReplaced, domain.UsageSourceRecord{
		BindingID:       retired.BindingID,
		Kind:            retired.Kind,
		NativeSessionID: retired.NativeSessionID,
		ArtifactPath:    retired.ArtifactPath,
		FileIdentity:    retired.FileIdentity + "-next",
		Generation:      retired.Generation + 1,
		State:           domain.UsageSourcePending,
		UpdatedAt:       now.Add(time.Second),
	}, now.Add(time.Second))
	mustNoError(t, err, "replace source")

	// The replacement replays a byte-identical logical event.
	mustNoError(t, s.ApplyUsageChunk(ctx, replacement.ID, 0, replacement.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10, State: domain.UsageSourceActive, UpdatedAt: now.Add(2 * time.Second),
	}, []domain.ModelUsageEvent{event}), "replay onto the replacement")

	assertHomedTo := func(want int64, why string) {
		t.Helper()
		var count int
		mustNoError(t, raw.QueryRow(
			`SELECT COUNT(*) FROM model_usage_events WHERE usage_source_id = ?`, want,
		).Scan(&count), "count homed events")
		if count != 1 {
			t.Fatalf("%s: source %d owns %d events, want 1", why, want, count)
		}
	}
	assertHomedTo(replacement.ID, "after replacement replay")

	var retiredCount int
	mustNoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM model_usage_events WHERE usage_source_id = ?`, retired.ID,
	).Scan(&retiredCount), "count retired events")
	if retiredCount != 0 {
		t.Fatal("the retired generation still owns the open event")
	}

	// Idempotent: replaying again neither duplicates the row nor moves it back.
	mustNoError(t, s.ApplyUsageChunk(ctx, replacement.ID, 10, now.Add(2*time.Second), domain.SourceCursorState{
		ByteOffset: 20, State: domain.UsageSourceActive, UpdatedAt: now.Add(3 * time.Second),
	}, []domain.ModelUsageEvent{event}), "replay twice")
	assertHomedTo(replacement.ID, "after a repeated replay")

	open, err := s.HasOpenUsageAttribution(ctx, replacement.ID)
	mustNoError(t, err, "check open attribution")
	if !open {
		t.Fatal("the rehomed row is not reported open, so ingestion will not ask for a repair pass")
	}
}

// An inferred attribution is open everywhere else, so it has to move too:
// stranded on a retired generation it keeps a guessed provider and the cost
// derived from it, where no observation can reach either.
func TestApplyUsageChunkRehomesAnInferredDuplicateToTheReplacementSource(t *testing.T) {
	dataDir := t.TempDir()
	s := sqlitetest.MustOpenAt(t, dataDir)
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "open-agents.db"))
	mustNoError(t, err, "open raw sqlite")
	t.Cleanup(func() { _ = raw.Close() })
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	retired := seedUsageSource(t, s, sess, now)

	event := openaiUsageEvent("inferred-event", 5, 10, 5, 4)
	event.BillingProviderID = "openai"
	event.BillingProviderSource = domain.UsageBillingProviderInferred
	mustNoError(t, s.ApplyUsageChunk(ctx, retired.ID, 0, retired.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10, State: domain.UsageSourceActive, UpdatedAt: now,
	}, []domain.ModelUsageEvent{event}), "seed inferred event")

	replacement, err := s.ReplaceUsageSource(ctx, retired.ID, domain.UsageErrorArtifactReplaced, domain.UsageSourceRecord{
		BindingID:       retired.BindingID,
		Kind:            retired.Kind,
		NativeSessionID: retired.NativeSessionID,
		ArtifactPath:    retired.ArtifactPath,
		FileIdentity:    retired.FileIdentity + "-next",
		Generation:      retired.Generation + 1,
		State:           domain.UsageSourcePending,
		UpdatedAt:       now.Add(time.Second),
	}, now.Add(time.Second))
	mustNoError(t, err, "replace source")

	mustNoError(t, s.ApplyUsageChunk(ctx, replacement.ID, 0, replacement.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10, State: domain.UsageSourceActive, UpdatedAt: now.Add(2 * time.Second),
	}, []domain.ModelUsageEvent{event}), "replay onto the replacement")

	var replacementSource string
	mustNoError(t, raw.QueryRow(
		`SELECT billing_provider_source FROM model_usage_events WHERE usage_source_id = ?`, replacement.ID,
	).Scan(&replacementSource), "read replacement attribution")
	if replacementSource != string(domain.UsageBillingProviderInferred) {
		t.Fatalf("replacement attribution = %q, want the inferred one", replacementSource)
	}
	var retiredCount int
	mustNoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM model_usage_events WHERE usage_source_id = ?`, retired.ID,
	).Scan(&retiredCount), "count retired events")
	if retiredCount != 0 {
		t.Fatalf("the retired generation kept %d open events", retiredCount)
	}
}

// Break caught: replay used to compare the inferred provider with the newly
// observed provider before rehoming the stable event, rejecting the evidence
// that should replace the inference and its provider-specific cost.
func TestApplyUsageChunkPromotesRehomedInferenceToObservedProvider(t *testing.T) {
	dataDir := t.TempDir()
	s := sqlitetest.MustOpenAt(t, dataDir)
	ctx := context.Background()
	sess := seedUsageSession(t, s, domain.HarnessOpenCode)
	now := time.Unix(1700000000, 0).UTC()
	retired := seedUsageSource(t, s, sess, now)

	inferred := openaiUsageEvent("provider-promotion", 5, 10, 5, 4)
	inferred.BillingProviderID = "openai"
	inferred.BillingProviderSource = domain.UsageBillingProviderInferred
	mustNoError(t, s.ApplyUsageChunk(ctx, retired.ID, 0, retired.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10, State: domain.UsageSourceActive, UpdatedAt: now,
	}, []domain.ModelUsageEvent{inferred}), "seed inferred OpenAI event")

	replacement, err := s.ReplaceUsageSource(ctx, retired.ID, domain.UsageErrorArtifactReplaced, domain.UsageSourceRecord{
		BindingID:       retired.BindingID,
		Kind:            retired.Kind,
		NativeSessionID: retired.NativeSessionID,
		ArtifactPath:    retired.ArtifactPath,
		FileIdentity:    retired.FileIdentity + "-next",
		Generation:      retired.Generation + 1,
		State:           domain.UsageSourcePending,
		UpdatedAt:       now.Add(time.Second),
	}, now.Add(time.Second))
	mustNoError(t, err, "replace source")

	observed := inferred
	observed.BillingProviderID = "zai"
	observed.BillingProviderSource = domain.UsageBillingProviderObserved
	mustNoError(t, s.ApplyUsageChunk(ctx, replacement.ID, 0, replacement.UpdatedAt, domain.SourceCursorState{
		ByteOffset: 10, State: domain.UsageSourceActive, UpdatedAt: now.Add(2 * time.Second),
	}, []domain.ModelUsageEvent{observed}), "promote replayed event to observed Z.AI")

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "open-agents.db"))
	mustNoError(t, err, "open raw sqlite")
	t.Cleanup(func() { _ = raw.Close() })
	var sourceID int64
	var provider, providerSource string
	mustNoError(t, raw.QueryRow(`
SELECT usage_source_id, billing_provider_id, billing_provider_source
FROM model_usage_events
WHERE source_event_key = 'provider-promotion'`).Scan(
		&sourceID, &provider, &providerSource,
	), "read promoted event")
	if sourceID != replacement.ID || provider != "zai" || providerSource != "observed" {
		t.Fatalf("promoted event = source %d provider %q/%q; want source %d zai/observed",
			sourceID, provider, providerSource, replacement.ID)
	}
	open, err := s.HasOpenUsageAttribution(ctx, replacement.ID)
	mustNoError(t, err, "check promoted attribution")
	if open {
		t.Fatal("observed replacement remains open for attribution repair")
	}
}
