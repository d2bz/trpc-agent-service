package main

import (
	"context"
	"errors"

	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	"github.com/liuzengh/trpc-agent-service/trpcservice/security"
	"github.com/liuzengh/trpc-agent-service/trpcservice/storagebundle"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// runtimeStack is the two caches a serving process keeps in front of its
// storage: Runtimes, and the storage Bundles those Runtimes run on.
//
// They are one type because they have one shutdown order and it is not
// negotiable. A Runtime holds a lease on its Bundle for its whole life, so the
// Router cannot finish closing until every Runtime has been closed — closing
// them in the other order blocks until the process is killed. Registering two
// defers in the right sequence would work too, right up until somebody inserts
// a third thing between them.
type runtimeStack struct {
	resolver *platformagent.RuntimeResolver
	router   *storagebundle.Router
}

// openRuntimeStack wires the Router and the RuntimeResolver over storage this
// process already opened.
//
// The Router borrows stack.sessions and never closes it: that store belongs to
// the storageStack, which has to be able to release it on a startup failure at
// a point where no Router exists yet.
//
// Dynamic profiles are NoProfiles here. This process has no profile storage, so
// it cannot honour a reference to one, and a revision that names a profile is
// refused rather than served by the default store it did not ask for.
func openRuntimeStack(
	cfg storageConfig,
	stack *storageStack,
	revisions security.RevisionAuthorizer,
) (*runtimeStack, error) {
	router, err := storagebundle.NewRouter(storagebundle.Options{
		Default: storagebundle.Bundle{Session: stack.sessions},
		Source:  storagebundle.NoProfiles(),
		Factory: storagebundle.NewSessionFactory(processConstraints(cfg)),
	})
	if err != nil {
		return nil, err
	}
	resolver, err := platformagent.NewRuntimeResolver(
		stack.repository,
		func(
			ctx context.Context,
			revision tenant.AgentRevision,
		) (*platformagent.Runtime, error) {
			// ctx is the resolver's build context, not a request's: it is
			// cancelled when the resolver closes, so a build that is waiting on
			// storage stops when the process stops rather than after it.
			//
			// revisions is the same authorizer value the Admin API checks
			// against. One instance, not two equivalent ones: a revision that
			// Admin accepted and a Runtime later refused — or the reverse —
			// would be a disagreement about what this tenant may do, and there
			// is no correct way to resolve one at request time.
			return platformagent.NewRuntime(ctx, revision, router, revisions)
		},
	)
	if err != nil {
		return nil, errors.Join(err, router.Close())
	}
	return &runtimeStack{resolver: resolver, router: router}, nil
}

// processConstraints derives what a tenant backend profile may not do in this
// process from what this process itself is.
//
// Both axes come from the storage configuration rather than being configured
// again: they are the same two invariants storage.go refuses to boot without,
// and a second switch for them would be a second answer to one question.
func processConstraints(cfg storageConfig) storagebundle.ProcessConstraints {
	return storagebundle.ProcessConstraints{
		DurablePins: cfg.profile == profilePostgres,
		MultiWorker: cfg.coordination == coordinationRedis,
	}
}

// close releases the Runtimes, then the Bundles they were running on.
func (s *runtimeStack) close() error {
	if s == nil {
		return nil
	}
	// The resolver first: it waits for in-flight runs, then closes every
	// cached Runtime, and each of those releases its Bundle lease on the way
	// out. Only then can the Router's own wait finish.
	closeErr := s.resolver.Close()
	return errors.Join(closeErr, s.router.Close())
}
