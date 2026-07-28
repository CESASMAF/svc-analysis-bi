# TICKET-008 — Poison event: fechar a raiz (id não-UUID) em vez de tratar sintoma

## Origem

Code review do PR CESASMAF/svc-analysis-bi#2 (`fix/poison-event-consumer`).
O PR corrige o travamento do consumidor no `MarkProcessed`, mas o review
levantou seis achados. Este ticket ataca a raiz e fecha os cinco relevantes.

## Achados a resolver

| # | Severidade | Achado |
|---|---|---|
| A1 | 🔴 crítico | `ack` sem gravar o dedup marker → **contagem dupla** em 5 dos 7 fatos (os `Increment*` fazem `col = col + EXCLUDED.col`, não são idempotentes) |
| A2 | 🟠 alto | Correção cobre 1 de 4 caminhos; os 3 `SendToDLQ` continuam travando (`event_dlq.event_id` também é UUID; e `eventID = "unknown"` nas linhas 158/191 nunca é UUID) |
| A3 | 🟠 médio | `Ack()` significa "processado com sucesso"; para poison o JetStream tem `Term()`, que emite advisory `$JS.EVENT.ADVISORY.CONSUMER.MSG_TERMINATED` |
| A5 | 🟡 baixo | Doc comment do `extractEventID` ficou órfão, colado em `isPermanentDBError` |
| A6 | 🟡 baixo | `strings.HasPrefix(code, "22")` cobre a classe inteira (inclui `22001`/`22003`, que são bug de schema, não dado ruim do produtor) |

## Decisão de projeto

**Atacar a raiz.** O problema não é "o que fazer quando o insert falha", é "o id
textual não cabe numa coluna UUID". Normalizando o id na fronteira de
persistência (`PgEventStore`), com **UUIDv5 determinístico** (RFC 4122 §4.3)
derivado do id original quando ele não for um UUID:

- o **marker volta a ser gravado** → dedup preservado → A1 resolvido na origem;
- os **três caminhos de DLQ param de falhar** → A2 resolvido junto, sem repetir
  o tratamento de erro em quatro lugares;
- `isPermanentDBError` **permanece** como defesa em profundidade, agora restrito
  a `22P02` (A6), para o caso de aparecer outro dado impossível.

UUIDv5 é determinístico: o mesmo id textual gera sempre o mesmo UUID, então
`IsProcessed` e `MarkProcessed` continuam concordando entre si e entre réplicas.
Mesmo princípio do `DeterministicUUID` do `svc-social-care` (ADR-021).

## Fora de escopo

- Métrica Prometheus (A4): o repo não tem infraestrutura de métricas. O advisory
  do `Term()` (A3) cobre a observabilidade no lado do servidor NATS, que é o
  ganho principal; métrica de aplicação fica como item separado.
- `MaxDeliver` no consumidor: mudança de configuração de infra, não de código.

## Referências

- Vaughn Vernon, *Implementing Domain-Driven Design*, p. 412 — "Event De-duplication" e "An Idempotent Operation"
- RFC 4122 §4.3 — name-based UUID (v5, SHA-1)
- NATS JetStream — [Term e advisory de DLQ](https://docs.nats.io/using-nats/developer/develop_jetstream/consumers)
