package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessionbackend"
	"github.com/liuzengh/trpc-agent-service/trpcservice/sessiondir"
	sessiondirpostgres "github.com/liuzengh/trpc-agent-service/trpcservice/sessiondir/postgres"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	tenantpostgres "github.com/liuzengh/trpc-agent-service/trpcservice/tenant/postgres"
	"trpc.group/trpc-go/trpc-agent-go/session"
)

// This file decides where one process keeps its state.
//
// # One profile, not three switches
//
// The three things a worker stores — the control plane, the session revision
// pin, and the conversation history — are a set, not three independent
// choices. A pin that survives a restart is worthless if the revision it names
// was lost with the in-memory control plane, and a persistent session whose pin
// is forgotten resumes on whatever revision is published now, which is the
// exact failure the pin exists to prevent. So there is one process-level
// profile and it moves all three together. Per-component switches would let an
// operator configure combinations that are known to be broken.
//
// # What it is not
//
// This is not a backend registry and it does not generalise. Two profiles are
// spelled out; a third means editing this file, which is the point. Redis is
// not a profile here: sessionbackend can build a Redis session service, but
// there is no Redis control-plane repository or session directory to pair it
// with, so offering it would offer exactly the broken combination above.
const (
	// storageProfileEnvVar selects the profile. Unset or empty means inmemory,
	// so the demo still runs against an empty machine.
	storageProfileEnvVar = "TRPC_SERVICE_STORAGE_PROFILE"

	// postgresDSNEnvVar carries the connection string every component of the
	// postgres profile shares. It holds a password: it is never logged, and
	// every error built from it goes through sessionbackend.Scrub.
	postgresDSNEnvVar = "TRPC_SERVICE_POSTGRES_DSN"

	// postgresSchemaEnvVar places the tables. The schema must already exist —
	// nothing here creates one — and both this process's migrations and the
	// upstream session tables land in it. Optional; empty means the server's
	// default search_path, normally "public".
	postgresSchemaEnvVar = "TRPC_SERVICE_POSTGRES_SCHEMA"
)

// storageProfile names one whole storage arrangement.
type storageProfile string

const (
	// profileInMemory keeps everything in process memory. It needs no external
	// service and makes no network call, and every tenant, pin and conversation
	// is lost when the process exits.
	profileInMemory storageProfile = "inmemory"

	// profilePostgres puts the control plane, the session directory and the
	// upstream session store in one PostgreSQL database, on one DSN and one
	// schema.
	profilePostgres storageProfile = "postgres"
)

// errStorageConfig is the sentinel behind every startup refusal this file
// reports, so a caller can tell "you configured this wrong" from "the database
// was unreachable".
var errStorageConfig = errors.New("storage: invalid configuration")

// storageConfig is the whole storage configuration of one process.
type storageConfig struct {
	// dsn and schema are populated only under profilePostgres; see
	// loadStorageConfig.
	profile storageProfile
	dsn     string
	schema  string
}

// loadStorageConfig reads the environment and refuses anything it cannot serve,
// before the caller owns a single resource.
//
// getenv is a parameter rather than os.Getenv so a test can hand over an
// environment without touching the process's own.
//
// The PostgreSQL variables are read only under the profile that uses them. That
// is what "inmemory ignores stray PostgreSQL settings" means here: a DSN left
// in the environment from an integration run cannot alter what an inmemory
// process does, and — just as important — presence of a DSN never selects a
// profile. Storage that reaches the network is something an operator asks for
// explicitly.
func loadStorageConfig(getenv func(string) string) (storageConfig, error) {
	profile, err := parseStorageProfile(getenv(storageProfileEnvVar))
	if err != nil {
		return storageConfig{}, err
	}
	cfg := storageConfig{profile: profile}
	if profile == profilePostgres {
		cfg.dsn = getenv(postgresDSNEnvVar)
		cfg.schema = getenv(postgresSchemaEnvVar)
	}
	if err := cfg.validate(); err != nil {
		return storageConfig{}, err
	}
	return cfg, nil
}

// parseStorageProfile maps the environment value to a profile.
//
// The match is exact: no trimming and no case folding. A profile decides
// whether this process writes to a shared database, so "Postgres", "pg" and a
// value with a stray trailing space are refused rather than guessed at. Being
// generous here means an operator who mistyped the profile silently gets the
// other one.
func parseStorageProfile(value string) (storageProfile, error) {
	switch storageProfile(value) {
	case "":
		return profileInMemory, nil
	case profileInMemory, profilePostgres:
		return storageProfile(value), nil
	default:
		return "", unknownProfileError(value)
	}
}

