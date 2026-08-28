package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/liuzengh/trpc-agent-service/trpcservice/identity"
	trpcagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
)

// ErrUntrustedRun reports a run that carries no platform-authenticated scope,
// or one whose scope does not belong to this Runtime.
var ErrUntrustedRun = errors.New("agent: run has no trusted identity context")

// contextRunner is the Runner handed to protocol adapters. An adapter derives
// userID and sessionID from request fields the client controls, so this wrapper
// drops both arguments and replays the run with the scope the platform
// authenticated. The Runtime keeps the real Runner, so a future IM or scheduler
// path can still call it directly with a scope it derived itself.
type contextRunner struct {
	inner      runner.Runner
	tenantID   string
	appID      string
	revisionID string
}

// Run ignores userID and sessionID: both arrive from the untrusted request body
// and headers. A request without a matching trusted scope never reaches the
// agent.
func (r *contextRunner) Run(
	ctx context.Context,
	_ string,
	_ string,
	message model.Message,
	options ...trpcagent.RunOption,
) (<-chan *event.Event, error) {
	if r == nil || r.inner == nil {
		return nil, fmt.Errorf("%w: protocol runner is not configured", ErrUntrustedRun)
	}
	runContext, err := identity.RunContextFrom(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUntrustedRun, err)
	}
	if runContext.TenantID != r.tenantID ||
		runContext.AppID != r.appID ||
		runContext.RevisionID != r.revisionID {
		return nil, fmt.Errorf(
			"%w: scope %q/%q/%q does not match runtime %q/%q/%q",
			ErrUntrustedRun,
			runContext.TenantID,
			runContext.AppID,
			runContext.RevisionID,
			r.tenantID,
			r.appID,
			r.revisionID,
		)
	}
	return r.inner.Run(ctx, runContext.UserID(), runContext.SessionID, message, options...)
}

// Close is deliberately a no-op. The Runtime owns the real Runner and closes it
// exactly once, so an adapter that decided to close its injected Runner must
// not be able to reach it through this wrapper.
func (r *contextRunner) Close() error {
	return nil
}
