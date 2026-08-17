#!/usr/bin/env bash
# Шинэ байрлуулалтад эхний байгууллага, эхний админыг өгнө.
#
#   ./first-admin.sh <и-мэйл> <нууц үг> ["Байгууллагын нэр"] [slug]
#
# Яагаад SQL вэ: бүртгүүлэх дэлгэц гэж байхгүй (`/api/v1/auth/register`
# байхгүй), баримтжсан зам болох control plane нь өөрийн vhost,
# CONTROL_PLANE_HOST, TOTP бүртгэл шаарддаг. Business, Benzin, Цахим Засаг,
# Gerege App бүгд ингэж эхэлсэн.
#
# Энэ суулгац дээр нэмэлт учир бий. Нэвтрэлт нь nexus.gerege.mn дээр болдог
# тул энд үүсгэсэн нууц үг нь ердийн үед хэрэглэгдэхгүй — түүний оронд
# провайдерийн баталгаажуулсан И-МЭЙЛ нь энэ бүртгэлтэй тааран холбогдож,
# ирсэн хүн энэ админ эрхийг өвлөнө (цөмийн docs/SSO_FEDERATION.md, «Хэн
# болохыг эндэх бүртгэлтэй холбох», хоёр дахь алхам). Тиймээс и-мэйл нь
# провайдер дээрх и-мэйлтэй ЯГ ижил байх ёстой. Нууц үг нь
# SSO_CLIENT_LOCAL_LOGIN=true болгосон өдрийн буцах зам.
#
# Дахин ажиллуулж болно: дөрвөн INSERT бүгд ON CONFLICT DO NOTHING.
set -euo pipefail

email="${1:?и-мэйл}"; password="${2:?нууц үг}"
tenant_name="${3:-Gerege Client}"; tenant_slug="${4:-client}"

docker exec -i gerege_client_nexus_postgres psql -v ON_ERROR_STOP=1 -U postgres -d platform_db \
  -v email="$email" -v pass="$password" -v tname="$tenant_name" -v tslug="$tenant_slug" <<'SQL'
-- Миграцууд үүнийг суулгадаггүй. Нууц үгийн hash-ийг сангийн дотор үүсгэж,
-- ил бичвэрийг psql-ийн түүх, процессын жагсаалтад гаргахгүй байхын тулд.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

INSERT INTO tenants (slug, name) VALUES (:'tslug', :'tname')
  ON CONFLICT (slug) DO NOTHING;

-- gen_salt('bf', 10) нь Go-гийн bcrypt уншдаг `$2a$10$` хэлбэрийг өгнө.
INSERT INTO users (email, password_hash, name)
  VALUES (:'email', crypt(:'pass', gen_salt('bf', 10)), :'email')
  ON CONFLICT (email) DO NOTHING;

INSERT INTO memberships (tenant_id, user_id)
  SELECT t.id, u.id FROM tenants t, users u
   WHERE t.slug = :'tslug' AND u.email = :'email'
  ON CONFLICT (tenant_id, user_id) DO NOTHING;

-- Тусдаа өгүүлбэр байх нь заавал: `admin` рольд tenants дээрх AFTER INSERT
-- trigger үүсгэдэг тул тенантыг үүсгэсэн өгүүлбэр дотроос харагдахгүй — нэг
-- CTE дотор бичвэл юу ч оруулалгүй чимээгүй амжилттай болно.
INSERT INTO membership_roles (membership_id, role_id)
  SELECT m.id, r.id
    FROM memberships m
    JOIN tenants t ON t.id = m.tenant_id AND t.slug = :'tslug'
    JOIN users   u ON u.id = m.user_id   AND u.email = :'email'
    JOIN roles   r ON r.tenant_id = t.id AND r.code = 'admin'
  ON CONFLICT DO NOTHING;

DROP EXTENSION pgcrypto;
SQL

# Шалгалт нь нэвтрэлт биш, SQL. /api/v1/auth/login нь энэ суулгац дээр
# унтраалттай (SSO_CLIENT_LOCAL_LOGIN=false) тул түүгээр шалгавал зөв
# бичигдсэн админыг «бэлэн биш» гэж дуудна. Шалгаж байгаа зүйл нь яг
# оруулахыг оролдсон зүйл: гишүүнчлэл дээр admin роль бодитоор суусан эсэх —
# trigger-ийн улмаас энэ мөр л чимээгүй алга болдог.
granted="$(docker exec -i gerege_client_nexus_postgres psql -tA -U postgres -d platform_db \
  -v email="$email" -v tslug="$tenant_slug" <<'SQL'
SELECT count(*) FROM membership_roles mr
  JOIN memberships m ON m.id = mr.membership_id
  JOIN tenants t ON t.id = m.tenant_id AND t.slug = :'tslug'
  JOIN users   u ON u.id = m.user_id   AND u.email = :'email'
  JOIN roles   r ON r.id = mr.role_id  AND r.code = 'admin';
SQL
)"
[ "$granted" = "1" ] || { echo "admin роль суугаагүй (олдсон: $granted)" >&2; exit 1; }
echo "OK: ${email} → ${tenant_name} (${tenant_slug}) — админ"
echo "Нэвтрэх нь nexus.gerege.mn дээр. Тэнд ижил и-мэйлээр орсон хүн энэ бүртгэлийг өвлөнө."
