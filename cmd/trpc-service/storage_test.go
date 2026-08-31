package main

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessionbackend"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessiondir"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

// Nothing in this file contacts a database. The point of most of it is that the
// refusals and the cleanup happen *before* anything could: a test that needed a
// server to prove a startup failure would not be proving it.
//
// Every test sets all three storage variables explicitly. A developer with the
// integration environment exported has a DSN in their shell, and a test that
// only cleared the profile would then assert something different from what CI
// asserts.

const testPassword = "s3cret"

// setStorageEnv puts the three storage variables in a known state for the
// duration of one test. Empty means unset as far as this process is concerned.
func setStorageEnv(t *testing.T, profile, dsn, schema string) {
	t.Helper()
	t.Setenv(storageProfileEnvVar, profile)
	t.Setenv(postgresDSNEnvVar, dsn)
	t.Setenv(postgresSchemaEnvVar, schema)
}

// The demo has to keep running on an empty machine: no profile at all means
// in-memory, and in-memory means no external dependency.
func TestLoadStorageConfigDefaultsToInMemory(t *testing.T) {
	setStorageEnv(t, "", "", "")

	cfg, err := loadStorageConfig(os.Getenv)
	require.NoError(t, err)
	require.Equal(t, profileInMemory, cfg.profile)
	require.Equal(t, sessionbackend.DefaultConfig(), cfg.sessionConfig())
}

// A DSN sitting in the environment must never be what turns persistence on.
// Storage that writes to a shared database is something an operator asks for by
// name, and an integration variable left exported is not that request.
func TestLoadStorageConfigIgnoresStrayPostgresSettings(t *testing.T) {
	const strayDSN = "postgres://user:" + testPassword + "@127.0.0.1:55432/db"

	for _, profile := range []string{"", string(profileInMemory)} {
		t.Run("profile="+strconv.Quote(profile), func(t *testing.T) {
			setStorageEnv(t, profile, strayDSN, "some_schema")

			cfg, err := loadStorageConfig(os.Getenv)
			require.NoError(t, err)
			require.Equal(t, profileInMemory, cfg.profile)
			require.Empty(t, cfg.dsn)
			require.Empty(t, cfg.schema)
			require.Equal(t, sessionbackend.DefaultConfig(), cfg.sessionConfig())
			require.NotContains(t, cfg.describe(), testPassword)

			// And it stays in-memory all the way through construction. Building
			// the session service is the only step there is: nothing opens a
			// pool, pings, migrates, or builds a control plane.
			stub := &stubStorage{}
			stack, err := openStorage(context.Background(), cfg, stub.deps())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, stack.close()) })
			require.Equal(t, []string{"new sessions"}, stub.steps)
		})
	}
}

// An unknown profile is a refusal, not a fallback. Quietly defaulting would
// give an operator who mistyped the profile a process that looks healthy and
// loses every conversation on restart.
func TestLoadStorageConfigRejectsUnknownProfiles(t *testing.T) {
	for _, profile := range []string{
		"redis",     // a backend that exists, but not as a whole profile
		"Postgres",  // case matters
		"INMEMORY",  // including here
		"pg",        // an abbreviation nothing accepts
		" postgres", // padding is not trimmed
		"postgres ",
		"inmemory\n",
		"in-memory",
		"memory",
		"true",
	} {
		t.Run(strconv.Quote(profile), func(t *testing.T) {
			setStorageEnv(t, profile, "", "")

			_, err := loadStorageConfig(os.Getenv)
			require.ErrorIs(t, err, errStorageConfig)
			require.ErrorContains(t, err, storageProfileEnvVar)
			// The refusal has to say what is accepted instead.
			require.ErrorContains(t, err, string(profileInMemory))
			require.ErrorContains(t, err, string(profilePostgres))
		})
	}
}

