// Package sessionbackend builds the upstream session.Service that one process
// stores its conversation history in.
//
// The surface is deliberately small: a backend name, the single connection
// string that backend needs, and the namespacing knobs the integration tests
// need to run against a shared server. Every other upstream option keeps its
// default, so this package never turns into a second, drifting copy of the
// upstream option set. There is no backend registry, no capability table and
// no control-plane storage here; those belong to a later slice.
//
// Ownership: New returns a service the caller owns and must Close exactly
// once, after every Runner sharing it has stopped. This package never closes
// a service it has returned and keeps no reference to one.
package sessionbackend

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/session"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	sessionpostgres "trpc.group/trpc-go/trpc-agent-go/session/postgres"
	sessionredis "trpc.group/trpc-go/trpc-agent-go/session/redis"
)

// Backend names a session storage implementation.
type Backend string

const (
	// BackendInMemory keeps sessions in process memory. It needs no external
	// service, and every session is lost when the process exits.
	BackendInMemory Backend = "inmemory"

	// BackendPostgres stores sessions in PostgreSQL. Upstream creates its own
	// tables on construction and deletes sessions softly; see
	// docs/session-backend.md.
	BackendPostgres Backend = "postgres"

	// BackendRedis stores sessions in Redis under the upstream key layout.
	BackendRedis Backend = "redis"
)

// DefaultBackend is the backend a process should pick when nothing else is
// configured. It is the only one that needs no external service, so the demo
// entrypoint keeps running against an empty machine.
const DefaultBackend = BackendInMemory

// ErrInvalidConfig is the sentinel behind every configuration error this
// package reports, so a caller can separate "you configured this wrong" from
// "the backend was unreachable" with errors.Is.
var ErrInvalidConfig = errors.New("sessionbackend: invalid configuration")

// redacted replaces a secret that would otherwise reach a caller's log.
const redacted = "[REDACTED]"

// maxNamespaceLen bounds a table prefix and a schema name. Upstream allows 64
// characters, but PostgreSQL truncates identifiers at 63 bytes and the longest
// upstream table name ("session_track_events") already spends 20 of them, so a
// prefix upstream accepts can still collide after truncation. 32 leaves room
// for the suffix and is far above any real prefix.
const maxNamespaceLen = 32

// identifierPattern mirrors the upstream SQL identifier rule. Upstream applies
// it through a Must-style helper that panics, so Config.Validate has to reject
// a bad prefix or schema before New reaches that helper.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// keyPrefixPattern bounds a Redis key prefix. Upstream does not validate it,
// but the prefix is concatenated into every key, so whitespace or a Redis
// cluster hash tag would silently reshape the key space.
var keyPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

// Config selects one session backend and carries only that backend's
// settings. The struct for an unselected backend is ignored, not validated.
type Config struct {
	// Backend names the implementation to build. It is required: an empty
	// backend is a configuration typo, not a request for the default. Use
	// DefaultConfig when you want the default.
	Backend Backend

	// Postgres applies when Backend is BackendPostgres.
	Postgres PostgresConfig

	// Redis applies when Backend is BackendRedis.
	Redis RedisConfig
}

// PostgresConfig configures BackendPostgres.
type PostgresConfig struct {
	// DSN is the connection string, in either URL form
	// ("postgres://user:password@host:5432/db?sslmode=disable") or libpq
	// keyword form ("host=... user=... password=..."). Required.
	//
	// It carries a password. Never log this field; log Describe instead.
	DSN string

	// TablePrefix namespaces every upstream table, which is what lets two
	// runs share one database without colliding. Upstream appends an
	// underscore when the prefix does not end in one. Optional.
	TablePrefix string

	// Schema is the PostgreSQL schema the tables live in. It must already
	// exist: upstream creates tables, not schemas. Optional; empty means the
	// server default, normally "public".
	Schema string
}

// RedisConfig configures BackendRedis.
type RedisConfig struct {
	// URL is the connection URL ("redis://user:password@host:6379/0").
	// Required.
	//
	// It carries a password. Never log this field; log Describe instead.
	URL string

	// KeyPrefix namespaces every upstream key, which is what lets two runs
	// share one Redis instance without colliding. Optional.
	KeyPrefix string
}

