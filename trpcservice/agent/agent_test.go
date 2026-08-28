package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/liuzengh/trpc-agent-service/trpcservice/identity"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/stretchr/testify/require"
	trpcagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

func TestDemoRuntimePersistsMultipleTurns(t *testing.T) {
	runtime := NewDemoRuntime()
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })

	for _, input := range []string{"first", "second"} {
		events, err := runtime.Runner.Run(
			context.Background(),
			"u/test-user",
			"c/test-session",
			model.NewUserMessage(input),
			trpcagent.WithRequestID("request-"+input),
		)
		require.NoError(t, err)
		var completionSeen bool
		for evt := range events {
			completionSeen = completionSeen || evt.IsRunnerCompletion()
		}
		require.True(t, completionSeen)
	}

	sess, err := runtime.SessionService.GetSession(context.Background(), session.Key{
		AppName:   DemoAppName,
		UserID:    "u/test-user",
		SessionID: "c/test-session",
	})
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.GreaterOrEqual(t, sess.GetEventCount(), 4)
}

func TestRuntimeCloseIsIdempotent(t *testing.T) {
	runtime := NewDemoRuntime()
	require.NoError(t, runtime.Close())
	require.NoError(t, runtime.Close())
}

func TestRuntimeOwnsExactlyOneOpenAIHandler(t *testing.T) {
	runtime := NewDemoRuntime()
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })

	require.NotNil(t, runtime.openAI)
	handler, err := runtime.OpenAIHandler()
	require.NoError(t, err)
	require.NotNil(t, handler)
	again, err := runtime.OpenAIHandler()
	require.NoError(t, err)
	require.Same(t, handler, again)

	response := serveChatCompletion(t, runtime, `{
		"model":"deterministic-echo","messages":[{"role":"user","content":"hello"}]
	}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "echo: hello")
	require.Contains(t, response.Body.String(), DemoModelName)
}

func TestRuntimeOpenAIHandlerRejectsIncompleteRuntime(t *testing.T) {
	handler, err := (&Runtime{}).OpenAIHandler()
	require.ErrorContains(t, err, "runtime identity is incomplete")
	require.Nil(t, handler)

	handler, err = (&Runtime{
		TenantID: "tenant-a", AgentAppID: "assistant", RevisionID: "revision-1",
	}).OpenAIHandler()
	require.ErrorContains(t, err, "runtime execution unit is incomplete")
	require.Nil(t, handler)
}

func TestRuntimeCloseOrderAndErrorRetention(t *testing.T) {
	adapterErr := errors.New("adapter close failure")
	runnerErr := errors.New("runner close failure")
	sessionErr := errors.New("session close failure")
	recorder := &closeRecorder{}
	runtime := recordingRuntime(recorder, adapterErr, runnerErr, sessionErr)

	firstErr := runtime.Close()
	require.ErrorIs(t, firstErr, adapterErr)
	require.ErrorIs(t, firstErr, runnerErr)
	require.ErrorIs(t, firstErr, sessionErr)
	require.Equal(t, []string{"adapter", "runner", "session"}, recorder.order())

	secondErr := runtime.Close()
	require.ErrorIs(t, secondErr, adapterErr)
	require.ErrorIs(t, secondErr, runnerErr)
	require.ErrorIs(t, secondErr, sessionErr)
	require.Equal(t, []string{"adapter", "runner", "session"}, recorder.order())
}

func TestRuntimeCloseIsConcurrentSafe(t *testing.T) {
	adapterErr := errors.New("adapter close failure")
	recorder := &closeRecorder{}
	runtime := recordingRuntime(recorder, adapterErr, nil, nil)

	const callers = 16
	results := make(chan error, callers)
	var waitGroup sync.WaitGroup
	for caller := 0; caller < callers; caller++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			results <- runtime.Close()
		}()
	}
	waitGroup.Wait()
	close(results)

	for closeErr := range results {
		require.ErrorIs(t, closeErr, adapterErr)
	}
	require.Equal(t, []string{"adapter", "runner", "session"}, recorder.order())
}

// A Runtime that shares the process Session service must never close it: the
// resolver closes runtimes independently, and the remaining runtimes keep
// serving from the same conversation state.
func TestRuntimeCloseKeepsSharedSessionService(t *testing.T) {
	shared := sessioninmemory.NewSessionService()
	t.Cleanup(func() { require.NoError(t, shared.Close()) })

	first, err := NewRuntimeFromRevision(publishedRevision("revision-1", "echo-v1"), shared)
	require.NoError(t, err)
	second, err := NewRuntimeFromRevision(publishedRevision("revision-2", "echo-v2"), shared)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })

	require.NoError(t, first.Close())

	// The body user is attacker-controlled input, so the surviving Runtime must
	// still write the conversation under the authenticated principal.
	response := serveChatCompletion(t, second, `{
		"model":"echo-v2","user":"user-1","messages":[{"role":"user","content":"after close"}]
	}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "echo: after close")

	sess, err := shared.GetSession(context.Background(), session.Key{
		AppName:   second.AppName,
		UserID:    "u/principal-1",
		SessionID: "shared-session",
	})
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.GreaterOrEqual(t, sess.GetEventCount(), 2)
}

