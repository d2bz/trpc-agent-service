package wecom

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"

	"github.com/liuzengh/trpc-agent-service/trpcservice/channels"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessionrun"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// orderStore is the delivery side of a Store, enough to drive drain. Every
// other method is the embedded nil interface: a test that reaches one has left
// the path it meant to exercise.
type orderStore struct {
	channels.Store
	parts     map[string]channels.OutboxPart
	runs      map[string]channels.Run
	dispatch  []channels.OutboxDispatch
	failScans int
	claims    []string
	runScans  int
}

func (s *orderStore) ListDispatchableRuns(
	context.Context, channels.DispatchScanRequest,
) ([]channels.RunDispatch, error) {
	s.runScans++
	return nil, nil
}

func (s *orderStore) ListDispatchableOutbox(
	context.Context, channels.DispatchScanRequest,
) ([]channels.OutboxDispatch, error) {
	if s.failScans > 0 {
		s.failScans--
		return nil, errors.New("scan unavailable")
	}
	return s.dispatch, nil
}

func (s *orderStore) GetOutboxPart(
	_ context.Context, _ tenant.TenantContext, outboxID string,
) (channels.OutboxPart, error) {
	return s.parts[outboxID], nil
}

func (s *orderStore) GetRun(
	_ context.Context, _ tenant.TenantContext, runID string,
) (channels.Run, error) {
	return s.runs[runID], nil
}

func (s *orderStore) ClaimOutbox(
	_ context.Context, _ tenant.TenantContext, request channels.ClaimOutboxRequest,
) (channels.OutboxClaim, bool, error) {
	s.claims = append(s.claims, request.OutboxID)
	part := s.parts[request.OutboxID]
	part.SendToken = request.SendToken
	part.Attempt++
	return channels.OutboxClaim{Part: part}, true, nil
}

func (s *orderStore) CompleteOutbox(
	_ context.Context, _ tenant.TenantContext, request channels.CompleteOutboxRequest,
) (channels.OutboxStatus, error) {
	delete(s.parts, request.Token.OutboxID)
	remaining := s.dispatch[:0]
	for _, candidate := range s.dispatch {
		if candidate.OutboxID != request.Token.OutboxID {
			remaining = append(remaining, candidate)
		}
	}
	s.dispatch = remaining
	return channels.OutboxFailed, nil
}

func (s *orderStore) RecoverRuns(
	context.Context, channels.RecoverRequest,
) ([]channels.RunRecovery, error) {
	return nil, nil
}

func (s *orderStore) RecoverOutbox(
	context.Context, channels.RecoverRequest,
) ([]channels.OutboxRecovery, error) {
	return nil, nil
}

// answerPart is one recorded answer of session-s, at accept position sequence.
func answerPart(binding Binding, outboxID, runID string, sequence int64) (
	channels.OutboxPart, channels.Run, channels.OutboxDispatch,
) {
	part := channels.OutboxPart{
		TenantID:         binding.TenantID,
		OutboxID:         outboxID,
		RunID:            runID,
		Channel:          channels.ChannelWeCom,
		ChannelBindingID: binding.BindingID,
		SessionID:        "session-s",
		Status:           channels.OutboxPending,
		MaxAttempts:      sendAttempts,
	}
	run := channels.Run{
		TenantID:         binding.TenantID,
		RunID:            runID,
		Channel:          channels.ChannelWeCom,
		ChannelBindingID: binding.BindingID,
		AgentAppID:       binding.AgentAppID,
		SessionID:        "session-s",
		AcceptSequence:   sequence,
	}
	dispatch := channels.OutboxDispatch{
		OutboxRef: channels.OutboxRef{TenantID: binding.TenantID, OutboxID: outboxID},
		Channel:   channels.ChannelWeCom,
	}
	return part, run, dispatch
}

func newTestConsumer(t *testing.T, store channels.Store) *Consumer {
	t.Helper()
	binding := testBinding()
	client, err := New(testConfig(t, newMockServer(t), binding))
	require.NoError(t, err)
	consumer, err := NewConsumer(ConsumerConfig{
		Binding:   binding,
		Client:    client,
		Store:     store,
		Runs:      &sessionrun.Service{},
		Revisions: func(context.Context, string, string, string) error { return nil },
	})
	require.NoError(t, err)
	return consumer
}

// A failed send scan must stop the pass, and the answers of one Session must go
// out in the order they were accepted however the scan happens to sort them.
func TestConsumerSendsOneSessionAnswersInAcceptOrder(t *testing.T) {
	binding := testBinding()
	first, firstRun, firstDispatch := answerPart(binding, "out-a", "run-a", 1)
	second, secondRun, secondDispatch := answerPart(binding, "out-b", "run-b", 2)
	store := &orderStore{
		parts: map[string]channels.OutboxPart{"out-a": first, "out-b": second},
		runs:  map[string]channels.Run{"run-a": firstRun, "run-b": secondRun},
		// Reverse identifier order, which is all the scan promises.
		dispatch:  []channels.OutboxDispatch{secondDispatch, firstDispatch},
		failScans: 1,
	}
	consumer := newTestConsumer(t, store)

	require.NoError(t, consumer.drain(context.Background()))
	require.Empty(t, store.claims)
	require.Zero(t, store.runScans, "a Run must not be executed while the send side is unknown")

	require.NoError(t, consumer.drain(context.Background()))
	require.Equal(t, []string{"out-a", "out-b"}, store.claims)
	require.Equal(t, 1, store.runScans)
}