// The postgres profile without a DSN is refused before anything is opened, and
// the refusal names the variable to set — "postgres backend requires a DSN"
// from the layer below does not tell an operator which one that is.
func TestLoadStorageConfigRequiresADSNForPostgres(t *testing.T) {
	for _, dsn := range []string{"", "   ", "\t", "\n\n", " \t\n "} {
		t.Run(strconv.Quote(dsn), func(t *testing.T) {
			setStorageEnv(t, string(profilePostgres), dsn, "")

			_, err := loadStorageConfig(os.Getenv)
			require.ErrorIs(t, err, errStorageConfig)
			require.ErrorContains(t, err, postgresDSNEnvVar)
		})
	}
}

// A bad schema is caught by configuration, not by a failing statement half way
// through a migration. The rules are sessionbackend's, and they have to be
// applied before construction because the upstream schema option panics rather
// than returning an error.
func TestLoadStorageConfigRejectsABadSchema(t *testing.T) {
	const dsn = "postgres://user:" + testPassword + "@127.0.0.1:55432/db"

	for _, tc := range []struct {
		name   string
		schema string
	}{
		{name: "leading digit", schema: "1_schema"},
		{name: "hyphen", schema: "bad-schema"},
		{name: "space", schema: "two words"},
		{name: "injection attempt", schema: `x"; DROP SCHEMA public CASCADE; --`},
		{name: "qualified name", schema: "public.tenants"},
		{name: "one over the limit", schema: strings.Repeat("s", 33)},
		{name: "far too long", schema: strings.Repeat("s", 200)},
		{name: "leading space", schema: " public"},
		{name: "control character", schema: "sch\tema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setStorageEnv(t, string(profilePostgres), dsn, tc.schema)

			_, err := loadStorageConfig(os.Getenv)
			require.ErrorIs(t, err, errStorageConfig)
			require.ErrorIs(t, err, sessionbackend.ErrInvalidConfig)
			require.ErrorContains(t, err, postgresSchemaEnvVar)
			require.NotContains(t, err.Error(), testPassword, "a schema error must not carry the DSN")
		})
	}

	t.Run("a valid schema is accepted", func(t *testing.T) {
		setStorageEnv(t, string(profilePostgres), dsn, "trpc_service")

		cfg, err := loadStorageConfig(os.Getenv)
		require.NoError(t, err)
		require.Equal(t, profilePostgres, cfg.profile)
		require.Equal(t, "trpc_service", cfg.schema)
		// The session store shares the profile's DSN and schema; nothing
		// configures it separately.
		require.Equal(t, "trpc_service", cfg.sessionConfig().Postgres.Schema)
		require.Equal(t, dsn, cfg.sessionConfig().Postgres.DSN)
		require.Equal(t, sessionbackend.BackendPostgres, cfg.sessionConfig().Backend)
	})

	t.Run("no schema is accepted", func(t *testing.T) {
		setStorageEnv(t, string(profilePostgres), dsn, "")

		cfg, err := loadStorageConfig(os.Getenv)
		require.NoError(t, err)
		require.Empty(t, cfg.schema)
	})
}

// A NUL byte is the one bad schema that cannot be tested through the
// environment: os.Setenv rejects a value containing one, so t.Setenv would fail
// the test rather than exercise the code under it. validate still has to refuse
// it — a config assembled in code reaches validate as well — so it is asserted
// at that layer instead.
func TestValidateRejectsASchemaTheEnvironmentCannotCarry(t *testing.T) {
	err := storageConfig{
		profile: profilePostgres,
		dsn:     "postgres://user:" + testPassword + "@127.0.0.1:55432/db",
		schema:  "sch\x00ema",
	}.validate()
	require.ErrorIs(t, err, errStorageConfig)
	require.ErrorIs(t, err, sessionbackend.ErrInvalidConfig)
	require.ErrorContains(t, err, postgresSchemaEnvVar)
	require.NotContains(t, err.Error(), testPassword, "a schema error must not carry the DSN")
}

