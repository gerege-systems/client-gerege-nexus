-- Баримт бичиг, гарын үсэг: аппын өөрийн схем.
--
-- Есөн хүснэгт, платформын арваад миграцаас цуглав — 00013 (баримт, загвар),
-- 00046 (гарын үсэг), 00050 (ажлын урсгал), 00058 (хадгалалт), 00062
-- (зөвшөөрлийн алхмууд), 00065 (eID ёслолын session), 00068 (файл), 00072
-- (зурагдсан PDF). Тэднийг зөөх боломжгүй: хэрэглэгдсэн миграцыг дахин бичих
-- боломжгүй бөгөөд файл бүр нь платформын үлдэх хүснэгтүүдийг хамт үүсгэдэг.
-- Тиймээс энд эцсийн хэлбэрээр нь дахин зарлав.
--
-- Бүгд `IF NOT EXISTS`, шалтгаан нь organisation-ийхтой ижил.
--
-- `document_files`-ийн бодлого бусдаас өөр бөгөөд санаатай: бусад хүснэгт нь
-- session-ий зөвшөөрөгдсөн бүх байгууллагыг уншуулдаг (нэг хүн хэд хэдэн
-- байгууллагад харьяалагдаж болно), файлын агуулга нь зөвхөн идэвхтэй
-- байгууллагынх. Хамгийн нарийн мэдээлэл нь хамгийн нарийн бодлоготой.
--
-- ADR 0002, 0003-ыг тайлбар болгон авчирлаа: тэдгээр нь баганы утга юу
-- болохыг тодорхойлдог бөгөөд платформын түүхэнд үлдвэл энд уншигдахгүй.

-- +goose Up

CREATE TABLE IF NOT EXISTS document_records (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    title               VARCHAR(255) NOT NULL,
    doc_type            VARCHAR(64)  NOT NULL DEFAULT 'CONTRACT',
    status              VARCHAR(32)  NOT NULL DEFAULT 'DRAFT',
    signed_by           VARCHAR(255),
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    signature_hash      VARCHAR(255),
    signer_reg_number   VARCHAR(64),
    signer_method       VARCHAR(32),
    signed_at           TIMESTAMPTZ,
    required_signatures SMALLINT NOT NULL DEFAULT 1
);
COMMENT ON COLUMN document_records.signature_hash IS
    'Хамгийн сүүлийн зөвшөөрлийн ёслолын лавлагаа, агуулгын хэш БИШ — ADR 0002.';
CREATE INDEX IF NOT EXISTS idx_document_records_tenant ON document_records(tenant_id);
CREATE INDEX IF NOT EXISTS idx_document_records_page   ON document_records(tenant_id, created_at, id);

CREATE TABLE IF NOT EXISTS document_files (
    document_id    UUID PRIMARY KEY REFERENCES document_records(id) ON DELETE CASCADE,
    tenant_id      UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    file_name      VARCHAR(255) NOT NULL,
    content_type   VARCHAR(128) NOT NULL,
    size_bytes     BIGINT NOT NULL,
    sha256         CHAR(64) NOT NULL,
    content        BYTEA NOT NULL,
    uploaded_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    signed_content BYTEA,
    signed_at      TIMESTAMPTZ,
    CONSTRAINT document_files_size_check CHECK (size_bytes > 0 AND size_bytes <= 26214400)
);
COMMENT ON TABLE document_files IS
    'Баримтын хавсралт — гарын үсэг хамаарах зүйл. Нэг баримт нэг файл; зурагдсаны дараа солигдохгүй (ADR 0003).';
COMMENT ON COLUMN document_files.signed_content IS
    'PAdES-ээр зурагдсан хамгийн сүүлийн хувь. NULL бол энэ баримт detached эсвэл approval-аар зурагдсан — ADR 0003.';
CREATE INDEX IF NOT EXISTS idx_document_files_tenant ON document_files(tenant_id);

