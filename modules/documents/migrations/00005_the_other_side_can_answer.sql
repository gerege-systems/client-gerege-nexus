-- ХҮЛЭЭН АВАГЧ ХАРИУЛЖ ЧАДДАГ БОЛОВ.
--
-- 00002 нь талын хүснэгтүүдэд хоёр талын бодлого өгсөн: гаргагч ба хүлээн
-- авагч хоёулаа мөрөө хардаг. Гэвч гарын үсэг зурах нь тэр хүснэгтүүдээр
-- ЗОГСДОГГҮЙ — ёслол `document_eid_sign_sessions`-д, үр дүн нь
-- `document_signatures`-д, нэгтгэсэн төлөв нь `document_records`-д бичигдэнэ.
-- Тэр гурав нь 00001-ийн нэг талын бодлоготой хэвээр байсан тул хүлээн
-- авагчийн PIN2 зөвшөөрөл нь `WITH CHECK`-ийн ханан дээр унана: иргэн
-- гарын үсгээ зурчихсан, систем нь бүртгэж чадахгүй.
--
-- Энэ миграц тэр гурван хананд хаалга гаргана — ГАНЦХАН чиглэлд, ГАНЦХАН
-- нөхцөлөөр: мөр нь тухайн хүлээн авагчийн ТАЛД харьяалагдаж байвал.
--
-- Мөн гурван зүйлийг зэрэг засна:
--
--  1. ХЭН зурж байгааг ёслол өөрөө санадаг болов. `pollPartySignature` нь
--     «гарын үсэг зураагүй хамгийн эртний бүртгэлтэй хүн» гэж ТААМАГЛАДАГ
--     байв. Хоёр гарын үсэг зурагчтай тал дээр хоёр дахь нь ёслол
--     эхлүүлбэл poll нь ЭХНИЙХИЙН регистрийн дугаараар рельсээс асууна —
--     өөр иргэний нэр дээр өөр хүний PIN2 бүртгэгдэх зам.
--
--  2. Хүлээн авагч `document_records`-ыг УНШИХ ШААРДЛАГАГҮЙ болов. Гарчиг,
--     төрөл, хугацаа нь илгээх агшинд талын мөрөнд хөлдөнө. Энэ нь зөвхөн
--     бодлогын хялбарчлал биш: хүлээн авагчийн харсан гарчиг нь дараа нь
--     гаргагч гарчгаа заcахад ӨӨРЧЛӨГДӨХ ЁСГҮЙ.
--
--  3. Гэрээний нэгтгэсэн төлөв нь ГУРВАН зам (гаргагч, хүлээн авагч,
--     урилгын токен) дээр давхардан бичигдэхээ болив. Гурвуулангийнх нь
--     оронд нэг trigger тоолно — гурван газарт бичигдсэн логик нь хоёр
--     дахь газраа мартагдах логик.

-- +goose Up

-- SECURITY DEFINER нь ДООРХ хоёр функцийн үндэс: тэдгээр нь эзэмшигчийн
-- эрхээр ажиллаж RLS-ээс гардаг. Эзэмшигч нь RLS-ийг тойрч чаддаггүй бол
-- FORCE ROW LEVEL SECURITY-тэй хүснэгт дээр UPDATE нь алдаа өгөхгүй, зүгээр
-- ЮУ Ч ХИЙХГҮЙ — гэрээ гарын үсэг зурагдаад төлөв нь SENT хэвээр үлдэнэ.
-- Чимээгүй буруу хариултаас чанга алдаа дээр.
-- +goose StatementBegin
DO $guard$
BEGIN
    IF NOT (SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user) THEN
        RAISE EXCEPTION
            'migration role % can neither bypass RLS nor is a superuser: the SECURITY DEFINER refresh in this migration would silently update nothing',
            current_user;
    END IF;
END $guard$;
-- +goose StatementEnd

-- ═══════════════════════════════════════ 0. гарын үсэг зурагч ЗУРСАН БОЛОВ
--
-- 00002 нь гарын үсэг зурах эрхтэй хүнийг бүртгэсэн боловч ТЭР ЗУРСАН
-- эсэхийг тэмдэглэх газар үлдээгээгүй. Үр дагавар нь зөвхөн дутуу баримт
-- биш: `signerFor` нь «зураагүй хүн» гэж шүүдэг, `recordPartySignature` нь
-- ёслол бүрийн төгсгөлд энэ талбарыг бичдэг, хасах зам нь «зураагүй бол л»
-- гэж хамгаалдаг. Багана байхгүй бол талын өмнөөс гарын үсэг зурах бүхэл
-- зам ажиллахгүй.
ALTER TABLE document_party_signatories
    ADD COLUMN IF NOT EXISTS signed_at TIMESTAMPTZ;
