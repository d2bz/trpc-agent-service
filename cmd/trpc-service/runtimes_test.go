package main

import (
	"context"
	"sync"
	"testing"
	"time"

	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	platformconfig "github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/security"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessiondir"
	"github.com/liuzengh/trpc-agent-service/trpcservice/storagebundle"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

// countingSessions is the process session store, instrumented.
//
// It wraps a working service rather than stubbing one out: these tests build
// real Runtimes on it, and what has to be observable is only who closes it —
// the storageStack owns that store, and the Router must never take it.
type countingSessions struct {
	session.Service

	mu     sync.Mutex
	closes int
}

func (s *countingSessions) Close() error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	return s.Service.Close()
}

func (s *countingSessions) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

// seededStack builds the storage a Worker runs on, in memory, with the demo
// tenant seeded so a revision can actually be resolved through it.
func seededStack(t *testing.T) (*storageStack, *countingSessions) {
	t.Helper()
	sessions := &countingSessions{Service: sessioninmemory.NewSessionService()}
	stack := &storageStack{
		repository: tenant.NewMemoryRepository(),
		directory:  sessiondir.NewMemoryDirectory(),
		sessions:   sessions,
	}
	require.NoError(t, platformconfig.SeedDemo(context.Background(), stack.repository))
	return stack, sessions
}

func demoScope() tenant.TenantContext {
	return tenant.TenantContext{TenantID: platformconfig.DemoTenantID}
}

// The Router this process assembles borrows the store the process opened, and
// resolves the empty profile id to it. Nothing else is reachable.
func TestOpenRuntimeStackServesTheProcessDefaultStore(t *testing.T) {
	stack, sessions := seededStack(t)
	t.Cleanup(func() { require.NoError(t, stack.close()) })

	runtimes, err := openRuntimeStack(
		inMemoryTestConfig(), stack, security.DenyCapabilities())
	require.NoError(t, err)

	lease, err := runtimes.router.Resolve(context.Background(), demoScope(), "")
	require.NoError(t, err)
	require.Same(t, stack.sessions, lease.Bundle().Session)
	require.NoError(t, lease.Release())

	require.NoError(t, runtimes.close())
	require.Zero(t, sessions.closeCount(), "the Router closed a store it only borrowed")
}

// This process has no profile storage, so it cannot honour a profile
// reference — and a revision that names one is refused rather than served by
// the default store it did not ask for.
func TestOpenRuntimeStackRefusesEveryNamedProfile(t *testing.T) {
	stack, _ := seededStack(t)
	t.Cleanup(func() { require.NoError(t, stack.close()) })

	runtimes, err := openRuntimeStack(
		inMemoryTestConfig(), stack, security.DenyCapabilities())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtimes.close()) })

	lease, err := runtimes.router.Resolve(context.Background(), demoScope(), "tenant-postgres")
	require.ErrorIs(t, err, storagebundle.ErrProfileNotFound)
	require.Nil(t, lease)

	// And the same refusal reaches a Runtime built the way the process builds
	// them, so the revision is refused rather than quietly re-pointed.
	revision, err := stack.repository.ResolveRevision(
		context.Background(), demoScope(), platformconfig.DemoAgentAppID, "")
	require.NoError(t, err)
	revision.Config.BackendProfileID = "tenant-postgres"
	digest, err := revision.Config.Digest()
	require.NoError(t, err)
	revision.ConfigDigest = digest

	runtime, err := platformagent.NewRuntime(
		context.Background(), revision, runtimes.router, security.DenyCapabilities())
	require.ErrorIs(t, err, storagebundle.ErrProfileNotFound)
	require.Nil(t, runtime)
}

