package store

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TICKET-008 / W0 (RED) — normalização do event id na fronteira de persistência.
//
// As colunas `event_processing_log.event_id` e `event_dlq.event_id` são UUID.
// Um evento cujo id não é UUID (ou o literal "unknown", usado quando o id não
// pôde ser extraído do payload) faz TODO insert falhar com SQLSTATE 22P02 —
// o que travava o consumidor e, na correção anterior, fazia o marker de dedup
// ser pulado (abrindo espaço para contagem dupla nos fatos `Increment*`).
//
// A saída é normalizar o id ANTES do SQL: se já for UUID, passa intacto; se não
// for, deriva um UUIDv5 (RFC 4122 §4.3) determinístico do valor original.
// ---------------------------------------------------------------------------

func TestNormalizeEventID_ValidUUIDPassesThroughUnchanged(t *testing.T) {
	// Um id que já é UUID NÃO pode ser transformado — senão a dedup de todos os
	// eventos bem-formados (a esmagadora maioria) mudaria de chave.
	in := "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	got := normalizeEventID(in)
	if got != in {
		t.Errorf("UUID válido deve passar intacto\n  in:  %s\n  got: %s", in, got)
	}
}

func TestNormalizeEventID_UppercaseUUIDIsCanonicalizedToLowercase(t *testing.T) {
	// Postgres devolve UUID em minúsculas. Normalizar aqui evita que o mesmo
	// evento gere duas chaves distintas dependendo de como o produtor formatou.
	got := normalizeEventID("3F2504E0-4F89-11D3-9A0C-0305E82C3301")
	want := "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	if got != want {
		t.Errorf("UUID maiúsculo deve virar minúsculo\n  got:  %s\n  want: %s", got, want)
	}
}

func TestNormalizeEventID_NonUUIDBecomesDeterministicUUIDv5(t *testing.T) {
	got := normalizeEventID("not-a-uuid")

	if !isUUID(got) {
		t.Fatalf("id não-UUID deve virar um UUID válido, veio %q", got)
	}
	// Versão 5 (name-based, SHA-1) e variante RFC 4122: o 15º nibble é '5' e o
	// 17º caractere está em [8,9,a,b].
	if got[14] != '5' {
		t.Errorf("esperava UUID versão 5, veio versão %c em %q", got[14], got)
	}
	if v := got[19]; v != '8' && v != '9' && v != 'a' && v != 'b' {
		t.Errorf("esperava variante RFC 4122, veio %c em %q", v, got)
	}
}

func TestNormalizeEventID_IsDeterministic(t *testing.T) {
	// Sem isto a dedup não funciona: duas execuções (ou duas réplicas) gerariam
	// chaves diferentes para o mesmo evento e ele seria materializado 2x.
	first := normalizeEventID("evento-malformado-001")
	second := normalizeEventID("evento-malformado-001")
	if first != second {
		t.Errorf("derivação deve ser determinística\n  1ª: %s\n  2ª: %s", first, second)
	}
}

func TestNormalizeEventID_DistinctInputsProduceDistinctUUIDs(t *testing.T) {
	a := normalizeEventID("evento-a")
	b := normalizeEventID("evento-b")
	if a == b {
		t.Errorf("ids distintos não podem colidir: ambos viraram %s", a)
	}
}

func TestNormalizeEventID_UnknownLiteralIsHandled(t *testing.T) {
	// `pipeline.go` usa o literal "unknown" quando não consegue extrair o id do
	// payload. Era o caso MAIS provável de travar a fila (todo evento sem `id`
	// que caísse em DLQ) e precisa virar um UUID estável como qualquer outro.
	got := normalizeEventID("unknown")
	if !isUUID(got) {
		t.Fatalf(`o literal "unknown" deve virar UUID válido, veio %q`, got)
	}
	if got != normalizeEventID("unknown") {
		t.Error(`"unknown" deve derivar sempre o mesmo UUID`)
	}
}

func TestNormalizeEventID_EmptyStringIsHandled(t *testing.T) {
	got := normalizeEventID("")
	if !isUUID(got) {
		t.Fatalf("string vazia deve virar UUID válido, veio %q", got)
	}
}

// isUUID valida o formato canônico 8-4-4-4-12 hexadecimal. Helper de teste —
// a validação de produção vive em normalizeEventID.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}
