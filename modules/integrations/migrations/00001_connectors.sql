-- Холбогчийн схем: аппын өөрийн гурван хүснэгт.
--
-- Платформын түүхээс зөөгдөв. `integrations`, `integration_oauth_states`,
-- `integration_deliveries` гурав нь цөмийн 00022-т үүсч, 00023 нь status-ийн
-- утгыг («санаа», «сүүлийн үр дүн» хоёрын аль нэг нь) нарийсгасан. Ажилласан
-- миграцыг дахин бичих боломжгүй тул энд эцсийн хэлбэрээр нь дахин зарлав —
-- urtuu-гийн 00001-ийн яг тэр арга.
--
-- Цөмийн 00088 нь гурвуулангийн эзэмшлийг өгч, хүснэгтүүдийг хаяна. Бүгд
-- `IF NOT EXISTS`: энэ түүх нь цөмийн хуулбарыг аль хэдийн үүрсэн өгөгдлийн
-- сан дээр ажиллана.
--
-- Итгэмжлэл энд ил хэвтэхгүй. `secret_ciphertext` (webhook-ийн HMAC түлхүүр)
-- ба `oauth_ciphertext` (провайдерын token) хоёрыг апп нь AES-GCM-ээр
-- битүүмжилнэ — түлхүүр нь платформынх (`INTEGRATION_ENCRYPTION_KEY`,
-- `nexus.SecretSealer`), учир нь нэг суулгацад нэг л шифр байх ёстой.
--
-- Ролийн нэрийг заахгүй, хайна: цөмийн 00079 нь gerege_nexus_app-ыг
-- gerege_nexus_tenant болгосон бөгөөд цэвэр өгөгдлийн сан зөвхөн сүүлийнхийг
-- авна.

-- +goose Up

