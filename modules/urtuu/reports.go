/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Three reports: what got done, what was late, and how hard the channel worked.
 *
 * Each is a declaration and nothing more — the list screen, the parameter form,
 * the chart, the Excel export, the schedule that mails it out and the audit
 * entry recording that somebody ran it all come from the engine. What is
 * written here is a title in seven languages, a parameter, some columns and one
 * query.
 *
 * All three declare ScopeFull and none declares ScopeCounterparty. That is not
 * an omission: counterparty scope means "the rows that relate to the reader",
 * and the reader would have to be identified in the data by a
 * `counterparty_ref` — a registration number — which nothing in the task schema
 * carries. A link is not a registration number, and treating it as one would
 * quietly widen a grant meant for one organisation into a view of every
 * subordinate. A report that cannot filter by counterparty must not offer to.
 */

package urtuu

import (
	"context"
	"log/slog"
	"time"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// The directory is handed to each report rather than fetched inside it: a
// report runs minutes or months after construction, and a dependency looked up
// at that moment is one nothing checks at boot.
func registerReports(peers contract.PeerDirectory) {
	nexus.RegisterReport(taskCompletion{peers})
	nexus.RegisterReport(slaBreaches{peers})
	nexus.RegisterReport(channelLoad{peers})
}

// period is the parameter all three share.
func period(window time.Duration) []nexus.ParamSpec {
	return []nexus.ParamSpec{{
		Key:  "period",
		Kind: nexus.ParamDateRange,
		Titles: map[string]string{
			"mn": "Хугацаа", "en": "Period", "ru": "Период",
			"zh": "期间", "fr": "Période", "es": "Periodo", "ar": "الفترة",
		},
		DefaultWindow: window,
	}}
}

// fullOnly is the scope declaration all three make. See the file comment.
func fullOnly() []string { return []string{nexus.ReportScopeFull} }

// ------------------------------------------------------- task completion

type taskCompletion struct{ peers contract.PeerDirectory }

func (taskCompletion) Key() string      { return "urtuu.task_completion" }
func (taskCompletion) App() string      { return ID }
func (taskCompletion) Scopes() []string { return fullOnly() }

func (taskCompletion) Titles() map[string]string {
	return map[string]string{
		"mn": "Даалгаврын биелэлт",
		"en": "Task completion",
		"ru": "Исполнение заданий",
		"zh": "任务完成情况",
		"fr": "Exécution des tâches",
		"es": "Cumplimiento de tareas",
		"ar": "إنجاز المهام",
	}
}

func (taskCompletion) Params() []nexus.ParamSpec { return period(90 * 24 * time.Hour) }

func (taskCompletion) Columns() []nexus.ColumnSpec {
	return []nexus.ColumnSpec{
		{Key: "code", Kind: nexus.ColumnText, Chart: nexus.ChartCategory,
			Titles: map[string]string{"mn": "Код", "en": "Code", "ru": "Код", "zh": "代码", "fr": "Code", "es": "Código", "ar": "الرمز"}},
		{Key: "line", Kind: nexus.ColumnText,
			Titles: map[string]string{"mn": "Шугам", "en": "Line", "ru": "Линия", "zh": "线路", "fr": "Ligne", "es": "Línea", "ar": "الخط"}},
		{Key: "peer", Kind: nexus.ColumnText,
			Titles: map[string]string{"mn": "Платформ", "en": "Installation", "ru": "Платформа", "zh": "平台", "fr": "Installation", "es": "Instalación", "ar": "المنصّة"}},
		{Key: "raised", Kind: nexus.ColumnNumber, Total: true,
			Titles: map[string]string{"mn": "Нийт", "en": "Raised", "ru": "Всего", "zh": "总数", "fr": "Émises", "es": "Emitidas", "ar": "الإجمالي"}},
		{Key: "completed", Kind: nexus.ColumnNumber, Chart: nexus.ChartValue, Total: true,
			Titles: map[string]string{"mn": "Биелсэн", "en": "Completed", "ru": "Исполнено", "zh": "已完成", "fr": "Terminées", "es": "Completadas", "ar": "منجزة"}},
		{Key: "returned", Kind: nexus.ColumnNumber, Total: true,
			Titles: map[string]string{"mn": "Буцаасан", "en": "Returned", "ru": "Возвращено", "zh": "退回", "fr": "Retournées", "es": "Devueltas", "ar": "معادة"}},
		{Key: "rate", Kind: nexus.ColumnPercent,
			Titles: map[string]string{"mn": "Биелэлт", "en": "Completion", "ru": "Доля исполнения", "zh": "完成率", "fr": "Taux", "es": "Tasa", "ar": "النسبة"}},
		{Key: "avg_days", Kind: nexus.ColumnNumber,
			Titles: map[string]string{"mn": "Дундаж хоног", "en": "Average days", "ru": "Среднее, дней", "zh": "平均天数", "fr": "Jours en moyenne", "es": "Días de media", "ar": "متوسط الأيام"}},
	}
}

// Run groups by code and by the installation the work concerns.
//
// "The installation" is whichever end of the link this row is about — where it
// came from for received work, where it went for delegated. Both are the same
// question from the reader's side: who this task was between.
func (r taskCompletion) Run(ctx context.Context, q nexus.Querier, p nexus.Params) (nexus.Result, error) {
	// Grouped by line as well as by code, because the two lines answer to
	// different people: a service request's completion is owed to somebody
	// outside the platform, and mixing the two into one rate would average
	// away exactly the number a citizen-facing office is judged on.
	const query = `
		SELECT t.code, t.line,
		       coalesce(t.origin_peer_id::text, t.target_peer_id::text, '')   AS peer_id,
		       count(*)                                                       AS raised,
		       count(*) FILTER (WHERE t.status IN ('COMPLETED', 'CLOSED'))     AS completed,
		       count(*) FILTER (WHERE t.status = 'RETURNED')                   AS returned,
		       -- Days from raising to the last move, over the tasks that
		       -- finished. Averaging the unfinished ones in would make a
		       -- backlog look like fast work.
		       coalesce(avg(EXTRACT(EPOCH FROM (t.updated_at - t.created_at)) / 86400)
		                FILTER (WHERE t.status IN ('COMPLETED', 'CLOSED')), 0) AS avg_days
		  FROM urtuu_tasks t
		 WHERE t.tenant_id = $1 AND t.created_at >= $2 AND t.created_at <= $3
		 GROUP BY 1, 2, 3
		 ORDER BY 4 DESC, 1
		 LIMIT 500`

	rows, err := q.Query(ctx, query,
		nexus.WorkspaceOf(ctx), p.Time("period_from"), p.Time("period_to"))
	if err != nil {
		return nexus.Result{}, err
	}

	// The peer is grouped by id and named afterwards: the name lives in the
	// channel's table and this app stopped reading it (see peers.go). Two links
	// with one name stay two rows, which is the more honest grouping anyway.
	names := reportPeerNames(ctx, r.peers)
	collected, err := nexus.Collect(rows, func() (map[string]any, error) {
		var code, line, peerID string
		var raised, completed, returned int64
		var avgDays float64
		if err := rows.Scan(&code, &line, &peerID, &raised, &completed, &returned, &avgDays); err != nil {
			return nil, err
		}
		peer := peerLabel(names, peerID)
		rate := 0.0
		if raised > 0 {
			rate = float64(completed) / float64(raised) * 100
		}
		return map[string]any{
			"code": code, "line": line, "peer": peer, "raised": raised, "completed": completed,
			"returned": returned, "rate": rate, "avg_days": round1(avgDays),
		}, nil
	})
	if err != nil {
		return nexus.Result{}, err
	}
	return truncated(collected), nil
}

// ------------------------------------------------------------ SLA breaches

type slaBreaches struct{ peers contract.PeerDirectory }

func (slaBreaches) Key() string      { return "urtuu.sla_breaches" }
func (slaBreaches) App() string      { return ID }
func (slaBreaches) Scopes() []string { return fullOnly() }

func (slaBreaches) Titles() map[string]string {
	return map[string]string{
		"mn": "Хугацаа хэтрэлт",
		"en": "SLA breaches",
		"ru": "Нарушения сроков",
		"zh": "超期情况",
		"fr": "Dépassements de délai",
		"es": "Plazos incumplidos",
		"ar": "تجاوزات المهلة",
	}
}

func (slaBreaches) Params() []nexus.ParamSpec { return period(90 * 24 * time.Hour) }

func (slaBreaches) Columns() []nexus.ColumnSpec {
	return []nexus.ColumnSpec{
		{Key: "title", Kind: nexus.ColumnText, Chart: nexus.ChartCategory,
			Titles: map[string]string{"mn": "Даалгавар", "en": "Task", "ru": "Задание", "zh": "任务", "fr": "Tâche", "es": "Tarea", "ar": "المهمة"}},
		{Key: "code", Kind: nexus.ColumnText,
			Titles: map[string]string{"mn": "Код", "en": "Code", "ru": "Код", "zh": "代码", "fr": "Code", "es": "Código", "ar": "الرمز"}},
		{Key: "line", Kind: nexus.ColumnText,
			Titles: map[string]string{"mn": "Шугам", "en": "Line", "ru": "Линия", "zh": "线路", "fr": "Ligne", "es": "Línea", "ar": "الخط"}},
		{Key: "peer", Kind: nexus.ColumnText,
			Titles: map[string]string{"mn": "Платформ", "en": "Installation", "ru": "Платформа", "zh": "平台", "fr": "Installation", "es": "Instalación", "ar": "المنصّة"}},
		{Key: "status", Kind: nexus.ColumnText,
			Titles: map[string]string{"mn": "Төлөв", "en": "Status", "ru": "Состояние", "zh": "状态", "fr": "État", "es": "Estado", "ar": "الحالة"}},
		{Key: "deadline", Kind: nexus.ColumnDate,
			Titles: map[string]string{"mn": "Товлосон", "en": "Due", "ru": "Срок", "zh": "期限", "fr": "Échéance", "es": "Plazo", "ar": "المهلة"}},
		{Key: "days_late", Kind: nexus.ColumnNumber, Chart: nexus.ChartValue,
			Titles: map[string]string{"mn": "Хэтэрсэн хоног", "en": "Days late", "ru": "Дней просрочки", "zh": "超期天数", "fr": "Jours de retard", "es": "Días de retraso", "ar": "أيام التأخير"}},
	}
}

// Run lists what missed its deadline, worst first.
//
// A task that is still open counts from now; one that finished counts from when
// it finished, so a fortnight late and settled is not the same row as a
// fortnight late and still open. Closed tasks are excluded: the originator
// accepting the outcome is what ends the question.
func (r slaBreaches) Run(ctx context.Context, q nexus.Querier, p nexus.Params) (nexus.Result, error) {
	const query = `
		SELECT t.title, t.code, t.line,
		       coalesce(t.origin_peer_id::text, t.target_peer_id::text, '') AS peer_id,
		       t.status, t.deadline::date,
		       EXTRACT(EPOCH FROM (
		           CASE WHEN t.status IN ('COMPLETED', 'RETURNED') THEN t.updated_at
		                ELSE NOW() END - t.deadline)) / 86400          AS days_late
		  FROM urtuu_tasks t
		 WHERE t.tenant_id = $1
		   AND t.deadline IS NOT NULL AND t.status <> 'CLOSED'
		   AND t.created_at >= $2 AND t.created_at <= $3
		   AND (CASE WHEN t.status IN ('COMPLETED', 'RETURNED') THEN t.updated_at
		             ELSE NOW() END) > t.deadline
		 ORDER BY 7 DESC
		 LIMIT 500`

	rows, err := q.Query(ctx, query,
		nexus.WorkspaceOf(ctx), p.Time("period_from"), p.Time("period_to"))
	if err != nil {
		return nexus.Result{}, err
	}

	names := reportPeerNames(ctx, r.peers)
	collected, err := nexus.Collect(rows, func() (map[string]any, error) {
		var title, code, line, peerID, status string
		var deadline time.Time
		var late float64
		if err := rows.Scan(&title, &code, &line, &peerID, &status, &deadline, &late); err != nil {
			return nil, err
		}
		peer := peerLabel(names, peerID)
		return map[string]any{
			"title": title, "code": code, "line": line, "peer": peer, "status": status,
			"deadline": deadline, "days_late": round1(late),
		}, nil
	})
	if err != nil {
		return nexus.Result{}, err
	}
	return truncated(collected), nil
}

// ------------------------------------------------------------ channel load

type channelLoad struct{ peers contract.PeerDirectory }

func (channelLoad) Key() string      { return "urtuu.channel_load" }
func (channelLoad) App() string      { return ID }
func (channelLoad) Scopes() []string { return fullOnly() }

func (channelLoad) Titles() map[string]string {
	return map[string]string{
		"mn": "Сувгийн ачаалал",
		"en": "Channel load",
		"ru": "Нагрузка канала",
		"zh": "通道负载",
		"fr": "Charge du canal",
		"es": "Carga del canal",
		"ar": "حِمل القناة",
	}
}

func (channelLoad) Params() []nexus.ParamSpec { return period(30 * 24 * time.Hour) }

func (channelLoad) Columns() []nexus.ColumnSpec {
	return []nexus.ColumnSpec{
		{Key: "peer", Kind: nexus.ColumnText, Chart: nexus.ChartCategory,
			Titles: map[string]string{"mn": "Холбоос", "en": "Link", "ru": "Связь", "zh": "连接", "fr": "Lien", "es": "Enlace", "ar": "الرابط"}},
		{Key: "envelopes", Kind: nexus.ColumnNumber, Chart: nexus.ChartValue, Total: true,
			Titles: map[string]string{"mn": "Дугтуй", "en": "Envelopes", "ru": "Конвертов", "zh": "信封数", "fr": "Enveloppes", "es": "Sobres", "ar": "المظاريف"}},
		{Key: "delivered", Kind: nexus.ColumnNumber, Total: true,
			Titles: map[string]string{"mn": "Хүргэгдсэн", "en": "Delivered", "ru": "Доставлено", "zh": "已送达", "fr": "Remises", "es": "Entregados", "ar": "مُسلَّمة"}},
		{Key: "pending", Kind: nexus.ColumnNumber, Total: true,
			Titles: map[string]string{"mn": "Хүлээгдэж буй", "en": "Outstanding", "ru": "Ожидают", "zh": "待送达", "fr": "En attente", "es": "Pendientes", "ar": "معلّقة"}},
		{Key: "retries", Kind: nexus.ColumnNumber, Total: true,
			Titles: map[string]string{"mn": "Дахин оролдлого", "en": "Retries", "ru": "Повторов", "zh": "重试次数", "fr": "Reprises", "es": "Reintentos", "ar": "المحاولات"}},
	}
}

// Run counts what actually went over each link.
//
// Retries are `attempts - 1` summed rather than `attempts`: an envelope that
// went first time has one attempt and no retry, and a column that counted it as
// one would make a healthy channel look like a struggling one.
func (r channelLoad) Run(ctx context.Context, _ nexus.Querier, p nexus.Params) (nexus.Result, error) {
	// Not a query. Deliveries are the channel's rows — the outbox, the
	// attempts, the acknowledgements — and this app stopped reading them on
	// 2026-08-23. The contract answers the one question a report can ask about
	// them, and the engine's Querier is unused here because there is nothing of
	// this app's to join it to.
	load, err := r.peers.DeliveryLoad(ctx, nexus.WorkspaceOf(ctx), p.Time("period_from"), p.Time("period_to"))
	if err != nil {
		return nexus.Result{}, err
	}
	names := reportPeerNames(ctx, r.peers)

	rows := make([]map[string]any, 0, len(load))
	for _, one := range load {
		rows = append(rows, map[string]any{
			"peer": peerLabel(names, one.PeerID), "envelopes": one.Envelopes,
			"delivered": one.Delivered, "pending": one.Pending, "retries": one.Retries,
		})
	}
	return truncated(rows), nil
}

// ----------------------------------------------------------------- helpers

// reportPeerNames is the page-level peer read a report does instead of a join.
//
// A report runs inside the engine's read-only transaction and this read is
// outside it, which is the right way round: the names are the channel's and are
// not part of the snapshot the report is measuring. A deployment that cannot
// answer gets a report with the ids left as labels rather than no report — see
// peerLabel.
func reportPeerNames(ctx context.Context, directory contract.PeerDirectory) map[string]string {
	if directory == nil {
		// A report constructed without one. Nothing in this binary does it —
		// registerReports hands every report the same directory — and a nil
		// dereference inside a report is a panic in whatever goroutine the
		// engine happens to be running it on, which is worse than a column of
		// ids.
		return nil
	}
	peers, err := directory.Peers(ctx, nexus.WorkspaceOf(ctx))
	if err != nil {
		slog.Warn("urtuu: the links could not be read; a report will name peers by id", "error", err)
		return nil
	}
	names := make(map[string]string, len(peers))
	for _, peer := range peers {
		names[peer.ID] = peer.Name
	}
	return names
}

// peerLabel is what to print for a peer id.
//
// An empty id is a task that never left this installation — the dash it gets is
// the same one the SQL used to produce. An id with no name is a link that has
// been revoked since the work went over it: the id is worse than a name and far
// better than an empty column, which would read as "local".
func peerLabel(names map[string]string, peerID string) string {
	switch {
	case peerID == "":
		return "—"
	case names[peerID] != "":
		return names[peerID]
	default:
		return peerID
	}
}

// truncated says so when the limit was reached. A limit that is not said is a
// lie: five hundred rows read as the whole answer.
func truncated(rows []map[string]any) nexus.Result {
	result := nexus.Result{Rows: rows}
	if len(rows) == 500 {
		result.Notes = append(result.Notes, nexus.Note{
			Level:   "warning",
			Message: "Эхний 500 мөрийг харуулав; жагсаалт таслагдсан.",
		})
	}
	return result
}

// round1 keeps a day count readable. Three decimal places of "days late" is
// precision nobody asked for and every export has to carry.
func round1(value float64) float64 {
	return float64(int64(value*10+0.5)) / 10
}