// An unknown profile is refused wherever it comes from, not only from the
// environment. openStorage validates again precisely because a config can also
// be built in code, and "not postgres" must not mean "in-memory": a profile
// nobody recognises has to fail before any constructor rather than quietly
// producing a process that looks healthy and loses every conversation on
// restart.
func TestOpenStorageRefusesAProfileItDoesNotRecognise(t *testing.T) {
	for _, profile := range []storageProfile{"redis", "Postgres", "postgres ", "memory", ""} {
		t.Run(strconv.Quote(string(profile)), func(t *testing.T) {
			stub := &stubStorage{}

			stack, err := openStorage(
				context.Background(), storageConfig{profile: profile}, stub.deps())
			require.ErrorIs(t, err, errStorageConfig)
			require.ErrorContains(t, err, string(profileInMemory))
			require.ErrorContains(t, err, string(profilePostgres))
			require.Nil(t, stack)
			require.Empty(t, stub.steps, "an unrecognised profile must not reach a constructor")
			require.Empty(t, stub.closed)
		})
	}
}

// The startup log is the one place a DSN could reach a file, so what describe
// renders is part of the contract.
func TestStorageConfigDescribeNeverRendersTheDSN(t *testing.T) {
	const dsn = "postgres://user:" + testPassword + "@127.0.0.1:55432/db"

	described := storageConfig{
		profile: profilePostgres,
		dsn:     dsn,
		schema:  "trpc_service",
	}.describe()
	require.NotContains(t, described, testPassword)
	require.NotContains(t, described, dsn)
	require.Contains(t, described, "profile=postgres")
	require.Contains(t, described, "dsn=set")
	require.Contains(t, described, "trpc_service")

	require.Contains(t, storageConfig{profile: profilePostgres}.describe(), "dsn=absent")
	require.Equal(t, "profile=inmemory", storageConfig{profile: profileInMemory}.describe())
}

// The default stack is the three concrete in-memory implementations, built
// without a pool, a dial or a name lookup.
func TestOpenStorageInMemoryBuildsTheConcreteImplementations(t *testing.T) {
	stack, err := openStorage(
		context.Background(), storageConfig{profile: profileInMemory}, defaultStorageDeps())
	require.NoError(t, err)
	require.NotNil(t, stack)
	t.Cleanup(func() { require.NoError(t, stack.close()) })

	require.IsType(t, (*tenant.MemoryRepository)(nil), stack.repository)
	require.IsType(t, (*sessiondir.MemoryDirectory)(nil), stack.directory)
	require.IsType(t, (*sessioninmemory.SessionService)(nil), stack.sessions)
	require.Empty(t, stack.connString, "there is no connection string to scrub")
}

// openStorage validates before it constructs. This is checked on openStorage
// itself and not only on loadStorageConfig, because openStorage is the function
// that opens things: a caller that built a config by hand must not be able to
// reach a constructor with one that was never checked.
func TestOpenStorageValidatesBeforeConstructingAnything(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  storageConfig
	}{
		{
			name: "postgres without a dsn",
			cfg:  storageConfig{profile: profilePostgres},
		},
		{
			name: "postgres with a blank dsn",
			cfg:  storageConfig{profile: profilePostgres, dsn: "   "},
		},
		{
			name: "postgres with a bad schema",
			cfg: storageConfig{
				profile: profilePostgres,
				dsn:     "postgres://user:" + testPassword + "@127.0.0.1:55432/db",
				schema:  "bad-schema",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubStorage{}

			stack, err := openStorage(context.Background(), tc.cfg, stub.deps())
			require.ErrorIs(t, err, errStorageConfig)
			require.Nil(t, stack)
			require.Empty(t, stub.steps, "a rejected config must not reach a constructor")
			require.Empty(t, stub.closed)
		})
	}
}

// The order of the postgres startup is the substance of it, so it is asserted
// directly. Ping in particular has to come before the upstream session
// constructor, which creates its tables on a context this process cannot
// cancel; and both migrations have to be done before anything reads.
func TestOpenPostgresStorageRunsTheStepsInOrder(t *testing.T) {
	stub := &stubStorage{}

	stack, err := openStorage(context.Background(), postgresTestConfig(), stub.deps())
	require.NoError(t, err)
	require.NotNil(t, stack)

	require.Equal(t, []string{
		"open pool",
		"ping",
		"migrate",
		"new control plane",
		"new sessions",
	}, stub.steps)
	require.NotNil(t, stack.repository)
	require.NotNil(t, stack.directory)
	require.NotNil(t, stack.sessions)

	// Shutdown releases what startup acquired, newest first: the session
	// service holds a pool of its own, and the shared pool has to outlive
	// everything reading through it.
	require.NoError(t, stack.close())
	require.Equal(t, []string{"session service", "postgres pool"}, stub.closed)
}

