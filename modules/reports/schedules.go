/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Scheduled reports: the CRUD behind the screen. What a schedule has to satisfy
 * before it is stored is in backend/domain/reports.
 */

package reports

import (
	"encoding/json"
	"net/http"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/reports"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const maxScheduleBody = 16 << 10

func (m *Module) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}

	schedules, err := m.schedules.List(r.Context(), tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the schedules")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{
		"schedules": schedules,
		// Whether anything can actually be sent. Without it the screen would
		// let somebody create a schedule, show it as active, and never say why
		// nothing arrives.
		"delivery_configured": m.engine.Deliverable(),
	})
}

func (m *Module) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	edit, ok := decodeSchedule(w, r)
	if !ok {
		return
	}

	schedule, err := m.svc.CreateSchedule(acting(r), tenantID, edit)
	if err != nil {
		fail(w, r, err)
		return
	}

	m.record(r, tenantID, "reports.schedule_created", schedule.ReportKey, map[string]any{
		"schedule_id": schedule.ID,
		"cron":        schedule.Cron,
		"recipients":  len(schedule.Recipients),
	})
	nexus.JSON(w, http.StatusCreated, map[string]any{"id": schedule.ID})
}

func (m *Module) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := m.schedule(w, r)
	if !ok {
		return
	}
	edit, ok := decodeSchedule(w, r)
	if !ok {
		return
	}

	schedule, err := m.svc.UpdateSchedule(acting(r), tenantID, id, edit)
	if err != nil {
		fail(w, r, err)
		return
	}

	m.record(r, tenantID, "reports.schedule_updated", schedule.ReportKey, map[string]any{
		"schedule_id": id, "cron": schedule.Cron, "active": schedule.Active,
	})
	nexus.JSON(w, http.StatusOK, map[string]any{"id": id})
}

func (m *Module) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := m.schedule(w, r)
	if !ok {
		return
	}

	reportKey, err := m.svc.DeleteSchedule(r.Context(), tenantID, id)
	if err != nil {
		fail(w, r, err)
		return
	}

	m.record(r, tenantID, "reports.schedule_removed", reportKey, map[string]any{"schedule_id": id})
	w.WriteHeader(http.StatusNoContent)
}

// schedule resolves the caller's tenant and the schedule id together. The id is
// checked for shape here for the same reason a grant's is: an unparseable
// identifier is a malformed request rather than a rule anybody broke.
func (m *Module) schedule(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return "", "", false
	}
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid schedule id")
		return "", "", false
	}
	return tenantID, id, true
}

func decodeSchedule(w http.ResponseWriter, r *http.Request) (domain.ScheduleEdit, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxScheduleBody)
	var edit domain.ScheduleEdit
	if err := json.NewDecoder(r.Body).Decode(&edit); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid schedule")
		return edit, false
	}
	return edit, true
}
