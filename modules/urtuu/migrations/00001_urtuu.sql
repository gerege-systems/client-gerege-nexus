-- Өртөөгийн даалгаврын самбар: аппын өөрийн схем.
--
-- Платформын түүхээс зөөгдөв. `urtuu_tasks` ба `urtuu_task_events` нь цөмийн
-- 00063-д үүсч, 00065 (хоёр шугам), 00066 (дугаарлалт), 00073 (RLS-ийн шинэ
-- хэлбэр) нөхөж бичсэн; `urtuu_numbers` нь 00066-д. Аль нь ч байснаараа
-- зөөгдөж чадахгүй — хэрэглэгдсэн миграцыг дахин бичих боломжгүй — тул энд
-- эцсийн хэлбэрээр нь дахин зарлав, organisation-ийн 00001-ийн яг тэр арга.
--
-- Юу энд байхгүй вэ: `urtuu_peers`, `urtuu_deliveries`, `urtuu_inbox`,
-- `urtuu_outbox`, `urtuu_request_codes`, `urtuu_peer_codes`. Тэдгээр нь
-- сувгийнх — холбоос, гарын үсэг, дараалал, дахин оролдлого — бөгөөд энэ
-- файл бичигдэх үед платформынх байв. 2026-08-27-нд суваг ч аппд ирсэн тул
-- тэдгээр нь одоо **00002**-т байна; энэ файлаас хойш үүсдэг гэдэг нь доорх
-- гурван гадаад түлхүүр яагаад тэнд байгаагийн шалтгаан.
--
-- Бүгд `IF NOT EXISTS`: энэ түүх нь платформын хуулбарыг аль хэдийн үүрсэн
-- өгөгдлийн сан дээр ажиллана.

-- +goose Up

CREATE TABLE IF NOT EXISTS urtuu_tasks (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code             TEXT NOT NULL,
    line             TEXT NOT NULL DEFAULT 'assignment',
    title            TEXT NOT NULL DEFAULT '',
    payload          JSONB NOT NULL DEFAULT '{}'::jsonb,
    applicant        JSONB NOT NULL DEFAULT '{}'::jsonb,
    answer           TEXT NOT NULL DEFAULT '',
    number           VARCHAR(32) NOT NULL DEFAULT '',
    origin_number    VARCHAR(32) NOT NULL DEFAULT '',
    -- Хоёр талын аль нэг нь: ирсэн ажил origin_peer_id-тэй, өгсөн ажил
    -- target_peer_id-тэй, дотоод ажил хоёулангүй. Гуравдагч байдал байхгүйг
    -- urtuu_tasks_one_direction барина.
    -- Гадаад түлхүүр нь энд БИШ, 00002-т. Сувгийн `urtuu_peers` нь цөмийнх
    -- байхад энэ файл бичигдсэн тул REFERENCES нь энд байсан; 00087-оор суваг
    -- аппд ирснээр тэр хүснэгт 00002-т үүсдэг болсон — өөрөөр хэлбэл ЭНЭ
    -- файлаас ХОЙШ. Цэвэр өгөгдлийн сан дээр 00001 нь байхгүй хүснэгтийг иш
    -- татаж унана.
    origin_peer_id   UUID,
    origin_task_id   TEXT NOT NULL DEFAULT '',
    target_peer_id   UUID,
    parent_task_id   UUID REFERENCES urtuu_tasks(id) ON DELETE CASCADE,
    origin_chain     TEXT[] NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT 'RECEIVED',
    deadline         TIMESTAMPTZ,
    assigned_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    note             TEXT NOT NULL DEFAULT '',
    evidence         JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_by       UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT urtuu_tasks_status_check CHECK (status IN
        ('RECEIVED','ACCEPTED','IN_PROGRESS','DELEGATED','COMPLETED','RETURNED','CLOSED')),
    CONSTRAINT urtuu_tasks_line_check CHECK (line IN ('service','assignment')),
    CONSTRAINT urtuu_tasks_one_direction CHECK (origin_peer_id IS NULL OR target_peer_id IS NULL),
    -- Толин тусгал бол хүүхдийн бичлэг: эцэггүй толь нь хэний ч мэдэхгүй ажил.
    CONSTRAINT urtuu_tasks_mirror_has_parent CHECK (target_peer_id IS NULL OR parent_task_id IS NOT NULL),
    -- Иргэнд өгөх хариу. Үйлчилгээний шугамын ажил хариугүйгээр дуусахгүй —
    -- өгсөн салбарынх нь өөр асуудал тул тэр нь чөлөөлөгдөнө.
    CONSTRAINT urtuu_tasks_service_has_answer CHECK (
        line <> 'service' OR target_peer_id IS NOT NULL
        OR status NOT IN ('COMPLETED','CLOSED') OR answer <> ''),
    CONSTRAINT urtuu_tasks_service_has_applicant CHECK (
        line <> 'service' OR applicant <> '{}'::jsonb)
);

