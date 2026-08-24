-- Гэрээнд ТАЛУУД байдаг.
--
-- Өнөөдрийн энэ апп нь бүртгэл: гарчиг, төрөл, төлөв, зөвшөөрлийн дараалал.
-- Түүнд ХЭНТЭЙ гэрээ байгуулж байгааг хэлэх багана байхгүй, ХЭНД илгээхийг
-- хэлэх маршрут байхгүй. Тиймээс «гэрээ байгуулах» гэдэг үйлдэл энэ бүтээгдэхүүн
-- дотор оршдоггүй. Энэ миграц түүнийг нэмнэ.
--
-- ДӨРВӨН ШИЙДВЭР, ТУС ТУСДАА ШАЛТГААНТАЙ
--
-- 1. ХӨЛДСӨН БАЙТ. Тал бүрд хүргэсэн агшинд зурагдсан PDF нь `document_party_files`-д
--    хадгалагдаж, ДАХИН ХЭЗЭЭ Ч ҮҮСГЭГДЭХГҮЙ. fpdf-ийн `/CreationDate` ганцаараа
--    байтыг хөдөлгөнө; хөдөлмөгц covered_digest нь оршихгүй байтыг нэрлэнэ, ба
--    «би юунд гарын үсэг зурав» гэдэг асуулт хариултгүй болно.
--
-- 2. ГАРЫН ҮСЭГ ЗУРАХ ЭРХ НЬ ТЕКСТЭЭС ГАРАХГҮЙ. Тал нь хуулийн этгээд, гарын
--    үсэг зурагч нь хүн. Хоёрыг нэг мөрөнд нийлүүлбэл эрх нь «албан тушаал»
--    гэсэн чөлөөт бичвэрээс гарах ба тэр бичвэрийг засаж чадах хүн бүр өөрийгөө
--    гэрээнд гарын үсэг зурах эрхтэй болгоно. eduge үүнийг нэг удаа төлсөн
--    (migrations/contract/00001_contract.sql:35-49) — давтахгүй.
--
-- 3. ХОЁР ТАЛЫН ХАРАГДАЦ НЬ САНГИЙН ДҮРЭМ, КОДЫН БИШ. Хүлээн авагч байгууллага
--    нь ӨӨР ТЕНАНТ. `document_parties`-д tenant_id (гаргагч) БА
--    counterparty_tenant_id (хүлээн авагч) хоёулаа байна; уншилтыг ХОЁР ЗӨВШӨӨРӨХ
--    (permissive) бодлого хариуцна. Стандарт `tenant_isolation` нь 00037-ийн
--    хэлбэрээрээ ҮГ ҮСГЭЭРЭЭ үлдэнэ — цөм хэзээ нэгэн өдөр давталтаа дахин
--    ажиллуулбал энэ бодлогыг дахин үүсгэхэд юу ч өөрчлөгдөхгүй. Хоёр дахь тал
--    нь ӨӨР НЭРТЭЙ бодлогод; permissive бодлогууд OR-оор нийлдэг тул тэр нь
--    үлдэнэ.
--
-- 4. ХҮЛЭЭН АВАГЧ ГЭРЭЭГ ХАРИУЛНА, ДАХИН БИЧИХГҮЙ. Бодлого нь мөрийг нээдэг,
--    багануудыг нээдэггүй — тиймээс баганы хөдөлгөөнгүй байдлыг trigger хамгаална.
--
-- ЭНЭ ФАЙЛ ӨӨРИЙН RLS БЛОКОО АВЧИРНА. Цөмийн 00029/00037-ийн давталт нэг л
-- удаа ажилласан; түүнээс хойш үүссэн хүснэгт өөрөө тавихгүй бол мөрийн
-- түвшинд хамгаалалтгүй үлдэнэ, харин бусадтай ижил харагдана.

-- +goose Up

