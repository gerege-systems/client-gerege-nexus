/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package documents

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// Artifact is what a document carries — the thing a signature is supposed to
// cover.
//
// A document may carry nothing, which is the state every document was in until
// now and remains a real one: a note in a workflow has nothing to sign and an
// approval is the whole of what its record can mean.
type Artifact struct {
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	// SHA256 is the artifact's digest, hex, lowercase. It is what a detached
	// signature covers and what proves a stored file is the one that was
	// signed.
	SHA256 string `json:"sha256"`
}

// Present reports whether the document carries anything at all.
func (a Artifact) Present() bool { return a.SHA256 != "" }

// MaxArtifactBytes is the ceiling on what may be attached.
//
// Гэрээ бол заримдаа том баримт: хавсралт, зураг, план бүхий барилгын гэрээ
// хэдэн арван мегабайт болдог. Тиймээс хавсаргах тааз нь гарын үсгийн
// рельсийн таазаас ТУСДАА байна.
//
// Урьд нь энэ хоёр нэг байсан бөгөөд шалтгаан нь зөв байв: рельс татгалзах
// баримтыг хүлээж авбал «хавсаргасан ч зурагдахгүй» баримт үүснэ. Гэвч тэр нь
// шийдэл биш, зайлсхийлт байсан — том гэрээг огт хавсаргуулахгүй байснаараа.
//
// Жинхэнэ шийдэл нь ФОРМАТ: 25 МБ-аас том PDF нь PAdES-ээр (гарын үсгийг
// баримт дотор шигтгэх) биш, DETACHED-ээр (хеш дээр) зурагдана. Detached
// ёслолд зөвхөн SHA-256 л рельс рүү явдаг тул хэмжээний хязгаар байхгүй.
// Гарын үсэг нь адилхан хүчинтэй; ялгаа нь тэр PDF-ийн дотор сууж байхгүй,
// хажууд нь бүртгэгдэнэ.
const MaxArtifactBytes = 100 << 20

// MaxEmbeddedSignatureBytes нь PAdES боломжтой хамгийн том баримт.
//
// Энэ тоог бид сонгоогүй: `open-gerege-core`-ийн гарын үсгийн usecase
// (`sign_usecase.go`, maxPDFBytes) ба платформын esign рельс хоёулаа 25 МБ
// дээр татгалздаг. Түүнээс том баримтыг PAdES-ээр илгээх нь рельсийн алдаа
// авах ба хэрэглэгчид ойлгомжгүй байна — тиймээс энд мэдээд detached руу
// шилжинэ.
const MaxEmbeddedSignatureBytes = 25 << 20

// Format is how a signature covers what the document carries.
type Format string

const (
	// FormatPAdES — the signed PDF itself. It verifies away from this
	// platform, which is the strongest thing this app can produce.
	FormatPAdES Format = "pades"
	// FormatDetached — a qualified signature over the artifact's digest. It
	// covers any content type, and verifying it needs the file and the record
	// together.
	FormatDetached Format = "detached"
	// FormatApproval — no artifact, so nothing to cover: who approved, when,
	// with which certificate. See ADR 0002.
	FormatApproval Format = "approval"
)

// Proof reports what a format establishes, which is what a reader of the record
// actually needs to know.
func (f Format) Proof() Proof {
	switch f {
	case FormatPAdES, FormatDetached:
		return ProofSignature
	case FormatApproval:
		return ProofApproval
	default:
		return ProofUnknown
	}
}

// FormatFor decides how this document will be signed.
//
// The document decides, not the caller. Offering the choice would let somebody
// take a PDF down the detached path and end up with a PDF that no reader can
// verify on its own — the weaker artifact, chosen by accident, on the document
// that could have had the stronger one.
//
// pdfRail is whether the installation can sign PDFs at all. When it cannot, a
// PDF is signed the same way everything else is: the digest still covers the
// bytes, and a detached signature over a PDF is worth more than no signature.
func FormatFor(artifact Artifact, pdfRail bool) Format {
	switch {
	case !artifact.Present():
		return FormatApproval
	case IsPDF(artifact.ContentType) && pdfRail && artifact.SizeBytes <= MaxEmbeddedSignatureBytes:
		return FormatPAdES
	case IsPDF(artifact.ContentType) && pdfRail:
		// 25 МБ-аас том PDF. Рельс түүнийг дотроо гарын үсэглэж чадахгүй тул
		// хеш дээр нь зурна — гарын үсэг адилхан хүчинтэй, зөвхөн баримтын
		// дотор биш, хажууд нь бүртгэгдэнэ.
		return FormatDetached
	default:
		return FormatDetached
	}
}

// IsPDF reports whether a content type is the one the PAdES rail understands.
func IsPDF(contentType string) bool {
	return strings.EqualFold(strings.TrimSpace(contentType), "application/pdf")
}

// SniffContentType decides what an upload is from its bytes rather than its
// name.
//
// A file called contract.pdf that does not begin %PDF- is not a PDF, and
// sending it down the PAdES rail produces an error from the provider rather
// than an answer here. The rule is narrow on purpose: this app has to tell one
// type apart from all the others, and everything it cannot recognise is carried
// as what the uploader said it was.
func SniffContentType(content []byte, declared string) string {
	if len(content) >= 5 && string(content[:5]) == "%PDF-" {
		return "application/pdf"
	}
	declared = strings.TrimSpace(declared)
	if declared == "" || IsPDF(declared) {
		// Either nothing was said, or a PDF was claimed and the bytes disagree.
		// "Unknown bytes" is the honest answer and signs detached.
		return "application/octet-stream"
	}
	return declared
}

// Digest is the artifact's SHA-256, hex, lowercase — the one shape every part
// of this app compares.
func Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// ErrArtifactFrozen refuses to change what a signature already covers.
//
// The reason is the whole point of signing: a signature names bytes, and bytes
// that can be swapped afterwards make the signature a statement about nothing.
// A document whose content has to change is a new document, and saying so is
// cheaper than explaining later why an old signature is on new text.
var ErrArtifactFrozen = errors.New("this document has been signed; its file cannot be replaced")

// ErrArtifactTooLarge and ErrArtifactEmpty are the two ways an upload is not
// one.
var (
	ErrArtifactTooLarge = errors.New("the file is larger than 25MB")
	ErrArtifactEmpty    = errors.New("an empty file is not an attachment")
)

// CheckAttachable reports whether this document may take this file.
//
// signatures is how many the document already carries. Zero is the only number
// that allows an attachment to change, which is also why attaching is not part
// of signing: a document is prepared, then signed, and the order is the
// guarantee.
func CheckAttachable(signatures int, content []byte) error {
	if signatures > 0 {
		return ErrArtifactFrozen
	}
	if len(content) == 0 {
		return ErrArtifactEmpty
	}
	if len(content) > MaxArtifactBytes {
		return ErrArtifactTooLarge
	}
	return nil
}