// A failure part way through must leave nothing open. Each case fails one step
// and states exactly what should have been released — no more, so a resource is
// not closed twice or before its user, and no less, so a failed boot does not
// leak connections for the life of the process.
func TestOpenPostgresStorageClosesWhatItBuiltOnFailure(t *testing.T) {
	for _, tc := range []struct {
		failAt string
		closed []string
	}{
		{failAt: "open pool", closed: nil},
		{failAt: "ping", closed: []string{"postgres pool"}},
		{failAt: "migrate", closed: []string{"postgres pool"}},
		{failAt: "new control plane", closed: []string{"postgres pool"}},
		{failAt: "new sessions", closed: []string{"postgres pool"}},
	} {
		t.Run("fails at "+tc.failAt, func(t *testing.T) {
			stepErr := errors.New("step failure")
			stub := &stubStorage{failAt: tc.failAt, failErr: stepErr}

			stack, err := openStorage(context.Background(), postgresTestConfig(), stub.deps())
			require.ErrorIs(t, err, stepErr)
			require.Nil(t, stack, "a failed open returns nothing for the caller to close")
			require.Equal(t, tc.closed, nilIfEmpty(stub.closed))
			// The failing step is the last one that ran: nothing after a
			// failure is attempted.
			require.Equal(t, tc.failAt, stub.steps[len(stub.steps)-1])
		})
	}
}

// A close that fails during a failed startup must not disappear behind the
// failure that caused it. Both are things an operator has to see: one says why
// the process did not start, the other says the machine may still be holding
// connections.
func TestOpenPostgresStorageReportsCloseFailuresAlongsideTheCause(t *testing.T) {
	stepErr := errors.New("migration failure")
	closeErr := errors.New("pool close failure")
	stub := &stubStorage{
		failAt:    "migrate",
		failErr:   stepErr,
		closeErrs: map[string]error{"postgres pool": closeErr},
	}

	_, err := openStorage(context.Background(), postgresTestConfig(), stub.deps())
	require.ErrorIs(t, err, stepErr)
	require.ErrorIs(t, err, closeErr)
	require.ErrorContains(t, err, "close postgres pool")
}

func TestStorageStackCloseJoinsEveryFailureAndIsIdempotent(t *testing.T) {
	sessionsErr := errors.New("session flush failure")
	poolErr := errors.New("pool close failure")
	stub := &stubStorage{closeErrs: map[string]error{
		"session service": sessionsErr,
		"postgres pool":   poolErr,
	}}

	stack, err := openStorage(context.Background(), postgresTestConfig(), stub.deps())
	require.NoError(t, err)

	closeErr := stack.close()
	require.ErrorIs(t, closeErr, sessionsErr)
	require.ErrorIs(t, closeErr, poolErr, "a failure closing one resource must not stop the next")
	require.Equal(t, []string{"session service", "postgres pool"}, stub.closed)

	// Closing again is a no-op, which is what lets the startup path close a
	// partial stack and the shutdown path close on the way out without either
	// having to know what the other did.
	require.NoError(t, stack.close())
	require.Equal(t, []string{"session service", "postgres pool"}, stub.closed)
}

// A pool reports a failure by echoing the string it was built from, and a close
// failure ends up in the process's error like any other, so the shutdown path
// needs the same redaction as the startup path.
func TestStorageStackCloseScrubsTheConnectionString(t *testing.T) {
	cfg := postgresTestConfig()
	stub := &stubStorage{closeErrs: map[string]error{
		"postgres pool": errors.New("cannot close " + cfg.dsn),
	}}

	stack, err := openStorage(context.Background(), cfg, stub.deps())
	require.NoError(t, err)

	closeErr := stack.close()
	require.Error(t, closeErr)
	require.NotContains(t, closeErr.Error(), testPassword)
	require.Contains(t, closeErr.Error(), "127.0.0.1", "the host is what makes it debuggable")
}

