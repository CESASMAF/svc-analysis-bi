# TICKET-008 · W0 — RED (test-writer)

**Status:** DONE · **Data:** 2026-07-28

## Objetivo

Escrever os testes que descrevem o contrato das correções do code review do PR #2
e verificar que eles **falham** antes da implementação (TDD).

## Testes escritos

### `internal/store/event_id_test.go` (novo, 8 casos)

Contrato do `normalizeEventID` — a normalização do id na fronteira de persistência:

| Teste | Fixa o quê |
|---|---|
| `ValidUUIDPassesThroughUnchanged` | id já-UUID **não** pode ser transformado — senão a chave de dedup de todos os eventos bem-formados mudaria |
| `UppercaseUUIDIsCanonicalizedToLowercase` | mesmo evento não pode gerar duas chaves por formatação do produtor |
| `NonUUIDBecomesDeterministicUUIDv5` | valida versão (nibble 15 = `5`) e variante RFC 4122 (nibble 17 ∈ `8,9,a,b`) |
| `IsDeterministic` | **o teste central**: sem determinismo a dedup não funciona e o evento é materializado 2× |
| `DistinctInputsProduceDistinctUUIDs` | ids diferentes não podem colidir |
| `UnknownLiteralIsHandled` | o literal `"unknown"` (usado em `pipeline.go:158,191`) era o caso mais provável de travar a fila |
| `EmptyStringIsHandled` | borda |

### `internal/ingestion/pipeline_test.go` (+3 casos)

| Teste | Fixa o quê |
|---|---|
| `PermanentMarkError_TerminatesInsteadOfAcking` | descarte usa `Term()`, **e** afirma que `Ack()` não é chamado (semântica: descarte ≠ sucesso) |
| `PermanentDLQError_TerminatesInsteadOfLooping` | fecha o caminho **mais provável** de travamento (handler falha → DLQ falha), não coberto pelo PR #2 |
| `TransientDLQError_LeavesMessageForRedelivery` | contrapeso: falha transitória **não** consome a mensagem (sem Ack e sem Term) — impede que a correção vire perda de evento |

## Verificação RED

```
internal/store/event_id_test.go:25:9: undefined: normalizeEventID
… (10 ocorrências)
FAIL github.com/acdgbrasil/svc-analysis-bi/internal/store [build failed]
FAIL github.com/acdgbrasil/svc-analysis-bi/internal/ingestion [build failed]
```

Falha por símbolo inexistente (`normalizeEventID`, `RawMessage.Term`) — RED
legítimo: nenhum teste passa sem implementação.

## Observação de design registrada no W0

Os três testes de pipeline formam um **par de contrapeso**: dois exigem que a
mensagem seja consumida em falha permanente, o terceiro exige que **não** seja
em falha transitória. Isoladamente, cada um admitiria uma implementação
preguiçosa (terminar tudo, ou nunca terminar); juntos, fixam a decisão.
