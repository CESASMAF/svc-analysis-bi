package ingestion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/acdgbrasil/svc-analysis-bi/internal/domain"
)

// Anonymizer transforms raw event JSON into AnonymizedRecord values by
// hashing PII, generalizing quasi-identifiers, and discarding sensitive fields.
// It delegates all business logic to domain functions.
type Anonymizer struct {
	geo  domain.GeographyLookup
	salt string
}

// NewAnonymizer creates an Anonymizer with the given geography lookup and
// hashing salt.
func NewAnonymizer(geo domain.GeographyLookup, salt string) *Anonymizer {
	return &Anonymizer{geo: geo, salt: salt}
}

// Anonymize deserializes raw JSON data based on the eventType, hashes the
// patientID, generalizes quasi-identifiers, and returns an AnonymizedRecord
// with all PII removed.
func (a *Anonymizer) Anonymize(ctx context.Context, eventType domain.EventType, data []byte) (AnonymizedRecord, error) {
	switch eventType {
	case domain.EventPatientCreated:
		return a.anonymizePatientCreated(data)
	case domain.EventHealthStatusUpdated:
		return a.anonymizeHealthStatus(data)
	case domain.EventAppointmentRegistered:
		return a.anonymizeAppointment(data)
	case domain.EventReferralCreated:
		return a.anonymizeReferral(data)
	case domain.EventRightsViolationReported:
		return a.anonymizeViolation(data)
	case domain.EventFamilyMemberAdded:
		return a.anonymizeFamilyMemberAdded(data)
	case domain.EventFamilyMemberRemoved:
		return a.anonymizeFamilyMemberRemoved(data)
	case domain.EventPrimaryCaregiverAssigned:
		return a.anonymizeCaregiverAssigned(data)
	case domain.EventSocialIdentityUpdated,
		domain.EventHousingConditionUpdated,
		domain.EventSocioEconomicUpdated,
		domain.EventWorkAndIncomeUpdated,
		domain.EventEducationalStatusUpdated,
		domain.EventCommunitySupportUpdated,
		domain.EventSocialHealthSummaryUpdated,
		domain.EventIntakeInfoUpdated,
		domain.EventPlacementHistoryUpdated:
		return a.anonymizeGenericAssessment(eventType, data)
	default:
		return AnonymizedRecord{}, fmt.Errorf("%w: %s", ErrUnknownEventType, eventType)
	}
}

// ---------------------------------------------------------------------------
// PatientCreated
// ---------------------------------------------------------------------------

func (a *Anonymizer) anonymizePatientCreated(data []byte) (AnonymizedRecord, error) {
	var evt jsonPatientCreated
	if err := unmarshalEvent(data, &evt); err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrDeserializationFailed, err)
	}

	if evt.PatientID == "" || evt.ID == "" {
		return AnonymizedRecord{}, fmt.Errorf("%w: missing required fields (patientId or id)", ErrAnonymizationFailed)
	}

	hash, err := domain.HashPatientID(evt.PatientID, a.salt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrAnonymizationFailed, err)
	}

	occurredAt, err := time.Parse(time.RFC3339, evt.OccurredAt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: invalid occurredAt: %v", ErrDeserializationFailed, err)
	}

	period := domain.PeriodFromTime(occurredAt)
	snapshot := &SnapshotPayload{
		HousingType: evt.HousingType,
	}

	// BirthDate, Sex, CEP are optional in PatientCreatedEvent — they may
	// arrive in separate assessment update events (SocialIdentityUpdated, etc.)
	if evt.BirthDate != "" {
		birthDate, parseErr := time.Parse("2006-01-02", evt.BirthDate)
		if parseErr == nil {
			// Use occurredAt as age reference (not year-1) so that newborns
			// registered in their birth year get a valid age band (0-4).
			if ageBand, ageErr := domain.GeneralizeAge(birthDate, occurredAt); ageErr == nil {
				snapshot.AgeBand = ageBand
			}
		}
	}

	snapshot.Sex = mapSex(evt.Sex) // defaults to SexUnknown for empty string

	if evt.CEP != "" {
		geo, geoErr := a.geo.FindByCEP(evt.CEP)
		if geoErr == nil {
			snapshot.Geography = geo
		} else if !errors.Is(geoErr, domain.ErrCEPNotFound) {
			// CEP format errors (wrong length, non-digit) are real data issues —
			// not found is graceful (unmapped CEP), but format errors should not
			// silently produce empty geography.
			return AnonymizedRecord{}, fmt.Errorf("%w: CEP validation: %v", ErrAnonymizationFailed, geoErr)
		}
	}

	if evt.TotalIncomeCents != nil {
		snapshot.IncomeBand = domain.GeneralizeIncome(*evt.TotalIncomeCents)
	}

	return AnonymizedRecord{
		Kind:        FactKindPatientSnapshot,
		EventID:     evt.ID,
		EventType:   domain.EventPatientCreated,
		OccurredAt:  occurredAt,
		Period:      period,
		PatientHash: hash,
		Snapshot:    snapshot,
	}, nil
}

