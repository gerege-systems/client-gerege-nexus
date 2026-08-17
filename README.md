# Gerege Client — client.gerege.mn

[Gerege Nexus](https://github.com/gerege-systems/open-gerege-nexus) платформын
**Түвшин 2 distribution**. Онцлог нь нэг өгүүлбэрээр: **энэ суулгац хүнийг
өөрөө танихаа больж, `nexus.gerege.mn` дээр таниулдаг.**

Цөмийн код энд нэг ч мөр байхгүй — `go.mod`-ын нэг мөр л байна
(`open-gerege-nexus/backend v1.9.1`).

## Юу нь ялгаатай вэ

| | |
| --- | --- |
| Нэвтрэлт | `nexus.gerege.mn` (OIDC provider). Эндэх нууц үг, eID-ийн дэлгэц унтраалттай |
| Клиентийн id | `client-gerege-nexus`, confidential |
| Байгууллага | `client` — провайдер дээр танигдсан шинэ хүн энд үүснэ |
| Модуль | Одоогоор өөрийн модульгүй: платформын өөрийн аппууд ажиллана |
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

`modules/` үүсгээд `main.go`-гийн `Modules` callback дотор бүртгэнэ, өөрийн
migration-ууд бол `MIGRATIONS_DIR`/`MIGRATIONS_TABLE`-тэй хамт. Түвшин 2-ын
бүтэц эхний commit-оос бэлэн байгаа нь яг үүний тулд: эхний модуль нэмэх нь
нэг файлын өөрчлөлт, байрлуулалтын нүүлгэлт биш.
