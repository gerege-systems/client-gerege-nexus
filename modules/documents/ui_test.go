/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

package documents

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Урилгын хуудасны нэрлэсэн хөрөнгө бүр ҮЙЛЧЛЭГДЭХ ёстой.
//
// HTML нь замыг тэмдэгт мөрөөр нэрлэдэг, ui.go нь өөрийнхөөрөө — хоёрын
// хооронд зөвхөн ижил бичиглэл л байдаг. Аль нэгийг нь өөрчилсөн хүн нөгөөг
// мартвал хуудас чимээгүй хоосон ачаалагдана: 404 нь зөвхөн консол дээр.
func TestEveryAssetTheInvitePageNamesIsServed(t *testing.T) {
	router := chi.NewRouter()
	(&DocumentsModule{}).registerUI(router)

	refs := regexp.MustCompile(`(?:src|href)="([^"]+)"`).
		FindAllStringSubmatch(string(invitePageHTML), -1)
	checked := 0
	for _, match := range refs {
		ref := match[1]
		if strings.HasPrefix(ref, "http") || strings.HasPrefix(ref, "#") || ref == "/" {
			continue
		}
		// Нэвтрэлтгүй хуудас АБСОЛЮТ зам л нэрлэнэ: /contract/<токен> доор
		// харьцангуй зам токенийг хавтас гэж уншина.
		if !strings.HasPrefix(ref, "/") {
			t.Errorf("урилгын хуудас харьцангуй зам нэрлэв: %q", ref)
			continue
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ref, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("хуудас %q-г нэрлэсэн боловч тэр нь %d буцаав", ref, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%q хоосон ирэв", ref)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("нэг ч хөрөнгө олдсонгүй — regexp эсвэл хуудас хуучирсан")
	}
}

// Хөрөнгийн зам нь токены замаас ТҮРҮҮЛЖ таарах ёстой: chi статикийг
// эрхэмлэдэг гэдэгт энэ хүснэгт тулгуурладаг тул тэр нь энд батлагдана.
func TestAssetPathsAreNotSwallowedByTheTokenRoute(t *testing.T) {
	router := chi.NewRouter()
	(&DocumentsModule{}).registerUI(router)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/contract/assets/app.css", nil))
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Errorf("app.css нь %q төрөлтэй ирэв — токены зам залгичихсан байж магадгүй", got)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/contract/some-token", nil))
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("токены хуудас %q төрөлтэй ирэв", got)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff алга")
	}
}
