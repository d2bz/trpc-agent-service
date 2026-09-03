package storagebundle

import (
	"context"
	"fmt"
	"sync"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// ProfileSource resolves a profile id within one tenant.
//
// Two rules bind every implementation:
//
//   - A profile that does not exist and a profile that belongs to another
//     tenant are both ErrProfileNotFound, and they are not distinguishable.
//     Telling them apart would answer "does tenant B have a profile called
//     p1", which is not a question tenant A may ask.
//   - The same (tenant, id) returns the same content forever. The id is the
//     version; a source that mutates content under a stable id breaks the
//     contract every Bundle built from it depends on, and Router reports that
//     as ErrProfileChanged rather than following it.
type ProfileSource interface {
	ResolveProfile(
		ctx context.Context, scope tenant.TenantContext, profileID string,
	) (Profile, error)
}

// NoProfiles is the source of a process that has no dynamic profiles: every
// lookup is ErrProfileNotFound.
//
// It is what production runs on in this slice, and it is a real answer rather
// than a placeholder — a process with no profile storage cannot honour a
// profile reference, so a revision that names one is refused instead of being
// served by the default store it did not ask for.
func NoProfiles() ProfileSource {
	return noProfiles{}
}

type noProfiles struct{}

func (noProfiles) ResolveProfile(
	ctx context.Context,
	scope tenant.TenantContext,
	profileID string,
) (Profile, error) {
	if err := scope.Validate(); err != nil {
		return Profile{}, err
	}
	return Profile{}, fmt.Errorf("%w: %q", ErrProfileNotFound, profileID)
}

// MemoryProfileSource holds profiles in memory, keyed by (tenant, id).
//
// It is the source tests run against, and the one a later slice replaces with
// a repository. It exists to make the immutability contract executable rather
// than aspirational: Put refuses to overwrite, so no test can accidentally
// depend on a profile whose content moved.
type MemoryProfileSource struct {
	mu       sync.RWMutex
	profiles map[profileKey]Profile
}

func NewMemoryProfileSource() *MemoryProfileSource {
	return &MemoryProfileSource{profiles: make(map[profileKey]Profile)}
}

// Put stores a valid profile under its own (TenantID, ID).
//
// An existing key is tenant.ErrAlreadyExists, whatever the content — including
// content identical to what is already there. The id is the version, so
// "replace p1" is never a legal operation; publishing different storage means
// publishing a different id.
//
// What is stored is a deep copy. Refusing to overwrite is only half of "the id
// is the version": a caller that kept the Profile it passed in still holds the
// pointers inside its SessionSpec, and writing through one of those would edit
// stored content without going through Put at all.
func (s *MemoryProfileSource) Put(p Profile) error {
	// Copied before it is validated rather than after. Validating the caller's
	// value and copying it later would leave a window between the two, and what
	// ended up stored would be content no Validate had ever seen.
	stored := p.clone()
	if err := stored.Validate(); err != nil {
		return err
	}
	key := profileKey{tenantID: stored.TenantID, profileID: stored.ID}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.profiles[key]; exists {
		return fmt.Errorf(
			"%w: backend profile %q of tenant %q",
			tenant.ErrAlreadyExists, stored.ID, stored.TenantID)
	}
	s.profiles[key] = stored
	return nil
}

// ResolveProfile returns a copy of the profile scope owns under this id.
//
// A copy, because Router calls this on every Resolve and compares the
// fingerprint of what comes back against the one its Bundle was built from.
// Handing out the stored value would hand out the pointers inside it, and a
// caller that wrote through one would make every later resolution of that id
// fail as ErrProfileChanged.
func (s *MemoryProfileSource) ResolveProfile(
	ctx context.Context,
	scope tenant.TenantContext,
	profileID string,
) (Profile, error) {
	if err := scope.Validate(); err != nil {
		return Profile{}, err
	}
	// Keyed by the caller's tenant rather than filtered after the lookup:
	// another tenant's profile is not found here, it is not reachable.
	s.mu.RLock()
	defer s.mu.RUnlock()
	profile, found := s.profiles[profileKey{tenantID: scope.TenantID, profileID: profileID}]
	if !found {
		return Profile{}, fmt.Errorf("%w: %q", ErrProfileNotFound, profileID)
	}
	return profile.clone(), nil
}

// profileKey is the cache and storage key of this package: a profile id means
// nothing without the tenant that owns it.
type profileKey struct {
	tenantID  string
	profileID string
}

// String renders the key for a singleflight group. The separator is a NUL so
// no pair of ids can collide by concatenation; both halves are already
// constrained by tenant.ValidateResourceID, which excludes it.
func (k profileKey) String() string {
	return k.tenantID + "\x00" + k.profileID
}
