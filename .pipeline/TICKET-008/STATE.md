# TICKET-008 Pipeline State

| Wave | Agent | Status | Started | Completed |
|------|-------|--------|---------|-----------|
| W0 | test-writer (RED) | DONE | 2026-07-28 | 2026-07-28 |
| W1 | implementer (GREEN) | DONE | 2026-07-28 | 2026-07-28 |
| W2 | code-reviewer | DONE (R1: APPROVED) | 2026-07-28 | 2026-07-28 |
| W3 | go-quality-checker | DONE (PASSED) | 2026-07-28 | 2026-07-28 |

## Notes

- Origem: code review do PR #2 (`fix/poison-event-consumer`), 6 achados.
- **Decisão-chave:** atacar a raiz (id textual não cabe em coluna UUID) em vez do
  sintoma. `normalizeEventID` no `PgEventStore` resolve A1 e A2 de uma vez — o
  marker de dedup volta a ser gravado e os três `SendToDLQ` param de falhar.
- A1 era o mais grave: dar ack sem marker abre espaço para **contagem dupla** nos
  5 fatos `Increment*`, que somam (`col = col + EXCLUDED.col`) e portanto não são
  idempotentes — ao contrário do que o comentário do PR #2 afirmava.
- A3: `Term()` no lugar de `Ack()` para descarte; ganha o advisory
  `MSG_TERMINATED`, que cobre parte do A4 sem infra de métricas.
- W0 RED verificado por falha de compilação (símbolos inexistentes).
- W3: build/vet/testes/race todos limpos; lógica nova a 100% de cobertura.
- Pendências fora de escopo registradas no W3: métrica de aplicação, `gofmt -w`
  no repo (16 arquivos pré-existentes), `MaxDeliver` no consumidor.
- TICKET-008: COMPLETE
