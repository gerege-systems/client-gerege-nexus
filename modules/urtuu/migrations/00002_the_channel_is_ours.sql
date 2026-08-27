-- Суваг нь аппынх боллоо: зургаан хүснэгт.
--
-- 00001 нь даалгаврын самбарыг авчирсан бөгөөд «юу энд байхгүй вэ» гэсэн
-- хэсэгтээ яг эдгээрийг нэрлээд «тэдгээр нь сувгийнх — холбоос, гарын үсэг,
-- дараалал, дахин оролдлого — бөгөөд платформынх хэвээр» гэж бичсэн. Тэр
-- заагийн үндэслэл нэг л таамаг дээр тогтож байсан: сувгийг олон апп
-- ашиглана. Гурван сарын дараа ч ганц ашиглагчтай хэвээр — энэ апп — тул
-- тээвэр нь цөмөөс гарч modules/urtuu/channel болов. Цөмийн 00087 нь эдгээрийг
-- хаяж, энэ файл эзэмшлийг нь авна.
--
-- Хэлбэр нь цөмийн 00061 (peers, outbox, deliveries, inbox), 00062
-- (request_codes, peer_codes), 00067 (outbox.created_at) ба 00073 (RLS-ийн
-- шинэ хэлбэр) дөрвийн ЭЦСИЙН дүн. Ажилласан миграцыг дахин бичих боломжгүй
-- тул 00001-ийн яг тэр арга: бүгд `IF NOT EXISTS`, учир нь энэ түүх нь
-- цөмийн хуулбарыг аль хэдийн үүрсэн өгөгдлийн сан дээр ажиллана.
--
-- Ролийн нэрийг ЗААХГҮЙ, ХАЙНА. Цөмийн 00079 нь gerege_nexus_app-ыг
-- gerege_nexus_tenant болгож нэрлэсэн бөгөөд шинэ өгөгдлийн сан зөвхөн
-- сүүлийнхийг авна. Нэрлэсэн миграц нь хуучин кластерт ажиллаж, цэвэр
-- кластерт `role does not exist` гэж унана — 2026-08-26-нд энэ репогийн
-- гурван миграц яг үүгээр CI-д улаан болсон. NULL үед (superuser test
-- холболт) grant алгасагдана.

-- +goose Up

-- Холбоос. Гурван талын гэрээ: нэр, нөгөө талын нийтийн түлхүүр, token-ы hash.
CREATE TABLE IF NOT EXISTS urtuu_peers (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name               TEXT        NOT NULL DEFAULT '',
    -- Аль суулгацын түлхүүрээр байгуулагдав. URTUU_SIGNING_KEY солигдвол
    -- хуучин холбоосууд ажиллахаа болих бөгөөд энэ багана байхгүй бол тэд
    -- чимээгүй унтарна; байвал аль нь болохыг нэрлэж чадна.
    installation_id    TEXT        NOT NULL DEFAULT '',
    -- ЭНЭ холбоос дээр БИД хэн бэ. Мод биш, чиглэлтэй граф.
    role               TEXT        NOT NULL,
    base_url           TEXT        NOT NULL DEFAULT '',
    peer_public_key    TEXT        NOT NULL DEFAULT '',
    -- Тээврийн token-ий SHA-256. Түүхий token нь нэг л удаа харагдана.
    token_hash         CHAR(64)    NOT NULL DEFAULT '',
    status             TEXT        NOT NULL DEFAULT 'pending',
    -- Нэг удаагийн урилгын кодын SHA-256 (24 цаг), хэрэглэгдмэгц NULL.
    invite_code_hash   CHAR(64),
    invite_expires_at  TIMESTAMPTZ,
    last_seen_at       TIMESTAMPTZ,
    last_error         TEXT        NOT NULL DEFAULT '',
    clock_skew_seconds INTEGER     NOT NULL DEFAULT 0,
    revoked_at         TIMESTAMPTZ,
    created_by         UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT urtuu_peers_role_check   CHECK (role IN ('parent', 'child')),
    CONSTRAINT urtuu_peers_status_check CHECK (status IN ('pending', 'active', 'revoked')),
    CONSTRAINT urtuu_peers_child_has_base_url
        CHECK (role <> 'child' OR status <> 'active' OR base_url <> '')
);

