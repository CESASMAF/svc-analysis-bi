package store

import (
	"crypto/sha1" // #nosec G505 — SHA-1 aqui não é primitiva de segurança: é o hash que a RFC 4122 §4.3 exige para UUIDv5.
	"encoding/hex"
	"strings"
)

// uuidNamespaceAnalysisBIEvents é o namespace (UUIDv4 fixo) usado para derivar
// UUIDv5 de ids de evento não-UUID. Nunca deve mudar: alterá-lo troca a chave de
// dedup de todos os eventos malformados já processados, que passariam a ser
// materializados de novo.
var uuidNamespaceAnalysisBIEvents = [16]byte{
	0x6b, 0xa7, 0xb8, 0x14, 0x9d, 0xad, 0x11, 0xd1,
	0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
}

// normalizeEventID devolve um id de evento sempre gravável nas colunas UUID de
// `event_processing_log` e `event_dlq`.
//
// Eventos bem-formados trazem `id` UUID e passam intactos (apenas canonizados
// para minúsculas). Eventos malformados — id textual, ou o literal "unknown"
// que o pipeline usa quando não consegue extrair o `id` do payload — recebem um
// UUIDv5 (RFC 4122 §4.3) derivado do valor original.
//
// A derivação é **determinística**: o mesmo id textual gera sempre o mesmo UUID,
// em qualquer réplica e em qualquer execução. É isso que preserva a deduplicação
// para esses eventos — sem ela, o marker não seria gravado e o evento poderia ser
// materializado mais de uma vez, inflando os fatos `Increment*` (que somam,
// `col = col + EXCLUDED.col`, e portanto NÃO são idempotentes).
//
// Mesmo princípio do `DeterministicUUID` do svc-social-care (ADR-021).
func normalizeEventID(rawID string) string {
	if canonical, ok := canonicalUUID(rawID); ok {
		return canonical
	}
	return uuidV5(uuidNamespaceAnalysisBIEvents, rawID)
}

// canonicalUUID valida o formato 8-4-4-4-12 e devolve a forma em minúsculas.
// O segundo retorno é false se `s` não for um UUID.
func canonicalUUID(s string) (string, bool) {
	if len(s) != 36 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return "", false
			}
			continue
		}
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return "", false
		}
	}
	return strings.ToLower(s), true
}

// uuidV5 implementa o UUID name-based com SHA-1 da RFC 4122 §4.3: hash do
// namespace concatenado ao nome, com os campos de versão (5) e variante (RFC
// 4122) sobrescritos nos bits definidos pela especificação.
func uuidV5(namespace [16]byte, name string) string {
	h := sha1.New() // #nosec G401 — idem: exigência da RFC 4122 §4.3, não uso criptográfico.
	h.Write(namespace[:])
	h.Write([]byte(name))
	sum := h.Sum(nil)

	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x50 // versão 5 nos 4 bits altos do byte 6
	u[8] = (u[8] & 0x3f) | 0x80 // variante RFC 4122 nos 2 bits altos do byte 8

	buf := make([]byte, 0, 36)
	hexAppend := func(b []byte) { buf = append(buf, []byte(hex.EncodeToString(b))...) }
	hexAppend(u[0:4])
	buf = append(buf, '-')
	hexAppend(u[4:6])
	buf = append(buf, '-')
	hexAppend(u[6:8])
	buf = append(buf, '-')
	hexAppend(u[8:10])
	buf = append(buf, '-')
	hexAppend(u[10:16])
	return string(buf)
}
