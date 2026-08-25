/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// ГЭРЭЭНИЙ ТАЙЛАНГУУД.
//
// Тайлан бол дэлгэц ч биш, query ч биш — ЗАРЛАЛ. Модуль нь юу нэрлэгдэхээ,
// ямар параметр авахаа, ямар багана гаргахаа хэлнэ; жагсаалтын дэлгэц,
// параметрийн маягт, хүснэгт, график, Excel, товлолт, хэн ажиллуулсны
// бүртгэл — бүгд платформд нэг удаа бичигдсэн.
//
// # ЯАГААД ЭДГЭЭР ТАЙЛАН
//
// Гэрээний систем дээр гурван өөр хүн гурван өөр асуулт асуудаг:
//
//   - ЗАХИРГАА: «Одоо хэдэн гэрээ хүлээгдэж байна, хэн дээр гацсан бэ?»
//     Энэ бол `awaiting_signature` — өдөр тутам ажиллах ганц тайлан.
//   - ХУУЛЬ: «Энэ гэрээнд хэн, хэзээ, ямар аргаар гарын үсэг зурсан бэ?»
//     Энэ бол `signature_ledger` — маргааны үед хэвлэгддэг зүйл.
//   - УДИРДЛАГА: «Хэдэн гэрээ байгуулав, хэд нь татгалзагдав, дүн нь хэд вэ?»
//     Энэ бол `contract_register` ба `contract_funnel`.
//
// Тэдгээрийг нэг тайланд нийлүүлэх нь гурвуулангийнх нь хариултыг муутгана.
//
// # ХАМРАХ ХҮРЭЭ
//
// Тайлан нь дуудагчийн байгууллагад холбогдсон `Querier` авдаг ба сангийн мөр
// түвшний бодлогын дор ажилладаг. Гэсэн ч өгүүлбэр бүр `tenant_id = $1`-ээ
// өөрөө нэрлэнэ: бодлого бол баталгаа, WHERE бол зорилго.
package documents