CREATE TABLE IF NOT EXISTS document_signatures (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    document_id        UUID NOT NULL REFERENCES document_records(id) ON DELETE CASCADE,
    signer_name        VARCHAR(255) NOT NULL,
    signer_reg_number  VARCHAR(64)  NOT NULL,
    signer_method      VARCHAR(32)  NOT NULL,
    signature_hash     VARCHAR(255) NOT NULL,
    signed_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    certificate_serial VARCHAR(190),
    certificate_issuer VARCHAR(255),
    step_order         SMALLINT,
    format             VARCHAR(16),
    covered_digest     CHAR(64),
    CONSTRAINT document_signatures_once_per_signer  UNIQUE (document_id, signer_reg_number),
    CONSTRAINT document_signatures_one_per_approval UNIQUE (document_id, step_order)
);
COMMENT ON COLUMN document_signatures.signature_hash IS
    'Ёслолын лавлагаа (eid_session_… / dan_sig_…), агуулгын хэш БИШ. Энэ апп агуулгагүй тул баталгаажсан зөвшөөрөл бүртгэдэг — ADR 0002.';
COMMENT ON COLUMN document_signatures.format IS
    'pades | detached | approval — ADR 0003. NULL нь асуулт асуухаас өмнөх мөр, approval гэж уншигдана.';
COMMENT ON COLUMN document_signatures.covered_digest IS
    'Гарын үсэг хамарсан файлын SHA-256. approval дээр NULL: хамарсан зүйл байхгүй.';
CREATE INDEX IF NOT EXISTS idx_document_signatures_document ON document_signatures(document_id);

CREATE TABLE IF NOT EXISTS document_approval_steps (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    document_id       UUID NOT NULL REFERENCES document_records(id) ON DELETE CASCADE,
    step_order        SMALLINT NOT NULL,
    name              VARCHAR(255) NOT NULL,
    signer_reg_number VARCHAR(64) NOT NULL DEFAULT '',
    CONSTRAINT document_approval_steps_order_unique   UNIQUE (document_id, step_order),
    CONSTRAINT document_approval_steps_order_positive CHECK (step_order >= 1)
);
CREATE INDEX IF NOT EXISTS idx_document_approval_steps_document ON document_approval_steps(document_id);

CREATE TABLE IF NOT EXISTS document_workflow_steps (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    doc_type          VARCHAR(64) NOT NULL,
    step_order        SMALLINT NOT NULL,
    name              VARCHAR(255) NOT NULL,
    signer_reg_number VARCHAR(64) NOT NULL DEFAULT '',
    CONSTRAINT document_workflow_steps_order_unique   UNIQUE (tenant_id, doc_type, step_order),
    CONSTRAINT document_workflow_steps_order_positive CHECK (step_order >= 1)
);
CREATE INDEX IF NOT EXISTS idx_document_workflow_steps_type ON document_workflow_steps(tenant_id, doc_type);