// ---------------------------------------------------------------------------
// HealthStatusUpdated -> DiagnosisPayload
// ---------------------------------------------------------------------------

func (a *Anonymizer) anonymizeHealthStatus(data []byte) (AnonymizedRecord, error) {
	var evt jsonAssessmentUpdated
	if err := unmarshalEvent(data, &evt); err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrDeserializationFailed, err)
	}

	if evt.PatientID == "" || evt.ID == "" {
		return AnonymizedRecord{}, fmt.Errorf("%w: missing required fields", ErrAnonymizationFailed)
	}

	hash, err := domain.HashPatientID(evt.PatientID, a.salt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrAnonymizationFailed, err)
	}

	occurredAt, err := time.Parse(time.RFC3339, evt.OccurredAt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: invalid occurredAt: %v", ErrDeserializationFailed, err)
	}

	var afterData jsonHealthStatusAfter
	if len(evt.After) > 0 && string(evt.After) != "null" {
		if err := json.Unmarshal(evt.After, &afterData); err != nil {
			return AnonymizedRecord{}, fmt.Errorf("%w: invalid after payload: %v", ErrDeserializationFailed, err)
		}
	}

	period := domain.PeriodFromTime(occurredAt)

	return AnonymizedRecord{
		Kind:        FactKindDiagnosis,
		EventID:     evt.ID,
		EventType:   domain.EventHealthStatusUpdated,
		OccurredAt:  occurredAt,
		Period:      period,
		PatientHash: hash,
		Diagnosis: &DiagnosisPayload{
			ICDCode:  afterData.ICDCode,
			ICDLabel: afterData.ICDLabel,
			Chapter:  afterData.Chapter,
			Block:    afterData.Block,
			NewCases: 1,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// AppointmentRegistered
// ---------------------------------------------------------------------------

func (a *Anonymizer) anonymizeAppointment(data []byte) (AnonymizedRecord, error) {
	var evt jsonAppointmentRegistered
	if err := unmarshalEvent(data, &evt); err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrDeserializationFailed, err)
	}

	if evt.PatientID == "" || evt.ID == "" {
		return AnonymizedRecord{}, fmt.Errorf("%w: missing required fields", ErrAnonymizationFailed)
	}

	hash, err := domain.HashPatientID(evt.PatientID, a.salt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrAnonymizationFailed, err)
	}

	occurredAt, err := time.Parse(time.RFC3339, evt.OccurredAt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: invalid occurredAt: %v", ErrDeserializationFailed, err)
	}

	period := domain.PeriodFromTime(occurredAt)

	return AnonymizedRecord{
		Kind:        FactKindAppointment,
		EventID:     evt.ID,
		EventType:   domain.EventAppointmentRegistered,
		OccurredAt:  occurredAt,
		Period:      period,
		PatientHash: hash,
		Appointment: &AppointmentPayload{
			AppointmentType: evt.Type,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// ReferralCreated
// ---------------------------------------------------------------------------

func (a *Anonymizer) anonymizeReferral(data []byte) (AnonymizedRecord, error) {
	var evt jsonReferralCreated
	if err := unmarshalEvent(data, &evt); err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrDeserializationFailed, err)
	}

	if evt.PatientID == "" || evt.ID == "" {
		return AnonymizedRecord{}, fmt.Errorf("%w: missing required fields", ErrAnonymizationFailed)
	}

	hash, err := domain.HashPatientID(evt.PatientID, a.salt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrAnonymizationFailed, err)
	}

	occurredAt, err := time.Parse(time.RFC3339, evt.OccurredAt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: invalid occurredAt: %v", ErrDeserializationFailed, err)
	}

	period := domain.PeriodFromTime(occurredAt)

	return AnonymizedRecord{
		Kind:        FactKindReferral,
		EventID:     evt.ID,
		EventType:   domain.EventReferralCreated,
		OccurredAt:  occurredAt,
		Period:      period,
		PatientHash: hash,
		Referral: &ReferralPayload{
			DestinationService: evt.DestinationService,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// RightsViolationReported
// ---------------------------------------------------------------------------

func (a *Anonymizer) anonymizeViolation(data []byte) (AnonymizedRecord, error) {
	var evt jsonRightsViolationReported
	if err := unmarshalEvent(data, &evt); err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrDeserializationFailed, err)
	}

	if evt.PatientID == "" || evt.ID == "" {
		return AnonymizedRecord{}, fmt.Errorf("%w: missing required fields", ErrAnonymizationFailed)
	}

	hash, err := domain.HashPatientID(evt.PatientID, a.salt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrAnonymizationFailed, err)
	}

	occurredAt, err := time.Parse(time.RFC3339, evt.OccurredAt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: invalid occurredAt: %v", ErrDeserializationFailed, err)
	}

	period := domain.PeriodFromTime(occurredAt)

	return AnonymizedRecord{
		Kind:        FactKindViolation,
		EventID:     evt.ID,
		EventType:   domain.EventRightsViolationReported,
		OccurredAt:  occurredAt,
		Period:      period,
		PatientHash: hash,
		Violation: &ViolationPayload{
			ViolationType: evt.ViolationType,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// FamilyMemberAdded
// ---------------------------------------------------------------------------

func (a *Anonymizer) anonymizeFamilyMemberAdded(data []byte) (AnonymizedRecord, error) {
	var evt jsonFamilyMemberAdded
	if err := unmarshalEvent(data, &evt); err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrDeserializationFailed, err)
	}

	if evt.PatientID == "" || evt.ID == "" {
		return AnonymizedRecord{}, fmt.Errorf("%w: missing required fields", ErrAnonymizationFailed)
	}

	hash, err := domain.HashPatientID(evt.PatientID, a.salt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrAnonymizationFailed, err)
	}

	occurredAt, err := time.Parse(time.RFC3339, evt.OccurredAt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: invalid occurredAt: %v", ErrDeserializationFailed, err)
	}

	period := domain.PeriodFromTime(occurredAt)

	return AnonymizedRecord{
		Kind:        FactKindFamilyComposition,
		EventID:     evt.ID,
		EventType:   domain.EventFamilyMemberAdded,
		OccurredAt:  occurredAt,
		Period:      period,
		PatientHash: hash,
		FamilyComposition: &FamilyCompositionPayload{
			FamilySizeDelta:    1,
			IsAddition:         true,
			MemberRelationship: evt.Relationship,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// FamilyMemberRemoved
// ---------------------------------------------------------------------------

func (a *Anonymizer) anonymizeFamilyMemberRemoved(data []byte) (AnonymizedRecord, error) {
	var evt jsonFamilyMemberRemoved
	if err := unmarshalEvent(data, &evt); err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrDeserializationFailed, err)
	}

	if evt.PatientID == "" || evt.ID == "" {
		return AnonymizedRecord{}, fmt.Errorf("%w: missing required fields", ErrAnonymizationFailed)
	}

	hash, err := domain.HashPatientID(evt.PatientID, a.salt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrAnonymizationFailed, err)
	}

	occurredAt, err := time.Parse(time.RFC3339, evt.OccurredAt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: invalid occurredAt: %v", ErrDeserializationFailed, err)
	}

	period := domain.PeriodFromTime(occurredAt)

	return AnonymizedRecord{
		Kind:        FactKindFamilyComposition,
		EventID:     evt.ID,
		EventType:   domain.EventFamilyMemberRemoved,
		OccurredAt:  occurredAt,
		Period:      period,
		PatientHash: hash,
		FamilyComposition: &FamilyCompositionPayload{
			FamilySizeDelta: -1,
			IsAddition:      false,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// PrimaryCaregiverAssigned
// ---------------------------------------------------------------------------

func (a *Anonymizer) anonymizeCaregiverAssigned(data []byte) (AnonymizedRecord, error) {
	var evt jsonPrimaryCaregiverAssigned
	if err := unmarshalEvent(data, &evt); err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrDeserializationFailed, err)
	}

	if evt.PatientID == "" || evt.ID == "" {
		return AnonymizedRecord{}, fmt.Errorf("%w: missing required fields", ErrAnonymizationFailed)
	}

	hash, err := domain.HashPatientID(evt.PatientID, a.salt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrAnonymizationFailed, err)
	}

	occurredAt, err := time.Parse(time.RFC3339, evt.OccurredAt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: invalid occurredAt: %v", ErrDeserializationFailed, err)
	}

	period := domain.PeriodFromTime(occurredAt)

	// Minimal snapshot update -- caregiverId is PII and discarded
	return AnonymizedRecord{
		Kind:        FactKindPatientSnapshot,
		EventID:     evt.ID,
		EventType:   domain.EventPrimaryCaregiverAssigned,
		OccurredAt:  occurredAt,
		Period:      period,
		PatientHash: hash,
		Snapshot:    &SnapshotPayload{},
	}, nil
}

