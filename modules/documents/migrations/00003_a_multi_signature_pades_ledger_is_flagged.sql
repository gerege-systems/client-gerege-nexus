-- Хоёр дахь гарын үсгээс хойшхи PAdES мөрүүдийн `covered_digest` буруу байв.
--
-- `contentsign.go:57` нь ӨСӨЖ БУЙ хувийг илгээдэг байсан ч `:102` нь
-- `artifact.SHA256` — ЭХ хувийн хэшийг — `requested_digest` болгож бичдэг байв,
-- ба `:188` түүнийг `covered_digest` болгодог. Өөрөөр хэлбэл 2 дахь гарын
-- үсгээс хойш ledger нь тэр гарын үсэг хамраагүй байтыг нэрлэж байна.
--
-- Кодыг Үе 1-д зассан. Бичигдсэн мөрийг ЗАСАХ БОЛОМЖГҮЙ — жинхэнэ утга нь
-- хэнд ч мэдэгдэхгүй. Тиймээс тэднийг ТЭМДЭГЛЭНЭ: чимээгүй зөв болгох нь
-- маргаан дээр илүү аюултай.

-- +goose Up
ALTER TABLE document_signatures
    ADD COLUMN IF NOT EXISTS covered_digest_suspect BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE document_signatures s
   SET covered_digest_suspect = TRUE
 WHERE s.format = 'pades'
   AND s.party_id IS NULL
   AND EXISTS (SELECT 1 FROM document_signatures e
                WHERE e.document_id = s.document_id
                  AND e.format = 'pades'
                  AND e.signed_at < s.signed_at);

COMMENT ON COLUMN document_signatures.covered_digest_suspect IS
    'covered_digest нь энэ гарын үсэг хамарсан байтыг нэрлэхгүй байж болно — 2026-08 өмнөх PAdES гинжний алдаа. Гарын үсэг өөрөө хүчинтэй; хамрах хүрээ нь зөвхөн PAdES файлаас уншигдана.';

-- +goose Down
ALTER TABLE document_signatures DROP COLUMN IF EXISTS covered_digest_suspect;