-- Token-оор холбоос олох нь тенантаас ӨМНӨ болдог тул индекс нь глобал.
CREATE UNIQUE INDEX IF NOT EXISTS idx_urtuu_peers_token
    ON urtuu_peers (token_hash) WHERE token_hash <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_urtuu_peers_invite
    ON urtuu_peers (invite_code_hash) WHERE invite_code_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_urtuu_peers_tenant
    ON urtuu_peers (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_urtuu_peers_dialable
    ON urtuu_peers (installation_id, role, status) WHERE revoked_at IS NULL;

-- Гарах дугтуй. Нэг дугтуй нэг л удаа гарын үсэг зурагдана, хэдэн ч холбоос
-- руу явахаас үл хамааран.
CREATE TABLE IF NOT EXISTS urtuu_outbox (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    message_id TEXT        NOT NULL,
    kind       TEXT        NOT NULL,
    -- Дугтуйн доторх created_at — гарын үсэгт багтсан утга, мөр үүссэн цаг
    -- биш. 00067-оор TEXT: гарын үсгийн вход нь байт мөр бөгөөд timestamptz-
    -- ээр буцаан уншихад RFC 3339-ийн бичиглэл өөрчлөгдөж, гарын үсэг унана.
    created_at TEXT        NOT NULL,
    payload    TEXT        NOT NULL,
    signature  TEXT        NOT NULL,
    queued_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT urtuu_outbox_message_unique UNIQUE (tenant_id, message_id)
);

-- Хүргэлт: дугтуй бүр холбоос бүрд нэг мөр, retry-тэй.
CREATE TABLE IF NOT EXISTS urtuu_deliveries (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    outbox_id       UUID        NOT NULL REFERENCES urtuu_outbox(id) ON DELETE CASCADE,
    peer_id         UUID        NOT NULL REFERENCES urtuu_peers(id) ON DELETE CASCADE,
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error      TEXT        NOT NULL DEFAULT '',
    -- Нөгөө тал БАТАЛСАН агшин, илгээсэн агшин биш.
    delivered_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT urtuu_deliveries_once UNIQUE (outbox_id, peer_id)
);

CREATE INDEX IF NOT EXISTS idx_urtuu_deliveries_due
    ON urtuu_deliveries (peer_id, next_attempt_at) WHERE delivered_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_urtuu_deliveries_tenant
    ON urtuu_deliveries (tenant_id, created_at DESC);

-- Ирсэн дугтуй. message_id давхардвал хоёр дахь нь хаягдана — идемпотент
-- хүлээн авалтын гол механизм.
CREATE TABLE IF NOT EXISTS urtuu_inbox (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    peer_id      UUID        NOT NULL REFERENCES urtuu_peers(id) ON DELETE CASCADE,
    message_id   TEXT        NOT NULL,
    kind         TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    payload      TEXT        NOT NULL,
    signature    TEXT        NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    acked_at     TIMESTAMPTZ,
    -- Уншигч (энэ апп) даалгавар болгосон агшин. NULL нь «хүлээгдэж байна»:
    -- уншигчгүй суулгац дугтуйг хүлээн авч, хадгалж, баталгаажуулсаар байна.
    processed_at TIMESTAMPTZ,

    CONSTRAINT urtuu_inbox_message_unique UNIQUE (tenant_id, message_id)
);

CREATE INDEX IF NOT EXISTS idx_urtuu_inbox_unprocessed
    ON urtuu_inbox (tenant_id, received_at) WHERE processed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_urtuu_inbox_unacked
    ON urtuu_inbox (peer_id, received_at) WHERE acked_at IS NULL;

-- Хүсэлтийн кодын толь. `local.` угтвар нь схемийн шалгалт: энд зохиосон код
-- ring-ийн нэрийн орон зайг булааж чадахгүй.
CREATE TABLE IF NOT EXISTS urtuu_request_codes (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code             TEXT        NOT NULL,
    -- 7 хэл. Сервер эзэмшдэг агуулга тул орчуулга нь кодтойгоо хамт аялна —
    -- доод тал кодыг синклэхдээ нэрийг нь ч авна.
    names            JSONB       NOT NULL DEFAULT '{}'::jsonb,
    -- Даалгаврын биеийн JSON Schema. Форм үүнээс үүсч, бөглөсөн зүйл нь
    -- үүгээр шалгагдана.
    schema           JSONB       NOT NULL DEFAULT '{}'::jsonb,
    -- NULL нь «энэ код норм заагаагүй»; INTERVAL 0 нь «хугацаагүй» гэсэн
    -- өөр утга.
    default_sla      INTERVAL,
    -- 00065-аар нэмэгдсэн: код нь шугамаа өөрөө хэлнэ, үүсгэгч хүн биш.
    line             TEXT        NOT NULL DEFAULT 'assignment',
    source           TEXT        NOT NULL,
    -- Холбоос цуцлагдвал түүний зарласан кодууд хамт алга болно.
    source_peer_id   UUID        REFERENCES urtuu_peers(id) ON DELETE CASCADE,
    ring_process_ref TEXT        NOT NULL DEFAULT '',
    -- Эх сурвалж дээрх хувилбар: хоцорсон зарлал шинэ тодорхойлолтыг
    -- буцаахгүй.
    version          INTEGER     NOT NULL DEFAULT 1,
    active           BOOLEAN     NOT NULL DEFAULT TRUE,
    created_by       UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT urtuu_request_codes_source_check
        CHECK (source IN ('ring', 'link', 'local')),
    CONSTRAINT urtuu_request_codes_line_check
        CHECK (line IN ('service', 'assignment')),
    -- ЭНД зохиогдсон код заавал 'local.' угтвартай. Урвуу нь үнэн БИШ:
    -- дээд тал өөрийн local. кодоо доод руугаа нээж болох бөгөөд тэр код
    -- доод тал дээр source='link' боловч угтвартайгаа хэвээр ирнэ — код нь
    -- хоёр суулгац дээр ижил нэртэй байх ёстой.
    CONSTRAINT urtuu_request_codes_local_namespace
        CHECK (source <> 'local' OR code LIKE 'local.%'),
    CONSTRAINT urtuu_request_codes_link_has_peer
        CHECK ((source = 'link') = (source_peer_id IS NOT NULL)),
    CONSTRAINT urtuu_request_codes_unique UNIQUE (tenant_id, code)
);

CREATE INDEX IF NOT EXISTS idx_urtuu_request_codes_active
    ON urtuu_request_codes (tenant_id, source) WHERE active;

-- Холбоос бүрд нээгдсэн кодууд. Код дээрх массив биш тусдаа хүснэгт болсон
-- шалтгаан: холбоос устахад нээлт нь хамт устах ёстой бөгөөд үүнийг гадаад
-- түлхүүр үнэгүй хийж өгнө.
CREATE TABLE IF NOT EXISTS urtuu_peer_codes (
    tenant_id UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    peer_id   UUID        NOT NULL REFERENCES urtuu_peers(id) ON DELETE CASCADE,
    code      TEXT        NOT NULL,
    opened_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    opened_by UUID        REFERENCES users(id) ON DELETE SET NULL,

    PRIMARY KEY (peer_id, code),
    -- Зөвхөн энэ тенантад бүртгэлтэй кодыг нээж болно.
    CONSTRAINT urtuu_peer_codes_known
        FOREIGN KEY (tenant_id, code) REFERENCES urtuu_request_codes (tenant_id, code)
        ON DELETE CASCADE
);

-- 00001-ийн хоёр гадаад түлхүүр, эцэст нь.
--
-- urtuu_tasks.origin_peer_id ба target_peer_id нь urtuu_peers руу заадаг
-- бөгөөд тэр хүснэгт цөмийнх байхад 00001 нь түүнийг иш татаж чаддаг байв.
-- Одоо ЭНЭ файл үүсгэдэг тул 00001-д REFERENCES байж болохгүй — цэвэр
-- өгөгдлийн сан дээр байхгүй хүснэгт рүү заана. Тиймээс энд, хүснэгт
-- үүссэний дараа нэмнэ. Аль хэдийн байгаа суулгац дээр (00001 нь FK-тайгаа
-- ажилласан) дахин нэмэхгүй.
-- +goose StatementBegin
DO $fk$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'urtuu_tasks_origin_peer_fk') THEN
        ALTER TABLE urtuu_tasks ADD CONSTRAINT urtuu_tasks_origin_peer_fk
            FOREIGN KEY (origin_peer_id) REFERENCES urtuu_peers(id) ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'urtuu_tasks_target_peer_fk') THEN
        ALTER TABLE urtuu_tasks ADD CONSTRAINT urtuu_tasks_target_peer_fk
            FOREIGN KEY (target_peer_id) REFERENCES urtuu_peers(id) ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'urtuu_task_events_actor_peer_fk') THEN
        ALTER TABLE urtuu_task_events ADD CONSTRAINT urtuu_task_events_actor_peer_fk
            FOREIGN KEY (actor_peer_id) REFERENCES urtuu_peers(id) ON DELETE SET NULL;
    END IF;
