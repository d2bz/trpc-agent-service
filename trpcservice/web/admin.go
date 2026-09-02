package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	"github.com/liuzengh/trpc-agent-service/trpcservice/identity"
	"github.com/liuzengh/trpc-agent-service/trpcservice/security"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tool"
)

const maxAdminRequestBytes = 1 << 20

type createTenantRequest struct {
	ID          string             `json:"id"`
	Slug        string             `json:"slug"`
	Name        string             `json:"name"`
	Status      tenant.Status      `json:"status,omitempty"`
	Quota       tenant.Quota       `json:"quota,omitempty"`
	AuditPolicy tenant.AuditPolicy `json:"audit_policy,omitempty"`
}

type createAgentAppRequest struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Status tenant.AppStatus `json:"status,omitempty"`
}

// createRevisionRequest carries no created_by. Authorship is not something a
// request may state: it is the principal of the credential that made the
// request, and the field is absent rather than accepted-and-overwritten so that
// a body still carrying it is refused by DisallowUnknownFields instead of
// quietly meaning something other than what it says.
type createRevisionRequest struct {
	ID         string                `json:"id"`
	RevisionNo uint64                `json:"revision_no"`
	Config     tenant.RevisionConfig `json:"config"`
}

type publishRevisionResponse struct {
	App      tenant.AgentApp      `json:"app"`
	Revision tenant.AgentRevision `json:"revision"`
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// adminPathPrefix is the control-plane subtree. Everything at or below it is
// the Admin API — see adminFirst, which is what makes that true of the paths
// that only look like they are below it.
const adminPathPrefix = "/admin"

// adminTenantsPrefix is the only admin route family that exists. Everything
// else under /admin reaches handleAdmin too, and is answered — after
// authentication — as a route that is not there.
const adminTenantsPrefix = adminPathPrefix + "/v1/tenants"

// adminMediaTypeJSON is what an admin write request must declare.
const adminMediaTypeJSON = "application/json"

// handleAdmin is the control-plane trust boundary.
//
// Authentication comes first, before the path is looked at and before the
// method is judged. That ordering is the point: a caller who has not presented
// a valid admin credential gets the same 401 from a real route, an unknown
// route and a wrong method alike, so this API answers no questions about its
// own shape. Routing after authentication also means no handler below can be
// reached by a caller this function has not already identified.
//
// No response from here carries an Access-Control-Allow header — not on
// success, not on failure, and there is no preflight branch. A browser page on
// another origin therefore cannot read an admin response, and cannot send an
// admin write at all: requireAdminContentType keeps every write outside the
// "simple request" set that browsers send without asking first, and the
// preflight they must send instead is refused by the absence of those headers.
// An OPTIONS request is not special-cased anywhere; it authenticates like
// everything else and is then answered as a method these routes do not take.
func (s *PlatformServer) handleAdmin(w http.ResponseWriter, r *http.Request) {
	admin, ok := s.authenticateAdmin(w, r)
	if !ok {
		return
	}
	if !requireAdminContentType(w, r) {
		return
	}
	path := r.URL.Path
	if path != adminTenantsPrefix && !strings.HasPrefix(path, adminTenantsPrefix+"/") {
		writeAPIError(w, http.StatusNotFound, "not_found", "admin route not found")
		return
	}
	trimmed := strings.Trim(strings.TrimPrefix(path, adminTenantsPrefix), "/")
	var segments []string
	if trimmed != "" {
		segments = strings.Split(trimmed, "/")
	}
	// Tenant scope is decided once, here, for every route that names a tenant —
	// and before the route is dispatched, so no handler can reach the
	// repository on behalf of a tenant the caller does not administer.
	if len(segments) > 0 && !s.allowsAdminTenant(w, admin, segments[0]) {
		return
	}

	switch {
	case len(segments) == 0:
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		// Creating a tenant is the one control-plane operation that belongs to
		// no tenant, so it belongs to the role that belongs to no tenant.
		if !admin.IsPlatformAdmin() {
			writeForbidden(w)
			return
		}
		s.createTenant(w, r)
	case len(segments) == 1:
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		s.getTenant(w, r, segments[0])
	case len(segments) == 2 && segments[1] == "apps":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		s.createAgentApp(w, r, segments[0])
	case len(segments) == 3 && segments[1] == "apps":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		s.getAgentApp(w, r, segments[0], segments[2])
	case len(segments) == 4 && segments[1] == "apps" && segments[3] == "revisions":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		s.createRevision(w, r, admin, segments[0], segments[2])
	case len(segments) == 5 && segments[1] == "apps" && segments[3] == "revisions":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		s.getRevision(w, r, segments[0], segments[2], segments[4])
	case len(segments) == 6 && segments[1] == "apps" && segments[3] == "revisions" && segments[5] == "publish":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		s.publishRevision(w, r, segments[0], segments[2], segments[4])
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "admin route not found")
	}
}

