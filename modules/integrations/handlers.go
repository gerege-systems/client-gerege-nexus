/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package integrations

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/go-chi/chi/v5"
)

// Handler serves the platform's integration connectors API.
type Handler struct {
	mgr *Manager
}

// NewHandler constructs the integration HTTP handler.
func NewHandler(mgr *Manager) *Handler {
	return &Handler{mgr: mgr}
}

// integrationSaveRequest is the wire shape of the connector form.
type integrationSaveRequest struct {
	Provider  string            `json:"provider"`
	Name      string            `json:"name"`
	TargetURL string            `json:"target_url"`
	Secret    string            `json:"secret_key"`
	Status    string            `json:"status"`
	Config    map[string]string `json:"config"`
}

func (r integrationSaveRequest) toSave() SaveRequest {
	return SaveRequest{
		Provider:  Provider(r.Provider),
		Name:      r.Name,
		TargetURL: r.TargetURL,
		Secret:    r.Secret,
		Status:    ConnectorStatus(r.Status),
		Config:    r.Config,
	}
}

func integrationError(w http.ResponseWriter, err error) {
	var invalid *InvalidError
	switch {
	case errors.Is(err, ErrNotFound):
		nexus.Error(w, http.StatusNotFound, "integration not found")
	case errors.Is(err, ErrDuplicateName):
		nexus.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNoEncryptionKey),
		errors.Is(err, ErrProviderUnavailable):
		nexus.Error(w, http.StatusServiceUnavailable, err.Error())
	case errors.As(err, &invalid):
		nexus.Error(w, http.StatusBadRequest, invalid.Error())
	default:
		slog.Error("integration: request failed", "error", err)
		nexus.Error(w, http.StatusInternalServerError, "the integration could not be saved")
	}
}

func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}
	list, err := h.mgr.List(r.Context(), tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "failed to load integrations")
		return
	}
	nexus.JSON(w, http.StatusOK, list)
}

func (h *Handler) HandleProviders(w http.ResponseWriter, r *http.Request) {
	nexus.JSON(w, http.StatusOK, map[string]any{
		"providers":             Catalog(),
		"encryption_configured": EncryptionConfigured(),
		"redirect_uri":          RedirectURI(),
	})
}

func (h *Handler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}
	var req integrationSaveRequest
	if decodeLimited(r, &req, 32<<10) != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid integration configuration")
		return
	}
	conn, err := h.mgr.Create(r.Context(), tenantID, req.toSave())
	if err != nil {
		integrationError(w, err)
		return
	}
	claims, _ := nexus.UserFromContext(r.Context())
	nexus.Audit(r.Context(), tenantID, claims.UserID, "integration.create", "integration",
		map[string]any{"id": conn.ID, "provider": conn.Provider})
	nexus.JSON(w, http.StatusCreated, conn)
}

func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}
	var req integrationSaveRequest
	if decodeLimited(r, &req, 32<<10) != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid integration configuration")
		return
	}
	conn, err := h.mgr.Update(r.Context(), tenantID, chi.URLParam(r, "id"), req.toSave())
	if err != nil {
		integrationError(w, err)
		return
	}
	claims, _ := nexus.UserFromContext(r.Context())
	nexus.Audit(r.Context(), tenantID, claims.UserID, "integration.update", "integration",
		map[string]any{"id": conn.ID})
	nexus.JSON(w, http.StatusOK, conn)
}

func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.mgr.Delete(r.Context(), tenantID, id); err != nil {
		integrationError(w, err)
		return
	}
	claims, _ := nexus.UserFromContext(r.Context())
	nexus.Audit(r.Context(), tenantID, claims.UserID, "integration.delete", "integration",
		map[string]any{"id": id})
	nexus.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) HandleConnect(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}
	claims, _ := nexus.UserFromContext(r.Context())
	authURL, err := h.mgr.BeginConnect(r.Context(), tenantID, claims.UserID, chi.URLParam(r, "id"))
	if err != nil {
		integrationError(w, err)
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]string{"authorization_url": authURL})
}

func (h *Handler) HandleDisconnect(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.mgr.Disconnect(r.Context(), tenantID, id); err != nil {
		integrationError(w, err)
		return
	}
	claims, _ := nexus.UserFromContext(r.Context())
	nexus.Audit(r.Context(), tenantID, claims.UserID, "integration.disconnect", "integration",
		map[string]any{"id": id})
	nexus.JSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}

func (h *Handler) HandleDeliveries(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, convErr := strconv.Atoi(raw); convErr == nil {
			limit = parsed
		}
	}
	list, err := h.mgr.Deliveries(r.Context(), tenantID, limit)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "failed to load delivery history")
		return
	}
	nexus.JSON(w, http.StatusOK, list)
}

func (h *Handler) HandleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	settingsURL := strings.TrimRight(os.Getenv("PUBLIC_ORIGIN"), "/") + "/settings/integrations"
	if strings.TrimSpace(settingsURL) == "/settings/integrations" {
		settingsURL = "/settings/integrations"
	}

	if providerErr := query.Get("error"); providerErr != "" {
		http.Redirect(w, r, settingsURL+"?connected=0&reason="+url.QueryEscape(providerErr), http.StatusFound)
		return
	}

	res, err := h.mgr.CompleteConnect(r.Context(), query.Get("state"), query.Get("code"))
	if err != nil {
		slog.Warn("integration: oauth callback failed", "error", err)
		http.Redirect(w, r, settingsURL+"?connected=0&reason="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}

	nexus.Audit(r.Context(), res.TenantID, res.UserID, "integration.connected", "integration",
		map[string]any{
			"id":            res.Connector.ID,
			"provider":      res.Connector.Provider,
			"account_label": res.Connector.AccountLabel,
		})

	http.Redirect(w, r, settingsURL+"?connected=1&name="+url.QueryEscape(res.Connector.Name), http.StatusFound)
}
