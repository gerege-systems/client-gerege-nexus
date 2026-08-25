package documents

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Статик зам нь параметртэй замыг ДАРАХ ЁСТОЙ.
//
// `/inbox` ба `/contracts` хоёул `/{id}` -тэй нэг түвшинд амьдардаг. chi
// статикийг эрхэмлэдэг гэдгийг МЭДЭХ нь хангалтгүй — тэр нь энэ хүснэгтийн
// шинж чанар, ба маршрут нэмэх бүрд дахин үнэн байх албагүй.
func TestStaticRoutesWinOverTheDocumentID(t *testing.T) {
	router := routerFor(t, "documents.read").(*chi.Mux)
	for _, want := range []string{
		"/api/v1/documents/inbox", "/api/v1/documents/contracts",
	} {
		found := false
		_ = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if method == http.MethodGet && route == want {
				found = true
			}
			return nil
		})
		if !found {
			t.Errorf("%s хүснэгтэд алга", want)
		}
		rctx := chi.NewRouteContext()
		if !router.Match(rctx, http.MethodGet, want) {
			t.Errorf("%s таарсангүй", want)
		}
		if id := rctx.URLParam("id"); id != "" {
			t.Errorf("%s нь /{id} рүү унав (id=%q)", want, id)
		}
	}
}

// Хүлээн авагчийн бүх маршрут ТАЛЫН id-гаар хаяглагдана, баримтын id-гаар биш.
func TestTheInboxIsAddressedByParty(t *testing.T) {
	router := routerFor(t, "documents.read").(*chi.Mux)
	_ = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/v1/documents/inbox") {
			return nil
		}
		if strings.Contains(route, "{id}") {
			t.Errorf("%s %s нь баримтын id нэрлэсэн — хүлээн авагч түүнийг эзэмшдэггүй", method, route)
		}
		return nil
	})
}