// authenticateAdmin resolves the control-plane caller, or answers 401.
//
// Every failure is the same answer. A malformed header, an unknown key, a chat
// key presented here, an expired context: all of them are "the credential is
// not valid", because the differences between them are only useful to someone
// who does not have one.
func (s *PlatformServer) authenticateAdmin(
	w http.ResponseWriter,
	r *http.Request,
) (identity.AdminIdentity, bool) {
	token, ok := bearerToken(r.Header.Get(HeaderAuthorization))
	if !ok {
		writeUnauthenticated(w, "a Bearer credential is required")
		return identity.AdminIdentity{}, false
	}
	admin, err := s.admin.AuthenticateAdmin(r.Context(), token)
	if err != nil {
		writeUnauthenticated(w, "the credential is not valid")
		return identity.AdminIdentity{}, false
	}
	if err := admin.Validate(); err != nil {
		writeUnauthenticated(w, "the credential is not valid")
		return identity.AdminIdentity{}, false
	}
	return admin, true
}

// allowsAdminTenant reports whether admin may act on tenantID, and answers the
// caller when it may not.
//
// A platform admin passes, and a malformed id is left to the repository to
// reject as a bad request: that is not a disclosure to a caller who administers
// every tenant anyway. A tenant admin is compared exactly against its own
// tenant, and a mismatch is answered right here — before any repository call —
// with a body byte-identical to a real not-found. Both halves matter. A
// different body, or a different status, would turn this API into an oracle for
// which tenants exist; reaching the repository first would let one tenant's
// admin make this process query the database on another tenant's behalf, which
// is a load and an audit trail it has no business creating.
func (s *PlatformServer) allowsAdminTenant(
	w http.ResponseWriter,
	admin identity.AdminIdentity,
	tenantID string,
) bool {
	if admin.IsPlatformAdmin() || admin.AllowsTenant(tenantID) {
		return true
	}
	writeNotFound(w)
	return false
}

// requireAdminContentType refuses an admin write that does not declare JSON.
//
// This is not about parsing — decodeAdminJSON rejects a body that is not JSON
// anyway, and publish sends no body at all. It is about what a browser may send
// without asking permission first. A POST whose Content-Type is one of three
// form-ish values is a CORS "simple request": a page on any origin can send it
// with whatever credentials the browser holds, and never needs to read the
// response for the write to have happened. Requiring application/json puts
// every admin write outside that set, so the browser must preflight — and the
// preflight has nothing to succeed against, because no admin response carries a
// single Access-Control-Allow header.
func requireAdminContentType(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, adminMediaTypeJSON) {
		writeAPIError(
			w,
			http.StatusUnsupportedMediaType,
			"unsupported_media_type",
			"admin requests must send Content-Type: "+adminMediaTypeJSON,
		)
		return false
	}
	return true
}