-- ─────────────────────────────────────────────── document_records: гэрээний талбарууд
--
-- Бүгд NULL эсвэл анхдагчтай: амьд байрлуулалт дээрх мөр бүр яг өмнөх утгаараа
-- үлдэнэ. `signing_mode='internal'` нь өнөөдрийн зан төлөв.
ALTER TABLE document_records
    ADD COLUMN IF NOT EXISTS signing_mode        VARCHAR(16)  NOT NULL DEFAULT 'internal',
    -- Талуудын нэгтгэсэн төлөв. `status` руу шинэ утга нэмэхгүй: тэр баганыг
    -- nexus.FiledDocument уншдаг бөгөөд өөр репо дахь клиентүүд түүн дээр
    -- шилждэг. Гэрээний амьдрал өөр асуулт тул өөр багана.
    ADD COLUMN IF NOT EXISTS contract_state      VARCHAR(24)  NOT NULL DEFAULT 'NONE',
    ADD COLUMN IF NOT EXISTS contract_number     VARCHAR(64)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS effective_from      DATE,
    ADD COLUMN IF NOT EXISTS effective_to        DATE,
    ADD COLUMN IF NOT EXISTS amount              NUMERIC(20,2),
    ADD COLUMN IF NOT EXISTS currency            CHAR(3),
    -- Нэмэлт гэрээ / сунгалт. Өөрийн рүүгээ заасан FK: гинж нь мод, жагсаалт биш.
    ADD COLUMN IF NOT EXISTS parent_document_id  UUID REFERENCES document_records(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS created_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS due_at              TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS sent_at             TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS executed_at         TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS withdrawn_at        TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS terminated_at       TIMESTAMPTZ;

ALTER TABLE document_records
    ADD CONSTRAINT document_records_signing_mode_sane
        CHECK (signing_mode IN ('internal', 'counterpart', 'joint')),
    ADD CONSTRAINT document_records_contract_state_sane
        CHECK (contract_state IN ('NONE','DRAFT','SENT','PARTIALLY_SIGNED',
                                  'EXECUTED','DECLINED','WITHDRAWN','EXPIRED','TERMINATED')),
    -- Мөнгөн дүн байхад валют заавал: дүн нь өөрөө утгагүй.
    ADD CONSTRAINT document_records_amount_has_currency
        CHECK (amount IS NULL OR currency IS NOT NULL),
    ADD CONSTRAINT document_records_dates_ordered
        CHECK (effective_to IS NULL OR effective_from IS NULL OR effective_to >= effective_from);

CREATE INDEX IF NOT EXISTS idx_document_records_contract
    ON document_records (tenant_id, contract_state, created_at DESC)
    WHERE signing_mode <> 'internal';
CREATE INDEX IF NOT EXISTS idx_document_records_expiring
    ON document_records (tenant_id, effective_to)
    WHERE effective_to IS NOT NULL;

-- ─────────────────────────────────────────────── document_bodies: гэрээний бичвэр
--
-- Тусдаа хүснэгт, `document_records`-д багана биш: жагсаалтын query бүр
-- `documentColumns`-ыг уншдаг бөгөөд тэнд хэдэн арван килобайт TEXT байх нь
-- хуудас бүрийг тэр хэмжээгээр хүндрүүлнэ. Хүн гэрээ уншихдаа нэгийг л уншина.
CREATE TABLE IF NOT EXISTS document_bodies (
    document_id UUID PRIMARY KEY REFERENCES document_records(id) ON DELETE CASCADE,
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- Орлуулагчтай загварын бичвэр: {{тал}} {{регистр}} {{огноо}} г.м.
    -- Танигдахгүй орлуулагчийг ХЭВЭЭР үлдээнэ — алдаатай бичсэн нь дэлгэц дээр
    -- `{{албан_тушал}}` болж харагдах нь хоосон зай болж алга болохоос дээр.
    body        TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by  UUID REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_document_bodies_tenant ON document_bodies (tenant_id);

-- ─────────────────────────────────────────────── document_parties: гэрээний талууд
CREATE TABLE IF NOT EXISTS document_parties (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- ГАРГАГЧ. Бичих эрх нь эцсийн эцэст энэ талынх.
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    document_id UUID NOT NULL REFERENCES document_records(id) ON DELETE CASCADE,

    ordinal     SMALLINT NOT NULL,
    party_role  VARCHAR(16) NOT NULL,   -- issuer | counterparty | witness | guarantor
    party_kind  VARCHAR(24) NOT NULL,   -- member | tenant | peer | person | organisation

    -- Хуулийн этгээдийн байдал — гэрээн дээр ХЭВЛЭГДЭХ хэлбэрээрээ, ба
    -- илгээх агшинд ХӨЛДӨНӨ. Дараа нь профайл засагдвал гэрээ өөрчлөгдөх ёсгүй.
    display_name        VARCHAR(255) NOT NULL,
    legal_name          VARCHAR(255) NOT NULL DEFAULT '',
    registration_number VARCHAR(64)  NOT NULL DEFAULT '',
    address_line        VARCHAR(255) NOT NULL DEFAULT '',
    contact_email       VARCHAR(255) NOT NULL DEFAULT '',
    contact_phone       VARCHAR(64)  NOT NULL DEFAULT '',

    -- Тал хаана оршдог вэ. Гурвын аль нэг нь л, эсвэл аль нь ч биш (ринг ба
    -- энэ суулгацаас гадна хүн — түүнд токен ба PIN2-оор л хүрнэ).
    member_user_id         UUID REFERENCES users(id)   ON DELETE SET NULL,
    counterparty_tenant_id UUID REFERENCES tenants(id) ON DELETE SET NULL,
    peer_id                UUID,

    -- Энэ гэрээний БҮХ тал бие биенээ харна: хэн гарын үсэг зурж байгааг мэдэхгүй
    -- бол юунд нэгдэж байгаагаа мэдэхгүй. Массив нь илгээх агшинд бичигдэж,
    -- талууд хөлдсөний дараа хэзээ ч өөрчлөгдөхгүй.
    audience    UUID[] NOT NULL DEFAULT '{}',

    required    BOOLEAN  NOT NULL DEFAULT TRUE,
    sign_order  SMALLINT,                   -- зөвхөн 'joint'; NULL нь дараалалгүй

    state       VARCHAR(16) NOT NULL DEFAULT 'draft',
    -- draft → invited → viewed → signed | declined | withdrawn | expired

    invited_at  TIMESTAMPTZ,
    viewed_at   TIMESTAMPTZ,
    signed_at   TIMESTAMPTZ,
    declined_at TIMESTAMPTZ,
    withdrawn_at TIMESTAMPTZ,
    -- Татгалзал нь eID-ийн үйлдэл биш, бизнесийн шийдвэр. Шалтгаангүй
    -- татгалзал гаргагчид юу засахыг нь хэлэхгүй тул CHECK-ээр шаардана —
    -- кодоор биш: кодыг дараагийн хүн тойрч бичиж чадна.
    decline_reason TEXT NOT NULL DEFAULT '',

    -- Нээлттэй ёслол — бүртгэл биш, ХАМГААЛАЛТ. Хоёр цонхноос зэрэг эхлүүлсэн
    -- ёслол нэг нэгнийхээ дугаарыг дарж бичвэл эхнийх нь ЖИНХЭНЭ PIN2
    -- зөвшөөрөл өнчирч, хоёр дахийн нь poll түүнийг өөрийнхөөрөө бүртгэнэ.
    session_id VARCHAR(190),
    session_at TIMESTAMPTZ,
    session_by UUID REFERENCES users(id) ON DELETE SET NULL,

    added_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT document_parties_ordinal_unique   UNIQUE (document_id, ordinal),
    CONSTRAINT document_parties_ordinal_positive CHECK (ordinal >= 1),
    CONSTRAINT document_parties_role_sane  CHECK (party_role IN ('issuer','counterparty','witness','guarantor')),
    CONSTRAINT document_parties_kind_sane  CHECK (party_kind IN ('member','tenant','peer','person','organisation')),
    CONSTRAINT document_parties_state_sane CHECK (state IN ('draft','invited','viewed','signed','declined','withdrawn','expired')),
    -- Тал нэг л газар оршино.
    CONSTRAINT document_parties_one_home CHECK (
        (CASE WHEN member_user_id         IS NOT NULL THEN 1 ELSE 0 END) +
        (CASE WHEN counterparty_tenant_id IS NOT NULL THEN 1 ELSE 0 END) +
        (CASE WHEN peer_id                IS NOT NULL THEN 1 ELSE 0 END) <= 1),
    -- Өөртэйгөө гэрээ байгуулах нь утгагүй.
    CONSTRAINT document_parties_not_self CHECK (
        counterparty_tenant_id IS NULL OR counterparty_tenant_id <> tenant_id),
    CONSTRAINT document_parties_decline_reasoned CHECK (
        declined_at IS NULL OR btrim(decline_reason) <> ''),
    CONSTRAINT document_parties_order_only_joint CHECK (
        sign_order IS NULL OR sign_order >= 1)
);

-- Нэг хүн, нэг тал. '' нь хаяг биш тул хэсэгчилсэн индекс.
CREATE UNIQUE INDEX IF NOT EXISTS document_parties_one_per_registration
    ON document_parties (document_id, registration_number)
    WHERE registration_number <> '';
CREATE UNIQUE INDEX IF NOT EXISTS document_parties_one_per_counterparty
    ON document_parties (document_id, counterparty_tenant_id)
    WHERE counterparty_tenant_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_document_parties_document ON document_parties (document_id, ordinal);
CREATE INDEX IF NOT EXISTS idx_document_parties_tenant   ON document_parties (tenant_id, state);
-- «Надад ирсэн гэрээ» — хоёр индекс, хоёулаа хэсэгчилсэн: мөрийн дийлэнх нь
-- аль нэгийг нь ч агуулахгүй.
CREATE INDEX IF NOT EXISTS idx_document_parties_inbox_tenant
    ON document_parties (counterparty_tenant_id, state)
    WHERE counterparty_tenant_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_document_parties_inbox_user
    ON document_parties (member_user_id, state)
    WHERE member_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_document_parties_audience
    ON document_parties USING GIN (audience);
-- «Хугацаа хэтэрсэн» тайлангийн ганц query.
CREATE INDEX IF NOT EXISTS idx_document_parties_outstanding
    ON document_parties (tenant_id, invited_at)
    WHERE state IN ('invited','viewed');

-- ─────────────────────────────────────────────── document_party_signatories
--
-- Шийдвэр 2. Тал нь байгууллага бол ЭНЭ хүснэгт «дотор нь хэн» гэдэгт хариулна.
-- Хүлээн авагч байгууллага өөрөө бичнэ (доорх restrictive бодлого) — гаргагч
-- нөгөө талын хэн гарын үсэг зурахыг сонгодог бол хуулийн үүргийг нөгөө талын
-- өмнөөс хуваарилж байгаа хэрэг.
CREATE TABLE IF NOT EXISTS document_party_signatories (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    party_id    UUID NOT NULL REFERENCES document_parties(id) ON DELETE CASCADE,
    -- Доорх trigger нь эдгээрийг талын мөрөөс ХУУЛНА. Гараар бичих зам байхгүй:
    -- буруу хуулбарласан tenant нь бүхэл механизмыг нэг мөрөнд эвдэнэ.
    tenant_id              UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    counterparty_tenant_id UUID REFERENCES tenants(id) ON DELETE SET NULL,
    document_id            UUID NOT NULL REFERENCES document_records(id) ON DELETE CASCADE,

    user_id       UUID REFERENCES users(id) ON DELETE SET NULL,
    membership_id UUID,
    full_name     VARCHAR(255) NOT NULL DEFAULT '',
    position      VARCHAR(128) NOT NULL DEFAULT '',
    -- Ёслолыг ХАЯГЛАХ цорын ганц утга. Хоосон байж болно: томилох агшинд админ
    -- мэдэхгүй байж болно. Ёслол эхлэхэд хоосон бол ТАТГАЛЗАНА — хүсэлтийн
    -- биеэс авахгүй.
    reg_number    VARCHAR(64) NOT NULL DEFAULT '',
    -- Регистрийн дугаарыг хэн, хэзээ зарлав. Өөрөө зарласан дугаарыг PIN2
    -- баталгаажуулдаг ч, ХЭН гэдгийг мөр өөрөө хэлэх ёстой.
    reg_number_declared_by UUID REFERENCES users(id) ON DELETE SET NULL,
    reg_number_declared_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS document_party_signatories_one_per_reg
    ON document_party_signatories (party_id, reg_number) WHERE reg_number <> '';
CREATE UNIQUE INDEX IF NOT EXISTS document_party_signatories_one_per_user
    ON document_party_signatories (party_id, user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_document_party_signatories_party ON document_party_signatories (party_id);
CREATE INDEX IF NOT EXISTS idx_document_party_signatories_reg
    ON document_party_signatories (reg_number) WHERE reg_number <> '';

-- ─────────────────────────────────────────────── document_party_files
--
-- Шийдвэр 1. `document_files` нь `PRIMARY KEY (document_id)` — нэг баримт нэг
-- файл — тул тал тутмын хувь тэнд багтахгүй.
CREATE TABLE IF NOT EXISTS document_party_files (
    party_id    UUID PRIMARY KEY REFERENCES document_parties(id) ON DELETE CASCADE,
    tenant_id              UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    counterparty_tenant_id UUID REFERENCES tenants(id) ON DELETE SET NULL,
    document_id            UUID NOT NULL REFERENCES document_records(id) ON DELETE CASCADE,

    file_name    VARCHAR(255) NOT NULL,
    content_type VARCHAR(128) NOT NULL DEFAULT 'application/pdf',
    size_bytes   BIGINT NOT NULL,
    sha256       CHAR(64) NOT NULL,
    content      BYTEA NOT NULL,
    -- PDF-д орсон ЯГ ТЭР бичвэр. Платформ хариу бүрд `X-Frame-Options: DENY`
    -- явуулдаг тул PDF-ийг iframe-д харуулах боломжгүй — энэ багана нь
    -- дэлгэцийн цорын ганц шууд харагдац. Мөн хөлдсөн: шинээр орлуулсан
    -- бичвэр PDF-ээс зөрвөл хүн уншсанаасаа өөр зүйлд гарын үсэг зурна.
    body_text    TEXT NOT NULL DEFAULT '',
    signed_content BYTEA,
    signed_at      TIMESTAMPTZ,
    frozen_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT document_party_files_size_check CHECK (size_bytes > 0 AND size_bytes <= 26214400)
);
CREATE INDEX IF NOT EXISTS idx_document_party_files_document ON document_party_files (document_id);

-- ─────────────────────────────────────────────── document_invitations
--
-- Данснаас гадуурх талын цорын ганц хаалга. ТОКЕН ХАДГАЛАГДАХГҮЙ, зөвхөн
-- SHA-256 нь: сангийн dump нь ажиллаж байгаа гэрээний холбоосуудын багц байх
-- ёсгүй. Платформ session, төхөөрөмж, `urtuu_peers.token_hash` бүгдийг ингэж
-- хадгалдаг.
CREATE TABLE IF NOT EXISTS document_invitations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    document_id  UUID NOT NULL REFERENCES document_records(id) ON DELETE CASCADE,
    party_id     UUID NOT NULL REFERENCES document_parties(id) ON DELETE CASCADE,
    token_sha256 CHAR(64) NOT NULL UNIQUE,
    channel      VARCHAR(16) NOT NULL DEFAULT 'link',  -- link | email | sms | peer
    sent_to      VARCHAR(255) NOT NULL DEFAULT '',
    sent_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    sent_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    opened_at    TIMESTAMPTZ,
    open_count   INT NOT NULL DEFAULT 0,
    CONSTRAINT document_invitations_expiry_future CHECK (expires_at > sent_at)
);
CREATE INDEX IF NOT EXISTS idx_document_invitations_party ON document_invitations (party_id, sent_at DESC);
-- Нэг талд нэг л амьд токен. Хоёр байвал аль нь үйлчилж байгааг шийдэх дүрэм
-- байхгүй ба нэгийг нь цуцлах нь нөгөөг нь цуцлахгүй.
CREATE UNIQUE INDEX IF NOT EXISTS document_invitations_one_live
    ON document_invitations (party_id) WHERE revoked_at IS NULL;

-- ─────────────────────────────────────────────── document_party_events
--
-- Зөвхөн нэмэгддэг мөр (доор DELETE/UPDATE эрх ОЛГООГҮЙ). Тайлангийн түүхий
-- материал: «хэдэн хоног хүлээв», «хэдэн удаа сануулав», «хэн уншив».
CREATE TABLE IF NOT EXISTS document_party_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    party_id    UUID NOT NULL REFERENCES document_parties(id) ON DELETE CASCADE,
    tenant_id              UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    counterparty_tenant_id UUID REFERENCES tenants(id) ON DELETE SET NULL,
    document_id            UUID NOT NULL REFERENCES document_records(id) ON DELETE CASCADE,
    kind        VARCHAR(32) NOT NULL,
    -- sent | resent | opened | viewed | ceremony_started | signed | declined
    -- | withdrawn | reopened | expired | reminded
    actor_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    -- Токеноор ирсэн хүн бол хэрэглэгч биш; ямар суваг байсныг мөр өөрөө хэлнэ.
    actor_label VARCHAR(190) NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT '',
    at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_document_party_events_party ON document_party_events (party_id, at);
CREATE INDEX IF NOT EXISTS idx_document_party_events_tenant ON document_party_events (tenant_id, at DESC);

-- ─────────────────────────────────────────────── document_signatures: тал
ALTER TABLE document_signatures
    ADD COLUMN IF NOT EXISTS party_id UUID REFERENCES document_parties(id) ON DELETE CASCADE;
COMMENT ON COLUMN document_signatures.party_id IS
    'Гэрээний талын гарын үсэг. NULL бол дотоод зөвшөөрлийн дараалалд хамаарна — хоёр нь өөр асуулт, нэг баганаар тоологдох ёсгүй.';
CREATE INDEX IF NOT EXISTS idx_document_signatures_party
    ON document_signatures (party_id) WHERE party_id IS NOT NULL;

-- Хуучин хязгаарлалт нь нэг иргэнийг нэг баримт дээр дотоод зөвшөөрөл БА
-- талын гарын үсэг ХОЁУЛАНГ нь барихыг хориглодог байв. Хоёр хэсэгчилсэн
-- индекс нь юу гэсэн үг байсныг нь хэлнэ: хүн тутамд нэг зөвшөөрөл, тал
-- тутамд нэг гарын үсэг. Goose миграцыг нэг гүйлгээнд ажиллуулдаг тул
-- хаях ба үүсгэх нь атомик.
ALTER TABLE document_signatures DROP CONSTRAINT IF EXISTS document_signatures_once_per_signer;
CREATE UNIQUE INDEX IF NOT EXISTS document_signatures_one_approval_per_signer
    ON document_signatures (document_id, signer_reg_number) WHERE party_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS document_signatures_one_per_party
    ON document_signatures (document_id, party_id) WHERE party_id IS NOT NULL;

-- ─────────────────────────────────────────────── загвар нь бичвэртэй болов
ALTER TABLE document_templates
    ADD COLUMN IF NOT EXISTS body TEXT NOT NULL DEFAULT '';
COMMENT ON COLUMN document_templates.body IS
    'Гэрээний бичвэрийн загвар. Хоосон бол энэ загвар нь зөвхөн гарчиг өгнө — хуучин мөрүүд яг тэр байсан.';

-- ─────────────────────────────────────────────── нээлттэй ёслолын унтраалга
--
-- Өнөөдөр `startEIDSignature` регистрийн дугаарыг ХҮСЭЛТИЙН БИЕЭС авдаг бөгөөд
-- зөвшөөрлийн дараалал хэнийг ч нэрлээгүй бол ямар ч дугаарыг хүлээж авдаг.
-- Талтай баримт дээр энэ нь Үе 4-т хаагдана. Талгүй баримт дээр хаах нь амьд
-- байрлуулалтын зан төлөвийг өөрчлөх тул тенантын шийдвэр болгов —
-- анхдагч нь TRUE, өөрөөр хэлбэл өнөөдрийн зан төлөв.
ALTER TABLE document_signature_policies
    ADD COLUMN IF NOT EXISTS open_ceremony_allowed BOOLEAN NOT NULL DEFAULT TRUE;

-- ═══════════════════════════════════════════════ trigger-үүд

-- Хүүхэд хүснэгтүүдийн хамрах хүрээг ТАЛААС нь хуулна. Гараар бичих зам
-- байхгүй болсноор буруу хуулбарлагдсан tenant нь боломжгүй болно.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_party_scope() RETURNS TRIGGER AS $$
DECLARE owner UUID; other UUID; doc UUID;
BEGIN
    SELECT p.tenant_id, p.counterparty_tenant_id, p.document_id
      INTO owner, other, doc
      FROM document_parties p WHERE p.id = NEW.party_id;
    IF owner IS NULL THEN
        RAISE EXCEPTION 'no such party %', NEW.party_id USING ERRCODE = 'foreign_key_violation';
    END IF;
    NEW.tenant_id              := owner;
    NEW.counterparty_tenant_id := other;
    NEW.document_id            := doc;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS documents_scope_signatories ON document_party_signatories;
CREATE TRIGGER documents_scope_signatories BEFORE INSERT OR UPDATE ON document_party_signatories
    FOR EACH ROW EXECUTE FUNCTION documents_party_scope();
DROP TRIGGER IF EXISTS documents_scope_party_files ON document_party_files;
CREATE TRIGGER documents_scope_party_files BEFORE INSERT OR UPDATE ON document_party_files
    FOR EACH ROW EXECUTE FUNCTION documents_party_scope();
DROP TRIGGER IF EXISTS documents_scope_party_events ON document_party_events;
CREATE TRIGGER documents_scope_party_events BEFORE INSERT ON document_party_events
    FOR EACH ROW EXECUTE FUNCTION documents_party_scope();

-- Шийдвэр 4: хүлээн авагч гэрээг ХАРИУЛНА, ДАХИН БИЧИХГҮЙ.
-- Бодлого нь мөр нээдэг, багана нээдэггүй.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_party_answer_only() RETURNS TRIGGER AS $$
DECLARE acting UUID := NULLIF(current_setting('app.current_tenant', true), '')::uuid;
BEGIN
    IF acting IS NULL OR acting = OLD.tenant_id THEN
        RETURN NEW;                      -- гаргагч тал (эсвэл tenant-гүй платформын зам)
    END IF;
    IF NEW.tenant_id  IS DISTINCT FROM OLD.tenant_id
    OR NEW.document_id IS DISTINCT FROM OLD.document_id
    OR NEW.counterparty_tenant_id IS DISTINCT FROM OLD.counterparty_tenant_id
    OR NEW.peer_id     IS DISTINCT FROM OLD.peer_id
    OR NEW.audience    IS DISTINCT FROM OLD.audience
    OR NEW.party_role  IS DISTINCT FROM OLD.party_role
    OR NEW.party_kind  IS DISTINCT FROM OLD.party_kind
    OR NEW.ordinal     IS DISTINCT FROM OLD.ordinal
    OR NEW.required    IS DISTINCT FROM OLD.required
    OR NEW.sign_order  IS DISTINCT FROM OLD.sign_order
    OR NEW.display_name IS DISTINCT FROM OLD.display_name
    OR NEW.legal_name   IS DISTINCT FROM OLD.legal_name
    OR NEW.registration_number IS DISTINCT FROM OLD.registration_number
    OR NEW.created_at   IS DISTINCT FROM OLD.created_at
    THEN
        RAISE EXCEPTION 'a counterparty answers a contract, it does not rewrite one (party %)', OLD.id
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS documents_parties_answer_only ON document_parties;
CREATE TRIGGER documents_parties_answer_only BEFORE UPDATE ON document_parties
    FOR EACH ROW EXECUTE FUNCTION documents_party_answer_only();

-- Хөлдсөн байтыг хэн ч хөдөлгөхгүй; хүлээн авагч зөвхөн зурагдсан хувиа
-- буцаана.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_party_file_answer_only() RETURNS TRIGGER AS $$
DECLARE acting UUID := NULLIF(current_setting('app.current_tenant', true), '')::uuid;
BEGIN
    -- Хөлдсөн байт нь ХЭНД Ч өөрчлөгдөхгүй, гаргагчид ч. Дахин илгээх нь
    -- мөрийг УСТГААД шинээр үүсгэнэ — тэр нь тодорхой үйлдэл.
    IF NEW.content   IS DISTINCT FROM OLD.content
    OR NEW.sha256    IS DISTINCT FROM OLD.sha256
    OR NEW.body_text IS DISTINCT FROM OLD.body_text
    OR NEW.frozen_at IS DISTINCT FROM OLD.frozen_at
    THEN
        RAISE EXCEPTION 'the bytes a party was shown are the bytes they sign (party %)', OLD.party_id
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF acting IS NULL OR acting = OLD.tenant_id THEN RETURN NEW; END IF;
    IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.counterparty_tenant_id IS DISTINCT FROM OLD.counterparty_tenant_id
    OR NEW.file_name IS DISTINCT FROM OLD.file_name
    OR NEW.size_bytes IS DISTINCT FROM OLD.size_bytes
    THEN
        RAISE EXCEPTION 'a counterparty may return a signed copy, nothing else (party %)', OLD.party_id
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS documents_party_files_answer_only ON document_party_files;
CREATE TRIGGER documents_party_files_answer_only BEFORE UPDATE ON document_party_files
    FOR EACH ROW EXECUTE FUNCTION documents_party_file_answer_only();

-- ═══════════════════════════════════════════════ RLS

-- Гаргагчийн талд байх хүснэгтүүд: 00037-ийн хэлбэр, үг үсгээрээ.
-- +goose StatementBegin
DO $$
DECLARE t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['document_bodies', 'document_invitations'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE  ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format($p$
            CREATE POLICY tenant_isolation ON %I TO gerege_nexus_app
                USING (tenant_id IS NULL OR tenant_id = ANY (COALESCE(
                    NULLIF(current_setting('app.allowed_tenants', true), '')::uuid[],
                    ARRAY[NULLIF(current_setting('app.current_tenant', true), '')::uuid])))
                WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
        $p$, t);
    END LOOP;
END $$;
-- +goose StatementEnd

-- Хоёр талын хүснэгтүүд. `tenant_isolation` нь ДЭЭРХТЭЙ ЯГ ИЖИЛ хэвээр —
-- цөм давталтаа дахин ажиллуулбал энэ бодлогыг дахин үүсгэх нь юуг ч
-- өөрчлөхгүй. Хоёр дахь тал нь ӨӨР НЭРТЭЙ бодлогод амьдарна.
-- +goose StatementBegin
DO $$
DECLARE t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'document_parties', 'document_party_signatories', 'document_party_events'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE  ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format($p$
            CREATE POLICY tenant_isolation ON %I TO gerege_nexus_app
                USING (tenant_id IS NULL OR tenant_id = ANY (COALESCE(
                    NULLIF(current_setting('app.allowed_tenants', true), '')::uuid[],
                    ARRAY[NULLIF(current_setting('app.current_tenant', true), '')::uuid])))
                WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
        $p$, t);
        EXECUTE format('DROP POLICY IF EXISTS counterparty_visibility ON %I', t);
        EXECUTE format($p$
            CREATE POLICY counterparty_visibility ON %I TO gerege_nexus_app
                USING (counterparty_tenant_id = ANY (COALESCE(
                    NULLIF(current_setting('app.allowed_tenants', true), '')::uuid[],
                    ARRAY[NULLIF(current_setting('app.current_tenant', true), '')::uuid])))
                WITH CHECK (counterparty_tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
        $p$, t);
    END LOOP;
END $$;
-- +goose StatementEnd

-- Гэрээний БҮХ тал бие биенээ ХАРНА (уншина л, бичихгүй).
DROP POLICY IF EXISTS parties_see_each_other ON document_parties;
CREATE POLICY parties_see_each_other ON document_parties FOR SELECT TO gerege_nexus_app
    USING (NULLIF(current_setting('app.current_tenant', true), '')::uuid = ANY (audience));

-- Тал ҮҮСГЭХ нь ИЛГЭЭХ үйлдэл, түүнийг зөвхөн илгээгч хийнэ.
DROP POLICY IF EXISTS parties_are_created_by_the_issuer ON document_parties;
CREATE POLICY parties_are_created_by_the_issuer ON document_parties
    AS RESTRICTIVE FOR INSERT TO gerege_nexus_app
    WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);

-- Байгууллага өөрөө хэн гарын үсэг зурахаа шийднэ (шийдвэр 2).
DROP POLICY IF EXISTS signatories_belong_to_their_party ON document_party_signatories;
CREATE POLICY signatories_belong_to_their_party ON document_party_signatories
    AS RESTRICTIVE FOR ALL TO gerege_nexus_app
    USING (true)
    WITH CHECK (
        counterparty_tenant_id IS NULL
        OR counterparty_tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);

-- Файлын агуулга нь `document_files`-ийнхтэй ижил хатуу: ЗӨВХӨН идэвхтэй
-- байгууллага, зөвшөөрөгдсөн бүх байгууллага биш. Хамгийн нарийн мэдээлэл
-- хамгийн нарийн бодлоготой.
ALTER TABLE document_party_files ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_party_files FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON document_party_files;
CREATE POLICY tenant_isolation ON document_party_files TO gerege_nexus_app
    USING      (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
DROP POLICY IF EXISTS counterparty_visibility ON document_party_files;
CREATE POLICY counterparty_visibility ON document_party_files TO gerege_nexus_app
    USING      (counterparty_tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
    WITH CHECK (counterparty_tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON
    document_bodies, document_parties, document_party_signatories,
    document_party_files, document_invitations
TO gerege_nexus_app;
-- Зөвхөн нэмэгддэг: «энэ гэрээ хэнд, хэзээ очив» гэдэг устгагдвал хариулт нь алга.
GRANT SELECT, INSERT ON document_party_events TO gerege_nexus_app;

-- +goose Down

DROP TRIGGER IF EXISTS documents_party_files_answer_only ON document_party_files;
DROP TRIGGER IF EXISTS documents_parties_answer_only ON document_parties;
DROP TRIGGER IF EXISTS documents_scope_party_events ON document_party_events;
DROP TRIGGER IF EXISTS documents_scope_party_files ON document_party_files;
DROP TRIGGER IF EXISTS documents_scope_signatories ON document_party_signatories;
DROP FUNCTION IF EXISTS documents_party_file_answer_only();
DROP FUNCTION IF EXISTS documents_party_answer_only();
DROP FUNCTION IF EXISTS documents_party_scope();

DROP INDEX IF EXISTS document_signatures_one_per_party;
DROP INDEX IF EXISTS document_signatures_one_approval_per_signer;
ALTER TABLE document_signatures DROP COLUMN IF EXISTS party_id;
-- Хуучин хязгаарлалтыг буцааж тавихыг ЗОРИУДААР оролдохгүй: талын гарын үсэг
-- бичигдсэн байвал тэр нь унана, ба буцаалт амжилттай мэт харагдах ёсгүй.

DROP TABLE IF EXISTS document_party_events;
DROP TABLE IF EXISTS document_invitations;
DROP TABLE IF EXISTS document_party_files;
DROP TABLE IF EXISTS document_party_signatories;
DROP TABLE IF EXISTS document_parties;
DROP TABLE IF EXISTS document_bodies;

ALTER TABLE document_signature_policies DROP COLUMN IF EXISTS open_ceremony_allowed;
ALTER TABLE document_templates DROP COLUMN IF EXISTS body;
ALTER TABLE document_records
    DROP CONSTRAINT IF EXISTS document_records_dates_ordered,
    DROP CONSTRAINT IF EXISTS document_records_amount_has_currency,
    DROP CONSTRAINT IF EXISTS document_records_contract_state_sane,
    DROP CONSTRAINT IF EXISTS document_records_signing_mode_sane;
ALTER TABLE document_records
    DROP COLUMN IF EXISTS terminated_at, DROP COLUMN IF EXISTS withdrawn_at,
    DROP COLUMN IF EXISTS executed_at,   DROP COLUMN IF EXISTS sent_at,
    DROP COLUMN IF EXISTS due_at,        DROP COLUMN IF EXISTS created_by,
    DROP COLUMN IF EXISTS parent_document_id, DROP COLUMN IF EXISTS currency,
    DROP COLUMN IF EXISTS amount,        DROP COLUMN IF EXISTS effective_to,
    DROP COLUMN IF EXISTS effective_from, DROP COLUMN IF EXISTS contract_number,
    DROP COLUMN IF EXISTS contract_state, DROP COLUMN IF EXISTS signing_mode;