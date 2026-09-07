package wecom

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/liuzengh/trpc-agent-service/trpcservice/channels"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessionrun"
	"github.com/liuzengh/trpc-agent-service/trpcservice/telemetry"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// The attribute keys this package asserts on, spelled the way an operator would
// read them back out of a collector rather than borrowed from the constants
// that produced them.
const (
	keyStage    = "trpc.stage"
	keyOutcome  = "trpc.outcome"
	keyError    = "trpc.error_type"
	keyRequest  = "trpc.request_id"
	keyRun      = "trpc.run_id"
	keyOutbox   = "trpc.outbox_id"
	keyRevision = "trpc.revision_id"
	keyAttempt  = "trpc.attempt"
	keyEvents   = "trpc.event_count"
)

// inMemory is a Telemetry that exports nowhere, plus the readers a test asserts
// on. A synchronous span processor keeps the records in step with the loop that
// produced them, so nothing here waits for a batch.
func inMemory(t *testing.T) (*telemetry.Telemetry, *tracetest.InMemoryExporter, sdkmetric.Reader) {
	t.Helper()
	spans := tracetest.NewInMemoryExporter()
	reader := sdkmetric.NewManualReader()
	observer, err := telemetry.New(
		sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans)),
		sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
	)
	require.NoError(t, err)
	return observer, spans, reader
}

func newRecordingConsumer(t *testing.T, store channels.Store) (
	*Consumer, *tracetest.InMemoryExporter,
) {
	t.Helper()
	observer, spans, _ := inMemory(t)
	binding := testBinding()
	client, err := New(testConfig(t, newMockServer(t), binding))
	require.NoError(t, err)
	consumer, err := NewConsumer(ConsumerConfig{
		Binding:   binding,
		Client:    client,
		Store:     store,
		Runs:      &sessionrun.Service{},
		Revisions: func(context.Context, string, string, string) error { return nil },
		Telemetry: observer,
	})
	require.NoError(t, err)
	return consumer, spans
}

// stageSpans indexes what was recorded by stage, and fails a test that finds a
// span this package did not create.
func stageSpans(t *testing.T, spans *tracetest.InMemoryExporter) map[string][]attribute.Set {
	t.Helper()
	byStage := map[string][]attribute.Set{}
	for _, recorded := range spans.GetSpans() {
		attributes := attribute.NewSet(recorded.Attributes...)
		stage, ok := attributes.Value(keyStage)
		require.True(t, ok, recorded.Name)
		require.Equal(t, "channel."+stage.AsString(), recorded.Name)
		byStage[stage.AsString()] = append(byStage[stage.AsString()], attributes)
	}
	return byStage
}

// text returns one attribute of a recorded span.
func text(t *testing.T, attributes attribute.Set, key string) string {
	t.Helper()
	value, ok := attributes.Value(attribute.Key(key))
	require.Truef(t, ok, "%s was not recorded", key)
	return value.Emit()
}

// acceptStore is the accept side of a Store, and answers with the row an
// earlier delivery created.
type acceptStore struct {
	channels.Store
	stored   channels.AcceptResult
	requests []channels.AcceptRequest
	fail     int
}

func (s *acceptStore) Accept(
	_ context.Context, _ tenant.TenantContext, request channels.AcceptRequest,
) (channels.AcceptResult, error) {
	s.requests = append(s.requests, request)
	if s.fail > 0 {
		s.fail--
		return channels.AcceptResult{}, errors.New("storage unavailable")
	}
	return s.stored, nil
}

func directText(text string) DirectText {
	return DirectText{
		PrincipalID:       "principal-a",
		SessionID:         "session-s",
		ExternalMessageID: msgIDMarker,
		Text:              text,
		ReceivedAt:        time.Now(),
		Reply:             ReplyTarget{generation: "gen-1", reqID: "req-1", streamID: "stream-1"},
	}
}

