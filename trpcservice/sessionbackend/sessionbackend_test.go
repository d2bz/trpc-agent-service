package sessionbackend

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	sessionpostgres "trpc.group/trpc-go/trpc-agent-go/session/postgres"
	sessionredis "trpc.group/trpc-go/trpc-agent-go/session/redis"
)

// Every backend this package can build has to stay assignable to the one
// interface the Runner consumes. These assertions are the contract: an
// upstream release that changes a method set breaks the build here rather
// than at the call site, or silently at runtime.
var (
	_ session.Service = (*sessioninmemory.SessionService)(nil)
	_ session.Service = (*sessionpostgres.Service)(nil)
	_ session.Service = (*sessionredis.Service)(nil)
)

// The tests in this file must never contact postgres or redis, so `go test
// ./...` stays runnable on a machine with no infrastructure and no network.
// Anything needing a real server belongs in integration_test.go, behind the
// TRPC_SERVICE_SESSION_INTEGRATION gate.

// newTurn builds the two events of one conversation turn.
//
// Two upstream rules decide whether an appended event is still there on the
// next read, and a test that ignores either one passes against an empty
// session:
//
//  1. An event is only recorded when its Response is non-nil, not partial and
//     carries a payload. session.Session.UpdateUserSession applies this to
//     every backend, and the postgres persist path applies it again.
//  2. session.Session.ApplyEventFiltering discards the whole event list unless
//     it holds at least one user message. An assistant-only session therefore
//     reads back empty even though AppendEvent returned nil.
//
// So the fixture is a user message followed by the assistant reply, which is
// what a real turn looks like anyway.
func newTurn(invocationID, prompt, reply string) (*event.Event, *event.Event) {
	return newTestEvent(invocationID+"-user", model.NewUserMessage(prompt)),
		newTestEvent(invocationID+"-assistant", model.NewAssistantMessage(reply))
}

func newTestEvent(invocationID string, message model.Message) *event.Event {
	return event.NewResponseEvent(invocationID, "sessionbackend-test", &model.Response{
		Done:    true,
		Choices: []model.Choice{{Index: 0, Message: message}},
	})
}

func TestDefaultConfigIsInMemory(t *testing.T) {
	cfg := DefaultConfig()
	require.Equal(t, BackendInMemory, cfg.Backend)
	require.NoError(t, cfg.Validate())

	service, err := New(cfg)
	require.NoError(t, err)
	require.NotNil(t, service)
	t.Cleanup(func() { require.NoError(t, service.Close()) })

	// The default backend has to be usable with no external service running.
	ctx := context.Background()
	key := session.Key{AppName: "app", UserID: "user", SessionID: "sess"}
	created, err := service.CreateSession(ctx, key, session.StateMap{})
	require.NoError(t, err)
	require.NotNil(t, created)

	prompt, reply := newTurn("inv-1", "hello", "hello back")
	require.NoError(t, service.AppendEvent(ctx, created, prompt))
	require.NoError(t, service.AppendEvent(ctx, created, reply))

	loaded, err := service.GetSession(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Len(t, loaded.Events, 2)
}

// TestAssistantOnlySessionReadsBackEmpty pins the upstream filtering rule that
// newTurn exists to work around. It is not behaviour this package chose, but a
// caller that appends only assistant events loses them silently, so the spike
// records it as a fact rather than a surprise.
func TestAssistantOnlySessionReadsBackEmpty(t *testing.T) {
	service, err := New(DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Close()) })

	ctx := context.Background()
	key := session.Key{AppName: "app", UserID: "user", SessionID: "assistant-only"}
	created, err := service.CreateSession(ctx, key, session.StateMap{})
	require.NoError(t, err)

	// AppendEvent reports success.
	require.NoError(t, service.AppendEvent(
		ctx, created, newTestEvent("inv-1", model.NewAssistantMessage("hello")),
	))

	loaded, err := service.GetSession(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Empty(t, loaded.Events, "an event list with no user message is dropped upstream")
}

