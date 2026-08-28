package agent

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/liuzengh/trpc-agent-service/trpcservice/identity"
	"github.com/stretchr/testify/require"
	trpcagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

func testRunContext() identity.RunContext {
	return identity.RunContext{
		TenantID:    "tenant-a",
		AppID:       "assistant",
		PrincipalID: "principal-1",
		SessionID:   "conversation-1",
		RevisionID:  "revision-1",
	}
}

func testContextRunner(inner *recordingRunner) *contextRunner {
	return &contextRunner{
		inner:      inner,
		tenantID:   "tenant-a",
		appID:      "assistant",
		revisionID: "revision-1",
	}
}

// A request that never passed platform authentication carries no scope, so the
// wrapper must refuse it instead of inventing one.
func TestContextRunnerRejectsRunWithoutTrustedScope(t *testing.T) {
	inner := &recordingRunner{}
	wrapper := testContextRunner(inner)

	events, err := wrapper.Run(
		context.Background(),
		"attacker",
		"attacker-session",
		model.NewUserMessage("hello"),
	)
	require.ErrorIs(t, err, ErrUntrustedRun)
	require.ErrorIs(t, err, identity.ErrNoRunContext)
	require.Nil(t, events)
	require.Empty(t, inner.recorded())

	unconfigured := &contextRunner{}
	events, err = unconfigured.Run(
		context.Background(), "attacker", "attacker-session", model.NewUserMessage("hello"),
	)
	require.ErrorIs(t, err, ErrUntrustedRun)
	require.Nil(t, events)
}

// A scope that belongs to another tenant, app or revision must not execute on
// this Runtime, even though it is a well-formed authenticated scope.
func TestContextRunnerRejectsScopeFromAnotherRuntime(t *testing.T) {
	for name, mutate := range map[string]func(*identity.RunContext){
		"tenant":   func(c *identity.RunContext) { c.TenantID = "tenant-b" },
		"app":      func(c *identity.RunContext) { c.AppID = "reporter" },
		"revision": func(c *identity.RunContext) { c.RevisionID = "revision-2" },
	} {
		t.Run(name, func(t *testing.T) {
			inner := &recordingRunner{}
			wrapper := testContextRunner(inner)
			scope := testRunContext()
			mutate(&scope)
			ctx, err := identity.WithRunContext(context.Background(), scope)
			require.NoError(t, err)

			events, err := wrapper.Run(ctx, "", "", model.NewUserMessage("hello"))
			require.ErrorIs(t, err, ErrUntrustedRun)
			require.ErrorContains(t, err, "does not match runtime")
			require.Nil(t, events)
			require.Empty(t, inner.recorded())
		})
	}
}

// The protocol adapter derives userID from the request body and sessionID from
// a request header. Both arguments have to be dropped.
func TestContextRunnerIgnoresProtocolSuppliedIdentity(t *testing.T) {
	inner := &recordingRunner{}
	wrapper := testContextRunner(inner)
	ctx, err := identity.WithRunContext(context.Background(), testRunContext())
	require.NoError(t, err)

	events, err := wrapper.Run(
		ctx,
		"attacker",
		"attacker-session",
		model.NewUserMessage("hello"),
		trpcagent.WithRequestID("request-1"),
	)
	require.NoError(t, err)
	require.NotNil(t, events)
	for range events {
	}

	require.Equal(t, []recordedRun{{
		userID:    "u/principal-1",
		sessionID: "conversation-1",
		content:   "hello",
	}}, inner.recorded())
}

// The Runtime owns the real Runner. An adapter that closed its injected Runner
// must not reach it.
func TestContextRunnerCloseDoesNotCloseInnerRunner(t *testing.T) {
	inner := &recordingRunner{}
	wrapper := testContextRunner(inner)

	require.NoError(t, wrapper.Close())
	require.NoError(t, wrapper.Close())
	require.Zero(t, inner.closes())
}

func TestRuntimeGivesProtocolAdapterTheContextRunner(t *testing.T) {
	shared := sessioninmemory.NewSessionService()
	t.Cleanup(func() { require.NoError(t, shared.Close()) })
	runtime, err := NewRuntimeFromRevision(publishedRevision("revision-1", "echo-v1"), shared)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })

	wrapper, ok := runtime.protocolRunner.(*contextRunner)
	require.True(t, ok)
	require.NotSame(t, runtime.Runner, runtime.protocolRunner)
	require.Same(t, runtime.Runner, wrapper.inner)
	require.Equal(t, runtime.TenantID, wrapper.tenantID)
	require.Equal(t, runtime.AgentAppID, wrapper.appID)
	require.Equal(t, runtime.RevisionID, wrapper.revisionID)
}

// Close must reach the real Runner exactly once: the wrapper adds a second
// reference to it, not a second owner.
func TestRuntimeCloseClosesRealRunnerExactlyOnce(t *testing.T) {
	recorder := &closeRecorder{}
	runtime := recordingRuntime(recorder, nil, nil, nil)
	runtime.protocolRunner = &contextRunner{inner: runtime.Runner}

	require.NoError(t, runtime.Close())
	require.NoError(t, runtime.protocolRunner.Close())
	require.NoError(t, runtime.Close())
	require.Equal(t, []string{"adapter", "runner", "session"}, recorder.order())
}

// Without a trusted scope the adapter still answers, but the agent never runs.
func TestRuntimeOpenAIHandlerFailsClosedWithoutRunContext(t *testing.T) {
	runtime := NewDemoRuntime()
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })

	handler, err := runtime.OpenAIHandler()
	require.NoError(t, err)
	response := serveChatCompletionWithContext(
		t,
		context.Background(),
		handler,
		`{"model":"deterministic-echo","user":"attacker","messages":[
			{"role":"user","content":"hello"}
		]}`,
	)
	require.NotEqual(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), "echo: hello")
}

type recordedRun struct {
	userID    string
	sessionID string
	content   string
}

// recordingRunner stands in for the real Runner so a test can read back the
// identity arguments the wrapper produced.
type recordingRunner struct {
	mu         sync.Mutex
	runs       []recordedRun
	closeCalls int
}

func (r *recordingRunner) Run(
	_ context.Context,
	userID string,
	sessionID string,
	message model.Message,
	_ ...trpcagent.RunOption,
) (<-chan *event.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, recordedRun{
		userID:    userID,
		sessionID: sessionID,
		content:   message.Content,
	})
	events := make(chan *event.Event)
	close(events)
	return events, nil
}

func (r *recordingRunner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeCalls++
	return nil
}

func (r *recordingRunner) recorded() []recordedRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRun(nil), r.runs...)
}

func (r *recordingRunner) closes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeCalls
}
