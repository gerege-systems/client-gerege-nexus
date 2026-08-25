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

// Хуудас нэрлэсэн хөрөнгө нь ҮЙЛЧЛЭГДЭХ ЁСТОЙ.
//
// Энэ бол хамгийн хямд, хамгийн их үнэ цэнэтэй шалгалт: HTML нь `app.js` гэж
// нэрлэдэг, `ui.go` нь `/contracts/app.js`-ийг үйлчилдэг, ба хоёрын хооронд
// байгаа нь зөвхөн ижил тэмдэгт мөр. Файлыг нэрлэхээ өөрчилсөн хүн нөгөө
// талыг мартвал хуудас чимээгүй хоосон ачаалагдана — 404 нь консол дээр л
// гарах ба хэн ч харахгүй.
func TestEveryAssetThePageNamesIsServed(t *testing.T) {
	router := chi.NewRouter()
	(&DocumentsModule{}).registerUI(router)

	src := regexp.MustCompile(`(?:src|href)="([^"]+)"`)
	for _, page := range []struct {
		name, path string
		html       []byte
	}{
		{"консол", "/contracts/", pageHTML},
		{"урилга", "/contract/abc", invitePageHTML},
	} {
		for _, match := range src.FindAllStringSubmatch(string(page.html), -1) {
			ref := match[1]
			// Хэсгийн холбоос (#/inbox) нь хөрөнгө биш, хуудасны өөрийн
			// зам — сервер түүнийг хэзээ ч хардаггүй.
			if strings.HasPrefix(ref, "http") || strings.HasPrefix(ref, "#") || ref == "/" {
				continue
			}
			if i := strings.Index(ref, "#"); i >= 0 {
				ref = ref[:i]
			}
			if ref == "" || ref == "/contracts/" {
				continue
			}
			// Харьцангуй замыг хуудсынхаа хавтас дээр шийднэ.
			asset := ref
			if !strings.HasPrefix(ref, "/") {
				asset = "/contracts/" + ref
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, asset, nil))
			if rec.Code != http.StatusOK {
				t.Errorf("%s хуудас %q-г нэрлэсэн боловч тэр нь %d буцаав", page.name, asset, rec.Code)
			}
			if rec.Body.Len() == 0 {
				t.Errorf("%s: %q хоосон ирэв", page.name, asset)
			}
		}
	}
}

// Хөтөч файлыг зөв уншихын тулд төрөл нь зөв байх ёстой: буруу
// `Content-Type`-тай JS нь `nosniff`-ийн дор ОГТ ажиллахгүй.
func TestAssetsCarryTheirType(t *testing.T) {
	router := chi.NewRouter()
	(&DocumentsModule{}).registerUI(router)

	cases := map[string]string{
		"/contracts/app.js":    "application/javascript",
		"/contracts/invite.js": "application/javascript",
		"/contracts/app.css":   "text/css",
		"/contracts/":          "text/html",
		"/contract/some-token": "text/html",
	}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, want) {
			t.Errorf("%s: төрөл нь %q, %q байх ёстой", path, got, want)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: nosniff алга", path)
		}
	}
}

// `/contracts` (ташуу зураасгүй) нь хуудас руу заана.
//
// Үүнгүйгээр хөтөч `app.js`-ийг `/app.js`-ээс хайх ба хуудас скриптгүйгээр
// ачаална — цагаан дэлгэц, алдааны мэдээлэлгүй.
func TestTheSlashlessAddressLandsOnThePage(t *testing.T) {
	router := chi.NewRouter()
	(&DocumentsModule{}).registerUI(router)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/contracts", nil))
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("ташуу зураасгүй хаяг %d буцаав, 308 байх ёстой", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/contracts/" {
		t.Errorf("хаяглалт %q, /contracts/ байх ёстой", got)
	}
}

// Урилгын хуудсыг нэвтрээгүй хүн нээдэг тул хөрөнгүүд нь АБСОЛЮТ зам байх
// ёстой: `/contract/<токен>` доор харьцангуй `app.css` нь токенийг хавтас гэж
// уншина.
func TestTheInvitePageAsksForAbsoluteAssets(t *testing.T) {
	for _, ref := range regexp.MustCompile(`(?:src|href)="([^"]+)"`).
		FindAllStringSubmatch(string(invitePageHTML), -1) {
		if !strings.HasPrefix(ref[1], "/") && !strings.HasPrefix(ref[1], "http") {
			t.Errorf("урилгын хуудас харьцангуй зам нэрлэв: %q", ref[1])
		}
	}
}