COMMENT ON COLUMN document_party_signatories.signed_at IS
    'Энэ хүн PIN2-оороо зөвшөөрсөн агшин. NULL нь хараахан зураагүй — ёслол зөвхөн ийм мөрөнд хаяглагдана.';
-- «Хэн гарын үсэг зурсан бэ» гэдэг талын хамгийн олон асуугддаг асуулт.
CREATE INDEX IF NOT EXISTS idx_document_party_signatories_pending
    ON document_party_signatories (party_id) WHERE signed_at IS NULL;

-- ═══════════════════════════════════════ 1. ёслол хэнийхийг санана

ALTER TABLE document_parties
    ADD COLUMN IF NOT EXISTS session_signatory_id UUID
        REFERENCES document_party_signatories(id) ON DELETE SET NULL;
COMMENT ON COLUMN document_parties.session_signatory_id IS
    'Нээлттэй ёслолыг ЭХЛҮҮЛСЭН гарын үсэг зурагч. poll нь түүгээр рельсээс асууна — таамаглахгүй.';

-- ═══════════════════════════════════════ 2. хүлээн авагчид хэрэгтэй нь талын мөрөнд

ALTER TABLE document_parties
    ADD COLUMN IF NOT EXISTS doc_title  VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS doc_type   VARCHAR(64)  NOT NULL DEFAULT 'CONTRACT',
    ADD COLUMN IF NOT EXISTS doc_due_at TIMESTAMPTZ;
COMMENT ON COLUMN document_parties.doc_title IS
    'Илгээх агшны гарчиг. Гаргагч дараа нь гарчгаа заcахад хүлээн авагчийн харсан зүйл өөрчлөгдөхгүй.';

UPDATE document_parties p
   SET doc_title  = r.title,
       doc_type   = r.doc_type,
       doc_due_at = r.due_at
  FROM document_records r
 WHERE r.id = p.document_id AND p.doc_title = '';

-- Дүүргэлт нь SECURITY DEFINER: хүлээн авагчийн сесс `document_records`-ыг
-- уншиж чадахгүй ба чадах ч ёсгүй. Функц нь ганц баримтын гурван талбарыг
-- ганц id-гаар авдаг тул хамрах хүрээ нь нэг мөрөөр хаагдана.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_party_denormalise() RETURNS TRIGGER AS $$
BEGIN
    SELECT r.title, r.doc_type, r.due_at
      INTO NEW.doc_title, NEW.doc_type, NEW.doc_due_at
      FROM document_records r WHERE r.id = NEW.document_id;
    IF NEW.doc_title IS NULL THEN
        RAISE EXCEPTION 'no such document %', NEW.document_id
            USING ERRCODE = 'foreign_key_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS documents_parties_denormalise ON document_parties;
CREATE TRIGGER documents_parties_denormalise BEFORE INSERT ON document_parties
    FOR EACH ROW EXECUTE FUNCTION documents_party_denormalise();

-- Хүлээн авагч гэрээний гарчгийг ч дахин бичихгүй. 00002-ийн жагсаалтад
-- гурван багана нэмэгдэв — функц бүхэлдээ дахин үүсгэгдэнэ.
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
    OR NEW.doc_title    IS DISTINCT FROM OLD.doc_title
    OR NEW.doc_type     IS DISTINCT FROM OLD.doc_type
    OR NEW.doc_due_at   IS DISTINCT FROM OLD.doc_due_at
    THEN
        RAISE EXCEPTION 'a counterparty answers a contract, it does not rewrite one (party %)', OLD.id
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- ═══════════════════════════════════════ 2а. «гэрээ мөн үү» гэдгийн индекс
--
-- 00002 нь `signing_mode <> 'internal'` дээр хэсэгчилсэн индекс тавьсан
-- боловч тэр багана нь ИЛГЭЭХ агшинд бичигддэг. Гэрээний бүртгэл нь
-- илгээгээгүй гэрээг ч агуулах ёстой тул тайлангууд `contract_state`-аар
-- асуудаг — тэр нь анхны тал нэмэгдэхэд NONE-оос DRAFT болно.
CREATE INDEX IF NOT EXISTS idx_document_records_is_a_contract
    ON document_records (tenant_id, contract_state, created_at DESC)
    WHERE contract_state <> 'NONE';