// A redelivery is recorded against the request the first delivery created. The
// ids this pass minted were never stored, and reporting them would invent a
// request that no other stage will ever mention.
func TestAcceptRecordsTheStoredRequestOnce(t *testing.T) {
	store := &acceptStore{
		stored: channels.AcceptResult{
			InboxID:   "in-first",
			RunID:     "run-first",
			RequestID: "req-first",
			Duplicate: true,
		},
		// One write whose outcome is unknown, retried with the same ids.
		fail: 1,
	}
	consumer, spans := newRecordingConsumer(t, store)
	require.NoError(t, consumer.accept(context.Background(), directText(bodyMarker)))

	require.Len(t, store.requests, 2)
	require.Equal(t, store.requests[0].IDs, store.requests[1].IDs,
		"the retry is the same write, not a second message")
	minted := store.requests[0].IDs.RequestID

	recorded := spans.GetSpans()
	require.Len(t, recorded, 1, "one message is one record, however often the write was retried")
	accepted := stageSpans(t, spans)["accept"][0]
	require.Equal(t, "duplicate", text(t, accepted, keyOutcome))
	require.Equal(t, "req-first", text(t, accepted, keyRequest))
	require.Equal(t, "run-first", text(t, accepted, keyRun))
	require.Equal(t, "2", text(t, accepted, keyAttempt))
	require.NotContains(t, fmt.Sprintf("%+v", recorded[0]), minted)
	requireNoMarkers(t, recorded[0])
}

// A message that could not be recorded is a failed stage, and the class of the
// failure is a label rather than the error.
func TestAcceptRecordsAFailureWithoutItsCause(t *testing.T) {
	store := &acceptStore{fail: persistAttempts}
	consumer, spans := newRecordingConsumer(t, store)
	require.ErrorIs(t,
		consumer.accept(context.Background(), directText(bodyMarker)), ErrAcceptFailed)

	recorded := spans.GetSpans()
	require.Len(t, recorded, 1)
	failed := stageSpans(t, spans)["accept"][0]
	require.Equal(t, "failed", text(t, failed, keyOutcome))
	require.Equal(t, string(channels.ErrorInternal), text(t, failed, keyError))
	require.Equal(t, fmt.Sprint(persistAttempts), text(t, failed, keyAttempt))
	_, hasRequest := failed.Value(keyRequest)
	require.False(t, hasRequest, "nothing was stored, so there is no request to name")
	require.NotContains(t, fmt.Sprintf("%+v", recorded[0]), "storage unavailable")
	requireNoMarkers(t, recorded[0])
}

// The send record is taken around the delivery attempt, and says what happened
// to the attempt rather than what the Store will do about it.
func TestDeliverRecordsWhatTheAttemptDid(t *testing.T) {
	binding := testBinding()
	part, _, _ := answerPart(binding, "out-a", "run-a", 1)
	part.RequestID = "req-a"
	part.Attempt = 1
	part.Message = channels.OutboundMessage{Text: replyMarker}

	// A target this binding cannot open is an address from a connection that is
	// gone, which is not the platform refusing the answer.
	stale := part
	stale.DeliveryTarget = channels.DeliveryTarget{Channel: channels.ChannelWeCom}
	// An attempt that may already have been delivered is not attempted again,
	// and is not a send that failed either.
	risky := part
	risky.DuplicateRisk = true

	for _, expected := range []struct {
		outcome string
		part    channels.OutboxPart
		result  channels.SendOutcome
	}{
		{"stale_target", stale, channels.SendPermanent},
		{"skipped", risky, channels.SendUnknown},
	} {
		consumer, spans := newRecordingConsumer(t, &orderStore{})
		result := consumer.deliver(context.Background(), expected.part)
		require.Equal(t, expected.result, result.Outcome,
			"the record must not change what the Store is told")

		recorded := spans.GetSpans()
		require.Len(t, recorded, 1)
		delivered := stageSpans(t, spans)["deliver"][0]
		require.Equal(t, expected.outcome, text(t, delivered, keyOutcome))
		require.Equal(t, "req-a", text(t, delivered, keyRequest))
		require.Equal(t, "out-a", text(t, delivered, keyOutbox))
		require.Equal(t, "1", text(t, delivered, keyAttempt))
		requireNoMarkers(t, recorded[0])
	}
}

