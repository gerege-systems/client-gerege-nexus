# Gerege Client — client.gerege.mn

[Gerege Nexus](https://github.com/gerege-systems/open-gerege-nexus) платформын
**Түвшин 2 distribution**. Онцлог нь нэг өгүүлбэрээр: **энэ суулгац хүнийг
өөрөө танихаа больж, `nexus.gerege.mn` дээр таниулдаг.**

Цөмийн код энд нэг ч мөр байхгүй — `go.mod`-ын нэг мөр л байна
(`open-gerege-nexus/backend`). Энд байгаа Go код нь `modules/` ба `domain/`:
энэ distribution-ы өөрийн аппууд.

## Модулиуд

| Модуль | Юу | Хүснэгт | Хэзээ ирсэн |
| --- | --- | --- | --- |
| `organisation` | Байгууллага, хэлтэс нэгж, ажилтнууд | `departments`, `organisation_people` | 2026-08-23 |
| `documents` | Баримт бичиг, гарын үсгийн ёслол, PDF зурах | `document_*` ес | 2026-08-23 |
| `egov` | ХУР-ын лавлагаа, төрийн сувгийн төлөв, лавлагааны түүх | — | 2026-08-23 |

Гурвуулаа нэг өдөрт цөмөөс ирлээ. Ирж чадсан шалтгаан нь тэдний өмнөх долоо
хоногт цөмд нийтлэгдсэн гэрээнүүд:

| Модуль | Юу цөмөөс шууд уншдаг байсан | Одоо ямар гэрээгээр |
| --- | --- | --- |
| `egov` | `internal/platform/gerege`-ийн ХУР клиент, `audit_events` хүснэгт | `nexus.StateRegistry`, `nexus.AuditReader` |
| `documents` | `internal/platform/eid`, `dan`, `esign` | `nexus.EIDSigner`, `nexus.DANAuthenticator`, `nexus.SigningRails` |
| `organisation` | `users`, `memberships`, `roles`, `membership_roles`, `tenants` дээрх өөрийн SQL | `nexus.Directory` |

Хүснэгтүүд нь модультайгаа хамт ирлээ: `modules/<нэр>/migrations/` доторх
goose файлыг `nexus.Migrations` бүртгэж, апп суулгах үед платформ
ажиллуулна. Цөмийн `00077` тэдгээрийг өөрөөсөө устгасан.

`egov` өгөгдлийн сан огт хэрэглэдэггүй — төрөөс асууж, асуусан гэдгээ
платформоор санамсарлуулна.

## Юу нь ялгаатай вэ

| | |
| --- | --- |
| Нэвтрэлт | `nexus.gerege.mn` (OIDC provider). Эндэх нууц үг, eID-ийн дэлгэц унтраалттай |
| Клиентийн id | `client-gerege-nexus`, confidential |
| Байгууллага | `client` — провайдер дээр танигдсан шинэ хүн энд үүснэ |
| Модуль | `organisation`, `documents`, `egov` — 2026-08-23-нд цөмөөс энд нүүсэн |
| Портууд | 5439 / 8097 / 3017, зөвхөн loopback — **цөмийн анхдагч биш** |

Портууд нь чөлөөт сонголт биш: энэ стек `nexus.gerege.mn`-тэй **ижил машин**
дээр сууж байгаа бөгөөд цөм 3008/8082-ыг эзэлсэн. Үүнээс мөрдөгдөх хоёр дахь
дүрэм: `nginx/client.gerege.mn.conf` нь цөмийн `snippets/nexus-oauth.conf`-ыг
include **хийхгүй** — тэр snippet `127.0.0.1:8082` гэж хатуу бичсэн тул
include хийсэн бол энэ домэйн хөршийнхөө discovery баримтыг тарааж эхлэх байв.

## Нэвтрэлтийн урсгал

1. Хүн `client.gerege.mn/login` дээр ирнэ. Дэлгэц юу ч асуухгүй —
   `/api/v1/auth/sso/start` руу илгээнэ.
2. `nexus.gerege.mn` дээр танигдаад
   `client.gerege.mn/api/v1/auth/sso/callback` дээр кодтой буцна.
3. Холбоос нь `(issuer, subject)`-ээр суудаг. Анх удаа ирсэн хүнийг
   провайдерийн **баталгаажуулсан и-мэйлээр** энд байгаа бүртгэлтэй холбоно;
   олдохгүй бол `client` байгууллагад шинээр үүсгэнэ.

Дэлгэрэнгүй: цөмийн [`docs/SSO_FEDERATION.md`](https://github.com/gerege-systems/open-gerege-nexus/blob/main/docs/SSO_FEDERATION.md).

Провайдер дээр бүртгэгдсэн хаягууд:

```
redirect_uris             https://client.gerege.mn/api/v1/auth/sso/callback
post_logout_redirect_uris https://client.gerege.mn/
grant_types               authorization_code
scopes                    openid profile email
```

## Байрлуулалт

Сервер дээр `/opt/client-nexus/` — `src/` энэ репо, `.env` (chmod 600),
`brand/`.

```bash
cd /opt/client-nexus/src && git pull && ./deploy.sh
```

`deploy.sh` нь **backend образыг барина**, бүрхүүлийг цөмийн нийтэлсэн
образаас авна (`WEB_IMAGE`) — тэр образ энэ хост дээр аль хэдийн байдаг тул
ердийн үед ghcr.io руу орох хэрэггүй. Байхгүй бол `REGISTRY_USER` /
`REGISTRY_TOKEN` (read:packages) өгнө.

Цөмийг өргөх нь хоёр мөр: `go.mod`-ын хувилбар, `.env`-ийн `WEB_IMAGE` — хоёул
нэг commit дээр байх ёстой, эс бөгөөс backend, frontend хоёр өөр API гэрээ
дээр ажиллана.

Эхний админ:

```bash
./first-admin.sh <и-мэйл> <нууц үг>
```

И-мэйл нь провайдер дээрх и-мэйлтэй **яг ижил** байх ёстой — тэгж байж эхний
SSO нэвтрэлт энэ бүртгэлийн админ эрхийг өвлөнө. Нууц үг нь
`SSO_CLIENT_LOCAL_LOGIN=true` болгосон өдрийн буцах зам.

## Өөрийн модуль нэмэх

`modules/` үүсгээд `main.go`-гийн `Modules` callback дотор бүртгэнэ. Түвшин
2-ын бүтэц эхний commit-оос бэлэн байгаа нь яг үүний тулд: эхний модуль нэмэх
нь нэг файлын өөрчлөлт, байрлуулалтын нүүлгэлт биш.

Өөрийн хүснэгттэй бол `modules/<нэр>/migrations/00001_<нэр>.sql` гэж бичээд
конструктораасаа бүртгэнэ:

```go
//go:embed migrations/*.sql
var migrations embed.FS

func New(p nexus.Platform) *Module {
    m := &Module{...}
    nexus.Register(m)
    nexus.Migrations(m.ID(), schema()) // fs.Sub(migrations, "migrations")
    return m
}
```

Платформ нь аппыг суулгахын өмнө ажиллуулж, `goose_db_version_<slug>` гэсэн
өөрийн хувилбарын хүснэгтэд бичнэ — модулийн 00001 ба платформын 00001 хоёр
нэг мөр болохгүй. `MIGRATIONS_DIR`/`MIGRATIONS_TABLE` нь distribution
бүхэлдээ нэг схем үүрэх үеийн зам байсан бөгөөд хэвээр ажиллана; модулийн
хувьд дээрх нь дээр, учир нь схем нь модультайгаа хамт хөдөлнө.

Каталог: `catalog/apps.json`, `catalog/manifests/<slug>.json`, ба
хувилбарын түүхээ `catalog/chronicle/<slug>.json`-д.
