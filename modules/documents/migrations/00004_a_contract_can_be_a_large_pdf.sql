-- PDF том байж болно.
--
-- `document_files_size_check` ба `document_party_files_size_check` хоёулаа 25 МБ
-- дээр тогтсон байв. Тэр тоо нь гарын үсгийн рельсийн тааз — `open-gerege-core`
-- -ийн `maxPDFBytes` ба платформын esign рельс хоёулаа 25 МБ дээр татгалздаг —
-- бөгөөд хоёуланг нь тэнцүү барих нь «хавсаргасан ч зурагдахгүй баримт» үүсэхээс
-- сэргийлэх зорилготой байсан.
--
-- Гэвч тэр нь зайлсхийлт байв: том гэрээг огт хавсаргуулахгүй байснаараа.
-- Хавсралт, зураг, план бүхий барилгын гэрээ хэдэн арван мегабайт болдог.
--
-- Жинхэнэ шийдэл нь ФОРМАТ (`domain.FormatFor`): 25 МБ-аас том PDF нь
-- PAdES-ээр биш DETACHED-ээр зурагдана. Detached ёслолд зөвхөн SHA-256 л рельс
-- рүү явдаг тул хэмжээний хязгаар байхгүй. Гарын үсэг адилхан хүчинтэй; ялгаа нь
-- тэр PDF-ийн дотор сууж байхгүй, хажууд нь бүртгэгдэнэ.
--
-- Тиймээс сангийн тааз нь ХАВСРАЛТЫН тааз болж 100 МБ болно.

-- +goose Up

ALTER TABLE document_files
    DROP CONSTRAINT IF EXISTS document_files_size_check,
    ADD  CONSTRAINT document_files_size_check CHECK (size_bytes > 0 AND size_bytes <= 104857600);

ALTER TABLE document_party_files
    DROP CONSTRAINT IF EXISTS document_party_files_size_check,
    ADD  CONSTRAINT document_party_files_size_check CHECK (size_bytes > 0 AND size_bytes <= 104857600);

-- +goose Down

-- Буцаах нь 25 МБ-аас том хавсралттай баримт байвал УНАНА, тэр нь зөв: тэдгээрийг
-- чимээгүй устгах эсвэл таазаас давсан мөр үлдээх хоёрын аль нь ч зөв биш.
ALTER TABLE document_files
    DROP CONSTRAINT IF EXISTS document_files_size_check,
    ADD  CONSTRAINT document_files_size_check CHECK (size_bytes > 0 AND size_bytes <= 26214400);

ALTER TABLE document_party_files
    DROP CONSTRAINT IF EXISTS document_party_files_size_check,
    ADD  CONSTRAINT document_party_files_size_check CHECK (size_bytes > 0 AND size_bytes <= 26214400);
