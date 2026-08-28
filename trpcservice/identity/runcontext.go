package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// ErrNoRunContext reports a run that never passed platform authentication.
var ErrNoRunContext = errors.New("identity: request has no trusted run context")

// userIDPrefix namespaces platform principals inside the framework Session key
// space, so a principal can never collide with a raw client-supplied user.
const userIDPrefix = "u/"

// RunContext is the trusted execution scope of one data-plane request. The
// platform writes it once, before any protocol adapter sees the request, and
// everything downstream reads identity from here instead of from the payload.
type RunContext struct {
	TenantID    string
	AppID       string
	PrincipalID string
	SessionID   string
	RevisionID  string
}

// Validate rejects a scope that cannot address exactly one conversation of one
// revision.
func (c RunContext) Validate() error {
	if err := tenant.ValidateResourceID("tenant id", c.TenantID); err != nil {
		return err
	}
	if err := tenant.ValidateResourceID("app id", c.AppID); err != nil {
		return err
	}
	if err := tenant.ValidateResourceID("principal id", c.PrincipalID); err != nil {
		return err
	}
	if err := tenant.ValidateResourceID("session id", c.SessionID); err != nil {
		return err
	}
	return tenant.ValidateResourceID("revision id", c.RevisionID)
}

// UserID is the framework Session user key of this principal. A client-supplied
// user field never reaches it.
func (c RunContext) UserID() string {
	return userIDPrefix + c.PrincipalID
}

// runContextKey is unexported, so only this package can attach or replace a
// trusted scope.
type runContextKey struct{}

// WithRunContext attaches a validated scope. An invalid scope is an error here
// rather than a context value that silently fails later.
func WithRunContext(ctx context.Context, runContext RunContext) (context.Context, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", tenant.ErrInvalidArgument)
	}
	if err := runContext.Validate(); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, runContextKey{}, runContext), nil
}

// RunContextFrom returns the trusted scope of a request, or ErrNoRunContext
// when the request did not pass platform authentication.
func RunContextFrom(ctx context.Context) (RunContext, error) {
	if ctx == nil {
		return RunContext{}, ErrNoRunContext
	}
	runContext, ok := ctx.Value(runContextKey{}).(RunContext)
	if !ok {
		return RunContext{}, ErrNoRunContext
	}
	if err := runContext.Validate(); err != nil {
		return RunContext{}, fmt.Errorf("%w: %w", ErrNoRunContext, err)
	}
	return runContext, nil
}