CREATE TABLE IF NOT EXISTS document_templates (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name          VARCHAR(255) NOT NULL,
    doc_type      VARCHAR(64)  NOT NULL DEFAULT 'CONTRACT',
    title_pattern VARCHAR(255) NOT NULL,
    active        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT document_templates_name_unique UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_document_templates_tenant ON document_templates(tenant_id);

CREATE TABLE IF NOT EXISTS document_signature_policies (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    doc_type             VARCHAR(64) NOT NULL,
    allow_eid            BOOLEAN NOT NULL DEFAULT TRUE,
    allow_dan            BOOLEAN NOT NULL DEFAULT TRUE,
    require_named_signer BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT document_signature_policies_type_unique UNIQUE (tenant_id, doc_type),
    CONSTRAINT document_signature_policies_one_method  CHECK (allow_eid OR allow_dan)
);

CREATE TABLE IF NOT EXISTS document_retention_rules (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    doc_type      VARCHAR(64) NOT NULL,
    retain_years  SMALLINT NOT NULL,
    note          VARCHAR(255) NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT document_retention_rules_type_unique UNIQUE (tenant_id, doc_type),
    CONSTRAINT document_retention_rules_years_sane  CHECK (retain_years >= 1 AND retain_years <= 100)
);

CREATE TABLE IF NOT EXISTS document_eid_sign_sessions (
    session_id       VARCHAR(190) PRIMARY KEY,
    tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    document_id      UUID NOT NULL REFERENCES document_records(id) ON DELETE CASCADE,
    reg_number       VARCHAR(64)  NOT NULL,
    display_text     VARCHAR(255) NOT NULL,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    consumed_at      TIMESTAMPTZ,
    expires_at       TIMESTAMPTZ,
    requested_digest CHAR(64),
    format           VARCHAR(16)
);
CREATE INDEX IF NOT EXISTS idx_document_eid_sign_sessions_document ON document_eid_sign_sessions(document_id);

-- Тенант тусгаарлалт, платформын 00037-ийн хэлбэрээр.
-- Тенантын ролийг нэрлэхийн оронд хайна: платформын 00029 түүнийг
-- `gerege_nexus_app` нэрээр үүсгэсэн, цөмийн 00079 (v1.14.0) `gerege_nexus_tenant`
-- болгосон, шинэ өгөгдлийн сан дээр хуучин нэр огт үүсэхгүй. Аль нэгийг нь
-- бататгасан миграц нөгөө талдаа `role ... does not exist` гэж унана.
-- +goose StatementBegin
DO $rls$
DECLARE
    t TEXT;
    app_role TEXT;
BEGIN
    SELECT rolname INTO app_role FROM pg_roles
     WHERE rolname IN ('gerege_nexus_tenant', 'gerege_nexus_app')
     ORDER BY (rolname = 'gerege_nexus_tenant') DESC
     LIMIT 1;

    FOREACH t IN ARRAY ARRAY[
        'document_records', 'document_signatures', 'document_approval_steps',
        'document_workflow_steps', 'document_templates',
        'document_signature_policies', 'document_retention_rules',
        'document_eid_sign_sessions'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE  ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        CONTINUE WHEN app_role IS NULL;
        EXECUTE format($p$
            CREATE POLICY tenant_isolation ON %I TO %I
                USING (tenant_id IS NULL OR tenant_id = ANY (COALESCE(
                    NULLIF(current_setting('app.allowed_tenants', true), '')::uuid[],
                    ARRAY[NULLIF(current_setting('app.current_tenant', true), '')::uuid])))
                WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
        $p$, t, app_role);
    END LOOP;

    -- Файлын агуулга нь зөвхөн идэвхтэй байгууллагынх — дээрх тайлбарыг үз.
    EXECUTE 'ALTER TABLE document_files ENABLE ROW LEVEL SECURITY';
    EXECUTE 'ALTER TABLE document_files FORCE  ROW LEVEL SECURITY';
    EXECUTE 'DROP POLICY IF EXISTS tenant_isolation ON document_files';
    IF app_role IS NULL THEN
        RETURN;
    END IF;
    EXECUTE format($p$
        CREATE POLICY tenant_isolation ON document_files TO %I
            USING      (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
            WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
    $p$, app_role);

    EXECUTE format($p$
        GRANT SELECT, INSERT, UPDATE, DELETE ON
            document_records, document_files, document_signatures,
            document_approval_steps, document_workflow_steps, document_templates,
            document_signature_policies, document_retention_rules,
            document_eid_sign_sessions
        TO %I
    $p$, app_role);
END
$rls$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS document_eid_sign_sessions;
DROP TABLE IF EXISTS document_retention_rules;
DROP TABLE IF EXISTS document_signature_policies;
DROP TABLE IF EXISTS document_templates;
DROP TABLE IF EXISTS document_workflow_steps;
DROP TABLE IF EXISTS document_approval_steps;
DROP TABLE IF EXISTS document_signatures;
DROP TABLE IF EXISTS document_files;
DROP TABLE IF EXISTS document_records;
