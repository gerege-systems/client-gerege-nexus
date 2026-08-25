-- ХОЁР ИНВАРИАНТ, ХОЁУЛАА КОДООС ЗУГТАЖ БАЙВ.
--
-- 00002 нь `document_parties_one_home`-оор «тал НЭГЭЭС ИЛҮҮ газар оршихгүй»
-- гэж хэлсэн. Тэр нь буруу тал биш, харин ХАГАС нь: «тал заавал ГЭРТЭЙ»
-- гэдгийг хэн ч хэлээгүй. Үр дүнд нь `party_kind = 'tenant'` мөр
-- `counterparty_tenant_id` НУЛЬ-тэйгээр орж чаддаг байв — бөгөөд яг тэр
-- мөрийг дэлгэц үүсгэдэг байсан.
--
-- Тийм мөр нь эвдэрсэн мэт харагддаггүй нь хамгийн муу нь: гэрээнд тал
-- байна, нэр нь байна, илгээгдсэн гэж бичигдэнэ. Гэвч түүнд хүрэх зам
-- байхгүй — `inboxScope`-ийн гурван салааны аль нь ч түүнийг олохгүй, урилга
-- нь `person`/`organisation`-д зориулагдсан. Хүлээн авагч хэзээ ч гэрээгээ
-- харахгүй, гаргагч нь «илгээсэн» гэж уншсаар байна.
--
-- Хоёрдугаарт: гэрээнд ХОЁР ГАРГАГЧ байж болдог байв. `document_parties`-д
-- үүргийн CHECK нь утгыг л шалгадаг, тоог биш. Хоёр гаргагчтай гэрээнд
-- хүлээн авагчийн жагсаалт нэг гэрээг ХОЁР МӨРӨӨР харуулна (жагсаалтын
-- query нь гаргагчийн нэрийг олохын тулд талуудын хүснэгт рүү өөр рүүгээ
-- JOIN хийдэг), ба `issuerOf` нь дурын нэгийг сонгоод гэрээний хөл дээр
-- хэвлэнэ.
--
-- Хоёулаа Go-д шалгагдаж БОЛОХ байсан бөгөөд одоо шалгагдаж ч байгаа. Гэвч
-- Go-гийн шалгалт нь зэрэг ирсэн хоёр хүсэлтийг барихгүй, ба дараагийн
-- дуудагчийг ч барихгүй. Инвариант нь санд амьдрах ёстой.
--
-- ӨГӨГДӨЛ. Эдгээр нь хэрэгжиж БАЙГАА суулгацад унах боломжтой хязгаарлалт
-- — энэ апп хаана ч байрлуулагдаагүй тул тийм мөр байхгүй. Байсан бол
-- миграц УНАХ ёстой: гэргүй талыг чимээгүй засах нь түүнийг хэн рүү
-- илгээснийг таамаглана гэсэн үг, тэр таамаглалыг сан хийж болохгүй.

-- +goose Up

-- Тал бүр ЯГ НЭГ гэртэй, ба гэр нь төрөлтэйгөө таарна.
ALTER TABLE document_parties
    ADD CONSTRAINT document_parties_kind_has_a_home CHECK (
        (party_kind = 'member'       AND member_user_id         IS NOT NULL) OR
        (party_kind = 'tenant'       AND counterparty_tenant_id IS NOT NULL) OR
        (party_kind = 'peer'         AND peer_id                IS NOT NULL) OR
        (party_kind IN ('person', 'organisation')
             AND member_user_id         IS NULL
             AND counterparty_tenant_id IS NULL
             AND peer_id                IS NULL));

-- Гэрээ нэг гаргагчтай. Хэсэгчилсэн индекс: бусад үүрэг хэдэн ч байж болно.
CREATE UNIQUE INDEX IF NOT EXISTS document_parties_one_issuer
    ON document_parties (document_id) WHERE party_role = 'issuer';

-- +goose Down

DROP INDEX IF EXISTS document_parties_one_issuer;
ALTER TABLE document_parties DROP CONSTRAINT IF EXISTS document_parties_kind_has_a_home;
