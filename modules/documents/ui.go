/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// ГЭРЭЭНИЙ ДЭЛГЭЦ — backend-ийн үйлчилдэг хуудсууд.
//
// ЯАГААД БҮРХҮҮЛИЙН ДОТОР БИШ ВЭ. Цөмийн frontend нь модулийн дэлгэцийг
// ЕРӨНХИЙЛӨН зурдаг механизмгүй: `frontend/app/module/<апп>/<дэлгэц>` доторх
// хуудас бүр тэр репод ГАРААР бичигддэг ба v1.13.0-д баримтын апп нь
// templates, workflows, rails, retention гэх мэт ХУУЧИН дэлгэцүүдтэй,
// гэрээнийх байхгүй. Бид цөмийн репог хөндөхгүй тул зам нь ганц: модуль өөрөө
// HTML үйлчилж, цэсний мөр нь `ExternalURL`-ээр түүн рүү заана.
//
// iframe ч сонголт биш: бүрхүүл хариу бүрд `X-Frame-Options: DENY` явуулдаг.
//
// ГУРВАН ФАЙЛ ЯАГААД. Платформын CSP нь `script-src 'self'` — inline скрипт
// ажиллахгүй тул JS нь тусдаа хаягаас ирнэ.
//
// ХОЁР ҮЗЭГЧ, ХОЁР ХУУДАС:
//
//	/contracts/          нэвтэрсэн хүн — өөрийн гэрээ ба ирсэн гэрээ
//	/contract/<токен>    нэвтрээгүй хүн — урилгын холбоосоор ирсэн нэг гэрээ
//
// Хоёрдугаарх нь `/api/v1/documents` бүлгээс ГАДУУР: тэр бүлэг бүхэлдээ
// tenant-ийн хаалганы дор амьдардаг ба энэ хуудсыг нээж буй хүнд данс
// байхгүй.
package documents

import (
	_ "embed"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

//go:embed assets/app.html
var pageHTML []byte

//go:embed assets/app.js
var pageJS []byte

//go:embed assets/app.css
var pageCSS []byte

//go:embed assets/invite.html
var invitePageHTML []byte

//go:embed assets/invite.js
var invitePageJS []byte

// registerUI нь хоёр хуудсыг ҮНДСЭН router дээр бүрдүүлнэ.
//
// `/documents` БИШ: тэр замыг бүрхүүл эзэмшдэг (цэсний `Path`) ба хоёуланг нь
// нэг угтварт байрлуулбал аль нь хариулахыг байрлуулалтын nginx шийднэ — тэр
// нь нэг өдөр өөрчлөгдөх ба хэн ч анзаарахгүй.
func (m *DocumentsModule) registerUI(r chi.Router) {
	r.Route("/contracts", func(cr chi.Router) {
		// Хуудас өөрөө ба түүний хөрөнгүүд. Хөрөнгө нь эрх шаардахгүй:
		// доторх бүх өгөгдөл API-аас ирдэг ба тэр нь эрхээр хамгаалагдсан.
		cr.Get("/", m.uiPage)
		cr.Get("/app.js", m.uiAsset)
		cr.Get("/app.css", m.uiAsset)
		cr.Get("/invite.js", m.uiAsset)
	})
	// Ташуу зураасгүй хаяг руу орсон хүнийг алдаа рүү биш, хуудас руу.
	// Хэрэв 308 биш бол хөтөч `app.js`-ийг `/app.js`-ээс хайх ба 404 авна.
	r.Get("/contracts", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/contracts/", http.StatusPermanentRedirect)
	})
	r.Get("/contract/{token}", m.invitePage)
}

func (m *DocumentsModule) uiPage(w http.ResponseWriter, r *http.Request) {
	writePage(w, pageHTML)
}

func (m *DocumentsModule) invitePage(w http.ResponseWriter, r *http.Request) {
	writePage(w, invitePageHTML)
}

// writePage нь HTML-ийг КЭШГҮЙ буцаана: гэрээний төлөв минут тутам
// өөрчлөгдөж болно.
func writePage(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func (m *DocumentsModule) uiAsset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch {
	case strings.HasSuffix(r.URL.Path, "/invite.js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write(invitePageJS)
	case strings.HasSuffix(r.URL.Path, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write(pageJS)
	case strings.HasSuffix(r.URL.Path, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(pageCSS)
	default:
		http.NotFound(w, r)
	}
}
