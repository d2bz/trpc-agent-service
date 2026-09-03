package storagebundle

import (
	"context"
	"errors"
	"fmt"

	"github.com/liuzengh/trpc-agent-service/trpcservice/sessionbackend"
)

// Factory builds the Bundle a Profile describes.
//
// Build returns the Bundle together with the close that releases it, and the
// caller — in practice the Router — owns both from the moment they exist. On
// error it returns nothing to close: a Factory that failed halfway releases
// what it had built before it returns, because there is no second owner who
// could.
//
// ctx bounds the construction. It is the Router's own lifecycle context, not a
// request's: one caller going away must not cancel a build every other caller
// is waiting on.
type Factory interface {
	Build(ctx context.Context, profile Profile) (Bundle, func() error, error)
}

// ProcessConstraints is what the process's own storage arrangement forbids a
// tenant profile from doing.
//
// The two axes are the two invariants cmd/trpc-service/storage.go establishes
// at startup, carried to the one place that can otherwise reopen them. A
// per-tenant profile is a second storage decision made after the process-level
// one, and without these it could reintroduce exactly the combinations that
// file refuses to boot with.
type ProcessConstraints struct {
	// DurablePins reports whether this process's session directory survives a
	// restart. A durable session store under a non-durable directory would
	// keep the conversation and lose the revision it was pinned to.
	DurablePins bool

	// MultiWorker reports whether run leases are coordinated with other
	// Workers. An in-process session store under a shared lock is unshared
	// state behind a lock its peers cannot see anything through.
	MultiWorker bool
}

// NewSessionFactory returns the Factory this slice runs on: it builds the
// in-memory session backend and nothing else.
//
// The refusals are ordered so the more fundamental one is reported first. A
// durable backend in a process without durable pins is refused before it is
// reported as unimplemented, because implementing it would not make that
// combination safe — it is the arrangement that is wrong, not the missing code.
func NewSessionFactory(constraints ProcessConstraints) Factory {
	return sessionFactory{constraints: constraints}
}

type sessionFactory struct {
	constraints ProcessConstraints
}

func (f sessionFactory) Build(
	ctx context.Context,
	profile Profile,
) (Bundle, func() error, error) {
	if ctx == nil {
		return Bundle{}, nil, errors.New("storagebundle: context is required")
	}
	// Checked before anything is constructed rather than after: a Router that
	// is already shutting down should not open a store nobody will ever reach.
	if err := ctx.Err(); err != nil {
		return Bundle{}, nil, err
	}
	// Re-validated rather than assumed. Router validates on the way in, but
	// this is the function that builds, and a Factory reached from anywhere
	// else has to refuse the same input — the upstream namespacing options
	// panic rather than return on input they dislike.
	if err := profile.Validate(); err != nil {
		return Bundle{}, nil, err
	}
	if err := f.allows(profile.Session.Backend); err != nil {
		return Bundle{}, nil, fmt.Errorf(
			"storagebundle: backend profile %q of tenant %q: %w",
			profile.ID, profile.TenantID, err,
		)
	}
	service, err := sessionbackend.New(sessionbackend.Config{
		Backend: sessionbackend.BackendInMemory,
	})
	if err != nil {
		return Bundle{}, nil, fmt.Errorf(
			"storagebundle: build session store for profile %q: %w", profile.ID, err)
	}
	return Bundle{Session: service}, service.Close, nil
}

// allows reports whether this process may serve a tenant profile on this
// backend, and whether this Factory can build it at all.
func (f sessionFactory) allows(backend sessionbackend.Backend) error {
	if backend != sessionbackend.BackendInMemory {
		if !f.constraints.DurablePins {
			return ErrPinsNotDurable
		}
		// Reachable only for a backend Profile.Validate accepted, which is
		// postgres or redis. Resolving their references into a connection
		// string belongs to the slice that adds profile storage.
		return fmt.Errorf("%w: %q", ErrUnsupportedBackend, string(backend))
	}
	if f.constraints.MultiWorker {
		return ErrNotSharedAcrossWorkers
	}
	return nil
}