func publishedRevision(revisionID string, modelName string) tenant.AgentRevision {
	return tenant.AgentRevision{
		ID:         revisionID,
		TenantID:   "tenant-a",
		AgentAppID: "assistant",
		Status:     tenant.RevisionStatusPublished,
		Config: tenant.RevisionConfig{
			AgentName:   "test-agent",
			Instruction: "Answer through the deterministic model.",
			Model:       tenant.ModelConfig{Provider: "deterministic", Name: modelName},
		},
	}
}

// serveChatCompletion calls a Runtime adapter the way the platform does: with
// the trusted scope already attached to the request context.
func serveChatCompletion(
	t *testing.T,
	runtime *Runtime,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	handler, err := runtime.OpenAIHandler()
	require.NoError(t, err)
	ctx, err := identity.WithRunContext(context.Background(), identity.RunContext{
		TenantID:    runtime.TenantID,
		AppID:       runtime.AgentAppID,
		PrincipalID: "principal-1",
		SessionID:   "shared-session",
		RevisionID:  runtime.RevisionID,
	})
	require.NoError(t, err)
	return serveChatCompletionWithContext(t, ctx, handler, body)
}

func serveChatCompletionWithContext(
	t *testing.T,
	ctx context.Context,
	handler http.Handler,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost, "/v1/chat/completions", strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Session-ID", "shared-session")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request.WithContext(ctx))
	return response
}

// recordingRuntime builds a Runtime that passes validation and reports the order
// in which its owned resources are closed.
func recordingRuntime(
	recorder *closeRecorder,
	adapterErr error,
	runnerErr error,
	sessionErr error,
) *Runtime {
	return &Runtime{
		TenantID:   "tenant-a",
		AgentAppID: "assistant",
		RevisionID: "revision-1",
		AppName:    "t/tenant-a/a/assistant",
		ModelName:  "echo-test",
		Agent:      &stubAgent{},
		Runner: &closeRecordingRunner{
			recorder: recorder,
			closeErr: runnerErr,
		},
		SessionService: &closeRecordingSessionService{
			recorder: recorder,
			closeErr: sessionErr,
		},
		openAI: &closeRecordingAdapter{
			recorder: recorder,
			closeErr: adapterErr,
		},
		openAIHandler:      http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		ownsSessionService: true,
	}
}

type closeRecorder struct {
	mu       sync.Mutex
	recorded []string
}

func (c *closeRecorder) record(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recorded = append(c.recorded, name)
}

func (c *closeRecorder) order() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.recorded...)
}

type stubAgent struct {
	trpcagent.Agent
}

type closeRecordingAdapter struct {
	recorder *closeRecorder
	closeErr error
}

func (a *closeRecordingAdapter) Handler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

func (a *closeRecordingAdapter) Close() error {
	a.recorder.record("adapter")
	return a.closeErr
}

type closeRecordingRunner struct {
	recorder *closeRecorder
	closeErr error
}

func (r *closeRecordingRunner) Run(
	context.Context,
	string,
	string,
	model.Message,
	...trpcagent.RunOption,
) (<-chan *event.Event, error) {
	return nil, errors.New("unexpected run")
}

func (r *closeRecordingRunner) Close() error {
	r.recorder.record("runner")
	return r.closeErr
}

type closeRecordingSessionService struct {
	session.Service
	recorder *closeRecorder
	closeErr error
}

func (s *closeRecordingSessionService) Close() error {
	s.recorder.record("session")
	return s.closeErr
}