CREATE INDEX IF NOT EXISTS idx_urtuu_tasks_line
    ON urtuu_tasks(tenant_id, line, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_urtuu_tasks_incoming
    ON urtuu_tasks(tenant_id, status, created_at DESC) WHERE origin_peer_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_urtuu_tasks_deadline
    ON urtuu_tasks(tenant_id, deadline) WHERE deadline IS NOT NULL AND status <> 'CLOSED';
CREATE INDEX IF NOT EXISTS idx_urtuu_tasks_tree
    ON urtuu_tasks(parent_task_id) WHERE parent_task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_urtuu_tasks_mirror
    ON urtuu_tasks(target_peer_id, id) WHERE target_peer_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_urtuu_tasks_applicant
    ON urtuu_tasks(((applicant->>'registry_number')))
    WHERE line = 'service' AND (applicant->>'registry_number') IS NOT NULL;
-- Нэг дугтуй хоёр удаа ирвэл хоёр даалгавар болохгүй: идемпотент хүлээн авалт
-- бол схемийн хариулт, кодын биш.
CREATE UNIQUE INDEX IF NOT EXISTS idx_urtuu_tasks_from_peer
    ON urtuu_tasks(origin_peer_id, origin_task_id) WHERE origin_peer_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_urtuu_tasks_number
    ON urtuu_tasks(tenant_id, number) WHERE number <> '';

CREATE TABLE IF NOT EXISTS urtuu_task_events (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    task_id       UUID NOT NULL REFERENCES urtuu_tasks(id) ON DELETE CASCADE,
    from_status   TEXT NOT NULL DEFAULT '',
    to_status     TEXT NOT NULL,
    -- Нэг үйлдлийг хүн эсвэл нөгөө суулгац хийнэ, хоёулаа биш.
    actor_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    -- 00002-т FK болно, дээрх хоёрын адил шалтгаанаар.
    actor_peer_id UUID,
    note          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_urtuu_task_events_task ON urtuu_task_events(task_id, created_at);

-- Жилийн дугаарлалт. Мөр нь тоолуур тул шинэчлэлт нь мөрийн түгжээгээр
-- цувдаг: хоёр даалгавар нэг дугаар авахгүй.
CREATE TABLE IF NOT EXISTS urtuu_numbers (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    line      TEXT NOT NULL,
    year      INTEGER NOT NULL,
    next      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, line, year),
    CONSTRAINT urtuu_numbers_line_check CHECK (line IN ('service','assignment'))
);

-- Тенант тусгаарлалт, платформын 00037-ийн хэлбэрээр: уншихдаа session-ий
-- зөвшөөрөгдсөн байгууллагууд, бичихдээ зөвхөн идэвхтэй нэг нь.
ALTER TABLE urtuu_tasks        ENABLE ROW LEVEL SECURITY;
ALTER TABLE urtuu_tasks        FORCE  ROW LEVEL SECURITY;
ALTER TABLE urtuu_task_events  ENABLE ROW LEVEL SECURITY;
ALTER TABLE urtuu_task_events  FORCE  ROW LEVEL SECURITY;
ALTER TABLE urtuu_numbers      ENABLE ROW LEVEL SECURITY;
ALTER TABLE urtuu_numbers      FORCE  ROW LEVEL SECURITY;

-- Тенантын ролийг нэрлэхийн оронд хайна: платформын 00029 түүнийг
-- `gerege_nexus_app` нэрээр үүсгэсэн, цөмийн 00079 (v1.14.0) `gerege_nexus_tenant`
-- болгосон, шинэ өгөгдлийн сан дээр хуучин нэр огт үүсэхгүй. Аль нэгийг нь
-- бататгасан миграц нөгөө талдаа `role ... does not exist` гэж унана.
-- `gerege_nexus_operator` доор нэрээрээ хэвээр — тэр нэр солигдоогүй.
-- +goose StatementBegin
DO $rls$
DECLARE app_role TEXT;
BEGIN
    SELECT rolname INTO app_role FROM pg_roles
     WHERE rolname IN ('gerege_nexus_tenant', 'gerege_nexus_app')
     ORDER BY (rolname = 'gerege_nexus_tenant') DESC
     LIMIT 1;
    IF app_role IS NULL THEN
        RETURN;
    END IF;

    EXECUTE format($p$
        DROP POLICY IF EXISTS tenant_isolation ON urtuu_tasks;
        CREATE POLICY tenant_isolation ON urtuu_tasks TO %I
            USING (tenant_id IS NULL OR tenant_id = ANY (COALESCE(
                NULLIF(current_setting('app.allowed_tenants', true), '')::uuid[],
                ARRAY[NULLIF(current_setting('app.current_tenant', true), '')::uuid])))
            WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
    $p$, app_role);

    EXECUTE format($p$
        DROP POLICY IF EXISTS tenant_isolation ON urtuu_task_events;
        CREATE POLICY tenant_isolation ON urtuu_task_events TO %I
            USING (tenant_id IS NULL OR tenant_id = ANY (COALESCE(
                NULLIF(current_setting('app.allowed_tenants', true), '')::uuid[],
                ARRAY[NULLIF(current_setting('app.current_tenant', true), '')::uuid])))
            WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
    $p$, app_role);

    EXECUTE format($p$
        DROP POLICY IF EXISTS tenant_isolation ON urtuu_numbers;
        CREATE POLICY tenant_isolation ON urtuu_numbers TO %I
            USING (tenant_id IS NULL OR tenant_id = ANY (COALESCE(
                NULLIF(current_setting('app.allowed_tenants', true), '')::uuid[],
                ARRAY[NULLIF(current_setting('app.current_tenant', true), '')::uuid])))
            WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
    $p$, app_role);

    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON urtuu_tasks, urtuu_task_events, urtuu_numbers TO %I',
                   app_role);
END
$rls$;
-- +goose StatementEnd

-- Хяналтын хавтгай зөвхөн уншина: цөмийн 00064 энэ гурвыг нэрлэсэн бөгөөд
-- энд давтагдана, эс бөгөөс апп нүүсний дараа оператор харахаа болино.
DROP POLICY IF EXISTS operator_read ON urtuu_tasks;
CREATE POLICY operator_read ON urtuu_tasks FOR SELECT TO gerege_nexus_operator USING (true);
DROP POLICY IF EXISTS operator_read ON urtuu_task_events;
CREATE POLICY operator_read ON urtuu_task_events FOR SELECT TO gerege_nexus_operator USING (true);
DROP POLICY IF EXISTS operator_read ON urtuu_numbers;
CREATE POLICY operator_read ON urtuu_numbers FOR SELECT TO gerege_nexus_operator USING (true);

GRANT SELECT ON urtuu_tasks, urtuu_task_events, urtuu_numbers TO gerege_nexus_operator;

-- +goose Down

DROP TABLE IF EXISTS urtuu_task_events;
DROP TABLE IF EXISTS urtuu_numbers;
DROP TABLE IF EXISTS urtuu_tasks;