// unknownProfileError is shared so an operator sees the same refusal whether
// the profile came from the environment or from a config built in code.
func unknownProfileError(value string) error {
	return fmt.Errorf(
		"%w: unknown %s %q (want %q or %q; leave it unset for %q)",
		errStorageConfig, storageProfileEnvVar, value,
		profileInMemory, profilePostgres, profileInMemory,
	)
}

// validate reports whether this configuration can be opened. It contacts
// nothing: a configuration that validates can still fail to connect.
func (c storageConfig) validate() error {
	switch c.profile {
	case profileInMemory:
		return nil
	case profilePostgres:
		// Checked below.
	default:
		// Only a config built in code reaches this: loadStorageConfig cannot
		// produce another value, and an unset variable is normalised to
		// profileInMemory there rather than here. It is still a refusal and not
		// a fallback, because openStorage validates before it constructs and an
		// unrecognised profile has to fail there too. Treating it as "not
		// postgres, so in-memory" would be the fail-open this whole file exists
		// to prevent: a process that looks healthy and loses every conversation
		// on restart.
		return unknownProfileError(string(c.profile))
	}
	// Named explicitly, because "postgres backend requires a DSN" from the
	// layer below does not tell an operator which variable to set.
	if strings.TrimSpace(c.dsn) == "" {
		return fmt.Errorf(
			"%w: profile %q requires %s to be set",
			errStorageConfig, profilePostgres, postgresDSNEnvVar,
		)
	}
	// The schema rules belong to sessionbackend, which has to enforce them
	// anyway: the upstream schema option panics on input it dislikes instead of
	// returning an error. Restating them here would leave two copies to drift.
	// These messages quote the schema and never the DSN.
	if err := c.sessionConfig().Validate(); err != nil {
		return fmt.Errorf("%w: %s: %w", errStorageConfig, postgresSchemaEnvVar, err)
	}
	return nil
}

// sessionConfig renders the upstream session backend this profile implies. The
// session store shares the profile's DSN and schema; nothing configures it
// separately.
func (c storageConfig) sessionConfig() sessionbackend.Config {
	if c.profile != profilePostgres {
		return sessionbackend.DefaultConfig()
	}
	return sessionbackend.Config{
		Backend: sessionbackend.BackendPostgres,
		Postgres: sessionbackend.PostgresConfig{
			DSN:    c.dsn,
			Schema: c.schema,
		},
	}
}

// describe renders the configuration for the startup log: the profile, whether
// a connection string is present, and the schema. Never its contents.
func (c storageConfig) describe() string {
	if c.profile != profilePostgres {
		return fmt.Sprintf("profile=%s", c.profile)
	}
	return fmt.Sprintf(
		"profile=%s dsn=%s schema=%q",
		c.profile, presence(c.dsn), c.schema,
	)
}

func presence(value string) string {
	if strings.TrimSpace(value) == "" {
		return "absent"
	}
	return "set"
}

// namedCloser is one resource the stack owns, kept with the name an operator
// needs to make sense of a close failure.
type namedCloser struct {
	name  string
	close func() error
}

// storageStack is what one process runs on: the three components, plus the
// resources behind them.
//
// It exists so ownership has one home. Under the postgres profile the
// repository and the directory share a pool that neither of them owns — both
// packages borrow the pool they are handed and close nothing — while the
// upstream session service owns a pool of its own, internally. Whoever built
// those has to close them, in the right order, on the shutdown path and on
// every partial startup failure alike. That is this type's whole job.
type storageStack struct {
	repository tenant.Repository
	directory  sessiondir.Directory
	sessions   session.Service

	// connString is kept so close errors can be scrubbed too. A pool reports a
	// failure by echoing the string it was built from, and a close on the
	// shutdown path is logged like any other error.
	connString string

	closers []namedCloser
	closed  bool
}

// push records a resource to release. Order is the order of creation: close
// undoes it.
func (s *storageStack) push(name string, closeFn func() error) {
	s.closers = append(s.closers, namedCloser{name: name, close: closeFn})
}

