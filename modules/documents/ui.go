/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// УРИЛГЫН ХУУДАС — backend-ийн үйлчилдэг цорын ганц дэлгэц.
//
// Гэрээний БУСАД дэлгэц бүр бүрхүүлийн дотор амьдардаг:
// `/module/documents/contracts` ба `/module/documents/inbox` — бусад
// documents дэлгэцтэй яг нэг байранд. Аппын дэлгэц апп дотроо байх ёстой.
//
// Энэ нэг хуудас нь үл хамаарах зүйл, ба шалтгаан нь дэлгэцийн биш,
// ХЭРЭГЛЭГЧИЙН: `/contract/<токен>`-ийг нээж буй хүнд ДАНС БАЙХГҮЙ.
// Бүрхүүл нэвтрэлт шаарддаг тул түүний дотор энэ хүнд үзүүлэх юм алга —
// тэдний бүх эрх нь урилгын токен өөрөө (invite.go).
//
// CSP нь `script-src 'self'` тул JS тусдаа хаягаас ирнэ. Хөрөнгүүд нь
// `/contract/assets/` дор: chi-д статик зам параметртэйгээс түрүүлж
// таардаг тул `/contract/{token}` тэдгээрийг токен гэж уншихгүй.
package documents

import (
	_ "embed"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

//go:embed assets/invite.html
var invitePageHTML []byte

//go:embed assets/invite.js
var invitePageJS []byte

//go:embed assets/app.css
var pageCSS []byte

func (m *DocumentsModule) registerUI(r chi.Router) {
	r.Get("/contract/assets/invite.js", m.uiAsset)
	r.Get("/contract/assets/app.css", m.uiAsset)
	r.Get("/contract/{token}", m.invitePage)
}

// invitePage нь КЭШГҮЙ: гэрээний төлөв минут тутам өөрчлөгдөж болно.
func (m *DocumentsModule) invitePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(invitePageHTML)
}

func (m *DocumentsModule) uiAsset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch {
	case strings.HasSuffix(r.URL.Path, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write(invitePageJS)
	case strings.HasSuffix(r.URL.Path, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(pageCSS)
	default:
		http.NotFound(w, r)
	}
}
