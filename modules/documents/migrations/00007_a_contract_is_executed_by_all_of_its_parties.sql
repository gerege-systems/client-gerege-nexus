-- ГЭРЭЭ БҮХ ТАЛЫНХАА ГАРЫН ҮСГЭЭР Л ХҮЧИН ТӨГӨЛДӨР БОЛНО.
--
-- 00005-ийн тоолуур `required_total`-оо `state <> 'draft'` гэж шүүдэг байв.
-- Үр дагавар нь аудитаар илэрсэн хамгийн аюултай алдаа: ХОЁР заавал талтай
-- гэрээг эхлээд ганцад нь илгээхэд (party_ids сонголт, эсвэл нөгөөгийнх нь
-- Word хөрвүүлэлт түр унасан) хүргэгдээгүй тал нь 'draft' хэвээр тул
-- ТООЛОГДОХГҮЙ — эхний тал зурмагц required_total=1, signed=1 болж гэрээ
-- EXECUTED болно. Тэгмэгц илгээх, тал нэмэх, эргүүлж татах гурван зам
-- гурвуулаа 409-өөр хаагддаг: худал «хүчин төгөлдөр» гэрээ засах аргагүй
-- түгжигдэнэ.
--
-- Засвар: заавал тал нь ХҮРГЭГДСЭН эсэхээсээ үл хамааран тоологдоно. Trigger
-- зөвхөн SENT+ төлөвтэй гэрээн дээр ажилладаг тул ноорог гэрээний талууд үүнд
-- өртөхгүй; илгээгдсэн гэрээний 'draft' тал гэдэг нь «хараахан хүргэгдээгүй
-- заавал тал» — түүнийг хүлээхгүйгээр EXECUTED гэж хэлж болохгүй.
--
-- Хоёр дахь засвар: БҮХ тал нь заавал-биш гэрээ. required_total=0 үед хуучин
-- функц төлвийг огт хөдөлгөдөггүй байсан тул бүгд зурсан ч SENT хэвээр
-- үлддэг байв. Одоо заавал тал байхгүй бол илгээгдсэн бүх тал нь тооллын
-- суурь болно: бүгд зурвал EXECUTED, нэг нь татгалзвал DECLINED.

-- +goose Up

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_refresh_contract_state() RETURNS TRIGGER AS $$
DECLARE doc UUID := COALESCE(NEW.document_id, OLD.document_id);
BEGIN
    UPDATE document_records r
       SET contract_state = CASE
             WHEN c.denom = 0                  THEN r.contract_state
             WHEN c.declined > 0               THEN 'DECLINED'
             WHEN c.signed >= c.denom          THEN 'EXECUTED'
             WHEN c.signed > 0                 THEN 'PARTIALLY_SIGNED'
             ELSE 'SENT' END,
           executed_at = CASE
             WHEN c.denom > 0 AND c.declined = 0 AND c.signed >= c.denom
                  THEN COALESCE(r.executed_at, NOW()) ELSE r.executed_at END
      FROM (SELECT
              -- Заавал талууд — draft нь Ч тоологдоно: хүргэгдээгүй заавал
              -- тал бол хүлээгдэж буй гарын үсэг, алга болсон тал биш.
              count(*) FILTER (WHERE required AND party_role <> 'issuer') AS required_total,
              -- Заавал тал огт байхгүй бол илгээгдсэн бүх тал суурь болно.
              CASE WHEN count(*) FILTER (WHERE required AND party_role <> 'issuer') > 0
                   THEN count(*) FILTER (WHERE required AND party_role <> 'issuer')
                   ELSE count(*) FILTER (WHERE party_role <> 'issuer' AND state <> 'draft')
              END AS denom,
              CASE WHEN count(*) FILTER (WHERE required AND party_role <> 'issuer') > 0
                   THEN count(*) FILTER (WHERE required AND party_role <> 'issuer' AND state = 'signed')
                   ELSE count(*) FILTER (WHERE party_role <> 'issuer' AND state = 'signed')
              END AS signed,
              count(*) FILTER (WHERE party_role <> 'issuer' AND state = 'declined') AS declined
            FROM document_parties WHERE document_id = doc) c
     WHERE r.id = doc
       AND r.contract_state IN ('SENT', 'PARTIALLY_SIGNED', 'EXECUTED', 'DECLINED');
    RETURN NULL;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp;
-- +goose StatementEnd

-- Худал EXECUTED болчихсон гэрээ байвал зөв төлөвт нь буцаана: заавал тал нь
-- хараахан зураагүй атал executed_at-тай мөрүүд.
UPDATE document_records r
   SET contract_state = CASE
         WHEN (SELECT count(*) FROM document_parties p
                WHERE p.document_id = r.id AND p.party_role <> 'issuer' AND p.state = 'signed') > 0
         THEN 'PARTIALLY_SIGNED' ELSE 'SENT' END,
       executed_at = NULL
 WHERE r.contract_state = 'EXECUTED'
   AND EXISTS (SELECT 1 FROM document_parties p
                WHERE p.document_id = r.id AND p.party_role <> 'issuer'
                  AND p.required AND p.state <> 'signed');

-- +goose Down

-- 00005-ийн хувилбар руу буцаана.
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
     WHERE r.id = doc
       AND r.contract_state IN ('SENT', 'PARTIALLY_SIGNED', 'EXECUTED', 'DECLINED');
    RETURN NULL;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp;
-- +goose StatementEnd
