#!/usr/bin/env bash
# client.gerege.mn-ийг барьж, шинэчлэх. Серверийн /opt/client-nexus/src
# дотроос ажиллана.
#
#   cd /opt/client-nexus/src && git pull && ./deploy.sh
#
# Юу хийдэг вэ: энэ бүтээгдэхүүний backend образыг барина, бүрхүүлийг цөмийн
# нийтэлсэн образаас авна, стекийг солиод эрүүл эсэхийг асууна.
#
# Бүрхүүлийг барьдаггүй нь энэ байрлуулалтын онцлог: nexus.gerege.mn-тэй ижил
# хост дээр сууж байгаа тул цөмийн frontend образ энэ машин дээр аль хэдийн
# бий. Байхгүй бол л ghcr.io-оос татна — тэр үед REGISTRY_USER/REGISTRY_TOKEN
# (read:packages) хэрэгтэй, пакет нь хаалттай.
set -euo pipefail

SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
APP_DIR="$(dirname "$SRC_DIR")"

[ -f "$APP_DIR/.env" ] || { echo "$APP_DIR/.env алга. .env.example-ээс хуулж, нууцуудыг нь бөглө." >&2; exit 1; }

# Бүрхүүлийн хувилбар нь go.mod-оос ирнэ, салбарын толгойгоос биш: backend
# болон frontend хоёр өөр цөмийн хувилбар дээр байх нь API-гийн гэрээ
# зөрчигдөх ганцхан зам. .env дэх WEB_IMAGE-ийг энэ хувилбарын tag дээр
# байлгана — go.mod-ыг өргөх өдөр тэр мөр хамт өөрчлөгдөнө.
core_version="$(grep -oE 'open-gerege-nexus/backend v[0-9]+\.[0-9]+\.[0-9]+' "$SRC_DIR/go.mod" | head -1 | awk '{print $2}')"
[ -n "$core_version" ] || { echo "go.mod-оос цөмийн хувилбар уншигдсангүй" >&2; exit 1; }
echo "цөм: $core_version"

web_image="$(grep -E '^WEB_IMAGE=' "$APP_DIR/.env" | cut -d= -f2-)"
[ -n "$web_image" ] || { echo ".env дотор WEB_IMAGE алга" >&2; exit 1; }

if ! docker image inspect "$web_image" >/dev/null 2>&1; then
  : "${REGISTRY_USER:?$web_image хост дээр алга — REGISTRY_USER шаардлагатай}"
  : "${REGISTRY_TOKEN:?$web_image хост дээр алга — REGISTRY_TOKEN (read:packages) шаардлагатай}"
  echo "$REGISTRY_TOKEN" | docker login ghcr.io -u "$REGISTRY_USER" --password-stdin
  docker pull "$web_image"
  docker logout ghcr.io >/dev/null
fi

docker build -q -t client-nexus:latest -f "$SRC_DIR/deploy/Dockerfile" "$SRC_DIR"

# Бүрхүүл ./brand-ийг nginx-ээр өгдөг тул байхгүй бол лого 404. chmod нь сайн
# дурын биш: www-data 0700 хавтас дотор орж чадахгүй.
mkdir -p "$APP_DIR/brand" && chmod 755 "$APP_DIR/brand"

cp "$APP_DIR/.env" "$SRC_DIR/deploy/.env"
docker compose -f "$SRC_DIR/deploy/docker-compose.yml" up -d --remove-orphans

# Шалгалт: гурван хариу — API эрүүл, бүрхүүл ирж байна, брэнд .env-ийнхээ
# нэрийг хэлж байна. Гурав дахь нь энэ байрлуулалтыг цөмийн анхдагчаас ялгадаг
# цорын ганц зүйл тул сайн дурын биш.
for i in $(seq 1 30); do
  curl -fsS http://127.0.0.1:8097/health >/dev/null 2>&1 && break
  [ "$i" -eq 30 ] && { echo "backend 60 секундэд эрүүл болсонгүй" >&2; docker compose -f "$SRC_DIR/deploy/docker-compose.yml" logs --tail 40 backend >&2; exit 1; }
  sleep 2
done

brand="$(grep -E '^BRAND_NAME=' "$APP_DIR/.env" | cut -d= -f2-)"
for i in $(seq 1 30); do
  if body="$(curl -fsS http://127.0.0.1:3017/login 2>/dev/null)"; then
    [ -z "$brand" ] && break
    case "$body" in *"$brand"*) break ;; esac
  fi
  [ "$i" -eq 30 ] && { echo "бүрхүүл 60 секундэд «${brand:-хариу}» өгсөнгүй" >&2; exit 1; }
  sleep 2
done

# Дөрөв дэх шалгалт, зөвхөн энэ байрлуулалтад: нэвтрэлт нь провайдер дээр
# болдог гэдгээ хэлж байгаа эсэх. Энэ эндпойнт «холбоогүй» гэж хариулбал
# SSO_CLIENT_* тохиргоо контейнер хүртэл хүрээгүй гэсэн үг — стек бүхэлдээ
# эрүүл харагдана, зөвхөн нэвтрэх товч л буруу газар заана.
if grep -qE '^SSO_CLIENT_ISSUER=https' "$APP_DIR/.env"; then
  cfg="$(curl -fsS http://127.0.0.1:8097/api/v1/auth/sso/config)"
  case "$cfg" in
    *'"enabled":true'*) ;;
    *) echo "SSO клиент унтраалттай байна: $cfg" >&2; exit 1 ;;
  esac
fi

echo "OK: $(grep -E '^PUBLIC_ORIGIN=' "$APP_DIR/.env" | cut -d= -f2) — $brand"
