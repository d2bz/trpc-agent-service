package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	platformagent "github.com/liuzengh/trpc-agent-service/trpcservice/agent"
	"github.com/liuzengh/trpc-agent-service/trpcservice/identity"
	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
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

type createRevisionRequest struct {
	ID         string                `json:"id"`
	RevisionNo uint64                `json:"revision_no"`
	Config     tenant.RevisionConfig `json:"config"`
	CreatedBy  string                `json:"created_by"`
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

func (s *PlatformServer) handleAdmin(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/v1/tenants"), "/")
	var segments []string
	if path != "" {
		segments = strings.Split(path, "/")
	}

	switch {
	case len(segments) == 0:
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
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
		s.createRevision(w, r, segments[0], segments[2])
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

func (s *PlatformServer) createRevision(
	w http.ResponseWriter,
	r *http.Request,
	tenantID string,
	appID string,
) {
	var request createRevisionRequest
	if !decodeAdminJSON(w, r, &request) {
		return
	}
	created, err := s.repository.CreateRevision(
		r.Context(),
		tenant.TenantContext{TenantID: tenantID},
		tenant.AgentRevision{
			ID: request.ID, TenantID: tenantID, AgentAppID: appID,
			RevisionNo: request.RevisionNo, Config: request.Config, CreatedBy: request.CreatedBy,
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

func (s *PlatformServer) publishRevision(
	w http.ResponseWriter,
	r *http.Request,
	tenantID string,
	appID string,
	revisionID string,
) {
	app, revision, err := s.repository.PublishRevision(
		r.Context(), tenant.TenantContext{TenantID: tenantID}, appID, revisionID,
	)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publishRevisionResponse{App: app, Revision: revision})
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
		writeAPIError(w, http.StatusForbidden, "forbidden", "this credential is not allowed")
	case errors.Is(err, tenant.ErrInvalidArgument):
		writeAPIError(w, http.StatusBadRequest, "invalid_argument", err.Error())
	case errors.Is(err, tenant.ErrTenantScope), errors.Is(err, tenant.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, tenant.ErrAlreadyExists):
		writeAPIError(w, http.StatusConflict, "already_exists", err.Error())
	case errors.Is(err, tenant.ErrTenantInactive):
		writeAPIError(w, http.StatusForbidden, "tenant_inactive", err.Error())
	case errors.Is(err, tenant.ErrNoPublishedRevision), errors.Is(err, tenant.ErrRevisionNotPublished):
		writeAPIError(w, http.StatusConflict, "revision_unavailable", err.Error())
	case errors.Is(err, platformagent.ErrResolverClosed):
		writeAPIError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime is unavailable")
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
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