// DefaultConfig returns the configuration a process gets when it asks for no
// backend in particular: in-memory, no external dependency.
func DefaultConfig() Config {
	return Config{Backend: DefaultBackend}
}

// Describe renders the config for a log line. It names the backend and the
// namespacing knobs, and reports only whether a connection string is present,
// never its contents.
func (c Config) Describe() string {
	switch c.Backend {
	case BackendPostgres:
		return fmt.Sprintf(
			"backend=postgres dsn=%s table_prefix=%q schema=%q",
			presence(c.Postgres.DSN), c.Postgres.TablePrefix, c.Postgres.Schema,
		)
	case BackendRedis:
		return fmt.Sprintf(
			"backend=redis url=%s key_prefix=%q",
			presence(c.Redis.URL), c.Redis.KeyPrefix,
		)
	default:
		return fmt.Sprintf("backend=%q", string(c.Backend))
	}
}

func presence(value string) string {
	if strings.TrimSpace(value) == "" {
		return "absent"
	}
	return "set"
}

// Validate reports whether New can build this config. It is the whole of the
// input checking New performs, so a caller can reject a bad config at startup
// before it owns anything that needs closing.
//
// Validate contacts no external service: a config that validates can still
// fail to connect.
func (c Config) Validate() error {
	switch c.Backend {
	case BackendInMemory:
		return nil
	case BackendPostgres:
		return c.Postgres.validate()
	case BackendRedis:
		return c.Redis.validate()
	case "":
		return fmt.Errorf("%w: backend is required (one of %s)", ErrInvalidConfig, backendList())
	default:
		return fmt.Errorf(
			"%w: unknown backend %q (want one of %s)",
			ErrInvalidConfig, string(c.Backend), backendList(),
		)
	}
}

func (c PostgresConfig) validate() error {
	if strings.TrimSpace(c.DSN) == "" {
		return fmt.Errorf("%w: postgres backend requires a DSN", ErrInvalidConfig)
	}
	if err := validateIdentifier("postgres table prefix", strings.TrimSuffix(c.TablePrefix, "_")); err != nil {
		return err
	}
	return validateIdentifier("postgres schema", c.Schema)
}

func (c RedisConfig) validate() error {
	if strings.TrimSpace(c.URL) == "" {
		return fmt.Errorf("%w: redis backend requires a URL", ErrInvalidConfig)
	}
	if c.KeyPrefix == "" {
		return nil
	}
	if len(c.KeyPrefix) > maxNamespaceLen {
		return fmt.Errorf(
			"%w: redis key prefix is %d characters (max %d)",
			ErrInvalidConfig, len(c.KeyPrefix), maxNamespaceLen,
		)
	}
	if !keyPrefixPattern.MatchString(c.KeyPrefix) {
		return fmt.Errorf(
			"%w: invalid redis key prefix %q (letters, digits, '_', '.', ':' and '-' only)",
			ErrInvalidConfig, c.KeyPrefix,
		)
	}
	return nil
}

// validateIdentifier rejects a table prefix or schema upstream would either
// refuse or, worse, accept into a generated SQL identifier. Empty is allowed:
// both knobs are optional.
func validateIdentifier(what, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > maxNamespaceLen {
		return fmt.Errorf(
			"%w: %s is %d characters (max %d)",
			ErrInvalidConfig, what, len(value), maxNamespaceLen,
		)
	}
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf(
			"%w: invalid %s %q (must start with a letter or '_' and contain only letters, digits and '_')",
			ErrInvalidConfig, what, value,
		)
	}
	return nil
}

func backendList() string {
	return fmt.Sprintf("%q, %q, %q", BackendInMemory, BackendPostgres, BackendRedis)
}