// The parse failure is the one that leaks: pgx redacts the copy of the
// connection string it keeps on its own error, but the failure it wraps is
// reported against the original. No network is involved — these DSNs never get
// as far as a connection.
func TestOpenControlPlanePoolDoesNotLeakThePassword(t *testing.T) {
	for _, tc := range []struct {
		name string
		dsn  string
	}{
		{
			// An invalid port makes the URL unparseable without putting the
			// password anywhere the parser will quote, so pgx's own redaction
			// covers this one. It is kept as the control.
			name: "unparseable url",
			dsn:  "postgres://user:" + testPassword + "@127.0.0.1:notaport/db",
		},
		{
			name: "unparseable url with an encoded password",
			dsn:  "postgres://user:p%40ss%20w0rd@127.0.0.1:notaport/db",
		},
		{
			// An unencoded "/" in the password is itself what makes the URL
			// unparseable, and the host and port here are perfectly valid: pgx
			// ends the authority at that "/" and reports everything before it
			// as the port, which is this whole password in clear text. Its own
			// redaction rewrites its copy of the userinfo and misses it.
			name: "unencoded slash in the password",
			dsn:  "postgres://user:" + testPassword + "/x@127.0.0.1:5432/db",
		},
		{
			name: "unsupported sslmode",
			dsn:  "postgres://user:" + testPassword + "@127.0.0.1:55432/db?sslmode=nonsense",
		},
		{
			name: "keyword form",
			dsn:  "host=127.0.0.1 port=notaport user=u password=" + testPassword,
		},
		{
			name: "quoted keyword form",
			dsn:  "host=127.0.0.1 port=notaport user=u password='p@ss w0rd'",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, closePool, err := openControlPlanePool(
				context.Background(),
				storageConfig{profile: profilePostgres, dsn: tc.dsn},
			)
			require.Error(t, err)
			require.Nil(t, pool)
			require.Nil(t, closePool)

			require.NotContains(t, err.Error(), testPassword)
			require.NotContains(t, err.Error(), "p%40ss%20w0rd")
			require.NotContains(t, err.Error(), "p@ss w0rd")
			// It still has to say which setting was wrong.
			require.ErrorContains(t, err, postgresDSNEnvVar)
		})
	}
}

// TestOpenControlPlanePoolRedactsAQuotedPasswordFragment pins the end of the
// leak the table above cannot express. pgx ends the authority at the unencoded
// "/" and quotes what precedes it as the offending port — here a single
// character of the password, which no global substring rule can remove without
// also replacing the "p" in "parse" and "port". The assertion is therefore on
// the structure: the quoted position holds the marker, not the fragment.
func TestOpenControlPlanePoolRedactsAQuotedPasswordFragment(t *testing.T) {
	pool, closePool, err := openControlPlanePool(
		context.Background(),
		storageConfig{
			profile: profilePostgres,
			dsn:     "postgres://user:p/" + testPassword + "@127.0.0.1:notaport/db",
		},
	)
	require.Error(t, err)
	require.Nil(t, pool)
	require.Nil(t, closePool)

	require.NotContains(t, err.Error(), `invalid port ":p"`)
	require.Contains(t, err.Error(), `invalid port ":[REDACTED]" after host`)
	require.NotContains(t, err.Error(), testPassword)
	// The message still says what failed and which setting was wrong.
	require.Contains(t, err.Error(), "failed to parse as URL")
	require.ErrorContains(t, err, postgresDSNEnvVar)
}

// pingControlPlanePool exists because pgxpool.NewWithConfig does not dial:
// without it an unreachable database would first be reported from inside a
// migration, or from the upstream constructor, which ignores this context. If
// that ever changes, this test fails on a refused connection.
//
// The address is loopback with nothing listening, so no packet leaves the
// machine either way.
func TestOpenControlPlanePoolDoesNotConnect(t *testing.T) {
	pool, closePool, err := openControlPlanePool(context.Background(), storageConfig{
		profile: profilePostgres,
		dsn:     "postgres://user:" + testPassword + "@127.0.0.1:1/db?sslmode=disable",
		schema:  "trpc_service",
	})
	require.NoError(t, err, "creating a pool must not depend on a reachable server")
	require.NotNil(t, pool)
	require.NotNil(t, closePool)
	require.NoError(t, closePool())
}