func TestValidateAcceptsPersistentBackends(t *testing.T) {
	// Validate must decide on the config alone. If any of these cases tried to
	// connect, this test would fail or hang on a machine with no server.
	cases := map[string]Config{
		"postgres dsn only": {
			Backend:  BackendPostgres,
			Postgres: PostgresConfig{DSN: "postgres://u:p@127.0.0.1:55432/db?sslmode=disable"},
		},
		"postgres keyword dsn": {
			Backend:  BackendPostgres,
			Postgres: PostgresConfig{DSN: "host=127.0.0.1 port=55432 user=u password=p dbname=db"},
		},
		"postgres with namespacing": {
			Backend: BackendPostgres,
			Postgres: PostgresConfig{
				DSN:         "postgres://u:p@127.0.0.1:55432/db",
				TablePrefix: "spike_run_1",
				Schema:      "spike_schema",
			},
		},
		"postgres prefix with trailing underscore": {
			Backend: BackendPostgres,
			Postgres: PostgresConfig{
				DSN:         "postgres://u:p@127.0.0.1:55432/db",
				TablePrefix: "spike_",
			},
		},
		"redis url only": {
			Backend: BackendRedis,
			Redis:   RedisConfig{URL: "redis://127.0.0.1:56379/0"},
		},
		"redis with key prefix": {
			Backend: BackendRedis,
			Redis:   RedisConfig{URL: "redis://127.0.0.1:56379/0", KeyPrefix: "spike:run-1"},
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, cfg.Validate())
		})
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	cases := map[string]struct {
		cfg  Config
		want string
	}{
		"empty backend": {
			cfg:  Config{},
			want: "backend is required",
		},
		"unknown backend": {
			cfg:  Config{Backend: Backend("mysql")},
			want: `unknown backend "mysql"`,
		},
		"backend is case sensitive": {
			cfg:  Config{Backend: Backend("InMemory")},
			want: `unknown backend "InMemory"`,
		},
		"postgres without dsn": {
			cfg:  Config{Backend: BackendPostgres},
			want: "postgres backend requires a DSN",
		},
		"postgres with blank dsn": {
			cfg:  Config{Backend: BackendPostgres, Postgres: PostgresConfig{DSN: "   "}},
			want: "postgres backend requires a DSN",
		},
		"redis without url": {
			cfg:  Config{Backend: BackendRedis},
			want: "redis backend requires a URL",
		},
		"redis with blank url": {
			cfg:  Config{Backend: BackendRedis, Redis: RedisConfig{URL: "\t"}},
			want: "redis backend requires a URL",
		},
		// Upstream validates the prefix and the schema through a helper that
		// panics. These cases prove the panic is unreachable through New.
		"table prefix with a quote": {
			cfg: Config{Backend: BackendPostgres, Postgres: PostgresConfig{
				DSN: "postgres://u:p@127.0.0.1:55432/db", TablePrefix: "spike';DROP TABLE x;--",
			}},
			want: "invalid postgres table prefix",
		},
		"table prefix starting with a digit": {
			cfg: Config{Backend: BackendPostgres, Postgres: PostgresConfig{
				DSN: "postgres://u:p@127.0.0.1:55432/db", TablePrefix: "1spike",
			}},
			want: "invalid postgres table prefix",
		},
		"table prefix too long": {
			cfg: Config{Backend: BackendPostgres, Postgres: PostgresConfig{
				DSN: "postgres://u:p@127.0.0.1:55432/db", TablePrefix: strings.Repeat("a", maxNamespaceLen+1),
			}},
			want: "postgres table prefix is 33 characters",
		},
		"schema with a space": {
			cfg: Config{Backend: BackendPostgres, Postgres: PostgresConfig{
				DSN: "postgres://u:p@127.0.0.1:55432/db", Schema: "my schema",
			}},
			want: "invalid postgres schema",
		},
		"key prefix with a space": {
			cfg: Config{Backend: BackendRedis, Redis: RedisConfig{
				URL: "redis://127.0.0.1:56379/0", KeyPrefix: "spike run",
			}},
			want: "invalid redis key prefix",
		},
		"key prefix with a cluster hash tag": {
			cfg: Config{Backend: BackendRedis, Redis: RedisConfig{
				URL: "redis://127.0.0.1:56379/0", KeyPrefix: "spike{shard}",
			}},
			want: "invalid redis key prefix",
		},
		"key prefix too long": {
			cfg: Config{Backend: BackendRedis, Redis: RedisConfig{
				URL: "redis://127.0.0.1:56379/0", KeyPrefix: strings.Repeat("k", maxNamespaceLen+1),
			}},
			want: "redis key prefix is 33 characters",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.cfg.Validate()
			require.Error(t, err)
			require.ErrorIs(t, err, ErrInvalidConfig)
			require.Contains(t, err.Error(), tc.want)

			// New must reject exactly what Validate rejects, and must never
			// hand back a service the caller would then have to close.
			service, err := New(tc.cfg)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrInvalidConfig)
			require.Nil(t, service)
		})
	}
}

func TestValidateIgnoresUnselectedBackends(t *testing.T) {
	// A process that carries settings for all three backends and selects one
	// must not be rejected for the two it is not using.
	cfg := Config{
		Backend:  BackendInMemory,
		Postgres: PostgresConfig{TablePrefix: "not valid at all"},
		Redis:    RedisConfig{KeyPrefix: "not valid either"},
	}
	require.NoError(t, cfg.Validate())

	service, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, service.Close())
}

func TestDescribeHidesCredentials(t *testing.T) {
	const password = "sup3r-s3cret"
	cases := map[string]Config{
		"postgres url dsn": {
			Backend:  BackendPostgres,
			Postgres: PostgresConfig{DSN: "postgres://user:" + password + "@127.0.0.1:55432/db"},
		},
		"postgres keyword dsn": {
			Backend:  BackendPostgres,
			Postgres: PostgresConfig{DSN: "host=127.0.0.1 user=user password=" + password},
		},
		"redis url": {
			Backend: BackendRedis,
			Redis:   RedisConfig{URL: "redis://user:" + password + "@127.0.0.1:56379/0"},
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			described := cfg.Describe()
			require.NotContains(t, described, password)
			require.Contains(t, described, "set")
		})
	}

	require.Contains(t, Config{Backend: BackendRedis}.Describe(), "absent")
	require.Contains(t, DefaultConfig().Describe(), "inmemory")
}