// ---------------------------------------------------------------------------
// Generic assessment events -> SnapshotPayload
// ---------------------------------------------------------------------------

func (a *Anonymizer) anonymizeGenericAssessment(eventType domain.EventType, data []byte) (AnonymizedRecord, error) {
	var evt jsonGenericAssessment
	if err := unmarshalEvent(data, &evt); err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrDeserializationFailed, err)
	}

	if evt.PatientID == "" || evt.ID == "" {
		return AnonymizedRecord{}, fmt.Errorf("%w: missing required fields", ErrAnonymizationFailed)
	}

	hash, err := domain.HashPatientID(evt.PatientID, a.salt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: %v", ErrAnonymizationFailed, err)
	}

	occurredAt, err := time.Parse(time.RFC3339, evt.OccurredAt)
	if err != nil {
		return AnonymizedRecord{}, fmt.Errorf("%w: invalid occurredAt: %v", ErrDeserializationFailed, err)
	}

	period := domain.PeriodFromTime(occurredAt)

	return AnonymizedRecord{
		Kind:        FactKindPatientSnapshot,
		EventID:     evt.ID,
		EventType:   eventType,
		OccurredAt:  occurredAt,
		Period:      period,
		PatientHash: hash,
		Snapshot:    &SnapshotPayload{},
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mapSex converts a raw sex string to domain.Sex.
func mapSex(raw string) domain.Sex {
	switch raw {
	case "MALE":
		return domain.SexMale
	case "FEMALE":
		return domain.SexFemale
	default:
		return domain.SexUnknown
	}
}

// unmarshalEvent performs JSON unmarshalling and returns an error if the
// input is empty, null, or malformed.
// unmarshalEvent decodes an inbound event payload.
//
// It is deliberately TOLERANT of unknown fields and only WARNS about them.
// Rejecting would be wrong here: this is the consumer end of an event stream,
// and the producer must be able to add a field without taking the consumer
// down. Fail-closed on unknown fields would turn "social-care shipped a new
// attribute" into "analysis-bi stopped ingesting everything".
//
// The warning is the point. An unknown field is the earliest signal that the
// contract moved, and it is the only one visible at runtime.
//
// What this canNOT catch is a MISSING field — JSON omission is indistinguishable
// from a zero value, so it stays silent no matter how strict the decoder is.
// That asymmetry is exactly how `birthDate`, `sex` and `cep` were absent from
// PatientCreatedEvent for months while every test stayed green (audit
// 2026-08-06). Omission is caught by `scripts/check-event-contract.py`, which
// compares the Swift structs the producer emits against the Go structs read
// here. Runtime is tolerant; CI is strict.
//
// Renamed from `unmarshalEvent`: the old name promised rigor and delivered a
// plain json.Unmarshal, which is worse than no check — it bought confidence
// that was not earned.
func unmarshalEvent(data []byte, v any) error {
	if len(data) == 0 {
		return fmt.Errorf("empty input")
	}
	if string(data) == "null" {
		return fmt.Errorf("null input")
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		return nil
	}

	// Unknown field: warn and retry tolerantly. Any other error (type mismatch,
	// malformed JSON) is a real failure and propagates.
	if strings.Contains(err.Error(), "unknown field") {
		slog.Warn("inbound event carries a field this service does not declare — the contract may have moved",
			"detail", err.Error(),
			"hint", "run scripts/check-event-contract.py")
		return json.Unmarshal(data, v)
	}
	return err
}
