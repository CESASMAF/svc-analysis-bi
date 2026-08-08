package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/acdgbrasil/svc-analysis-bi/internal/ingestion"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors for fact store operations.
var (
	ErrFactInsertFailed  = errors.New("store: fact insert failed")
	ErrFactUpsertFailed  = errors.New("store: fact upsert failed")
	ErrNilPayload        = errors.New("store: record payload is nil for the declared kind")
	ErrMissingDimensions = errors.New("store: failed to resolve dimension IDs")
)

// PgFactStore implements the ingestion.FactStore interface using pgx
// and delegates dimension ID resolution to PgDimensionStore.
type PgFactStore struct {
	pool *pgxpool.Pool
	dims *PgDimensionStore
}

// NewFactStore creates a PgFactStore backed by the given pool.
// It uses a PgDimensionStore internally to resolve dimension foreign keys.
func NewFactStore(pool *pgxpool.Pool) *PgFactStore {
	return &PgFactStore{
		pool: pool,
		dims: NewDimensionStore(pool),
	}
}

// Verify PgFactStore satisfies ingestion.FactStore at compile time.
var _ ingestion.FactStore = (*PgFactStore)(nil)

// UpsertPatientSnapshot inserts or updates a patient snapshot for the given
// period. The UNIQUE(period_id, patient_hash) constraint ensures that only
// one snapshot per patient per period exists.
func (s *PgFactStore) UpsertPatientSnapshot(ctx context.Context, record ingestion.AnonymizedRecord) error {
	p := record.Snapshot
	if p == nil {
		return fmt.Errorf("%w: expected Snapshot payload for kind %q", ErrNilPayload, record.Kind)
	}

	periodID, err := s.dims.GetOrCreatePeriod(ctx, record.Period)
	if err != nil {
		return fmt.Errorf("%w: period: %v", ErrMissingDimensions, err)
	}

	ageBandID, err := s.dims.GetOrCreateAgeBand(ctx, p.AgeBand)
	if err != nil {
		return fmt.Errorf("%w: age_band: %v", ErrMissingDimensions, err)
	}

	sexID, err := s.dims.GetOrCreateSex(ctx, string(p.Sex))
	if err != nil {
		return fmt.Errorf("%w: sex: %v", ErrMissingDimensions, err)
	}

	geoID, err := s.dims.UpsertGeography(ctx, p.Geography)
	if err != nil {
		return fmt.Errorf("%w: geography: %v", ErrMissingDimensions, err)
	}

	// Resolve optional dimensions. A zero/empty value means NULL in the fact row.
	var housingTypeID *int
	if p.HousingType != "" {
		id, err := s.dims.GetOrCreate(ctx, "dim_housing_type", p.HousingType)
		if err != nil {
			return fmt.Errorf("%w: housing_type: %v", ErrMissingDimensions, err)
		}
		housingTypeID = &id
	}

	var educationLevelID *int
	if p.EducationLevel != "" {
		id, err := s.dims.GetOrCreate(ctx, "dim_education_level", p.EducationLevel)
		if err != nil {
			return fmt.Errorf("%w: education_level: %v", ErrMissingDimensions, err)
		}
		educationLevelID = &id
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO fact_patient_snapshot (
			period_id, age_band_id, sex_id, geography_id,
			housing_type_id, education_level_id, income_band,
			receives_benefit, has_deficiency, food_insecurity,
			is_overcrowded, family_size, assessment_completeness,
			patient_hash
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (period_id, patient_hash) DO UPDATE SET
			age_band_id             = EXCLUDED.age_band_id,
			sex_id                  = EXCLUDED.sex_id,
			geography_id            = EXCLUDED.geography_id,
			housing_type_id         = EXCLUDED.housing_type_id,
			education_level_id      = EXCLUDED.education_level_id,
			income_band             = EXCLUDED.income_band,
			receives_benefit        = EXCLUDED.receives_benefit,
			has_deficiency          = EXCLUDED.has_deficiency,
			food_insecurity         = EXCLUDED.food_insecurity,
			is_overcrowded          = EXCLUDED.is_overcrowded,
			family_size             = EXCLUDED.family_size,
			assessment_completeness = EXCLUDED.assessment_completeness
	`,
		periodID, ageBandID, sexID, geoID,
		housingTypeID, educationLevelID, string(p.IncomeBand),
		p.ReceivesBenefit, p.HasDeficiency, p.FoodInsecurity,
		p.IsOvercrowded, p.FamilySize, p.AssessmentCompleteness,
		string(record.PatientHash),
	)
	if err != nil {
		return fmt.Errorf("%w: fact_patient_snapshot: %v", ErrFactUpsertFailed, err)
	}
	return nil
}

// IncrementDiagnosis increments (or inserts) a diagnosis fact row for the
// given period, geography, diagnosis, age band, and sex.
func (s *PgFactStore) IncrementDiagnosis(ctx context.Context, record ingestion.AnonymizedRecord) error {
	p := record.Diagnosis
	if p == nil {
		return fmt.Errorf("%w: expected Diagnosis payload for kind %q", ErrNilPayload, record.Kind)
	}

	periodID, err := s.dims.GetOrCreatePeriod(ctx, record.Period)
	if err != nil {
		return fmt.Errorf("%w: period: %v", ErrMissingDimensions, err)
	}

	diagID, err := s.dims.GetOrCreateDiagnosis(ctx, p.ICDCode, p.ICDLabel, p.Chapter, p.Block)
	if err != nil {
		return fmt.Errorf("%w: diagnosis: %v", ErrMissingDimensions, err)
	}

	geoID, err := s.dims.UpsertGeography(ctx, p.Geography)
	if err != nil {
		return fmt.Errorf("%w: geography: %v", ErrMissingDimensions, err)
	}

	ageBandID, err := s.dims.GetOrCreateAgeBand(ctx, p.AgeBand)
	if err != nil {
		return fmt.Errorf("%w: age_band: %v", ErrMissingDimensions, err)
	}

	sexID, err := s.dims.GetOrCreateSex(ctx, string(p.Sex))
	if err != nil {
		return fmt.Errorf("%w: sex: %v", ErrMissingDimensions, err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO fact_diagnosis (period_id, diagnosis_id, geography_id, age_band_id, sex_id, new_cases, total_cases)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
		ON CONFLICT (period_id, diagnosis_id, geography_id, age_band_id, sex_id) DO UPDATE SET
			new_cases   = fact_diagnosis.new_cases + EXCLUDED.new_cases,
			total_cases = fact_diagnosis.total_cases + EXCLUDED.total_cases
	`, periodID, diagID, geoID, ageBandID, sexID, p.NewCases)
	if err != nil {
		return fmt.Errorf("%w: fact_diagnosis: %v", ErrFactInsertFailed, err)
	}
	return nil
}

// IncrementAppointment increments (or inserts) an appointment fact row for
// the given period, geography, and appointment type.
func (s *PgFactStore) IncrementAppointment(ctx context.Context, record ingestion.AnonymizedRecord) error {
	p := record.Appointment
	if p == nil {
		return fmt.Errorf("%w: expected Appointment payload for kind %q", ErrNilPayload, record.Kind)
	}

	periodID, err := s.dims.GetOrCreatePeriod(ctx, record.Period)
	if err != nil {
		return fmt.Errorf("%w: period: %v", ErrMissingDimensions, err)
	}

	geoID, err := s.dims.UpsertGeography(ctx, p.Geography)
	if err != nil {
		return fmt.Errorf("%w: geography: %v", ErrMissingDimensions, err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO fact_appointment (period_id, geography_id, appointment_type, count)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (period_id, geography_id, appointment_type) DO UPDATE SET
			count = fact_appointment.count + 1
	`, periodID, geoID, p.AppointmentType)
	if err != nil {
		return fmt.Errorf("%w: fact_appointment: %v", ErrFactInsertFailed, err)
	}
	return nil
}

// IncrementReferral increments (or inserts) a referral fact row for the given
// period, geography, and destination service.
func (s *PgFactStore) IncrementReferral(ctx context.Context, record ingestion.AnonymizedRecord) error {
	p := record.Referral
	if p == nil {
		return fmt.Errorf("%w: expected Referral payload for kind %q", ErrNilPayload, record.Kind)
	}

	periodID, err := s.dims.GetOrCreatePeriod(ctx, record.Period)
	if err != nil {
		return fmt.Errorf("%w: period: %v", ErrMissingDimensions, err)
	}

	geoID, err := s.dims.UpsertGeography(ctx, p.Geography)
	if err != nil {
		return fmt.Errorf("%w: geography: %v", ErrMissingDimensions, err)
	}

	destID, err := s.dims.GetOrCreate(ctx, "dim_referral_destination", p.DestinationService)
	if err != nil {
		return fmt.Errorf("%w: referral_destination: %v", ErrMissingDimensions, err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO fact_referral (period_id, geography_id, destination_id, count)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (period_id, geography_id, destination_id) DO UPDATE SET
			count = fact_referral.count + 1
	`, periodID, geoID, destID)
	if err != nil {
		return fmt.Errorf("%w: fact_referral: %v", ErrFactInsertFailed, err)
	}
	return nil
}

// IncrementViolation increments (or inserts) a violation fact row for the
// given period, geography, and violation type.
func (s *PgFactStore) IncrementViolation(ctx context.Context, record ingestion.AnonymizedRecord) error {
	p := record.Violation
	if p == nil {
		return fmt.Errorf("%w: expected Violation payload for kind %q", ErrNilPayload, record.Kind)
	}

	periodID, err := s.dims.GetOrCreatePeriod(ctx, record.Period)
	if err != nil {
		return fmt.Errorf("%w: period: %v", ErrMissingDimensions, err)
	}

	geoID, err := s.dims.UpsertGeography(ctx, p.Geography)
	if err != nil {
		return fmt.Errorf("%w: geography: %v", ErrMissingDimensions, err)
	}

	vtID, err := s.dims.GetOrCreate(ctx, "dim_violation_type", p.ViolationType)
	if err != nil {
		return fmt.Errorf("%w: violation_type: %v", ErrMissingDimensions, err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO fact_violation (period_id, geography_id, violation_type_id, count)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (period_id, geography_id, violation_type_id) DO UPDATE SET
			count = fact_violation.count + 1
	`, periodID, geoID, vtID)
	if err != nil {
		return fmt.Errorf("%w: fact_violation: %v", ErrFactInsertFailed, err)
	}
	return nil
}

// IncrementBenefit increments (or inserts) a benefit fact row for the given
// period, geography, and benefit type.
func (s *PgFactStore) IncrementBenefit(ctx context.Context, record ingestion.AnonymizedRecord) error {
	p := record.Benefit
	if p == nil {
		return fmt.Errorf("%w: expected Benefit payload for kind %q", ErrNilPayload, record.Kind)
	}

	periodID, err := s.dims.GetOrCreatePeriod(ctx, record.Period)
	if err != nil {
		return fmt.Errorf("%w: period: %v", ErrMissingDimensions, err)
	}

	geoID, err := s.dims.UpsertGeography(ctx, p.Geography)
	if err != nil {
		return fmt.Errorf("%w: geography: %v", ErrMissingDimensions, err)
	}

	btID, err := s.dims.GetOrCreate(ctx, "dim_benefit_type", p.BenefitType)
	if err != nil {
		return fmt.Errorf("%w: benefit_type: %v", ErrMissingDimensions, err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO fact_benefit (period_id, geography_id, benefit_type_id, beneficiary_count, total_amount)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (period_id, geography_id, benefit_type_id) DO UPDATE SET
			beneficiary_count = fact_benefit.beneficiary_count + EXCLUDED.beneficiary_count,
			total_amount      = fact_benefit.total_amount + EXCLUDED.total_amount
	`, periodID, geoID, btID, p.BeneficiaryDelta, p.Amount)
	if err != nil {
		return fmt.Errorf("%w: fact_benefit: %v", ErrFactInsertFailed, err)
	}
	return nil
}

// UpsertFamilyComposition updates family composition aggregates for the
// given period and geography.
func (s *PgFactStore) UpsertFamilyComposition(ctx context.Context, record ingestion.AnonymizedRecord) error {
	p := record.FamilyComposition
	if p == nil {
		return fmt.Errorf("%w: expected FamilyComposition payload for kind %q", ErrNilPayload, record.Kind)
	}

	periodID, err := s.dims.GetOrCreatePeriod(ctx, record.Period)
	if err != nil {
		return fmt.Errorf("%w: period: %v", ErrMissingDimensions, err)
	}

	geoID, err := s.dims.UpsertGeography(ctx, p.Geography)
	if err != nil {
		return fmt.Errorf("%w: geography: %v", ErrMissingDimensions, err)
	}

	// Delta-based: increment or decrement family counters.
	familyDelta := -1
	if p.IsAddition {
		familyDelta = 1
	}

	elderlyDelta := 0
	childrenDelta := 0
	if p.MemberRelationship == "elderly" {
		if p.IsAddition {
			elderlyDelta = 1
		} else {
			elderlyDelta = -1
		}
	}
	if p.MemberRelationship == "child" {
		if p.IsAddition {
			childrenDelta = 1
		} else {
			childrenDelta = -1
		}
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO fact_family_composition (
			period_id, geography_id, avg_family_size,
			total_families, families_with_elderly, families_with_children
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (period_id, geography_id) DO UPDATE SET
			total_families         = fact_family_composition.total_families + EXCLUDED.total_families,
			families_with_elderly  = fact_family_composition.families_with_elderly + EXCLUDED.families_with_elderly,
			families_with_children = fact_family_composition.families_with_children + EXCLUDED.families_with_children,
			avg_family_size        = (
				(fact_family_composition.avg_family_size * fact_family_composition.total_families)
				+ EXCLUDED.avg_family_size
			) / GREATEST(fact_family_composition.total_families + EXCLUDED.total_families, 1)
	`, periodID, geoID, float64(p.FamilySizeDelta), familyDelta, elderlyDelta, childrenDelta)
	if err != nil {
		return fmt.Errorf("%w: fact_family_composition: %v", ErrFactUpsertFailed, err)
	}
	return nil
}

// UpdatePatientLifecycle records a care-pathway transition on the patient's
// snapshot for the event's period.
//
// It is an UPDATE, never an upsert, and that is the whole point. The snapshot
// row carries the demographic dimensions (age band, sex, geography, all NOT
// NULL) that arrive with PatientCreated. An upsert driven by a lifecycle event
// would either fail those constraints or, worse, overwrite a populated snapshot
// with empty dimensions — losing the demographics to record a status.
//
// If no row matches, the transition is skipped and reported. That is expected,
// not exceptional: a patient admitted in March and discharged in July has no
// July snapshot until the carry-forward job creates one. Failing here would
// stall the whole ingestion pipeline over a row that will exist tomorrow.
func (s *PgFactStore) UpdatePatientLifecycle(ctx context.Context, record ingestion.AnonymizedRecord) error {
	p := record.Lifecycle
	if p == nil {
		return fmt.Errorf("%w: expected Lifecycle payload for kind %q", ErrNilPayload, record.Kind)
	}

	periodID, err := s.dims.GetOrCreatePeriod(ctx, record.Period)
	if err != nil {
		return fmt.Errorf("%w: period: %v", ErrMissingDimensions, err)
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE fact_patient_snapshot
		   SET lifecycle_status = $1,
		       lifecycle_reason = NULLIF($2, '')
		 WHERE period_id = $3 AND patient_hash = $4
	`, string(p.Status), p.Reason, periodID, string(record.PatientHash))
	if err != nil {
		return fmt.Errorf("update lifecycle: %w", err)
	}

	if tag.RowsAffected() == 0 {
		slog.Info("lifecycle transition has no snapshot in this period; skipped",
			"status", p.Status, "period", record.Period, "eventId", record.EventID)
	}
	return nil
}
