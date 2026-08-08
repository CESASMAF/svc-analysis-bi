package store

// AllMigrations returns all database migrations in version order.
// Forward-only, no rollback support (ADR-001).
func AllMigrations() []Migration {
	return []Migration{
		{
			Version: 1,
			Name:    "create dimension tables",
			SQL: `
CREATE TABLE IF NOT EXISTS dim_geography (
    id              SERIAL PRIMARY KEY,
    mesoregion_code VARCHAR(4) NOT NULL,
    mesoregion_name VARCHAR(100) NOT NULL,
    microregion_code VARCHAR(5),
    microregion_name VARCHAR(100),
    state_code      VARCHAR(2) NOT NULL,
    region          VARCHAR(20) NOT NULL,
    UNIQUE(mesoregion_code, microregion_code)
);

CREATE TABLE IF NOT EXISTS dim_age_band (
    id         SERIAL PRIMARY KEY,
    band_label VARCHAR(10) NOT NULL UNIQUE,
    min_age    INT NOT NULL,
    max_age    INT
);

CREATE TABLE IF NOT EXISTS dim_diagnosis (
    id        SERIAL PRIMARY KEY,
    icd_code  VARCHAR(10) NOT NULL UNIQUE,
    icd_label VARCHAR(255) NOT NULL,
    chapter   VARCHAR(5),
    block     VARCHAR(10)
);

CREATE TABLE IF NOT EXISTS dim_sex (
    id    SERIAL PRIMARY KEY,
    label VARCHAR(20) NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS dim_housing_type (
    id    SERIAL PRIMARY KEY,
    label VARCHAR(30) NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS dim_education_level (
    id    SERIAL PRIMARY KEY,
    label VARCHAR(50) NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS dim_benefit_type (
    id    SERIAL PRIMARY KEY,
    label VARCHAR(100) NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS dim_period (
    id         SERIAL PRIMARY KEY,
    year       INT NOT NULL,
    month      INT NOT NULL,
    year_month VARCHAR(7) NOT NULL UNIQUE,
    quarter    INT NOT NULL
);

CREATE TABLE IF NOT EXISTS dim_referral_destination (
    id    SERIAL PRIMARY KEY,
    label VARCHAR(100) NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS dim_violation_type (
    id    SERIAL PRIMARY KEY,
    label VARCHAR(100) NOT NULL UNIQUE
);`,
		},
		{
			Version: 2,
			Name:    "create fact tables",
			SQL: `
CREATE TABLE IF NOT EXISTS fact_patient_snapshot (
    id                      BIGSERIAL PRIMARY KEY,
    period_id               INT NOT NULL REFERENCES dim_period(id),
    age_band_id             INT NOT NULL REFERENCES dim_age_band(id),
    sex_id                  INT NOT NULL REFERENCES dim_sex(id),
    geography_id            INT NOT NULL REFERENCES dim_geography(id),
    housing_type_id         INT REFERENCES dim_housing_type(id),
    education_level_id      INT REFERENCES dim_education_level(id),
    income_band             VARCHAR(20),
    receives_benefit        BOOLEAN,
    has_deficiency          BOOLEAN,
    food_insecurity         BOOLEAN,
    is_overcrowded          BOOLEAN,
    family_size             INT,
    assessment_completeness DECIMAL(3,2),
    patient_hash            VARCHAR(64) NOT NULL,
    UNIQUE(period_id, patient_hash)
);

CREATE TABLE IF NOT EXISTS fact_diagnosis (
    id              BIGSERIAL PRIMARY KEY,
    period_id       INT NOT NULL REFERENCES dim_period(id),
    diagnosis_id    INT NOT NULL REFERENCES dim_diagnosis(id),
    geography_id    INT NOT NULL REFERENCES dim_geography(id),
    age_band_id     INT NOT NULL REFERENCES dim_age_band(id),
    sex_id          INT NOT NULL REFERENCES dim_sex(id),
    new_cases       INT NOT NULL DEFAULT 0,
    total_cases     INT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS fact_appointment (
    id               BIGSERIAL PRIMARY KEY,
    period_id        INT NOT NULL REFERENCES dim_period(id),
    geography_id     INT NOT NULL REFERENCES dim_geography(id),
    appointment_type VARCHAR(50),
    count            INT NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS fact_referral (
    id              BIGSERIAL PRIMARY KEY,
    period_id       INT NOT NULL REFERENCES dim_period(id),
    geography_id    INT NOT NULL REFERENCES dim_geography(id),
    destination_id  INT NOT NULL REFERENCES dim_referral_destination(id),
    count           INT NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS fact_violation (
    id                BIGSERIAL PRIMARY KEY,
    period_id         INT NOT NULL REFERENCES dim_period(id),
    geography_id      INT NOT NULL REFERENCES dim_geography(id),
    violation_type_id INT NOT NULL REFERENCES dim_violation_type(id),
    count             INT NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS fact_benefit (
    id                BIGSERIAL PRIMARY KEY,
    period_id         INT NOT NULL REFERENCES dim_period(id),
    geography_id      INT NOT NULL REFERENCES dim_geography(id),
    benefit_type_id   INT NOT NULL REFERENCES dim_benefit_type(id),
    beneficiary_count INT NOT NULL DEFAULT 0,
    total_amount      DECIMAL(12,2)
);

CREATE TABLE IF NOT EXISTS fact_family_composition (
    id                       BIGSERIAL PRIMARY KEY,
    period_id                INT NOT NULL REFERENCES dim_period(id),
    geography_id             INT NOT NULL REFERENCES dim_geography(id),
    avg_family_size          DECIMAL(4,1),
    total_families           INT NOT NULL DEFAULT 0,
    families_with_elderly    INT NOT NULL DEFAULT 0,
    families_with_children   INT NOT NULL DEFAULT 0
);`,
		},
		{
			Version: 3,
			Name:    "create control tables",
			SQL: `
CREATE TABLE IF NOT EXISTS event_processing_log (
    event_id     UUID PRIMARY KEY,
    event_type   VARCHAR(100) NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status       VARCHAR(20) NOT NULL DEFAULT 'processed'
);

CREATE TABLE IF NOT EXISTS event_dlq (
    id          BIGSERIAL PRIMARY KEY,
    event_id    UUID NOT NULL,
    event_type  VARCHAR(100) NOT NULL,
    payload     JSONB NOT NULL,
    error       TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    retried_at  TIMESTAMPTZ,
    retry_count INT NOT NULL DEFAULT 0
);`,
		},
		{
			Version: 4,
			Name:    "add unique constraints for fact table upserts",
			SQL: `
ALTER TABLE fact_diagnosis
    ADD CONSTRAINT uq_fact_diagnosis_composite
    UNIQUE (period_id, diagnosis_id, geography_id, age_band_id, sex_id);

ALTER TABLE fact_appointment
    ADD CONSTRAINT uq_fact_appointment_composite
    UNIQUE (period_id, geography_id, appointment_type);

ALTER TABLE fact_referral
    ADD CONSTRAINT uq_fact_referral_composite
    UNIQUE (period_id, geography_id, destination_id);

ALTER TABLE fact_violation
    ADD CONSTRAINT uq_fact_violation_composite
    UNIQUE (period_id, geography_id, violation_type_id);

ALTER TABLE fact_benefit
    ADD CONSTRAINT uq_fact_benefit_composite
    UNIQUE (period_id, geography_id, benefit_type_id);

ALTER TABLE fact_family_composition
    ADD CONSTRAINT uq_fact_family_composition_composite
    UNIQUE (period_id, geography_id);`,
		},
		{
			Version: 5,
			Name:    "create materialized views for indicator queries",
			SQL: `
CREATE MATERIALIZED VIEW IF NOT EXISTS mv_demographics AS
SELECT
    ab.band_label AS age_band,
    s.label AS sex,
    g.mesoregion_code,
    g.mesoregion_name,
    p.year_month AS period,
    COUNT(*) AS count
FROM fact_patient_snapshot fps
JOIN dim_age_band ab ON fps.age_band_id = ab.id
JOIN dim_sex s ON fps.sex_id = s.id
JOIN dim_geography g ON fps.geography_id = g.id
JOIN dim_period p ON fps.period_id = p.id
GROUP BY ab.band_label, s.label, g.mesoregion_code, g.mesoregion_name, p.year_month;

CREATE UNIQUE INDEX IF NOT EXISTS idx_mv_demographics
ON mv_demographics (age_band, sex, mesoregion_code, period);

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_epidemiological AS
SELECT
    d.icd_code,
    d.icd_label,
    p.year_month AS period,
    SUM(fd.total_cases) AS total_cases,
    SUM(fd.new_cases) AS new_cases
FROM fact_diagnosis fd
JOIN dim_diagnosis d ON fd.diagnosis_id = d.id
JOIN dim_period p ON fd.period_id = p.id
GROUP BY d.icd_code, d.icd_label, p.year_month;

CREATE UNIQUE INDEX IF NOT EXISTS idx_mv_epidemiological
ON mv_epidemiological (icd_code, period);`,
		},
		{
			Version: 6,
			Name:    "add care-pathway lifecycle to patient snapshot",
			SQL: `
-- Coluna, não dimensão: o conjunto é pequeno, fechado e vem de evento de
-- domínio (ADMITTED/DISCHARGED/READMITTED/WITHDRAWN). Segue o precedente de
-- income_band, que também é valor direto.
--
-- lifecycle_reason guarda o motivo CATEGÓRICO da transição (motivo de alta,
-- por exemplo). O campo 'notes' dos eventos de origem é texto livre escrito por
-- técnico e pode nomear pessoas — não é lido nem armazenado.
ALTER TABLE fact_patient_snapshot
    ADD COLUMN IF NOT EXISTS lifecycle_status VARCHAR(20),
    ADD COLUMN IF NOT EXISTS lifecycle_reason VARCHAR(80);

CREATE INDEX IF NOT EXISTS idx_fps_lifecycle_status
ON fact_patient_snapshot (lifecycle_status)
WHERE lifecycle_status IS NOT NULL;`,
		},
	}
}
