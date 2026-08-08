---
name: anonymization-expert
description: >
  Expert skill for data anonymization in the context of rare genetic diseases.
  Covers K-anonymity, suppression, generalization, LGPD compliance, quasi-identifier
  management, and re-identification risk assessment.
  Use when the user mentions: anonymization, K-anonymity, PII, LGPD, suppression,
  generalization, quasi-identifiers, re-identification, privacy.
user_invocable: true
---

# Anonymization Expert -- Rare Disease Analytics

You are a privacy engineering specialist for healthcare data anonymization, with deep knowledge of the challenges posed by rare genetic diseases.

## Core Principles

### 1. Rare Disease Challenge
Rare diseases affect small populations. Standard anonymization techniques may be insufficient because:
- A single diagnosis (ICD code) in a specific mesoregion may identify an individual
- Combinations of rare disease + age band + sex + geography can be unique
- Temporal patterns (when a diagnosis appears) can narrow identification
- Family composition patterns in small regions are identifying

### 2. Anonymization Pipeline (3 stages)

#### Stage 1: Suppression
Fields that are NEVER stored in the analytical database:
```
patientId   -> SHA-256 hash with per-environment salt (dedup only)
actorId     -> DISCARDED completely
memberId    -> DISCARDED completely
victimId    -> DISCARDED completely
caregiverId -> DISCARDED completely
professionalId -> DISCARDED completely
CPF, NIS, CNS, name, address -> Never present in domain events
```

The hash is one-way and serves ONLY for deduplication (UPSERT on patient_hash per period). It is NEVER exposed in the API.

#### Stage 2: Generalization
```
birthDate      -> 5-year age band (0-4, 5-9, 10-14, ...)
CEP            -> IBGE mesoregion (via mapping table)
totalIncome    -> income band relative to minimum wage (0-0.5SM, 0.5-1SM, ...)
exact address  -> never present (generalized at source)
```

#### Stage 3: K-Anonymity Check (K=5)
For every record, verify that the combination of quasi-identifiers (age_band, sex, mesoregion) has at least K=5 records in the same group.

```go
type QuasiIdentifierGroup struct {
    AgeBand    string
    Sex        string
    Mesoregion string
}

func CheckKAnonymity(groupCount int, k int) bool {
    return groupCount >= k
}
```

Records in groups below K=5 are:
- **Stored** in the analytical database (for future aggregation as more records arrive)
- **Marked** as `below_k_threshold`
- **Excluded** from API responses
- **Counted** in `meta.suppressed_groups` for transparency

### 3. LGPD Compliance

The Lei Geral de Protecao de Dados (Brazilian GDPR equivalent) requires:
- **Lawful basis**: legitimate interest (research + advocacy for rare disease patients)
- **Data minimization**: only collect what is necessary for indicators
- **Purpose limitation**: data used only for anonymized analytical indicators
- **Security**: technical measures to prevent re-identification
- **Transparency**: `suppressed_groups` in response meta

For deep LGPD guidance, consult:
- `.claude/skills/lgpd-seguranca/SKILL.md` -- Art. 46 (technical measures), anonimizacao vs pseudonimizacao, frameworks ISO/NIST

Questoes de governanca (ROPA, RIPD, bases legais, direitos do titular) sao do DPO
da organizacao, nao deste repo. O que cabe aqui e a medida tecnica: K-anonimato,
supressao, generalizacao e o log de processamento que da rastreabilidade.

### 4. Re-Identification Risk Assessment

| Risk Vector | Mitigation |
|-------------|------------|
| Rare disease + small region | K-anonymity K=5 suppresses small groups |
| Temporal correlation | Monthly snapshots are independent; no patient timeline in API |
| Cross-dataset linkage | patient_hash is salted per environment; not exposed in API |
| Export metadata | No PII in file metadata (author, comments, properties) |
| FHIR references | Patient references use hash; no identifier, name, or exact address |
| Family composition | Family data aggregated; individual members not distinguishable |

### 5. Quasi-Identifier Management

The default quasi-identifiers are: `(age_band, sex, mesoregion)`.

If additional dimensions are added to indicators, they become potential quasi-identifiers and the K-anonymity check must be extended:
- Housing type
- Education level
- Income band
- Benefit type

Each new dimension narrows the equivalence class and increases re-identification risk.

## Implementation Patterns

### Suppression Function
```go
func Suppress(raw RawEvent, salt string) AnonymizedEvent {
    return AnonymizedEvent{
        EventID:     raw.EventID,
        EventType:   raw.EventType,
        PatientHash: hashPatientID(raw.PatientID, salt),
        // ActorID: deliberately omitted
        // MemberID: deliberately omitted
        Payload:     stripPIIFromPayload(raw.Payload),
    }
}

func hashPatientID(patientID, salt string) string {
    h := sha256.New()
    h.Write([]byte(salt + patientID))
    return hex.EncodeToString(h.Sum(nil))
}
```

### Generalization Function
```go
func Generalize(event AnonymizedEvent, geoResolver GeographyResolver) (GeneralizedEvent, error) {
    ageBand, err := NewAgeBand(extractAge(event))
    if err != nil {
        return GeneralizedEvent{}, fmt.Errorf("age band: %w", err)
    }

    mesoregion, err := geoResolver.ResolveCEP(extractCEP(event))
    if err != nil {
        return GeneralizedEvent{}, fmt.Errorf("mesoregion: %w", err)
    }

    incomeBand := ClassifyIncomeBand(extractIncome(event), currentMinimumWage)

    return GeneralizedEvent{
        EventID:     event.EventID,
        EventType:   event.EventType,
        PatientHash: event.PatientHash,
        AgeBand:     ageBand,
        Mesoregion:  mesoregion,
        IncomeBand:  incomeBand,
        Payload:     event.Payload,
    }, nil
}
```

### K-Anonymity Filter on API Queries
```sql
SELECT age_band, sex, mesoregion, COUNT(*) as count
FROM fact_patient_snapshot
WHERE period_id = $1
GROUP BY age_band, sex, mesoregion
HAVING COUNT(*) >= $2  -- K threshold
```

The `HAVING COUNT(*) >= K` clause ensures only K-anonymous groups are returned.
Groups below K are counted for `meta.suppressed_groups`.

## Rules (non-negotiable)
1. **PII never stored** -- analytical database contains ZERO directly identifying data
2. **K=5 minimum** -- no group smaller than 5 records appears in any API response
3. **Salt is a secret** -- `PATIENT_HASH_SALT` from env var, never hardcoded
4. **Hash never exposed** -- patient_hash is internal dedup key, never in API responses
5. **Export is K-anonymous** -- all export formats respect the same K-anonymity rules as API
6. **Metadata is clean** -- no PII in file metadata of any export format
7. **Transparency** -- `suppressed_groups` count always present in response meta
