package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	trpcagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
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