// close releases everything the stack owns, newest first, and reports every
// failure rather than the first one.
//
// Reverse order is not decoration. The session service holds its own pool and
// may still be flushing; the shared pool must outlive the repository and
// directory that read through it. Closing in creation order would close a pool
// out from under a live user of it.
//
// Errors are joined, not logged and dropped: a session store that failed to
// flush on shutdown is exactly the kind of thing that turns into "the last turn
// of every conversation is missing" a week later.
//
// It is safe to call twice, which is what lets the startup path close a partial
// stack and the shutdown path close on the way out without either having to
// know what the other did.
func (s *storageStack) close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	for i := len(s.closers) - 1; i >= 0; i-- {
		closer := s.closers[i]
		if err := closer.close(); err != nil {
			errs = append(errs, sessionbackend.Scrub(
				fmt.Errorf("close %s: %w", closer.name, err), s.connString))
		}
	}
	s.closers = nil
	return errors.Join(errs...)
}

// storageDeps is the seam between the startup sequence and the steps that
// actually touch a database.
//
// It is here for one reason: the ordering rules in openPostgresStorage — ping
// before the upstream constructor, migrate before seeding, close in reverse on
// a partial failure — are the part most likely to break, and they cannot be
// tested against a real server on a machine that has none. With this, a unit
// test fails any single step and watches what gets closed.
//
// It is deliberately five concrete functions and not an abstraction over pgx. A
// fake supplies a nil pool, and every step that would touch one is replaced
// alongside, so nothing dereferences it.
type storageDeps struct {
	openPool        func(ctx context.Context, cfg storageConfig) (*pgxpool.Pool, func() error, error)
	ping            func(ctx context.Context, pool *pgxpool.Pool) error
	migrate         func(ctx context.Context, pool *pgxpool.Pool) error
	newControlPlane func(pool *pgxpool.Pool) (tenant.Repository, sessiondir.Directory, error)
	newSessions     func(cfg sessionbackend.Config) (session.Service, error)
}

// defaultStorageDeps is what the process runs with.
func defaultStorageDeps() storageDeps {
	return storageDeps{
		openPool:        openControlPlanePool,
		ping:            pingControlPlanePool,
		migrate:         migrateControlPlane,
		newControlPlane: newPostgresControlPlane,
		newSessions:     sessionbackend.New,
	}
}

// openStorage builds the stack cfg asks for. The caller owns the result and
// must close it exactly once; on error nothing is returned to close, because
// openStorage has already released whatever it had built.
func openStorage(ctx context.Context, cfg storageConfig, deps storageDeps) (*storageStack, error) {
	// Re-checked rather than assumed: loadStorageConfig validates, but this is
	// the function that opens things, and "validate the whole configuration
	// before constructing anything" is only true if the check lives with the
	// construction.
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	switch cfg.profile {
	case profileInMemory:
		return openInMemoryStorage(cfg, deps)
	case profilePostgres:
		return openPostgresStorage(ctx, cfg, deps)
	default:
		// Unreachable: validate has already refused every other profile. It is
		// spelled out rather than left as a default branch so that adding a
		// profile to the type without adding it here fails loudly instead of
		// quietly building the in-memory stack.
		return nil, unknownProfileError(string(cfg.profile))
	}
}

// openInMemoryStorage builds the zero-dependency stack. Nothing here connects,
// resolves a name or reads a file.
func openInMemoryStorage(cfg storageConfig, deps storageDeps) (*storageStack, error) {
	sessions, err := deps.newSessions(cfg.sessionConfig())
	if err != nil {
		return nil, err
	}
	stack := &storageStack{
		repository: tenant.NewMemoryRepository(),
		directory:  sessiondir.NewMemoryDirectory(),
		sessions:   sessions,
	}
	stack.push("session service", sessions.Close)
	return stack, nil
}

