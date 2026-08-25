/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

package documents

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Go-гийн нэрлэсэн ХЯЗГААРЛАЛТ БҮР миграцад БАЙХ ЁСТОЙ.
//
// Энэ тест нэг жинхэнэ алдааны дараа бичигдэв. `insertParty` нь
// `document_parties_registration_unique` гэдгийг хайдаг байсан ба сангийн
// индекс нь `document_parties_one_per_registration` нэртэй. Хоёр нэр хоёулаа
// боломжтой сонсогддог, хоёулаа хөрвүүлэгдэнэ, ба тэдгээрийн зөрүү нь
// ЗӨВХӨН давхардсан регистрийн дугаар илгээгдэх агшинд илэрдэг — тэр үед
// хэрэглэгч «энэ дугаартай тал аль хэдийн байна» гэсэн 409-ийн оронд «тал
// бүртгэгдсэнгүй» гэсэн 500 хардаг. Юуг засахаа мэдэхгүй хэрэглэгч, сангийн
// эвдрэл гэж уншиж буй ажиллуулагч.
//
// Ийм алдааг нэгжийн тестээр барихад жинхэнэ Postgres хэрэгтэй мэт санагддаг.
// Хэрэггүй: нэр нь хоёр газарт бичигдсэн ТЭМДЭГТ МӨР бөгөөд хоёуланг нь энд
// уншиж болно.
func TestEveryConstraintGoNamesExistsInTheMigrations(t *testing.T) {
	schema := readMigrations(t)

	named := regexp.MustCompile(`is(?:Constraint|Check)Violation\([^,]+,\s*"([a-z0-9_]+)"\)`)
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	seen := 0
	for _, file := range sources {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range named.FindAllStringSubmatch(string(body), -1) {
			seen++
			// Хязгаарлалт ба индекс хоёулаа: PostgreSQL нь UNIQUE INDEX
			// зөрчлийн алдаанд индексийн нэрийг хязгаарлалтын нэр болгон
			// тавьдаг тул Go тэднийг ялгадаггүй, энэ тест ч ялгахгүй.
			if !strings.Contains(schema, match[1]) {
				t.Errorf("%s нь %q хязгаарлалтыг нэрлэсэн боловч миграцад тийм нэр алга",
					file, match[1])
			}
		}
	}
	if seen == 0 {
		t.Fatal("нэрлэсэн хязгаарлалт олдсонгүй — regexp хуучирсан байж магадгүй")
	}
}

// Миграц ХООСОН биш байхыг мөн шалгана: уншигдаагүй схемийн эсрэг бүх
// нэр «олдсонгүй» гэж унах ба тэр нь дээрх тестийг ойлгомжгүй болгоно.
func readMigrations(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("migrations", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("миграц олдсонгүй")
	}
	var all strings.Builder
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(body)
	}
	return all.String()
}

// actorFor нь ЗӨВХӨН аудитын мөрөнд.
//
// Тэр функц имэйл буцаадаг бөгөөд яг нэг зорилготой: аудитын бичлэгт хүн
// уншигдахуйц нэр тавих. UUID багана руу өгвөл `NULLIF($n,”)::uuid` нь
// имэйлийг хөрвүүлж чадахгүй, бүхэл хүсэлт 500 болно. Энэ нь таамаг биш:
// eduge.mn дээр анхны байрлуулалт дээр бичвэр хадгалах, файл хавсаргах,
// урилга үүсгэх — нэвтэрсэн хүний хийдэг бараг бүх бичих үйлдэл унасан.
// UUID хэрэгтэй газар `actorID` байна.
func TestActorForFeedsOnlyTheAuditLog(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, file := range sources {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, "actorFor(") || strings.Contains(line, "func actorFor") {
				continue
			}
			seen++
			if !strings.Contains(line, "nexus.Audit(") {
				t.Errorf("%s:%d: actorFor нь аудитаас гадуур хэрэглэгдэв — "+
					"UUID багана бол actorID хэрэгтэй:\n\t%s", file, n+1, strings.TrimSpace(line))
			}
		}
	}
	if seen == 0 {
		t.Fatal("actorFor-ийн хэрэглээ олдсонгүй — шалгалт хуучирсан байж магадгүй")
	}
}