import (
	"context"
	"strconv"
	"time"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// reportApp нь эдгээр тайлан аль аппад харьяалагдахыг хэлнэ. Суулгаагүй
// байгууллагад тайлан нь ОГТ харагдахгүй.
const reportApp = "io.gerege.nexus.documents"

// registerReports нь модулийн байгуулагчаас дуудагдана.
func registerReports() {
	nexus.RegisterReport(contractRegister{})
	nexus.RegisterReport(awaitingSignature{})
	nexus.RegisterReport(signatureLedger{})
	nexus.RegisterReport(contractFunnel{})
	nexus.RegisterReport(expiringContracts{})
}

// t нь долоон хэлний гарчгийг богиносгоно. Платформ mn → en → түлхүүр гэж
// уначихдаг тул бүгдийг нь бөглөх шаардлагагүй ч, монгол нь ЗААВАЛ.
func t(mn, en, ru, zh, fr, es, ar string) map[string]string {
	return map[string]string{"mn": mn, "en": en, "ru": ru, "zh": zh, "fr": fr, "es": es, "ar": ar}
}

func contractPeriod(window time.Duration) nexus.ParamSpec {
	return nexus.ParamSpec{
		Key:           "period",
		Kind:          nexus.ParamDateRange,
		Titles:        t("Хугацаа", "Period", "Период", "期间", "Période", "Periodo", "الفترة"),
		DefaultWindow: window,
	}
}

// contractStates нь тайлангийн шүүлтүүр дэх төлвүүд.
func contractStateParam() nexus.ParamSpec {
	return nexus.ParamSpec{
		Key:    "state",
		Kind:   nexus.ParamSelect,
		Titles: t("Төлөв", "State", "Состояние", "状态", "État", "Estado", "الحالة"),
		Options: []nexus.ParamOption{
			{Value: "", Titles: t("Бүгд", "All", "Все", "全部", "Tous", "Todos", "الكل")},
			{Value: ContractSent, Titles: t("Илгээгдсэн", "Sent", "Отправлен", "已发送", "Envoyé", "Enviado", "مرسل")},
			{Value: ContractPartial, Titles: t("Хэсэгчлэн зурагдсан", "Partially signed", "Частично подписан", "部分签署", "Partiellement signé", "Parcialmente firmado", "موقع جزئيا")},
			{Value: ContractExecuted, Titles: t("Хүчин төгөлдөр", "Executed", "Исполнен", "已生效", "Exécuté", "Ejecutado", "نافذ")},
			{Value: ContractDeclined, Titles: t("Татгалзсан", "Declined", "Отклонён", "已拒绝", "Refusé", "Rechazado", "مرفوض")},
			{Value: ContractWithdrawn, Titles: t("Эргүүлж татсан", "Withdrawn", "Отозван", "已撤回", "Retiré", "Retirado", "مسحوب")},
		},
	}
}

// ═══════════════════════════════════════════════════ 1. Гэрээний бүртгэл

type contractRegister struct{}

func (contractRegister) Key() string { return "documents.contract_register" }
func (contractRegister) App() string { return reportApp }
func (contractRegister) Titles() map[string]string {
	return t("Гэрээний бүртгэл", "Contract register", "Реестр договоров",
		"合同登记簿", "Registre des contrats", "Registro de contratos", "سجل العقود")
}
func (contractRegister) Params() []nexus.ParamSpec {
	return []nexus.ParamSpec{contractPeriod(365 * 24 * time.Hour), contractStateParam()}
}
func (contractRegister) Columns() []nexus.ColumnSpec {
	return []nexus.ColumnSpec{
		{Key: "number", Kind: nexus.ColumnText, Titles: t("Дугаар", "Number", "Номер", "编号", "Numéro", "Número", "الرقم")},
		{Key: "title", Kind: nexus.ColumnText, Titles: t("Гэрээ", "Contract", "Договор", "合同", "Contrat", "Contrato", "العقد")},
		{Key: "counterparties", Kind: nexus.ColumnText, Titles: t("Талууд", "Counterparties", "Контрагенты", "对方", "Contreparties", "Contrapartes", "الأطراف")},
		{Key: "state", Kind: nexus.ColumnText, Titles: t("Төлөв", "State", "Состояние", "状态", "État", "Estado", "الحالة")},
		{Key: "signed_of", Kind: nexus.ColumnText, Titles: t("Гарын үсэг", "Signed", "Подписей", "签署", "Signés", "Firmados", "التوقيعات")},
		{Key: "sent_at", Kind: nexus.ColumnDate, Titles: t("Илгээсэн", "Sent", "Отправлен", "发送日", "Envoyé", "Enviado", "أُرسل")},
		{Key: "executed_at", Kind: nexus.ColumnDate, Titles: t("Хүчин төгөлдөр", "Executed", "Исполнен", "生效日", "Exécuté", "Ejecutado", "نفذ")},
		{Key: "amount", Kind: nexus.ColumnMoney, Total: true, Titles: t("Дүн", "Amount", "Сумма", "金额", "Montant", "Importe", "المبلغ")},
	}
}

// Run — нэг гэрээ нэг мөр. Талуудыг НЭГТГЭЖ гаргана: гэрээ бүрийг талын
// тоогоор нь давхардуулбал «хэдэн гэрээ байгуулав» гэдэг тоо буруу болно.
func (contractRegister) Run(ctx context.Context, q nexus.Querier, p nexus.Params) (nexus.Result, error) {
	const query = `
		SELECT r.contract_number, r.title,
		       COALESCE(string_agg(DISTINCT c.display_name, ', ')
		                FILTER (WHERE c.party_role <> 'issuer'), ''),
		       r.contract_state,
		       count(*) FILTER (WHERE c.party_role <> 'issuer' AND c.required AND c.state = 'signed'),
		       count(*) FILTER (WHERE c.party_role <> 'issuer' AND c.required),
		       r.sent_at, r.executed_at, COALESCE(r.amount, 0)::float8, COALESCE(r.currency, '')
		  FROM document_records r
		  LEFT JOIN document_parties c ON c.document_id = r.id AND c.tenant_id = r.tenant_id
		 WHERE r.tenant_id = $1
		   -- "Гэрээ мөн үү" гэдгийг signing_mode-оор БИШ, төлвөөр асууна:
		   -- горим нь ИЛГЭЭХ агшинд бичигддэг тул хараахан илгээгээгүй
		   -- гэрээ бүр бүртгэлээс унах байсан. contract_state нь анхны тал
		   -- нэмэгдэх агшинд NONE-оос DRAFT болдог.
		   AND r.contract_state <> 'NONE'
		   AND r.created_at >= $2 AND r.created_at <= $3
		   AND ($4 = '' OR r.contract_state = $4)
		 GROUP BY r.id
		 ORDER BY r.created_at DESC`

	rows, err := q.Query(ctx, query, nexus.TenantOf(ctx),
		p.Time("period_from"), p.Time("period_to"), p.String("state"))
	if err != nil {
		return nexus.Result{}, err
	}
	collected, err := nexus.Collect(rows, func() (map[string]any, error) {
		var number, title, others, state, currency string
		var signed, required int64
		var sentAt, executedAt *time.Time
		var amount float64
		if err := rows.Scan(&number, &title, &others, &state, &signed, &required,
			&sentAt, &executedAt, &amount, &currency); err != nil {
			return nil, err
		}
		return map[string]any{
			"number": number, "title": title, "counterparties": others,
			"state":     contractStateLabel(state, p.Locale()),
			"signed_of": signedOf(signed, required),
			"sent_at":   sentAt, "executed_at": executedAt, "amount": amount,
		}, nil
	})
	if err != nil {
		return nexus.Result{}, err
	}
	return nexus.Result{Rows: collected}, nil
}

// ═══════════════════════════════════════════════════ 2. Хүлээгдэж буй гарын үсэг

type awaitingSignature struct{}

func (awaitingSignature) Key() string { return "documents.awaiting_signature" }
func (awaitingSignature) App() string { return reportApp }
func (awaitingSignature) Titles() map[string]string {
	return t("Гарын үсэг хүлээгдэж буй", "Awaiting signature", "Ожидают подписи",
		"待签署", "En attente de signature", "Pendientes de firma", "بانتظار التوقيع")
}
func (awaitingSignature) Params() []nexus.ParamSpec {
	return []nexus.ParamSpec{{
		Key:    "overdue_only",
		Kind:   nexus.ParamBool,
		Titles: t("Зөвхөн хугацаа хэтэрсэн", "Overdue only", "Только просроченные", "仅逾期", "En retard seulement", "Sólo vencidos", "المتأخرة فقط"),
	}}
}
func (awaitingSignature) Columns() []nexus.ColumnSpec {
	return []nexus.ColumnSpec{
		{Key: "title", Kind: nexus.ColumnText, Titles: t("Гэрээ", "Contract", "Договор", "合同", "Contrat", "Contrato", "العقد")},
		{Key: "party", Kind: nexus.ColumnText, Titles: t("Хүлээгдэж буй тал", "Party", "Сторона", "对方", "Partie", "Parte", "الطرف")},
		{Key: "signatory", Kind: nexus.ColumnText, Titles: t("Гарын үсэг зурах хүн", "Signatory", "Подписант", "签署人", "Signataire", "Firmante", "الموقع")},
		{Key: "state", Kind: nexus.ColumnText, Titles: t("Төлөв", "State", "Состояние", "状态", "État", "Estado", "الحالة")},
		{Key: "sent_at", Kind: nexus.ColumnDate, Titles: t("Илгээсэн", "Sent", "Отправлен", "发送日", "Envoyé", "Enviado", "أُرسل")},
		{Key: "days", Kind: nexus.ColumnNumber, Titles: t("Хоног", "Days", "Дней", "天数", "Jours", "Días", "الأيام")},
		{Key: "due_at", Kind: nexus.ColumnDate, Titles: t("Эцсийн хугацаа", "Due", "Срок", "截止日", "Échéance", "Vencimiento", "الاستحقاق")},
	}
}

// Run — хугацаагүй. Энэ бол ажлын жагсаалт, түүхэн тайлан биш: хамгийн эртний
// нь хамгийн дээр, учир нь тэр хамгийн их гацсан нь.
func (awaitingSignature) Run(ctx context.Context, q nexus.Querier, p nexus.Params) (nexus.Result, error) {
	const query = `
		SELECT c.doc_title, c.display_name,
		       COALESCE(string_agg(g.full_name, ', ' ORDER BY g.created_at)
		                FILTER (WHERE g.signed_at IS NULL), ''),
		       c.state, c.invited_at,
		       GREATEST(0, EXTRACT(DAY FROM NOW() - c.invited_at))::int,
		       c.doc_due_at
		  FROM document_parties c
		  LEFT JOIN document_party_signatories g ON g.party_id = c.id
		 WHERE c.tenant_id = $1
		   AND c.party_role <> 'issuer'
		   AND c.required
		   AND c.state IN ('invited', 'viewed')
		   AND (NOT $2 OR (c.doc_due_at IS NOT NULL AND c.doc_due_at < NOW()))
		 GROUP BY c.id
		 ORDER BY c.invited_at NULLS LAST`

	rows, err := q.Query(ctx, query, nexus.TenantOf(ctx), p.Bool("overdue_only"))
	if err != nil {
		return nexus.Result{}, err
	}
	collected, err := nexus.Collect(rows, func() (map[string]any, error) {
		var title, party, signatory, state string
		var invitedAt, dueAt *time.Time
		var days int64
		if err := rows.Scan(&title, &party, &signatory, &state, &invitedAt, &days, &dueAt); err != nil {
			return nil, err
		}
		return map[string]any{
			"title": title, "party": party, "signatory": signatory,
			"state":   partyStateLabel(state, p.Locale()),
			"sent_at": invitedAt, "days": days, "due_at": dueAt,
		}, nil
	})
	if err != nil {
		return nexus.Result{}, err
	}
	return nexus.Result{Rows: collected}, nil
}

// ═══════════════════════════════════════════════════ 3. Гарын үсгийн бүртгэл

type signatureLedger struct{}

func (signatureLedger) Key() string { return "documents.signature_ledger" }
func (signatureLedger) App() string { return reportApp }
func (signatureLedger) Titles() map[string]string {
	return t("Гарын үсгийн бүртгэл", "Signature ledger", "Журнал подписей",
		"签署记录", "Registre des signatures", "Libro de firmas", "سجل التوقيعات")
}
func (signatureLedger) Params() []nexus.ParamSpec {
	return []nexus.ParamSpec{contractPeriod(365 * 24 * time.Hour)}
}
func (signatureLedger) Columns() []nexus.ColumnSpec {
	return []nexus.ColumnSpec{
		{Key: "signed_at", Kind: nexus.ColumnDate, Titles: t("Огноо", "Date", "Дата", "日期", "Date", "Fecha", "التاريخ")},
		{Key: "title", Kind: nexus.ColumnText, Titles: t("Гэрээ", "Contract", "Договор", "合同", "Contrat", "Contrato", "العقد")},
		{Key: "party", Kind: nexus.ColumnText, Titles: t("Тал", "Party", "Сторона", "对方", "Partie", "Parte", "الطرف")},
		{Key: "signer", Kind: nexus.ColumnText, Titles: t("Гарын үсэг зурсан", "Signer", "Подписал", "签署人", "Signataire", "Firmante", "الموقع")},
		{Key: "reg_number", Kind: nexus.ColumnText, Titles: t("Регистр", "Reg. number", "Рег. номер", "登记号", "N° reg.", "N.º reg.", "رقم التسجيل")},
		{Key: "method", Kind: nexus.ColumnText, Titles: t("Арга", "Method", "Способ", "方式", "Méthode", "Método", "الطريقة")},
		{Key: "format", Kind: nexus.ColumnText, Titles: t("Хэлбэр", "Format", "Формат", "格式", "Format", "Formato", "الصيغة")},
		{Key: "covered_digest", Kind: nexus.ColumnText, Titles: t("Хамарсан хеш", "Covered digest", "Хеш документа", "覆盖摘要", "Empreinte couverte", "Hash cubierto", "بصمة المشمول")},
	}
}

// Run — ХАМАРСАН ХЕШ нь энэ тайлангийн гол багана.
//
// Маргаан «гарын үсэг зурагдсан уу» дээр тулдаггүй, «ЮУНД зурагдсан бэ» дээр
// тулдаг. `covered_digest` нь тэр асуултын цорын ганц хариулт бөгөөд түүнийг
// хэвлэлтээс хассан тайлан нь маргаанд хэрэглэгдэхгүй.
func (signatureLedger) Run(ctx context.Context, q nexus.Querier, p nexus.Params) (nexus.Result, error) {
	const query = `
		SELECT s.signed_at, r.title, COALESCE(c.display_name, ''),
		       s.signer_name, s.signer_reg_number, s.signer_method,
		       COALESCE(s.format, 'approval'), COALESCE(s.covered_digest, '')
		  FROM document_signatures s
		  JOIN document_records r ON r.id = s.document_id
		  LEFT JOIN document_parties c ON c.id = s.party_id
		 WHERE s.tenant_id = $1
		   AND s.signed_at >= $2 AND s.signed_at <= $3
		 ORDER BY s.signed_at DESC`

	rows, err := q.Query(ctx, query, nexus.TenantOf(ctx),
		p.Time("period_from"), p.Time("period_to"))
	if err != nil {
		return nexus.Result{}, err
	}
	collected, err := nexus.Collect(rows, func() (map[string]any, error) {
		var signedAt time.Time
		var title, party, signer, reg, method, format, digest string
		if err := rows.Scan(&signedAt, &title, &party, &signer, &reg, &method,
			&format, &digest); err != nil {
			return nil, err
		}
		return map[string]any{
			"signed_at": signedAt, "title": title, "party": party,
			"signer": signer, "reg_number": reg, "method": method,
			"format": format, "covered_digest": digest,
		}, nil
	})
	if err != nil {
		return nexus.Result{}, err
	}
	return nexus.Result{Rows: collected}, nil
}

// ═══════════════════════════════════════════════════ 4. Гэрээний хэлхээ

type contractFunnel struct{}

func (contractFunnel) Key() string { return "documents.contract_funnel" }
func (contractFunnel) App() string { return reportApp }
func (contractFunnel) Titles() map[string]string {
	return t("Гэрээний урсгал", "Contract funnel", "Воронка договоров",
		"合同漏斗", "Entonnoir des contrats", "Embudo de contratos", "مسار العقود")
}
func (contractFunnel) Params() []nexus.ParamSpec {
	return []nexus.ParamSpec{contractPeriod(180 * 24 * time.Hour)}
}
func (contractFunnel) Columns() []nexus.ColumnSpec {
	return []nexus.ColumnSpec{
		{Key: "state", Kind: nexus.ColumnText, Chart: nexus.ChartCategory,
			Titles: t("Төлөв", "State", "Состояние", "状态", "État", "Estado", "الحالة")},
		{Key: "contracts", Kind: nexus.ColumnNumber, Chart: nexus.ChartValue, Total: true,
			Titles: t("Гэрээ", "Contracts", "Договоров", "合同数", "Contrats", "Contratos", "العقود")},
		{Key: "amount", Kind: nexus.ColumnMoney, Total: true,
			Titles: t("Дүн", "Amount", "Сумма", "金额", "Montant", "Importe", "المبلغ")},
	}
}

func (contractFunnel) Run(ctx context.Context, q nexus.Querier, p nexus.Params) (nexus.Result, error) {
	const query = `
		SELECT contract_state, count(*), COALESCE(sum(amount), 0)::float8
		  FROM document_records
		 WHERE tenant_id = $1
		   AND contract_state <> 'NONE'
		   AND created_at >= $2 AND created_at <= $3
		 GROUP BY contract_state
		 ORDER BY count(*) DESC`

	rows, err := q.Query(ctx, query, nexus.TenantOf(ctx),
		p.Time("period_from"), p.Time("period_to"))
	if err != nil {
		return nexus.Result{}, err
	}
	collected, err := nexus.Collect(rows, func() (map[string]any, error) {
		var state string
		var count int64
		var amount float64
		if err := rows.Scan(&state, &count, &amount); err != nil {
			return nil, err
		}
		return map[string]any{
			"state":     contractStateLabel(state, p.Locale()),
			"contracts": count, "amount": amount,
		}, nil
	})
	if err != nil {
		return nexus.Result{}, err
	}
	return nexus.Result{Rows: collected}, nil
}

// ═══════════════════════════════════════════════════ 5. Дуусах гэрээ

type expiringContracts struct{}

func (expiringContracts) Key() string { return "documents.expiring_contracts" }
func (expiringContracts) App() string { return reportApp }
func (expiringContracts) Titles() map[string]string {
	return t("Дуусах гэрээ", "Expiring contracts", "Истекающие договоры",
		"即将到期的合同", "Contrats arrivant à échéance", "Contratos por vencer", "العقود المنتهية")
}
func (expiringContracts) Params() []nexus.ParamSpec {
	return []nexus.ParamSpec{{
		Key:    "horizon",
		Kind:   nexus.ParamSelect,
		Titles: t("Хугацаа", "Horizon", "Горизонт", "范围", "Horizon", "Horizonte", "الأفق"),
		Options: []nexus.ParamOption{
			{Value: "30", Titles: t("30 хоног", "30 days", "30 дней", "30 天", "30 jours", "30 días", "٣٠ يوما")},
			{Value: "60", Titles: t("60 хоног", "60 days", "60 дней", "60 天", "60 jours", "60 días", "٦٠ يوما")},
			{Value: "90", Titles: t("90 хоног", "90 days", "90 дней", "90 天", "90 jours", "90 días", "٩٠ يوما")},
			{Value: "365", Titles: t("1 жил", "1 year", "1 год", "1 年", "1 an", "1 año", "سنة")},
		},
		Default: "90",
	}}
}
func (expiringContracts) Columns() []nexus.ColumnSpec {
	return []nexus.ColumnSpec{
		{Key: "number", Kind: nexus.ColumnText, Titles: t("Дугаар", "Number", "Номер", "编号", "Numéro", "Número", "الرقم")},
		{Key: "title", Kind: nexus.ColumnText, Titles: t("Гэрээ", "Contract", "Договор", "合同", "Contrat", "Contrato", "العقد")},
		{Key: "counterparties", Kind: nexus.ColumnText, Titles: t("Талууд", "Counterparties", "Контрагенты", "对方", "Contreparties", "Contrapartes", "الأطراف")},
		{Key: "effective_to", Kind: nexus.ColumnDate, Titles: t("Дуусах", "Expires", "Истекает", "到期日", "Expire", "Vence", "ينتهي")},
		{Key: "days_left", Kind: nexus.ColumnNumber, Titles: t("Үлдсэн хоног", "Days left", "Осталось дней", "剩余天数", "Jours restants", "Días restantes", "الأيام المتبقية")},
		{Key: "amount", Kind: nexus.ColumnMoney, Total: true, Titles: t("Дүн", "Amount", "Сумма", "金额", "Montant", "Importe", "المبلغ")},
	}
}

// Run — зөвхөн ХҮЧИН ТӨГӨЛДӨР гэрээ. Ноорог гэрээ дуусахгүй, татгалзсан
// гэрээ ч дуусахгүй: хугацаа нь зөвхөн байгуулагдсан зүйлд гүйнэ.
func (expiringContracts) Run(ctx context.Context, q nexus.Querier, p nexus.Params) (nexus.Result, error) {
	horizon := p.String("horizon")
	if horizon == "" {
		horizon = "90"
	}
	const query = `
		SELECT r.contract_number, r.title,
		       COALESCE(string_agg(DISTINCT c.display_name, ', ')
		                FILTER (WHERE c.party_role <> 'issuer'), ''),
		       r.effective_to,
		       (r.effective_to - CURRENT_DATE)::int,
		       COALESCE(r.amount, 0)::float8
		  FROM document_records r
		  LEFT JOIN document_parties c ON c.document_id = r.id AND c.tenant_id = r.tenant_id
		 WHERE r.tenant_id = $1
		   AND r.contract_state = 'EXECUTED'
		   AND r.terminated_at IS NULL
		   AND r.effective_to IS NOT NULL
		   AND r.effective_to >= CURRENT_DATE
		   AND r.effective_to <= CURRENT_DATE + ($2 || ' days')::interval
		 GROUP BY r.id
		 ORDER BY r.effective_to`

	rows, err := q.Query(ctx, query, nexus.TenantOf(ctx), horizon)
	if err != nil {
		return nexus.Result{}, err
	}
	collected, err := nexus.Collect(rows, func() (map[string]any, error) {
		var number, title, others string
		var expires time.Time
		var daysLeft int64
		var amount float64
		if err := rows.Scan(&number, &title, &others, &expires, &daysLeft, &amount); err != nil {
			return nil, err
		}
		return map[string]any{
			"number": number, "title": title, "counterparties": others,
			"effective_to": expires, "days_left": daysLeft, "amount": amount,
		}, nil
	})
	if err != nil {
		return nexus.Result{}, err
	}
	return nexus.Result{Rows: collected}, nil
}

// ═══════════════════════════════════════════════════ шошго

func signedOf(signed, required int64) string {
	if required == 0 {
		return "—"
	}
	return strconv.FormatInt(signed, 10) + " / " + strconv.FormatInt(required, 10)
}

// contractStateLabel нь баримтын нэгтгэсэн төлвийг уншигдахуйц болгоно.
//
// Түлхүүрийг ХЭВЭЭР үлдээж, шошгыг тусад нь орчуулна: тайлангийн Excel-д
// 'PARTIALLY_SIGNED' гэж гарах нь монгол уншигчид хариулт биш.
func contractStateLabel(state, locale string) string {
	labels := map[string]map[string]string{
		ContractNone:      t("—", "—", "—", "—", "—", "—", "—"),
		ContractDraft:     t("Ноорог", "Draft", "Черновик", "草稿", "Brouillon", "Borrador", "مسودة"),
		ContractSent:      t("Илгээгдсэн", "Sent", "Отправлен", "已发送", "Envoyé", "Enviado", "مرسل"),
		ContractPartial:   t("Хэсэгчлэн зурагдсан", "Partially signed", "Частично подписан", "部分签署", "Partiellement signé", "Parcialmente firmado", "موقع جزئيا"),
		ContractExecuted:  t("Хүчин төгөлдөр", "Executed", "Исполнен", "已生效", "Exécuté", "Ejecutado", "نافذ"),
		ContractDeclined:  t("Татгалзсан", "Declined", "Отклонён", "已拒绝", "Refusé", "Rechazado", "مرفوض"),
		ContractWithdrawn: t("Эргүүлж татсан", "Withdrawn", "Отозван", "已撤回", "Retiré", "Retirado", "مسحوب"),
		"EXPIRED":         t("Хугацаа дууссан", "Expired", "Истёк", "已过期", "Expiré", "Vencido", "منتهي"),
		"TERMINATED":      t("Цуцлагдсан", "Terminated", "Расторгнут", "已终止", "Résilié", "Rescindido", "منهي"),
	}
	if titles, ok := labels[state]; ok {
		return nexus.LocalizedTitle(titles, locale, state)
	}
	return state
}

func partyStateLabel(state, locale string) string {
	labels := map[string]map[string]string{
		PartyDraft:     t("Ноорог", "Draft", "Черновик", "草稿", "Brouillon", "Borrador", "مسودة"),
		PartyInvited:   t("Илгээгдсэн", "Sent", "Отправлено", "已发送", "Envoyé", "Enviado", "مرسل"),
		PartyViewed:    t("Уншсан", "Opened", "Просмотрено", "已查看", "Ouvert", "Abierto", "مفتوح"),
		PartySigned:    t("Гарын үсэг зурсан", "Signed", "Подписано", "已签署", "Signé", "Firmado", "موقع"),
		PartyDeclined:  t("Татгалзсан", "Declined", "Отклонено", "已拒绝", "Refusé", "Rechazado", "مرفوض"),
		PartyWithdrawn: t("Эргүүлж татсан", "Withdrawn", "Отозвано", "已撤回", "Retiré", "Retirado", "مسحوب"),
		PartyExpired:   t("Хугацаа дууссан", "Expired", "Истекло", "已过期", "Expiré", "Vencido", "منتهي"),
	}
	if titles, ok := labels[state]; ok {
		return nexus.LocalizedTitle(titles, locale, state)
	}
	return state
}
