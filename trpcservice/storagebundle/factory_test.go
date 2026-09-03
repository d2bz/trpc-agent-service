package storagebundle

import (
	"context"
	"testing"

	"github.com/liuzengh/trpc-agent-service/trpcservice/sessionbackend"
	"github.com/stretchr/testify/require"
)

// The one backend this slice builds, and the contract it builds it under: a
// Bundle and its close, both non-nil, and a store that actually works.
func TestSessionFactoryBuildsInMemoryWithItsClose(t *testing.T) {
	factory := NewSessionFactory(ProcessConstraints{})

	bundle, closeBundle, err := factory.Build(
		context.Background(), inMemoryProfile("tenant-a", "p1"))
	require.NoError(t, err)
	require.NotNil(t, closeBundle, "a Bundle without its close can never be released")
	require.NoError(t, bundle.Validate())

	require.NoError(t, closeBundle())
}

// Two builds of the same profile are two independent stores. The Router is what
// makes one profile one Bundle; a Factory that cached would be a second, hidden
// answer to the same question, and closing one would take the other down.
func TestSessionFactoryBuildsIndependentStores(t *testing.T) {
	factory := NewSessionFactory(ProcessConstraints{})
	profile := inMemoryProfile("tenant-a", "p1")

	first, closeFirst, err := factory.Build(context.Background(), profile)
	require.NoError(t, err)
	second, closeSecond, err := factory.Build(context.Background(), profile)
	require.NoError(t, err)
	require.NotSame(t, first.Session, second.Session)

	require.NoError(t, closeFirst())
	require.NoError(t, closeSecond())
}

// A durable session store in a process whose session directory is not durable
// keeps the conversation and loses the revision it was pinned to. It is refused
// before it is reported as unimplemented, because implementing it would not
// make the arrangement safe.
func TestSessionFactoryRefusesDurableBackendsWithoutDurablePins(t *testing.T) {
	factory := NewSessionFactory(ProcessConstraints{DurablePins: false})

	for _, profile := range []Profile{
		postgresProfile("tenant-a", "p1"),
		redisProfile("tenant-a", "p2"),
	} {
		bundle, closeBundle, err := factory.Build(context.Background(), profile)
		require.ErrorIs(t, err, ErrPinsNotDurable)
		require.NotErrorIs(t, err, ErrUnsupportedBackend)
		require.Nil(t, closeBundle)
		require.Equal(t, Bundle{}, bundle)
		// The refusal names the profile and its tenant, so an operator reading
		// one line of log knows which revision stopped working.
		require.ErrorContains(t, err, profile.ID)
		require.ErrorContains(t, err, "tenant-a")
	}
}

// With durable pins the same profiles are refused for the honest reason: this
// slice does not build them. Resolving a secret reference into a connection
// belongs to the slice that adds profile storage.
func TestSessionFactoryReportsUnbuiltBackendsAsUnsupported(t *testing.T) {
	factory := NewSessionFactory(ProcessConstraints{DurablePins: true})

	for _, profile := range []Profile{
		postgresProfile("tenant-a", "p1"),
		redisProfile("tenant-a", "p2"),
	} {
		bundle, closeBundle, err := factory.Build(context.Background(), profile)
		require.ErrorIs(t, err, ErrUnsupportedBackend)
		require.NotErrorIs(t, err, ErrPinsNotDurable)
		require.Nil(t, closeBundle)
		require.Equal(t, Bundle{}, bundle)
		require.ErrorContains(t, err, string(profile.Session.Backend))
	}
}

// An in-process store under a shared run lease is unshared state behind a lock
// its peers cannot see anything through. The process-level configuration
// refuses that combination at startup; a per-tenant profile must not be able to
// reintroduce it.
func TestSessionFactoryRefusesInMemoryAcrossWorkers(t *testing.T) {
	factory := NewSessionFactory(ProcessConstraints{DurablePins: true, MultiWorker: true})

	bundle, closeBundle, err := factory.Build(
		context.Background(), inMemoryProfile("tenant-a", "p1"))
	require.ErrorIs(t, err, ErrNotSharedAcrossWorkers)
	require.Nil(t, closeBundle)
	require.Equal(t, Bundle{}, bundle)

	// And with a single worker the same profile builds, so the refusal above is
	// about the arrangement and not about the profile.
	single := NewSessionFactory(ProcessConstraints{DurablePins: true})
	bundle, closeBundle, err = single.Build(
		context.Background(), inMemoryProfile("tenant-a", "p1"))
	require.NoError(t, err)
	require.NoError(t, bundle.Validate())
	require.NoError(t, closeBundle())
}

// Validation is repeated here rather than assumed from the Router: this is the
// function that builds, and the upstream namespacing options panic rather than
// return on input they dislike.
func TestSessionFactoryRevalidatesTheProfile(t *testing.T) {
	factory := NewSessionFactory(ProcessConstraints{})

	for _, tc := range []struct {
		name    string
		profile Profile
	}{
		{"no tenant", Profile{ID: "p1", Session: SessionSpec{Backend: sessionbackend.BackendInMemory}}},
		{"no id", Profile{TenantID: "tenant-a", Session: SessionSpec{Backend: sessionbackend.BackendInMemory}}},
		{"no backend", Profile{TenantID: "tenant-a", ID: "p1"}},
		{
			"unknown backend",
			Profile{TenantID: "tenant-a", ID: "p1", Session: SessionSpec{Backend: "cassandra"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle, closeBundle, err := factory.Build(context.Background(), tc.profile)
			require.ErrorIs(t, err, ErrInvalidProfile)
			require.Nil(t, closeBundle)
			require.Equal(t, Bundle{}, bundle)
		})
	}
}

// The context is checked before anything is constructed. A Router that is
// already shutting down must not open a store nobody will ever reach — and one
// nobody will ever close, since the Router has passed the point where it waits
// for builds.
func TestSessionFactoryRefusesADoneContextBeforeBuilding(t *testing.T) {
	factory := NewSessionFactory(ProcessConstraints{})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	bundle, closeBundle, err := factory.Build(cancelled, inMemoryProfile("tenant-a", "p1"))
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, closeBundle)
	require.Equal(t, Bundle{}, bundle)

	//nolint:staticcheck // a nil context is exactly what is under test here.
	bundle, closeBundle, err = factory.Build(nil, inMemoryProfile("tenant-a", "p1"))
	require.Error(t, err)
	require.Nil(t, closeBundle)
	require.Equal(t, Bundle{}, bundle)
}