func (s *PlatformServer) createTenant(w http.ResponseWriter, r *http.Request) {
	var request createTenantRequest
	if !decodeAdminJSON(w, r, &request) {
		return
	}
	created, err := s.repository.CreateTenant(r.Context(), tenant.Tenant{
		ID: request.ID, Slug: request.Slug, Name: request.Name,
		Status: request.Status, Quota: request.Quota, AuditPolicy: request.AuditPolicy,
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	w.Header().Set("Location", "/admin/v1/tenants/"+created.ID)
	writeJSON(w, http.StatusCreated, created)
}

func (s *PlatformServer) getTenant(w http.ResponseWriter, r *http.Request, tenantID string) {
	item, err := s.repository.GetTenant(r.Context(), tenantID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *PlatformServer) createAgentApp(
	w http.ResponseWriter,
	r *http.Request,
	tenantID string,
) {
	var request createAgentAppRequest
	if !decodeAdminJSON(w, r, &request) {
		return
	}
	created, err := s.repository.CreateAgentApp(
		r.Context(),
		tenant.TenantContext{TenantID: tenantID},
		tenant.AgentApp{
			ID: request.ID, TenantID: tenantID, Name: request.Name, Status: request.Status,
		},
	)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/admin/v1/tenants/%s/apps/%s", tenantID, created.ID))
	writeJSON(w, http.StatusCreated, created)
}

func (s *PlatformServer) getAgentApp(
	w http.ResponseWriter,
	r *http.Request,
	tenantID string,
	appID string,
) {
	item, err := s.repository.GetAgentApp(
		r.Context(), tenant.TenantContext{TenantID: tenantID}, appID,
	)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// createRevision stores a draft revision, authored by the caller.
//
// The entitlement check happens here, before the write, so a revision naming a
// capability this tenant does not hold never reaches the database. Checking only
// at publish would let a draft accumulate refs that look accepted and then fail
// much later, at the point where an operator is least able to tell whether the
// configuration or the platform is wrong.
func (s *PlatformServer) createRevision(
	w http.ResponseWriter,
	r *http.Request,
	admin identity.AdminIdentity,
	tenantID string,
	appID string,
) {
	var request createRevisionRequest
	if !decodeAdminJSON(w, r, &request) {
		return
	}
	if err := s.revisions.AuthorizeRevision(tenantID, request.Config); err != nil {
		writeNotEntitled(w)
		return
	}
	created, err := s.repository.CreateRevision(
		r.Context(),
		tenant.TenantContext{TenantID: tenantID},
		tenant.AgentRevision{
			ID: request.ID, TenantID: tenantID, AgentAppID: appID,
			RevisionNo: request.RevisionNo, Config: request.Config,
			// Authorship is the authenticated principal, never a request field.
			CreatedBy: admin.PrincipalID,
		},
	)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	w.Header().Set(
		"Location",
		fmt.Sprintf("/admin/v1/tenants/%s/apps/%s/revisions/%s", tenantID, appID, created.ID),
	)
	writeJSON(w, http.StatusCreated, created)
}

func (s *PlatformServer) getRevision(
	w http.ResponseWriter,
	r *http.Request,
	tenantID string,
	appID string,
	revisionID string,
) {
	item, err := s.repository.GetRevision(
		r.Context(), tenant.TenantContext{TenantID: tenantID}, appID, revisionID,
	)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// publishRevision makes a revision servable, after checking that it may be.
//
// Publishing is the moment a configuration stops being a draft and starts being
// something this platform will run, so both checks that decide whether it can
// run happen here, on the stored revision rather than on anything the request
// says. The revision is immutable, so reading it first is not a race: what is
// read is what will be published, and what a Runtime will later build from.
//
// The order is entitlement, then tool registry — the same order Runtime uses.
// Entitlement is about what this tenant is allowed to reach at all, and it
// answers identically whether or not the ref names anything real; the registry
// check is about whether a revision this tenant may run is internally coherent,
// and it can afford to say what is wrong because by then nothing is being
// disclosed to someone who should not know it.
func (s *PlatformServer) publishRevision(
	w http.ResponseWriter,
	r *http.Request,
	tenantID string,
	appID string,
	revisionID string,
) {
	scope := tenant.TenantContext{TenantID: tenantID}
	revision, err := s.repository.GetRevision(r.Context(), scope, appID, revisionID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.revisions.AuthorizeRevision(tenantID, revision.Config); err != nil {
		writeNotEntitled(w)
		return
	}
	if _, err := tool.Builtin().Resolve(
		revision.Config.ToolRefs, revision.Config.PolicyRefs,
	); err != nil {
		// Distinct from not_entitled on purpose: this caller is entitled to every
		// ref it named, so the remaining fault is in the revision itself, and
		// saying so is the difference between a fixable error and a mystery.
		writeAPIError(
			w, http.StatusBadRequest, "invalid_revision", "revision cannot be served: "+err.Error())
		return
	}
	app, published, err := s.repository.PublishRevision(r.Context(), scope, appID, revisionID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publishRevisionResponse{App: app, Revision: published})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func decodeAdminJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "request body must contain one JSON object")
		return false
	}
	return true
}

func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		writeUnauthenticated(w, "the credential is not valid")
	case errors.Is(err, identity.ErrForbidden):
		writeForbidden(w)
	case errors.Is(err, tenant.ErrInvalidArgument):
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", err.Error())
	case errors.Is(err, tenant.ErrTenantScope), errors.Is(err, tenant.ErrNotFound):
		// Same writer as the tenant-scope short-circuit above, so a refused
		// cross-tenant read and a genuinely missing resource stay identical.
		writeNotFound(w)
	case errors.Is(err, tenant.ErrAlreadyExists):
		writeAPIError(w, http.StatusConflict, "already_exists", err.Error())
	case errors.Is(err, tenant.ErrTenantInactive):
		writeAPIError(w, http.StatusForbidden, "tenant_inactive", err.Error())
	case errors.Is(err, tenant.ErrNoPublishedRevision), errors.Is(err, tenant.ErrRevisionNotPublished):
		writeAPIError(w, http.StatusConflict, "revision_unavailable", err.Error())
	case errors.Is(err, security.ErrNotEntitled), errors.Is(err, tenant.ErrConfigIntegrity):
		// A revision that reaches a Runtime build un-entitled, or whose stored
		// bytes no longer match their digest, is a revision this platform will not
		// serve — and the reason is not the caller's to learn. Both collapse into
		// the same "this revision is unavailable" that a client already gets for
		// an unpublished one, with no wording that says which of the three it was.
		writeAPIError(
			w, http.StatusConflict, "revision_unavailable", "revision is not available")
	case errors.Is(err, platformagent.ErrResolverClosed):
		writeAPIError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime is unavailable")
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

// writeNotFound is the one not-found answer this package gives.
//
// A tenant admin reaching for another tenant gets exactly this, and so does a
// caller asking for a tenant that was never created — same status, same code,
// same message, byte for byte. It is a single function rather than two identical
// literals so that they cannot drift apart later: the moment the two answers
// differ by a word, the difference between them is a way to enumerate tenants.
func writeNotFound(w http.ResponseWriter) {
	writeAPIError(w, http.StatusNotFound, "not_found", "resource not found")
}

// writeForbidden answers a caller who is known, and known not to be allowed.
//
// This is distinct from writeNotFound on purpose. It appears only where the
// existence of the thing is not a secret from this caller — a tenant admin
// cannot create tenants, and knows it already — never where the answer would
// reveal whether some other tenant's resource exists.
func writeForbidden(w http.ResponseWriter) {
	writeAPIError(w, http.StatusForbidden, "forbidden", "this credential is not allowed")
}

// writeNotEntitled refuses a revision that names a capability its tenant does
// not hold.
//
// The wording is fixed and says nothing about which ref was rejected. That is
// the whole design: a caller must get the same answer for a secret_ref whose
// environment variable exists and one whose does not, for a policy the registry
// knows and one it has never heard of. Any difference between those cases would
// make this endpoint a probe for the process environment and the tool registry
// of a platform the caller does not administer.
func writeNotEntitled(w http.ResponseWriter) {
	writeAPIError(
		w,
		http.StatusForbidden,
		"not_entitled",
		"this tenant is not entitled to a capability the revision references",
	)
}

func writeAPIError(w http.ResponseWriter, status int, code string, message string) {
	response := apiError{}
	response.Error.Code = code
	response.Error.Message = message
	writeJSON(w, status, response)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}
