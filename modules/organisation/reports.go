/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * The organisation's own reports: who is in it and what they have been doing.
 */

package organisation

import (
	"context"
	"time"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

func registerReports() {
	nexus.RegisterReport(userActivity{})
	nexus.RegisterReport(headcountByUnit{})
}

// ------------------------------------------------------- user activity

type userActivity struct{}

func (userActivity) Key() string { return "organisation.user_activity" }
func (userActivity) App() string { return ID }

func (userActivity) Titles() map[string]string {
	return map[string]string{
		"mn": "Хэрэглэгчийн идэвх",
		"en": "User activity",
		"ru": "Активность пользователей",
		"zh": "用户活动",
		"fr": "Activité des utilisateurs",
		"es": "Actividad de usuarios",
		"ar": "نشاط المستخدمين",
	}
}

func (userActivity) Params() []nexus.ParamSpec {
	return []nexus.ParamSpec{
		{
			Key:  "period",
			Kind: nexus.ParamDateRange,
			Titles: map[string]string{
				"mn": "Хугацаа", "en": "Period", "ru": "Период",
				"zh": "期间", "fr": "Période", "es": "Periodo", "ar": "الفترة",
			},
			DefaultWindow: 30 * 24 * time.Hour,
		},
	}
}

func (userActivity) Columns() []nexus.ColumnSpec {
	return []nexus.ColumnSpec{
		{Key: "person", Kind: nexus.ColumnText, Chart: nexus.ChartCategory,
			Titles: map[string]string{"mn": "Хэрэглэгч", "en": "User", "ru": "Пользователь", "zh": "用户", "fr": "Utilisateur", "es": "Usuario", "ar": "المستخدم"}},
		{Key: "actions", Kind: nexus.ColumnNumber, Chart: nexus.ChartValue, Total: true,
			Titles: map[string]string{"mn": "Үйлдлийн тоо", "en": "Actions", "ru": "Действий", "zh": "操作数", "fr": "Actions", "es": "Acciones", "ar": "الإجراءات"}},
		{Key: "distinct_actions", Kind: nexus.ColumnNumber,
			Titles: map[string]string{"mn": "Төрлийн тоо", "en": "Action kinds", "ru": "Типов действий", "zh": "操作类型", "fr": "Types d'actions", "es": "Tipos", "ar": "أنواع الإجراءات"}},
		{Key: "last_seen", Kind: nexus.ColumnDate,
			Titles: map[string]string{"mn": "Сүүлд", "en": "Last seen", "ru": "Последняя активность", "zh": "最近活动", "fr": "Dernière activité", "es": "Última actividad", "ar": "آخر نشاط"}},
	}
}

// Run reads audit_events, which is what migration 00043 added.
//
// That table was written for the audit trail, and this is the second reason it
// exists: "who has been using this" had no answer at all while the trail lived
// in stdout. The join to users is a LEFT one — an event recorded for a person
// since removed from the organisation still counts, and an actor that is not a
// user at all (the device handlers record `device:<id>`) is shown as itself
// rather than dropped.
func (userActivity) Run(ctx context.Context, q nexus.Querier, p nexus.Params) (nexus.Result, error) {
	const query = `
		SELECT coalesce(nullif(u.name, ''), u.email, a.user_id, '—') AS person,
		       count(*)                                              AS actions,
		       count(DISTINCT a.action)                              AS distinct_actions,
		       max(a.created_at)::date                               AS last_seen
		  FROM audit_events a
		  LEFT JOIN users u
		    ON a.user_id ~ '^[0-9a-fA-F-]{36}$' AND u.id = a.user_id::uuid
		 WHERE a.tenant_id = $1
		   AND a.created_at >= $2 AND a.created_at <= $3
		 GROUP BY 1
		 ORDER BY 2 DESC, 1
		 LIMIT 500`

	rows, err := q.Query(ctx, query,
		nexus.WorkspaceOf(ctx), p.Time("period_from"), p.Time("period_to"))
	if err != nil {
		return nexus.Result{}, err
	}

	collected, err := nexus.Collect(rows, func() (map[string]any, error) {
		var person string
		var actions, kinds int64
		var last time.Time
		if err := rows.Scan(&person, &actions, &kinds, &last); err != nil {
			return nil, err
		}
		return map[string]any{
			"person": person, "actions": actions,
			"distinct_actions": kinds, "last_seen": last,
		}, nil
	})
	if err != nil {
		return nexus.Result{}, err
	}

	result := nexus.Result{Rows: collected}
	// A limit that is not said is a lie. Five hundred people is past every
	// tenant this platform has, but a report that silently stopped there would
	// be read as the whole organisation.
	if len(collected) == 500 {
		result.Notes = append(result.Notes, nexus.Note{
			Level:   "warning",
			Message: "Хамгийн идэвхтэй 500 хэрэглэгчийг харуулав; жагсаалт таслагдсан.",
		})
	}
	return result, nil
}

// ----------------------------------------------------------- headcount

type headcountByUnit struct{}

func (headcountByUnit) Key() string { return "organisation.headcount_by_unit" }
func (headcountByUnit) App() string { return ID }

func (headcountByUnit) Titles() map[string]string {
	return map[string]string{
		"mn": "Хэлтэс нэгжийн бүрэлдэхүүн",
		"en": "Headcount by unit",
		"ru": "Численность по подразделениям",
		"zh": "各部门人数",
		"fr": "Effectifs par unité",
		"es": "Plantilla por unidad",
		"ar": "عدد الموظفين حسب الوحدة",
	}
}

func (headcountByUnit) Params() []nexus.ParamSpec { return nil }

func (headcountByUnit) Columns() []nexus.ColumnSpec {
	return []nexus.ColumnSpec{
		{Key: "unit", Kind: nexus.ColumnText, Chart: nexus.ChartCategory,
			Titles: map[string]string{"mn": "Нэгж", "en": "Unit", "ru": "Подразделение", "zh": "部门", "fr": "Unité", "es": "Unidad", "ar": "الوحدة"}},
		{Key: "people", Kind: nexus.ColumnNumber, Chart: nexus.ChartValue, Total: true,
			Titles: map[string]string{"mn": "Хүний тоо", "en": "People", "ru": "Сотрудников", "zh": "人数", "fr": "Personnes", "es": "Personas", "ar": "الأشخاص"}},
	}
}

// Run counts memberships per department, including the people in none.
//
// The "no unit" row is the point of the report as often as the others are: it
// is where somebody who joined and was never assigned shows up.
// The one place this app still names a platform table, and the reason it is
// allowed to.
//
// A report is SQL the app *declares* and the platform's engine *runs*: bound to
// the caller's organisation, read-only, inside a transaction the app does not
// open. That is not the same act as the app querying `memberships` itself,
// which is what migration 00076 and nexus.Directory between them removed.
//
// It cannot be answered from organisation_people alone. That table holds a row
// only for somebody who was given a job title or a unit, and the "no unit"
// bucket — where somebody who joined and was never assigned shows up — is the
// half of this report that is worth reading. The count of people is the
// platform's number; the grouping is this app's.
func (headcountByUnit) Run(ctx context.Context, q nexus.Querier, p nexus.Params) (nexus.Result, error) {
	const query = `
		SELECT coalesce(d.name, $2) AS unit,
		       count(m.id)          AS people
		  FROM memberships m
		  LEFT JOIN organisation_people op ON op.membership_id = m.id
		  LEFT JOIN departments d
		    ON d.id = op.department_id AND d.tenant_id = m.tenant_id
		 WHERE m.tenant_id = $1
		 GROUP BY 1
		 ORDER BY 2 DESC, 1`

	unassigned := "Нэгжид хамаараагүй"
	if p.Locale() == "en" {
		unassigned = "No unit"
	}

	rows, err := q.Query(ctx, query, nexus.WorkspaceOf(ctx), unassigned)
	if err != nil {
		return nexus.Result{}, err
	}

	collected, err := nexus.Collect(rows, func() (map[string]any, error) {
		var unit string
		var people int64
		if err := rows.Scan(&unit, &people); err != nil {
			return nil, err
		}
		return map[string]any{"unit": unit, "people": people}, nil
	})
	if err != nil {
		return nexus.Result{}, err
	}
	return nexus.Result{Rows: collected}, nil
}
