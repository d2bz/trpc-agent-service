// Package security loads the process security configuration and answers the
// one authorization question the data plane asks of it: may this revision use
// the capabilities it names?
//
// The package is deliberately small and static. It has no RBAC engine, no
// schema language and no hot reload: the manifest is read once at startup,
// validated as a whole, and turned into three values — a chat authenticator, an
// admin authenticator and a RevisionAuthorizer — that never change for the life
// of the process.
package security

import (
	"errors"
	"fmt"
	"strings"

	"github.com/liuzengh/trpc-agent-service/trpcservice/secretref"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// ErrNotEntitled reports a revision naming a capability its tenant is not
// entitled to.
//
// It is a single sentinel with a single message, and the message names neither
// the rejected reference nor anything about it. That is the whole point of the
// check: a caller who could tell "unknown policy" from "known but not yours",
// or "this env var exists" from "it does not", would have a probe for the tool
// registry and the process environment built out of nothing but refusals.
var ErrNotEntitled = errors.New("security: revision is not entitled to a referenced capability")

// RevisionAuthorizer decides whether a revision may use the capabilities its
// config names. It is consulted before the tool registry and before any secret
// is resolved, so a refusal costs nothing and reveals nothing.
//
// It is an interface with one method because every call site — the admin API on
// create and on publish, and the runtime builder — has to ask the same question
// of the same value. A second implementation is a test fixture, not a variant
// policy.
type RevisionAuthorizer interface {
	// AuthorizeRevision reports whether tenantID may run config as written. A
	// config that names no secret and no policy needs no entitlement and is
	// always allowed.
	AuthorizeRevision(tenantID string, config tenant.RevisionConfig) error
}

// Entitlements is the process entitlement table: which secret references and
// which tool policies each tenant may name.
//
// It is a value, not a registry: it is built once from the manifest and read
// concurrently for the rest of the process, so it needs no lock and offers no
// way to add a grant after startup.
type Entitlements struct {
	byTenant map[string]tenantEntitlement
}

type tenantEntitlement struct {
	secretRefs map[string]struct{}
	policyRefs map[string]struct{}
}

// Grant is one tenant's entitlement stated in code rather than in a manifest.
//
// It is the same grant the manifest's tenant_entitlements describes, and it goes
// through the same validation, so a fixture built here cannot express something
// the file format could not.
type Grant struct {
	TenantID   string
	SecretRefs []string
	PolicyRefs []string
}

// NewEntitlements builds an entitlement table from grants.
//
// It exists for callers that hold a capability configuration without a file: the
// gated live test that needs one real model key entitled, and the unit tests
// that exercise a revision naming a secret or a policy. Those cannot use a
// manifest — there is no file, and the key comes from the test environment — but
// they must not therefore run against a different authorization path than the
// process does, which is the whole reason this returns the same *Entitlements
// the loader builds rather than a permissive stand-in.
//
// The rules that do not need manifest context are enforced here, including the
// reserved-namespace rule: no grant, however it was built, may entitle a tenant
// to a TRPC_SERVICE_ variable. The manifest loader adds the one rule that does
// need context — that no grant names a variable holding one of this process's
// own credentials — because only it knows what those are.
func NewEntitlements(grants ...Grant) (*Entitlements, error) {
	table := &Entitlements{byTenant: make(map[string]tenantEntitlement, len(grants))}
	for _, grant := range grants {
		if err := table.add("entitlement for tenant "+quoteID(grant.TenantID), grant); err != nil {
			return nil, err
		}
	}
	return table, nil
}

// add validates one grant and records it.
//
// label names the grant in every error: the manifest passes
// "tenant_entitlements[2]" so an operator can find the line, and NewEntitlements
// passes the tenant id. One implementation of the rules, two ways of pointing at
// the thing that broke them.
func (e *Entitlements) add(label string, grant Grant) error {
	if err := tenant.ValidateResourceID("tenant_id", grant.TenantID); err != nil {
		return fmt.Errorf("security: %s: %w", label, err)
	}
	if _, repeated := e.byTenant[grant.TenantID]; repeated {
		return fmt.Errorf("security: %s repeats tenant %q", label, grant.TenantID)
	}
	secretRefs := make(map[string]struct{}, len(grant.SecretRefs))
	for _, ref := range grant.SecretRefs {
		envName, err := secretref.EnvName(ref)
		if err != nil {
			return fmt.Errorf("security: %s allowed_secret_refs: %w", label, err)
		}
		if _, repeated := secretRefs[ref]; repeated {
			return fmt.Errorf("security: %s repeats an allowed_secret_refs entry", label)
		}
		if strings.HasPrefix(envName, platformEnvPrefix) {
			return fmt.Errorf(
				"security: %s allowed_secret_refs names %q, "+
					"which is inside the reserved %s namespace",
				label, envName, platformEnvPrefix)
		}
		secretRefs[ref] = struct{}{}
	}
	policyRefs := make(map[string]struct{}, len(grant.PolicyRefs))
	for _, ref := range grant.PolicyRefs {
		if _, repeated := policyRefs[ref]; repeated {
			return fmt.Errorf("security: %s repeats an allowed_policy_refs entry", label)
		}
		policyRefs[ref] = struct{}{}
	}
	e.byTenant[grant.TenantID] = tenantEntitlement{secretRefs: secretRefs, policyRefs: policyRefs}
	return nil
}

// quoteID renders an identifier for a message. It is a helper only so that an
// empty tenant id reads as "" rather than vanishing from the sentence.
func quoteID(id string) string {
	return fmt.Sprintf("%q", id)
}

// DenyCapabilities returns an authorizer that entitles no tenant to anything.
//
// It is not "authorization turned off" — it is the strictest possible answer,
// and it exists so that a caller with no capability configuration has something
// explicit to pass. A revision that names no secret_ref and no policy_refs
// still runs under it, which is exactly the set of revisions that need no
// entitlement to begin with.
func DenyCapabilities() *Entitlements {
	return &Entitlements{}
}

// AuthorizeRevision implements RevisionAuthorizer.
//
// The empty case is first and is not a special case: a revision that names no
// secret and no policy is asking for nothing, so there is nothing to entitle.
// Every other revision needs its tenant to have been granted every reference it
// names, one by one, by exact string.
func (e *Entitlements) AuthorizeRevision(
	tenantID string,
	config tenant.RevisionConfig,
) error {
	if e == nil {
		// A nil table entitles nothing, and says so the same way a populated one
		// would. Failing closed here means a caller that forgot to build the
		// table cannot accidentally get the permissive answer.
		if config.Model.SecretRef == "" && len(config.PolicyRefs) == 0 {
			return nil
		}
		return ErrNotEntitled
	}
	if config.Model.SecretRef == "" && len(config.PolicyRefs) == 0 {
		return nil
	}
	// Looked up after the empty case, so a tenant that has no entitlements at
	// all is refused by the same path — and with the same error — as a tenant
	// that has some but not this one.
	granted := e.byTenant[tenantID]
	if config.Model.SecretRef != "" {
		if _, allowed := granted.secretRefs[config.Model.SecretRef]; !allowed {
			return ErrNotEntitled
		}
	}
	for _, ref := range config.PolicyRefs {
		if _, allowed := granted.policyRefs[ref]; !allowed {
			return ErrNotEntitled
		}
	}
	return nil
}