// A Runtime the resolver cached holds a lease on its Bundle for its whole life,
// so the Router cannot finish closing until every Runtime has been closed.
//
// That makes the order inside runtimeStack.close a liveness property rather
// than a preference: closing the Router first would block here until the
// process was killed. The assertion is a deadline, because a deadlock has no
// other symptom.
func TestRuntimeStackCloseReleasesRuntimesBeforeTheirBundles(t *testing.T) {
	stack, sessions := seededStack(t)
	t.Cleanup(func() { require.NoError(t, stack.close()) })

	runtimes, err := openRuntimeStack(
		inMemoryTestConfig(), stack, security.DenyCapabilities())
	require.NoError(t, err)

	resolved, err := runtimes.resolver.Resolve(
		context.Background(), demoScope(), platformconfig.DemoAgentAppID, "")
	require.NoError(t, err)
	require.Same(t, stack.sessions, resolved.Runtime.SessionService)
	// The run is over, but the Runtime stays cached — and keeps holding its
	// Bundle lease. This is the state a live process is in between requests.
	resolved.Release()

	closed := make(chan error, 1)
	go func() { closed <- runtimes.close() }()
	select {
	case closeErr := <-closed:
		require.NoError(t, closeErr)
	case <-time.After(10 * time.Second):
		t.Fatal("runtimeStack.close deadlocked: the Router waited for a Runtime " +
			"that had not been closed yet")
	}

	require.Zero(t, sessions.closeCount(), "the process store is the stack's to close")
}

// Closing twice is what a partial startup failure and the shutdown path do
// between them, so it must be safe and must report the same thing.
func TestRuntimeStackCloseIsSafeToRepeatAndOnNil(t *testing.T) {
	stack, _ := seededStack(t)
	t.Cleanup(func() { require.NoError(t, stack.close()) })

	runtimes, err := openRuntimeStack(
		inMemoryTestConfig(), stack, security.DenyCapabilities())
	require.NoError(t, err)

	require.NoError(t, runtimes.close())
	require.NoError(t, runtimes.close())

	var absent *runtimeStack
	require.NoError(t, absent.close())
}

// What a tenant profile may not do in this process is derived from what this
// process is, rather than configured a second time. A second switch for these
// two would be a second answer to one question.
func TestProcessConstraintsFollowTheStorageConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  storageConfig
		want storagebundle.ProcessConstraints
	}{
		{
			name: "inmemory alone",
			cfg:  storageConfig{profile: profileInMemory, coordination: coordinationInMemory},
			want: storagebundle.ProcessConstraints{},
		},
		{
			name: "postgres alone",
			cfg:  storageConfig{profile: profilePostgres, coordination: coordinationInMemory},
			want: storagebundle.ProcessConstraints{DurablePins: true},
		},
		{
			name: "postgres with redis coordination",
			cfg:  storageConfig{profile: profilePostgres, coordination: coordinationRedis},
			want: storagebundle.ProcessConstraints{DurablePins: true, MultiWorker: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, processConstraints(tc.cfg))
		})
	}
}

// The derivation is not decoration: it decides what the Factory refuses.
//
// Under the in-memory profile a tenant profile asking for a durable backend is
// refused, because the pin that names its revision would not survive the
// restart the sessions would. That is the same invariant storage.go refuses to
// boot without, reaching the one place that could otherwise reopen it.
func TestProcessConstraintsReachTheFactory(t *testing.T) {
	durable := storagebundle.Profile{
		TenantID: platformconfig.DemoTenantID,
		ID:       "durable",
		Session: storagebundle.SessionSpec{
			Backend:  "postgres",
			Postgres: &storagebundle.PostgresSpec{DSNRef: "env:DEMO_SESSION_DSN"},
		},
	}

	factory := storagebundle.NewSessionFactory(processConstraints(inMemoryTestConfig()))
	_, _, err := factory.Build(context.Background(), durable)
	require.ErrorIs(t, err, storagebundle.ErrPinsNotDurable)

	// A Worker that coordinates with others refuses the opposite arrangement:
	// an in-process store behind a lock its peers cannot see anything through.
	multiWorker := storagebundle.NewSessionFactory(processConstraints(storageConfig{
		profile:      profilePostgres,
		coordination: coordinationRedis,
	}))
	_, _, err = multiWorker.Build(context.Background(), storagebundle.Profile{
		TenantID: platformconfig.DemoTenantID,
		ID:       "shared",
		Session:  storagebundle.SessionSpec{Backend: "inmemory"},
	})
	require.ErrorIs(t, err, storagebundle.ErrNotSharedAcrossWorkers)
}

// inMemoryTestConfig is the configuration a Worker with no external
// dependencies boots on.
func inMemoryTestConfig() storageConfig {
	return storageConfig{profile: profileInMemory, coordination: coordinationInMemory}
}