// brokenExporter is a collector that refuses everything, in the place where the
// consumer would notice: the export runs inside the End of a stage.
type brokenExporter struct{ sdktrace.SpanExporter }

func (brokenExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("collector refused: " + bodyMarker)
}

func (brokenExporter) Shutdown(context.Context) error { return nil }

// An export that fails is not a message that failed.
func TestAFailedExportChangesNothing(t *testing.T) {
	observer, err := telemetry.New(
		sdktrace.NewTracerProvider(sdktrace.WithSyncer(brokenExporter{})),
		sdkmetric.NewMeterProvider(),
	)
	require.NoError(t, err)
	binding := testBinding()
	client, err := New(testConfig(t, newMockServer(t), binding))
	require.NoError(t, err)
	store := &acceptStore{stored: channels.AcceptResult{
		InboxID: "in-a", RunID: "run-a", RequestID: "req-a",
	}}
	consumer, err := NewConsumer(ConsumerConfig{
		Binding:   binding,
		Client:    client,
		Store:     store,
		Runs:      &sessionrun.Service{},
		Revisions: func(context.Context, string, string, string) error { return nil },
		Telemetry: observer,
	})
	require.NoError(t, err)
	require.NoError(t, consumer.accept(context.Background(), directText(bodyMarker)))
	require.Len(t, store.requests, 1, "a refused export is not a write to retry")
}

// A consumer without telemetry is the default, and records nothing anywhere.
func TestAConsumerWithoutTelemetryRecordsNothing(t *testing.T) {
	store := &acceptStore{stored: channels.AcceptResult{
		InboxID: "in-a", RunID: "run-a", RequestID: "req-a",
	}}
	consumer := newTestConsumer(t, store)
	require.Nil(t, consumer.stages)
	require.NoError(t, consumer.accept(context.Background(), directText(bodyMarker)))
	require.Len(t, store.requests, 1)
}

// requireNoMarkers asserts that one whole recorded span repeats nothing this
// package protects; see the markers in mockserver_test.go.
func requireNoMarkers(t *testing.T, recorded tracetest.SpanStub) {
	t.Helper()
	requireNoMarkerText(t, fmt.Sprintf("%+v", recorded))
}

// requireNoIdentifiers asserts that a measurement carries none of the
// identifiers a span does. They are unbounded, and a metric backend keeps one
// time series per label set.
func requireNoIdentifiers(t *testing.T, reader sdkmetric.Reader) {
	t.Helper()
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))
	require.NotEmpty(t, collected.ScopeMetrics)
	for _, scope := range collected.ScopeMetrics {
		for _, recorded := range scope.Metrics {
			rendered := fmt.Sprintf("%+v", recorded)
			for _, key := range []string{keyRequest, keyRun, keyOutbox, keyRevision, keyAttempt, keyEvents} {
				require.NotContainsf(t, rendered, key,
					"%s is not a label: %s", key, rendered)
			}
			requireNoMarkerText(t, rendered)
		}
	}
}

// requireNoMarkerText is that assertion over anything already rendered.
func requireNoMarkerText(t *testing.T, rendered string) {
	t.Helper()
	for _, marker := range []string{
		secretMarker, botIDMarker, userIDMarker, msgIDMarker, bodyMarker,
		errMsgMarker, replyMarker,
	} {
		require.NotContainsf(t, rendered, marker,
			"a record repeated a protected value: %s", rendered)
	}
}