func TestNewConsumerRequiresItsDependencies(t *testing.T) {
	binding := testBinding()
	client, err := New(testConfig(t, newMockServer(t), binding))
	require.NoError(t, err)
	consumer, err := NewConsumer(ConsumerConfig{Binding: binding, Client: client})
	require.Error(t, err)
	require.Nil(t, consumer)
}

func TestConsumerRejectsAnotherClientsBinding(t *testing.T) {
	server := newMockServer(t)
	binding := testBinding()
	client, err := New(testConfig(t, server, binding))
	require.NoError(t, err)
	binding.TenantID = "tenant-b"
	consumer, err := NewConsumer(ConsumerConfig{
		Binding: binding,
		Client:  client,
		Store:   &orderStore{},
		Runs:    &sessionrun.Service{},
		Revisions: func(context.Context, string, string, string) error {
			return nil
		},
	})
	require.Error(t, err, "client and consumer must share the same trusted binding")
	require.Nil(t, consumer)
}

func TestDeliveryTargetIsUsableOnlyByItsOwnBinding(t *testing.T) {
	binding := testBinding()
	reply := ReplyTarget{generation: "gen-1", reqID: "req-1", streamID: "stream-1"}
	target, err := encodeTarget(binding, reply)
	require.NoError(t, err)
	decoded, err := decodeTarget(binding, target)
	require.NoError(t, err)
	require.Equal(t, reply, decoded)

	foreign := binding
	foreign.TenantID = "tenant-b"
	_, err = decodeTarget(foreign, target)
	require.ErrorIs(t, err, ErrTargetInvalid)

	otherBinding := binding
	otherBinding.BindingID = "binding-b"
	_, err = decodeTarget(otherBinding, target)
	require.ErrorIs(t, err, ErrTargetInvalid)

	wrongVersion := target
	wrongVersion.Version = targetVersion + 1
	_, err = decodeTarget(binding, wrongVersion)
	require.ErrorIs(t, err, ErrTargetInvalid)

	wrongChannel := target
	wrongChannel.Channel = channels.ChannelFeishu
	_, err = decodeTarget(binding, wrongChannel)
	require.ErrorIs(t, err, ErrTargetInvalid)

	// A target that is missing the connection it belongs to cannot be answered
	// on any connection.
	empty, err := encodeTarget(binding, ReplyTarget{reqID: "req-1", streamID: "stream-1"})
	require.NoError(t, err)
	_, err = decodeTarget(binding, empty)
	require.ErrorIs(t, err, ErrTargetInvalid)
}

func finalAnswer(text string) *event.Event {
	return &event.Event{Response: &model.Response{
		Object: model.ObjectTypeChatCompletion,
		Done:   true,
		Choices: []model.Choice{{
			Message: model.Message{Role: model.RoleAssistant, Content: text},
		}},
	}}
}

func TestReplyCollectorKeepsOnlyTheFinalAnswer(t *testing.T) {
	var collected replyCollector
	collected.observe(&event.Event{Response: &model.Response{
		Object:    model.ObjectTypeChatCompletionChunk,
		IsPartial: true,
		Choices:   []model.Choice{{Delta: model.Message{Content: "par"}}},
	}})
	collected.observe(&event.Event{Response: &model.Response{
		Object: model.ObjectTypeChatCompletion,
		Choices: []model.Choice{{Message: model.Message{
			Role:      model.RoleAssistant,
			ToolCalls: []model.ToolCall{{ID: "call-1"}},
		}}},
	}})
	collected.observe(&event.Event{Response: &model.Response{
		Object: model.ObjectTypeToolResponse,
		Done:   true,
		Choices: []model.Choice{{Message: model.Message{
			Role:    model.RoleTool,
			ToolID:  "call-1",
			Content: "tool output",
		}}},
	}})
	collected.observe(finalAnswer("the answer"))
	collected.observe(&event.Event{Response: &model.Response{
		Object: model.ObjectTypeRunnerCompletion,
		Done:   true,
	}})
	require.Equal(t, "the answer", collected.answer())
	require.Equal(t, int32(5), collected.events)

	// A failure after the answer still fails the Run.
	collected.observe(&event.Event{Response: &model.Response{
		Object: model.ObjectTypeError,
		Error:  &model.ResponseError{Message: "upstream refused"},
	}})
	require.True(t, collected.failed)
	require.Empty(t, collected.answer())
}

func TestBoundReplyStaysStorableAndSendable(t *testing.T) {
	require.Equal(t, "line\tone\nline two", boundReply("line\tone\x00\nline\x07 two"))

	// Every rune is three bytes, so the cut lands inside one.
	long := strings.Repeat("汉", maxReplyTextBytes)
	bounded := boundReply(long)
	require.LessOrEqual(t, len(bounded), maxReplyTextBytes)
	require.True(t, strings.HasSuffix(bounded, truncationNotice))
	require.True(t, strings.HasPrefix(bounded, "汉汉"))
	require.NotContains(t, strings.TrimSuffix(bounded, truncationNotice), "�")
	require.NoError(t, channels.OutboundMessage{Text: bounded}.Validate())
}

func TestClassifySendFailsClosed(t *testing.T) {
	require.Equal(t, channels.SendSucceeded, classifySend(nil).Outcome)
	for _, err := range []error{
		ErrReplyRejected, ErrReplyAlreadySent, ErrReplyTargetExpired, ErrNotConnected,
	} {
		require.Equal(t, channels.SendPermanent, classifySend(err).Outcome)
	}
	require.Equal(t, channels.SendUnknown, classifySend(errAckTimeout).Outcome)
	require.Equal(t,
		channels.ErrorOutcomeUnknown, classifySend(context.DeadlineExceeded).ErrorType)
}
