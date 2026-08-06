# ADR-002: Ciclo de vida do atendimento e evento de erasure

**Data:** 2026-08-06
**Status:** Aceito
**Contexto:** auditoria de consistência entre sistemas (`CESASMAF/auditoria-skills-2026-08-06/AUDITORIA-SISTEMAS.md`, P1)

## Contexto

O `svc-social-care` publica 22 tipos de evento. Este serviço consumia 17. Os
cinco restantes chegavam pelo filtro `social-care.events.>`, caíam na DLQ com
`Ack` — sem redelivery, sem erro, sem ruído — e sumiam:

`PatientAdmittedEvent`, `PatientDischargedEvent`, `PatientReadmittedEvent`,
`PatientWithdrawnFromWaitlistEvent`, `PatientPIIAnonymizedEvent`.

Os quatro primeiros são as transições do **ciclo de vida do atendimento**. Um
serviço de analytics assistencial que não sabe quando alguém entrou ou teve alta
não responde à pergunta mais básica do domínio. O quinto é outra natureza: é o
sinal de que uma pessoa exerceu o direito de eliminação (LGPD Art. 18 V,
ADR-039 do social-care).

## Decisão

### 1. Ciclo de vida é COLUNA no snapshot, não dimensão nova

`fact_patient_snapshot` ganha `lifecycle_status` e `lifecycle_reason`
(migration 6).

Coluna e não `dim_lifecycle_status` porque o conjunto é pequeno, fechado e vem
de evento de domínio — não de texto digitado. Não há atributo a pendurar num
registro de dimensão, e uma dimensão de quatro linhas com FK só adiciona um
JOIN a toda consulta. É o mesmo tratamento que `income_band` já recebe.

### 2. A transição faz UPDATE, nunca upsert

`UpdatePatientLifecycle` atualiza a linha existente do período e **não cria**
linha nenhuma.

A razão é dura: o snapshot carrega as dimensões demográficas (`age_band_id`,
`sex_id`, `geography_id`, todas `NOT NULL`), que chegam com `PatientCreated`.
Um upsert disparado por evento de ciclo de vida ou violaria essas constraints,
ou — pior — sobrescreveria um snapshot populado com dimensões vazias, perdendo
a demografia para registrar um status.

Quando não há linha no período, a transição é **pulada e registrada em log**.
Isso é esperado, não excepcional: quem foi admitido em março e teve alta em
julho não tem snapshot de julho até o carry-forward criar. Falhar aqui travaria
a ingestão inteira por causa de uma linha que existe amanhã.

### 3. `notes` nunca é lido

Discharged, Readmitted e Withdrawn carregam um `notes` de texto livre, escrito
pelo técnico que registrou a transição. Pode nomear a pessoa, familiares,
endereço ou condição de saúde.

O campo **não é declarado** na struct de leitura. Declarar sem usar o deixaria a
uma linha de distância de ser persistido — e a promessa "PII não chega ao
analysis-bi" não deve depender de ninguém lembrar disso numa revisão futura.

O motivo categórico viaja em `reason`, que é fechado, e é esse que entra em
`lifecycle_reason`.

### 4. `PatientPIIAnonymized` é reconhecido e não faz nada

O evento é tratado explicitamente e materializa `FactKindNone`.

**Por que não fazer nada é o correto.** Este serviço nunca guardou o dado que
foi apagado. A identidade do paciente é um HMAC-SHA256 salgado e irreversível;
o resto é generalizado na origem (faixa etária, sexo, mesorregião) desde a
mudança de contrato de 2026-08-06. Não existe aqui PII a eliminar — apagar as
linhas de fato destruiria estatística agregada e não aumentaria a proteção de
ninguém.

**Por que ainda assim é tratado.** "Decidimos que este evento não tem efeito" e
"esquecemos de tratar este evento" são coisas diferentes, e na DLQ as duas têm
exatamente a mesma aparência. `FactKindNone` existe para que a decisão seja
visível como decisão — no código, no log e no gate de contrato.

**Revisar se:** este serviço passar a guardar qualquer atributo que permita
reidentificação, ou se o salt do hash deixar de ser secreto. Nesses dois casos a
premissa cai e o evento passa a exigir ação.

## Consequências

- O gate `scripts/check-event-contract.py` fica verde: 22 publicados, 22 consumidos.
- As divergências que restam são intencionais e estão em `scripts/.eventcontract-ignore`
  **com o motivo escrito** — `notes` (privacidade) e `reason` ausente em
  Admitted/Readmitted (entrar não precisa de justificativa).
- `lifecycle_status` ainda não alimenta view materializada nem endpoint de
  indicador. É o próximo passo natural, e é aditivo.