// TestIntegrationRecordsThreeStagesOfOneRequest is the correlation this slice
// promises: three independent records of one message, joined by the request id
// the Store minted, over the real protocol, the real Runner and a real
// database.
func TestIntegrationRecordsThreeStagesOfOneRequest(t *testing.T) {
	fixture := newE2E(t)
	observer, spans, reader := inMemory(t)
	fixture.observer = observer
	conn := fixture.start(t)

	conn.callback("req-1", message(msgIDMarker, bodyMarker))
	in, answer := waitReply(t, conn)
	require.Equal(t, "echo: "+bodyMarker, answer)
	conn.ack(in.Headers.ReqID, 0)

	// The same platform message id again, which the Store recognises.
	conn.callback("req-2", message(msgIDMarker, bodyMarker))
	conn.silent(500 * time.Millisecond)

	recorded := fixture.recordedRuns(t)
	require.Len(t, recorded, 1)
	part := fixture.awaitAnswer(t, recorded[0])
	require.Equal(t, channels.OutboxSent, part.Status)
	fixture.stop()

	byStage := stageSpans(t, spans)
	require.Len(t, byStage["accept"], 2)
	require.Len(t, byStage["execute"], 1, "the redelivery executed nothing")
	require.Len(t, byStage["deliver"], 1, "and was answered by nobody")

	request := recorded[0].RequestID
	require.Equal(t, "succeeded", text(t, byStage["accept"][0], keyOutcome))
	require.Equal(t, request, text(t, byStage["accept"][0], keyRequest))
	require.Equal(t, "duplicate", text(t, byStage["accept"][1], keyOutcome))
	require.Equal(t, request, text(t, byStage["accept"][1], keyRequest),
		"a redelivery is recorded against the request that was stored")

	executed := byStage["execute"][0]
	require.Equal(t, "succeeded", text(t, executed, keyOutcome))
	require.Equal(t, request, text(t, executed, keyRequest))
	require.Equal(t, recorded[0].RunID, text(t, executed, keyRun))
	require.Equal(t, recorded[0].RevisionID, text(t, executed, keyRevision),
		"the revision that answered, not the one that was published")
	require.NotEmpty(t, text(t, executed, keyEvents))

	delivered := byStage["deliver"][0]
	require.Equal(t, "succeeded", text(t, delivered, keyOutcome))
	require.Equal(t, request, text(t, delivered, keyRequest))
	require.Equal(t, part.OutboxID, text(t, delivered, keyOutbox))
	require.Equal(t, "1", text(t, delivered, keyAttempt))

	for _, span := range spans.GetSpans() {
		requireNoMarkers(t, span)
	}
	requireNoIdentifiers(t, reader)
}

// TestIntegrationARefusedCollectorChangesNothing is the acceptance for the one
// thing telemetry must never do. The collector refuses every export while a
// real message is accepted, executed and answered.
func TestIntegrationARefusedCollectorChangesNothing(t *testing.T) {
	fixture := newE2E(t)
	exports := make(chan string, 8)
	collector := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			select {
			case exports <- r.URL.Path:
			default:
			}
			http.Error(w, "the collector refused: "+bodyMarker, http.StatusBadRequest)
		}))
	defer collector.Close()
	observer, err := telemetry.Open(context.Background(),
		telemetry.Config{Enabled: true, Endpoint: collector.URL})
	require.NoError(t, err)
	fixture.observer = observer

	conn := fixture.start(t)
	conn.callback("req-1", message(msgIDMarker, bodyMarker))
	in, answer := waitReply(t, conn)
	require.Equal(t, "echo: "+bodyMarker, answer)
	conn.ack(in.Headers.ReqID, 0)

	recorded := fixture.recordedRuns(t)
	require.Len(t, recorded, 1)
	require.Equal(t, channels.OutboxSent, fixture.awaitAnswer(t, recorded[0]).Status)
	// Nothing is retried while the collector refuses: no second frame, and no
	// second execution.
	conn.silent(400 * time.Millisecond)
	fixture.stop()

	shutdown := observer.Shutdown(context.Background())
	require.NotErrorIs(t, shutdown, telemetry.ErrExportFailed,
		"what the collector said is not what the process reports")
	requireNoMarkerText(t, fmt.Sprint(shutdown))
	require.NotEmpty(t, exports, "the collector refused a real export")

	settled := fixture.recordedRuns(t)
	require.Len(t, settled, 1)
	require.Equal(t, channels.RunSucceeded, settled[0].Status)
	require.Equal(t, int32(1), settled[0].Attempt, "no Run was executed twice")
	part := fixture.answerOf(t, settled[0])
	require.Equal(t, channels.OutboxSent, part.Status)
	require.Equal(t, int32(1), part.Attempt, "a refused export is not a second send")
	require.False(t, part.DuplicateRisk)
	require.Equal(t, 1, fixture.userTurns(t))
}