// The listen-address guard is the only thing keeping the unauthenticated Admin
// API off the network, so it has to stay first. Here the storage configuration
// is broken too: if the order were ever reversed, this would fail with the
// storage error instead.
func TestRunRefusesANonLoopbackAddrBeforeItTouchesStorage(t *testing.T) {
	setStorageEnv(t, "nonsense-profile", "", "")

	err := run("192.0.2.1:8080")
	require.ErrorContains(t, err, "refusing to listen")
	require.NotErrorIs(t, err, errStorageConfig)
}

// A loopback address with a broken storage configuration fails on the storage,
// still without binding anything: openStorage is reached and returns before the
// HTTP server exists.
func TestRunRefusesABrokenStorageConfiguration(t *testing.T) {
	setStorageEnv(t, string(profilePostgres), "", "")

	err := run("127.0.0.1:0")
	require.ErrorIs(t, err, errStorageConfig)
	require.ErrorContains(t, err, postgresDSNEnvVar)
}

// postgresTestConfig is a syntactically valid postgres configuration. Nothing
// in the unit tests connects with it; the stub stands in for every step that
// would.
func postgresTestConfig() storageConfig {
	return storageConfig{
		profile: profilePostgres,
		dsn:     "postgres://user:" + testPassword + "@127.0.0.1:55432/db",
		schema:  "trpc_service",
	}
}

// stubStorage replaces the five steps that touch a database, records what ran
// and what was released, and can fail any one step.
//
// It hands out a nil *pgxpool.Pool on purpose: every step that would use one is
// replaced alongside openPool, so a nil pool reaching a real call is a bug this
// makes obvious rather than one it hides behind a mock.
type stubStorage struct {
	steps  []string
	closed []string

	// failAt names the step that returns failErr, if any.
	failAt  string
	failErr error

	// closeErrs makes a named resource fail to close.
	closeErrs map[string]error
}

func (s *stubStorage) deps() storageDeps {
	return storageDeps{
		openPool: func(context.Context, storageConfig) (*pgxpool.Pool, func() error, error) {
			if err := s.step("open pool"); err != nil {
				return nil, nil, err
			}
			return nil, s.closer("postgres pool"), nil
		},
		ping: func(context.Context, *pgxpool.Pool) error {
			return s.step("ping")
		},
		migrate: func(context.Context, *pgxpool.Pool) error {
			return s.step("migrate")
		},
		newControlPlane: func(*pgxpool.Pool) (tenant.Repository, sessiondir.Directory, error) {
			if err := s.step("new control plane"); err != nil {
				return nil, nil, err
			}
			return tenant.NewMemoryRepository(), sessiondir.NewMemoryDirectory(), nil
		},
		newSessions: func(sessionbackend.Config) (session.Service, error) {
			if err := s.step("new sessions"); err != nil {
				return nil, err
			}
			return &stubSessionService{closeFn: s.closer("session service")}, nil
		},
	}
}

func (s *stubStorage) step(name string) error {
	s.steps = append(s.steps, name)
	if name == s.failAt {
		return s.failErr
	}
	return nil
}

func (s *stubStorage) closer(name string) func() error {
	return func() error {
		s.closed = append(s.closed, name)
		return s.closeErrs[name]
	}
}

// stubSessionService is a session.Service that only implements Close. The
// interface is embedded rather than implemented: nothing here calls a session
// method, and a nil embedded interface panics loudly if that ever stops being
// true.
type stubSessionService struct {
	session.Service
	closeFn func() error
}

func (s *stubSessionService) Close() error { return s.closeFn() }

// nilIfEmpty lets a table say "nothing was closed" as nil.
func nilIfEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return values
}