-- ═══════════════════════════════════════ 3. ёслол ба гарын үсэг хоёр талтай болов

ALTER TABLE document_eid_sign_sessions
    ADD COLUMN IF NOT EXISTS party_id UUID REFERENCES document_parties(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS counterparty_tenant_id UUID REFERENCES tenants(id) ON DELETE SET NULL;
ALTER TABLE document_signatures
    ADD COLUMN IF NOT EXISTS counterparty_tenant_id UUID REFERENCES tenants(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_document_eid_sign_sessions_party
    ON document_eid_sign_sessions (party_id) WHERE party_id IS NOT NULL;

-- Хамрах хүрээг ТАЛААС нь хуулна — 00002-ийн `documents_party_scope()`-тэй
-- нэг санаа, гэхдээ энд `party_id` нь NULL байж БОЛНО: талгүй баримтын
-- дотоод зөвшөөрөл энэ хүснэгтүүдийг хуучнаараа хэрэглэсээр байна.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_party_scope_optional() RETURNS TRIGGER AS $$
DECLARE other UUID;
BEGIN
    IF NEW.party_id IS NULL THEN
        NEW.counterparty_tenant_id := NULL;
        RETURN NEW;
    END IF;
    SELECT p.counterparty_tenant_id INTO other
      FROM document_parties p WHERE p.id = NEW.party_id;
    NEW.counterparty_tenant_id := other;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS documents_scope_sign_sessions ON document_eid_sign_sessions;
CREATE TRIGGER documents_scope_sign_sessions BEFORE INSERT ON document_eid_sign_sessions
    FOR EACH ROW EXECUTE FUNCTION documents_party_scope_optional();
DROP TRIGGER IF EXISTS documents_scope_signatures ON document_signatures;
CREATE TRIGGER documents_scope_signatures BEFORE INSERT ON document_signatures
    FOR EACH ROW EXECUTE FUNCTION documents_party_scope_optional();

UPDATE document_signatures s
   SET counterparty_tenant_id = p.counterparty_tenant_id
  FROM document_parties p
 WHERE p.id = s.party_id AND s.counterparty_tenant_id IS NULL;

-- Хоёр дахь тал нь ӨӨР НЭРТЭЙ бодлогод амьдарна: 00001-ийн давталт дахин
-- ажиллавал `tenant_isolation`-ыг л дахин үүсгэх бөгөөд энэ нь үлдэнэ.
DROP POLICY IF EXISTS counterparty_visibility ON document_eid_sign_sessions;
CREATE POLICY counterparty_visibility ON document_eid_sign_sessions TO gerege_nexus_app
    USING      (counterparty_tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
    WITH CHECK (counterparty_tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
DROP POLICY IF EXISTS counterparty_visibility ON document_signatures;
CREATE POLICY counterparty_visibility ON document_signatures TO gerege_nexus_app
    USING      (counterparty_tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
    WITH CHECK (counterparty_tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);

-- Гарын үсэг нь БҮРТГЭЛ: нэгэнт бичигдсэн бол хүлээн авагч ч, гаргагч ч
-- устгахгүй. Бодлого мөр нээдэг, харин энэ нь үйлдлийг хаана.
DROP POLICY IF EXISTS signatures_are_not_erased ON document_signatures;
CREATE POLICY signatures_are_not_erased ON document_signatures
    AS RESTRICTIVE FOR DELETE TO gerege_nexus_app USING (false);

-- ═══════════════════════════════════════ 4. нэгтгэсэн төлөв нэг л газар тоологдоно
--
-- Урьд нь Go код гурван замаас дуудаж бичдэг байв. Trigger нь ТАЛУУДЫН
-- мөрөнд сууж байгаа тул зам бүрийг — гаргагчийн татгалзал, хүлээн авагчийн
-- гарын үсэг, урилгын токеноор ирсэн зурлага, ирээдүйн эргүүл — ЯЛГААГҮЙ
-- хамарна. SECURITY DEFINER нь хүлээн авагчийн сессээс `document_records`
-- руу бичих цорын ганц, нарийн хаалга: тэр нь ЗӨВХӨН contract_state ба
-- executed_at-г, ЗӨВХӨН тухайн баримтын өөрийнх нь талуудын тооноос гаргана.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_refresh_contract_state() RETURNS TRIGGER AS $$
DECLARE doc UUID := COALESCE(NEW.document_id, OLD.document_id);
BEGIN
    UPDATE document_records r
       SET contract_state = CASE
             WHEN c.required_total = 0            THEN r.contract_state
             WHEN c.declined > 0                  THEN 'DECLINED'
             WHEN c.signed >= c.required_total    THEN 'EXECUTED'
             WHEN c.signed > 0                    THEN 'PARTIALLY_SIGNED'
             ELSE 'SENT' END,
           executed_at = CASE
             WHEN c.required_total > 0 AND c.signed >= c.required_total
                  THEN COALESCE(r.executed_at, NOW()) ELSE r.executed_at END
      FROM (SELECT
              count(*) FILTER (WHERE required AND party_role <> 'issuer' AND state <> 'draft') AS required_total,
              count(*) FILTER (WHERE required AND party_role <> 'issuer' AND state = 'signed') AS signed,
              count(*) FILTER (WHERE required AND party_role <> 'issuer' AND state = 'declined') AS declined
            FROM document_parties WHERE document_id = doc) c
     -- ЗӨВХӨН илгээгдсэн гэрээ. Ноорог дээр тал нэмэх нь илгээх биш, ба
     -- эргүүлж татсан гэрээг гарын үсэг сэргээж болохгүй.
     WHERE r.id = doc
       AND r.contract_state IN ('SENT', 'PARTIALLY_SIGNED', 'EXECUTED', 'DECLINED');
    RETURN NULL;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS documents_parties_refresh_state ON document_parties;
CREATE TRIGGER documents_parties_refresh_state
    AFTER INSERT OR DELETE ON document_parties
    FOR EACH ROW EXECUTE FUNCTION documents_refresh_contract_state();
DROP TRIGGER IF EXISTS documents_parties_refresh_state_upd ON document_parties;
CREATE TRIGGER documents_parties_refresh_state_upd
    AFTER UPDATE OF state, required, party_role ON document_parties
    FOR EACH ROW EXECUTE FUNCTION documents_refresh_contract_state();

-- +goose Down

DROP TRIGGER IF EXISTS documents_parties_refresh_state_upd ON document_parties;
DROP TRIGGER IF EXISTS documents_parties_refresh_state ON document_parties;
DROP FUNCTION IF EXISTS documents_refresh_contract_state();
DROP POLICY IF EXISTS signatures_are_not_erased ON document_signatures;
DROP POLICY IF EXISTS counterparty_visibility ON document_signatures;
DROP POLICY IF EXISTS counterparty_visibility ON document_eid_sign_sessions;
DROP TRIGGER IF EXISTS documents_scope_signatures ON document_signatures;
DROP TRIGGER IF EXISTS documents_scope_sign_sessions ON document_eid_sign_sessions;
DROP FUNCTION IF EXISTS documents_party_scope_optional();
ALTER TABLE document_signatures DROP COLUMN IF EXISTS counterparty_tenant_id;
ALTER TABLE document_eid_sign_sessions
    DROP COLUMN IF EXISTS counterparty_tenant_id, DROP COLUMN IF EXISTS party_id;
DROP INDEX IF EXISTS idx_document_records_is_a_contract;
DROP TRIGGER IF EXISTS documents_parties_denormalise ON document_parties;
DROP FUNCTION IF EXISTS documents_party_denormalise();

-- Багануудыг хаяхын ӨМНӨ функцийг 00002-ийн хэлбэрт нь буцаана: plpgsql нь
-- талбаруудыг ажиллах үедээ шийддэг тул байхгүй болсон багана нэрлэсэн
-- функц нь дараагийн UPDATE бүрийг унагана.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_party_answer_only() RETURNS TRIGGER AS $$
DECLARE acting UUID := NULLIF(current_setting('app.current_tenant', true), '')::uuid;
BEGIN
    IF acting IS NULL OR acting = OLD.tenant_id THEN
        RETURN NEW;
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

ALTER TABLE document_parties
    DROP COLUMN IF EXISTS doc_due_at, DROP COLUMN IF EXISTS doc_type,
    DROP COLUMN IF EXISTS doc_title,  DROP COLUMN IF EXISTS session_signatory_id;
-- `signed_at`-г ЗОРИУДААР хаяхгүй: гарын үсэг хэзээ зурагдсаныг зөвхөн энэ
-- багана хэлдэг ба буцаалт нь тэр хариултыг устгах ёсгүй.
