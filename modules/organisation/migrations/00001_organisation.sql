-- Байгууллага ба түүний хүмүүс: аппын өөрийн схем.
--
-- Платформын түүхээс зөөгдөв. `departments` нь 00034_core_organisation.sql-д
-- (тухайн үед апп нь `core` нэртэй байсан), `organisation_people` нь
-- 00076_the_org_chart_leaves_the_membership.sql-д үүссэн. Аль нь ч байснаараа
-- зөөгдөж чадахгүй: хэрэглэгдсэн миграцыг дахин бичих боломжгүй, мөн 00034 нь
-- платформын үлдэх хүснэгтүүдийг хамт үүсгэдэг. Тиймээс энд дахин зарлав —
-- business-gerege-nexus-ийн 00001_commerce.sql-ийн яг тэр арга.
--
-- Бүгд `IF NOT EXISTS`. Энэ түүх нь платформын хуулбарыг аль хэдийн үүрсэн
-- өгөгдлийн сан дээр ажиллана: цөмийн 00077 хүснэгтүүдийг устгах хүртэл хоёул
-- байх бөгөөд устсаны дараа энэ файл нь тэднийг үүсгэдэг цорын ганц газар
-- болно. Хамгаалалтгүй CREATE INDEX нь эхний тохиолдолд уначих байсан.
--
-- Гадаад түлхүүрүүд нь платформын `tenants`, `memberships` рүү заана. Тэр
-- чиглэл зөв: апп платформыг мэднэ, платформ аппыг мэдэхгүй. Урвуу
-- хамаарлыг — `memberships.department_id` — 00076 таслав.
--
-- Роль `gerege_nexus_app`-ыг платформын 00029 үүсгэнэ, тэр нь энэ миграц
-- ажиллахаас өмнө бүрэн хэрэгжсэн байна: модулийн миграц нь апп суулгах үед
-- ажилладаг бөгөөд платформ түүнээс өмнө өөрийн схемээ гүйцээсэн байдаг.

-- +goose Up

CREATE TABLE IF NOT EXISTS departments (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    code                  VARCHAR(64) NOT NULL,
    name                  VARCHAR(255) NOT NULL,
    parent_id             UUID,
    manager_membership_id UUID,
    active                BOOLEAN NOT NULL DEFAULT TRUE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT departments_code_uniq   UNIQUE (tenant_id, code),
    -- Байгууллага хоорондын эцэг эсвэл дарга гарахгүй байх нь схемийн хариулт:
    -- (id, tenant_id) хос дээрх composite гадаад түлхүүрүүд үүнийг барина.
    CONSTRAINT departments_tenant_uniq UNIQUE (id, tenant_id),
    CONSTRAINT departments_not_self    CHECK (parent_id IS NULL OR parent_id <> id)
);

-- Өөрөө өөр рүүгээ заах түлхүүрийг хүснэгт үүссэний дараа нэмнэ. `IF NOT
-- EXISTS` нь ALTER TABLE ADD CONSTRAINT дээр байхгүй тул каталогоос асууна.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'departments_parent_fk') THEN
        ALTER TABLE departments ADD CONSTRAINT departments_parent_fk
            FOREIGN KEY (parent_id, tenant_id) REFERENCES departments(id, tenant_id) ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'departments_manager_fk') THEN
        ALTER TABLE departments ADD CONSTRAINT departments_manager_fk
            FOREIGN KEY (manager_membership_id, tenant_id) REFERENCES memberships(id, tenant_id) ON DELETE SET NULL;
    END IF;
END $$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS idx_departments_tenant ON departments(tenant_id, active);

CREATE TABLE IF NOT EXISTS organisation_people (
    membership_id UUID PRIMARY KEY REFERENCES memberships(id) ON DELETE CASCADE,
    tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    job_title     VARCHAR(255) NOT NULL DEFAULT '',
    department_id UUID REFERENCES departments(id) ON DELETE SET NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_organisation_people_tenant     ON organisation_people(tenant_id);
CREATE INDEX IF NOT EXISTS idx_organisation_people_department ON organisation_people(department_id);

-- Тенант тусгаарлалт, платформын 00037-ийн хэлбэрээр: уншихдаа session-ий
-- зөвшөөрөгдсөн байгууллагууд, бичихдээ зөвхөн идэвхтэй нэг нь.
ALTER TABLE departments          ENABLE ROW LEVEL SECURITY;
ALTER TABLE departments          FORCE  ROW LEVEL SECURITY;
ALTER TABLE organisation_people  ENABLE ROW LEVEL SECURITY;
ALTER TABLE organisation_people  FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON departments;
CREATE POLICY tenant_isolation ON departments TO gerege_nexus_app
    USING (tenant_id IS NULL OR tenant_id = ANY (COALESCE(
        NULLIF(current_setting('app.allowed_tenants', true), '')::uuid[],
        ARRAY[NULLIF(current_setting('app.current_tenant', true), '')::uuid])))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);

DROP POLICY IF EXISTS tenant_isolation ON organisation_people;
CREATE POLICY tenant_isolation ON organisation_people TO gerege_nexus_app
    USING (tenant_id IS NULL OR tenant_id = ANY (COALESCE(
        NULLIF(current_setting('app.allowed_tenants', true), '')::uuid[],
        ARRAY[NULLIF(current_setting('app.current_tenant', true), '')::uuid])))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);

-- Платформын 00029 нь ALTER DEFAULT PRIVILEGES тавьсан боловч тэр нь зөвхөн
-- тухайн ролийн үүсгэсэн объектод үйлчилнэ. Модулийн миграц ямар холболтоор
-- ажиллахыг энэ файл шийдэхгүй тул эрхийг шууд нэрлэв.
GRANT SELECT, INSERT, UPDATE, DELETE ON departments, organisation_people TO gerege_nexus_app;

-- +goose Down

DROP TABLE IF EXISTS organisation_people;
DROP TABLE IF EXISTS departments;