// openPostgresStorage builds the persistent stack.
//
// The order of the steps is the substance of this function:
//
//   - The pool is registered for closing in the same breath as it is created,
//     before anything can fail after it. A pool that is created and then
//     abandoned by an early return leaks connections for the life of the
//     process.
//   - Ping comes before the upstream session constructor. Upstream creates its
//     tables during construction, on a background context of its own that this
//     process cannot cancel, so an unreachable or wrong-schema database has to
//     be caught here — while the caller's deadline still applies — rather than
//     inside a call that ignores it.
//   - Migrations come before anything reads. Both migrations run on the shared
//     pool, so they land in the schema its search_path names.
//   - The session service is created last, so it is closed first.
func openPostgresStorage(ctx context.Context, cfg storageConfig, deps storageDeps) (*storageStack, error) {
	stack := &storageStack{connString: cfg.dsn}
	// fail closes exactly what has been created so far, in reverse order, and
	// reports the failure together with anything that went wrong closing.
	// Scrubbing here as well as at each step is redundant on purpose: Scrub is
	// idempotent, and this is the boundary the DSN must not cross.
	fail := func(err error) (*storageStack, error) {
		return nil, errors.Join(sessionbackend.Scrub(err, cfg.dsn), stack.close())
	}

	pool, closePool, err := deps.openPool(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	stack.push("postgres pool", closePool)

	if err := deps.ping(ctx, pool); err != nil {
		return fail(err)
	}
	if err := deps.migrate(ctx, pool); err != nil {
		return fail(err)
	}

	repository, directory, err := deps.newControlPlane(pool)
	if err != nil {
		return fail(err)
	}
	stack.repository, stack.directory = repository, directory

	sessions, err := deps.newSessions(cfg.sessionConfig())
	if err != nil {
		return fail(err)
	}
	stack.push("session service", sessions.Close)
	stack.sessions = sessions

	return stack, nil
}

// openControlPlanePool opens the pool the repository and the directory share.
//
// One pool for both is the point: they are two views of one control plane, they
// are always configured together, and a single pool means a single search_path
// and a single set of connections to size. It is returned with its closer
// rather than closed here, because the caller owns it from the moment it
// exists.
//
// The upstream session service is not on this pool. It builds and owns its own
// from the same DSN, which is upstream's design, not a choice made here.
func openControlPlanePool(
	ctx context.Context,
	cfg storageConfig,
) (*pgxpool.Pool, func() error, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.dsn)
	if err != nil {
		// This is the error that most needs scrubbing. pgx redacts the copy of
		// the connection string it keeps on its own error, but the parse failure
		// it wraps is reported against the original: a password containing an
		// unencoded "/" is re-read as a port and quoted back in clear text.
		return nil, nil, sessionbackend.Scrub(
			fmt.Errorf("parse %s: %w", postgresDSNEnvVar, err), cfg.dsn)
	}
	if cfg.schema != "" {
		// Set on the pool config rather than with a SET after checkout, so every
		// connection the pool opens — including one it opens later to replace a
		// dropped one — carries the same search_path. Both migrations and every
		// statement of both components name their tables unqualified, so this is
		// the only thing placing them.
		poolConfig.ConnConfig.RuntimeParams["search_path"] = cfg.schema
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, nil, sessionbackend.Scrub(
			fmt.Errorf("open control-plane pool: %w", err), cfg.dsn)
	}
	// pgxpool.Close reports nothing and is safe to call once; the signature is
	// uniform so the stack can hold it next to the closers that do fail.
	return pool, func() error { pool.Close(); return nil }, nil
}

// pingControlPlanePool is the first and only place an unreachable database is
// allowed to surface. NewWithConfig does not dial, so without this the failure
// would appear somewhere further in — from a migration, or from inside the
// upstream constructor, which does not honour this context.
func pingControlPlanePool(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("reach the configured PostgreSQL database: %w", err)
	}
	return nil
}

// migrateControlPlane brings the schema up to date for both components this
// process owns. Both are idempotent, both take an advisory lock, and both are
// safe to run from every worker on every boot.
//
// The upstream session tables are not created here. Upstream creates them
// itself when its service is constructed, in the schema it is pointed at.
func migrateControlPlane(ctx context.Context, pool *pgxpool.Pool) error {
	if err := tenantpostgres.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate control plane: %w", err)
	}
	if err := sessiondirpostgres.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate session directory: %w", err)
	}
	return nil
}

// newPostgresControlPlane wraps the shared pool in the two components. Neither
// constructor connects and neither takes ownership of the pool.
func newPostgresControlPlane(
	pool *pgxpool.Pool,
) (tenant.Repository, sessiondir.Directory, error) {
	repository, err := tenantpostgres.New(pool)
	if err != nil {
		return nil, nil, fmt.Errorf("create control-plane repository: %w", err)
	}
	directory, err := sessiondirpostgres.New(pool)
	if err != nil {
		return nil, nil, fmt.Errorf("create session directory: %w", err)
	}
	return repository, directory, nil
}