END
$fk$;
-- +goose StatementEnd

-- Тенант тусгаарлалт, 00073-ийн хэлбэрээр: уншихдаа session-ий зөвшөөрөгдсөн
-- байгууллагууд, бичихдээ зөвхөн идэвхтэй нэг нь.
-- +goose StatementBegin
DO $rls$
DECLARE
    target TEXT;
    app_role TEXT := (SELECT rolname FROM pg_roles
                       WHERE rolname IN ('gerege_nexus_tenant', 'gerege_nexus_app')
                       ORDER BY (rolname = 'gerege_nexus_tenant') DESC LIMIT 1);
BEGIN
    FOREACH target IN ARRAY ARRAY[
        'urtuu_peers', 'urtuu_outbox', 'urtuu_deliveries', 'urtuu_inbox',
        'urtuu_request_codes', 'urtuu_peer_codes'
    ] LOOP
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
END
$rls$;
-- +goose StatementEnd

-- Хяналтын хавтгай зөвхөн уншина, цөмийн 00064-ийн адилаар: оператор
-- хүргэгдээгүй дугтуйн тоо, дуугүй холбоосыг хардаг, АГУУЛГЫГ нь харахгүй.
-- Тиймээс энд зөвхөн peers ба deliveries нэрлэгдэв — inbox, outbox хоёр нь
-- юу яригдсаныг агуулна.
-- +goose StatementBegin
DO $op$
DECLARE target TEXT;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gerege_nexus_operator') THEN
        RETURN;
    END IF;
    FOREACH target IN ARRAY ARRAY['urtuu_peers', 'urtuu_deliveries'] LOOP
        EXECUTE format('DROP POLICY IF EXISTS operator_read ON %I', target);
        EXECUTE format('CREATE POLICY operator_read ON %I FOR SELECT '
                       'TO gerege_nexus_operator USING (true)', target);
        EXECUTE format('GRANT SELECT ON %I TO gerege_nexus_operator', target);
    END LOOP;
END
$op$;
-- +goose StatementEnd

-- +goose Down

DROP TABLE IF EXISTS urtuu_peer_codes;
DROP TABLE IF EXISTS urtuu_request_codes;
DROP TABLE IF EXISTS urtuu_inbox;
DROP TABLE IF EXISTS urtuu_deliveries;
DROP TABLE IF EXISTS urtuu_outbox;
DROP TABLE IF EXISTS urtuu_peers;