CREATE TABLE IF NOT EXISTS integrations (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- webhook | government | payment | custom_rest | google_drive | dropbox | google_meet
    provider          VARCHAR(32) NOT NULL,
    name              VARCHAR(255) NOT NULL,
    target_url        TEXT NOT NULL DEFAULT '',
    -- Зөвхөн администраторын САНАА: асаалттай эсэх. Сүүлийн хүргэлтийн үр дүн
    -- энд бичигдэхгүй (00023) — нэг удаагийн 503 холбогчийг үүрд унтраадаг
    -- байсан. Эрүүл эсэхийг `last_error` хэлнэ.
    status            VARCHAR(16) NOT NULL DEFAULT 'ACTIVE',
    -- Нууц биш тохиргоо: очих хавтас, Dropbox зам, календарийн id.
    config            JSONB NOT NULL DEFAULT '{}'::jsonb,
    secret_ciphertext BYTEA,
    oauth_ciphertext  BYTEA,
    -- Аль бүртгэлд грант олгогдсоныг админ баримт очихоос ӨМНӨ хардаг.
    account_label     VARCHAR(255) NOT NULL DEFAULT '',
    connected_at      TIMESTAMPTZ,
    last_ping_at      TIMESTAMPTZ,
    last_error        TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT integrations_status_known CHECK (status IN ('ACTIVE', 'INACTIVE')),
    CONSTRAINT integrations_provider_known CHECK (provider IN (
        'webhook', 'government', 'payment', 'custom_rest',
        'google_drive', 'dropbox', 'google_meet')),
    -- Байгууллага бүрд нэг нэр: нэр нь оператор очих газраа сонгодог зүйл
    -- бөгөөд «Архив» гэсэн хоёр холбогч нь баримтыг буруу бүртгэлд файлдах зам.
    CONSTRAINT integrations_name_unique_per_tenant UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS idx_integrations_tenant ON integrations(tenant_id, provider);

-- Илгээлт үйл явдал бүрд үүнийг уншина, зөвхөн ACTIVE webhook нь зорилт.
CREATE INDEX IF NOT EXISTS idx_integrations_dispatch
    ON integrations(tenant_id) WHERE provider = 'webhook' AND status = 'ACTIVE';

-- OAuth-ийн `state`: гарын үсэг зурахын оронд хадгална, ингэснээр нэг л удаа
-- хэрэглэгдэнэ. Алдагдсан гарын үсэгтэй state нь хугацаа дуустал давтагдана,
-- давтагдсан callback нь грантыг буруу бүртгэлд уяна.
CREATE TABLE IF NOT EXISTS integration_oauth_states (
    state          VARCHAR(64) PRIMARY KEY,
    tenant_id      UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    integration_id UUID NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    redirect_uri   TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at     TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_integration_oauth_states_expiry
    ON integration_oauth_states(expires_at);

-- Юу гадагш явсны бүртгэл. Гарын үсэгтэй PDF хэн нэгний Drive рүү явах нь
-- задруулалт бөгөөд «хуулагдсан байх шиг байна» гэдэг нь хойшид хэн ч
-- ажиллаж чадахгүй хариулт.
CREATE TABLE IF NOT EXISTS integration_deliveries (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    integration_id UUID NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    kind           VARCHAR(32) NOT NULL,
    reference      VARCHAR(255) NOT NULL DEFAULT '',
    outcome        VARCHAR(16) NOT NULL,
    detail         TEXT NOT NULL DEFAULT '',
    external_id    VARCHAR(255) NOT NULL DEFAULT '',
    external_url   TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT integration_deliveries_outcome_known CHECK (outcome IN ('OK', 'FAILED'))
);

CREATE INDEX IF NOT EXISTS idx_integration_deliveries_tenant
    ON integration_deliveries(tenant_id, created_at DESC);

-- Тенант тусгаарлалт, платформын 00037-ийн хэлбэрээр.
--
-- `integration_oauth_states` нь эхний хоёроос ялгаатай: уншихдаа ч зөвхөн
-- идэвхтэй байгууллага. State мөр нь нэг хүний нэг агшны зөвшөөрөл бөгөөд
-- байгууллага дамнасан тайлангийн уншилтад орох зүйл биш.
-- +goose StatementBegin
DO $rls$
DECLARE
    target TEXT;
    app_role TEXT := (SELECT rolname FROM pg_roles
                       WHERE rolname IN ('gerege_nexus_tenant', 'gerege_nexus_app')
                       ORDER BY (rolname = 'gerege_nexus_tenant') DESC LIMIT 1);
BEGIN
    FOREACH target IN ARRAY ARRAY['integrations', 'integration_deliveries'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', target);
        EXECUTE format('ALTER TABLE %I FORCE  ROW LEVEL SECURITY', target);
        CONTINUE WHEN app_role IS NULL;
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', target);
        EXECUTE format($p$
            CREATE POLICY tenant_isolation ON %I TO %I
                USING (tenant_id IS NULL OR tenant_id = ANY (COALESCE(
                    NULLIF(current_setting('app.allowed_tenants', true), '')::uuid[],
                    ARRAY[NULLIF(current_setting('app.current_tenant', true), '')::uuid])))
                WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
        $p$, target, app_role);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO %I', target, app_role);
    END LOOP;

    ALTER TABLE integration_oauth_states ENABLE ROW LEVEL SECURITY;
    ALTER TABLE integration_oauth_states FORCE  ROW LEVEL SECURITY;
    IF app_role IS NOT NULL THEN
        EXECUTE 'DROP POLICY IF EXISTS tenant_isolation ON integration_oauth_states';
        EXECUTE format($p$
            CREATE POLICY tenant_isolation ON integration_oauth_states TO %I
                USING      (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
                WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
        $p$, app_role);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON integration_oauth_states TO %I', app_role);
    END IF;
END
$rls$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS integration_deliveries;
DROP TABLE IF EXISTS integration_oauth_states;
DROP TABLE IF EXISTS integrations;