// New builds the session service named by cfg.
//
// The caller owns the returned service and must Close it exactly once. New
// closes nothing it returns, and on error returns no service to close.
//
// BackendInMemory and BackendRedis do not reach the network here: the redis
// client is created lazily, so a URL that points nowhere usually surfaces on
// the first session call rather than from New. BackendPostgres does connect,
// because upstream creates its tables during construction; that work runs on
// an upstream-owned background context and cannot be cancelled by the caller,
// which is why New takes no context.
func New(cfg Config) (session.Service, error) {
	// Validate first: the upstream prefix and schema options panic on input
	// they dislike instead of returning an error, so nothing may reach them
	// unchecked.
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch cfg.Backend {
	case BackendInMemory:
		return sessioninmemory.NewSessionService(), nil
	case BackendPostgres:
		return newPostgres(cfg.Postgres)
	case BackendRedis:
		return newRedis(cfg.Redis)
	default:
		// Unreachable: Validate has already rejected every other backend.
		return nil, fmt.Errorf("%w: unknown backend %q", ErrInvalidConfig, string(cfg.Backend))
	}
}

func newPostgres(cfg PostgresConfig) (session.Service, error) {
	opts := []sessionpostgres.ServiceOpt{sessionpostgres.WithPostgresClientDSN(cfg.DSN)}
	if cfg.TablePrefix != "" {
		opts = append(opts, sessionpostgres.WithTablePrefix(cfg.TablePrefix))
	}
	if cfg.Schema != "" {
		opts = append(opts, sessionpostgres.WithSchema(cfg.Schema))
	}
	service, err := sessionpostgres.NewService(opts...)
	if err != nil {
		return nil, scrub(
			fmt.Errorf("sessionbackend: create postgres session service: %w", err),
			cfg.DSN,
		)
	}
	return service, nil
}

func newRedis(cfg RedisConfig) (session.Service, error) {
	opts := []sessionredis.ServiceOpt{sessionredis.WithRedisClientURL(cfg.URL)}
	if cfg.KeyPrefix != "" {
		opts = append(opts, sessionredis.WithKeyPrefix(cfg.KeyPrefix))
	}
	service, err := sessionredis.NewService(opts...)
	if err != nil {
		return nil, scrub(
			fmt.Errorf("sessionbackend: create redis session service: %w", err),
			cfg.URL,
		)
	}
	return service, nil
}

// scrub replaces every secret of connString wherever it appears in err.
// Drivers echo the string they failed to parse or dial, so an unscrubbed error
// puts the password into whatever log the caller writes it to.
//
// A scrubbed error is a fresh error: it deliberately does not wrap the
// original, because an Unwrap chain would hand the secret straight back.
func scrub(err error, connString string) error {
	if err == nil {
		return nil
	}
	original := err.Error()
	scrubbed := original
	secrets := secretsOf(connString)
	// Longest first, so redacting a short secret cannot leave a fragment of a
	// longer one that happens to contain it.
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		scrubbed = strings.ReplaceAll(scrubbed, secret, redacted)
	}
	if scrubbed == original {
		return err
	}
	return errors.New(scrubbed)
}

// secretsOf returns the substrings of a connection string that must never be
// logged. It understands both shapes the upstream backends accept: a URL with
// userinfo ("postgres://user:password@host/db") and libpq keyword form
// ("host=... password=...").
func secretsOf(connString string) []string {
	var secrets []string
	if parsed, err := url.Parse(connString); err == nil && parsed.User != nil {
		if password, ok := parsed.User.Password(); ok && password != "" {
			secrets = append(secrets, password)
			// Password decodes percent escapes, but a driver echoes the DSN
			// as it was written, so the encoded spelling has to go too.
			if _, encoded, found := strings.Cut(parsed.User.String(), ":"); found && encoded != password {
				secrets = append(secrets, encoded)
			}
		}
	}
	for _, field := range keywordFields(connString) {
		if value, ok := strings.CutPrefix(field, "password="); ok && value != "" {
			secrets = append(secrets, value)
		}
	}
	return secrets
}

// keywordFields splits a libpq keyword connection string into its key=value
// tokens, honouring the single quotes libpq allows around a value containing
// spaces. Splitting on whitespace alone would cut a quoted password in half
// and redact only the first half.
//
// It does not implement libpq's backslash escaping, so a password containing
// an escaped quote is redacted only up to that quote.
func keywordFields(connString string) []string {
	var (
		fields  []string
		current strings.Builder
		quoted  bool
	)
	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		}
	}
	for i := 0; i < len(connString); i++ {
		switch c := connString[i]; {
		case c == '\'':
			quoted = !quoted
		case !quoted && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			flush()
		default:
			current.WriteByte(c)
		}
	}
	flush()
	return fields
}
