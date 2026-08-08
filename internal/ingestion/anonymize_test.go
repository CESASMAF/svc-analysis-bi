package ingestion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acdgbrasil/svc-analysis-bi/internal/domain"
)

// ---------------------------------------------------------------------------
// Test: PatientID is hashed, never raw
// ---------------------------------------------------------------------------

func TestAnonymize_PatientIDIsHashed(t *testing.T) {
	geoLookup := newFakeGeographyLookup()
	salt := "secret-test-salt"

	anonymizer := NewAnonymizer(geoLookup, salt)

	tests := []struct {
		name      string
		patientID string
	}{
		{"uuid format", "550e8400-e29b-41d4-a716-446655440000"},
		{"simple string", "patient-123"},
		{"long id", "very-long-patient-identifier-that-should-still-be-hashed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt := rawPatientEvent(tt.patientID, "30-34", "masculino", "3515")

			rec, err := anonymizer.Anonymize(context.Background(), domain.EventPatientCreated, evt)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Hash must NOT be the raw ID
			if string(rec.PatientHash) == tt.patientID {
				t.Error("PatientHash must not equal raw patientId")
			}

			// Hash must be a 64-char hex string (SHA-256)
			if len(string(rec.PatientHash)) != 64 {
				t.Errorf("PatientHash length = %d, want 64 (SHA-256 hex)", len(string(rec.PatientHash)))
			}

			// Hash must be deterministic
			rec2, _ := anonymizer.Anonymize(context.Background(), domain.EventPatientCreated, evt)
			if rec.PatientHash != rec2.PatientHash {
				t.Error("PatientHash must be deterministic for same input")
			}
		})
	}
}

func TestAnonymize_DifferentSaltsProduceDifferentHashes(t *testing.T) {
	geoLookup := newFakeGeographyLookup()

	anonymizer1 := NewAnonymizer(geoLookup, "salt-one")
	anonymizer2 := NewAnonymizer(geoLookup, "salt-two")

	evt := rawPatientEvent("same-patient-id", "30-34", "masculino", "3515")

	rec1, err := anonymizer1.Anonymize(context.Background(), domain.EventPatientCreated, evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rec2, err := anonymizer2.Anonymize(context.Background(), domain.EventPatientCreated, evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec1.PatientHash == rec2.PatientHash {
		t.Error("different salts must produce different hashes for same patientId")
	}
}

// ---------------------------------------------------------------------------
// Test: BirthDate is generalized to age band
// ---------------------------------------------------------------------------

func TestAnonymize_AgeBandComesFromProducerNotDerivedHere(t *testing.T) {
	anonymizer := NewAnonymizer(newFakeGeographyLookup(), "test-salt")

	// social-care generalizes at the source. This service must accept the label
	// as given — deriving it again would require birthDate, which is PII and no
	// longer crosses the boundary.
	for _, tt := range []struct {
		name  string
		label string
	}{
		{"infant", "0-4"},
		{"teenager", "15-19"},
		{"adult", "30-34"},
		{"open top band", "80+"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evt := rawPatientEvent("pat-age-test", tt.label, "masculino", "3515")

			rec, err := anonymizer.Anonymize(context.Background(), domain.EventPatientCreated, evt)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rec.Snapshot.AgeBand.Label != tt.label {
				t.Errorf("AgeBand.Label = %q, want %q", rec.Snapshot.AgeBand.Label, tt.label)
			}
		})
	}
}

func TestAnonymize_UnknownAgeBandLabelFails(t *testing.T) {
	anonymizer := NewAnonymizer(newFakeGeographyLookup(), "test-salt")

	// A label this service does not know is a contract break, not data to guess
	// around. Reconstructing "31-33" into something plausible would put a made-up
	// category into the cube.
	evt := rawPatientEvent("pat-bad-band", "31-33", "masculino", "3515")

	if _, err := anonymizer.Anonymize(context.Background(), domain.EventPatientCreated, evt); err == nil {
		t.Fatal("expected an error for an unknown age band label")
	}
}

func TestAnonymize_MissingAgeBandStaysEmpty(t *testing.T) {
	anonymizer := NewAnonymizer(newFakeGeographyLookup(), "test-salt")

	// A patient registered without personal data has no age band. Empty must
	// stay empty: turning "unknown" into a band would bias every demographic
	// indicator, and k-anonymity would be computed over a category that does
	// not exist.
	evt := rawPatientEvent("pat-no-band", "", "", "")

	rec, err := anonymizer.Anonymize(context.Background(), domain.EventPatientCreated, evt)
	if err != nil {
		t.Fatalf("missing quasi-identifiers must be tolerated, got: %v", err)
	}
	if rec.Snapshot.AgeBand.Label != "" {
		t.Errorf("AgeBand.Label = %q, want empty", rec.Snapshot.AgeBand.Label)
	}
	if rec.Snapshot.Sex != domain.SexUnknown {
		t.Errorf("Sex = %q, want %q", rec.Snapshot.Sex, domain.SexUnknown)
	}
}

// ---------------------------------------------------------------------------
// Test: mesoregion arrives already resolved; no CEP ever reaches this service
// ---------------------------------------------------------------------------

func TestAnonymize_MesoregionComesFromProducer(t *testing.T) {
	anonymizer := NewAnonymizer(newFakeGeographyLookup(), "test-salt")

	evt := rawPatientEvent("pat-geo-test", "30-34", "feminino", "3515")

	rec, err := anonymizer.Anonymize(context.Background(), domain.EventPatientCreated, evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Snapshot == nil {
		t.Fatal("expected Snapshot payload to be non-nil")
	}
	if rec.Snapshot.Geography.MesoregionCode != "3515" {
		t.Errorf("MesoregionCode = %q, want %q", rec.Snapshot.Geography.MesoregionCode, "3515")
	}
	if rec.Snapshot.Geography.MesoregionName != "Campinas" {
		t.Errorf("MesoregionName = %q, want %q", rec.Snapshot.Geography.MesoregionName, "Campinas")
	}

	// Regression guard for the whole point of generalizing at the source: no
	// CEP-shaped value may exist anywhere in the record.
	recJSON, _ := json.Marshal(rec)
	if strings.Contains(string(recJSON), "13083970") {
		t.Error("no exact CEP may appear in an anonymized record")
	}
}

func TestAnonymize_MissingMesoregion_GracefulDegradation(t *testing.T) {
	// The lookup is irrelevant now — this service no longer resolves CEP. What
	// matters is that a patient without an address (or with an unmapped CEP,
	// resolved to nothing upstream) still produces a valid record.
	anonymizer := NewAnonymizer(newFakeGeographyLookupWithError(domain.ErrCEPNotFound), "test-salt")

	evt := rawPatientEvent("pat-cep-fail", "30-34", "masculino", "")

	// mesoregion is optional — absence is gracefully skipped
	rec, err := anonymizer.Anonymize(context.Background(), domain.EventPatientCreated, evt)
	if err != nil {
		t.Fatalf("CEP failure should be graceful, got: %v", err)
	}
	if rec.Snapshot.Geography.MesoregionCode != "" {
		t.Errorf("expected empty MesoregionCode, got %q", rec.Snapshot.Geography.MesoregionCode)
	}
}

// ---------------------------------------------------------------------------
// Test: Income is generalized to income band
// ---------------------------------------------------------------------------

// TestAnonymize_IncomeGeneralizedToIncomeBand foi REMOVIDO (auditoria 2026-08-06).
//
// Ele exercitava `totalIncomeCents` no PatientCreated — um campo que
// social-care nunca enviou nesse evento. O teste passava porque a fixture o
// fabricava; nenhum evento real jamais teve renda ali. Testar contra uma
// fixture que o produtor não produz é como não testar.
//
// Renda muda por avaliação social (SocioEconomicSituationUpdatedEvent). Quando
// `anonymizeGenericAssessment` passar a extrair renda do `after`, o teste
// correto nasce lá — contra o payload que existe de verdade.

// ---------------------------------------------------------------------------
// Test: PII fields are completely absent from AnonymizedRecord
// ---------------------------------------------------------------------------

func TestAnonymize_PIIFieldsAbsent(t *testing.T) {
	geoLookup := newFakeGeographyLookup()
	salt := "test-salt"

	anonymizer := NewAnonymizer(geoLookup, salt)

	piiFields := []struct {
		name      string
		eventType domain.EventType
		rawEvent  []byte
		piiValues []string
	}{
		{
			name:      "actorId absent from appointment",
			eventType: domain.EventAppointmentRegistered,
			rawEvent: mustJSON(map[string]any{
				"id":                     "evt-pii-actor",
				"occurredAt":             "2025-06-15T10:00:00Z",
				"actorId":                "actor-uuid-must-vanish",
				"patientId":              "pat-pii-actor",
				"appointmentId":          "appt-pii-001",
				"professionalInChargeId": "prof-uuid-must-vanish",
				"type":                   "initial",
			}),
			piiValues: []string{"actor-uuid-must-vanish", "prof-uuid-must-vanish", "appt-pii-001"},
		},
		{
			name:      "memberId absent from family member added",
			eventType: domain.EventFamilyMemberAdded,
			rawEvent: mustJSON(map[string]any{
				"id":           "evt-pii-member",
				"occurredAt":   "2025-06-15T10:00:00Z",
				"actorId":      "actor-001",
				"patientId":    "pat-pii-member",
				"memberId":     "member-uuid-must-vanish",
				"relationship": "child",
			}),
			piiValues: []string{"member-uuid-must-vanish"},
		},
		{
			name:      "victimId absent from violation",
			eventType: domain.EventRightsViolationReported,
			rawEvent: mustJSON(map[string]any{
				"id":            "evt-pii-victim",
				"occurredAt":    "2025-06-15T10:00:00Z",
				"actorId":       "actor-001",
				"patientId":     "pat-pii-victim",
				"reportId":      "report-uuid-must-vanish",
				"victimId":      "victim-uuid-must-vanish",
				"violationType": "neglect",
			}),
			piiValues: []string{"victim-uuid-must-vanish", "report-uuid-must-vanish"},
		},
		{
			name:      "caregiverId absent from caregiver assigned",
			eventType: domain.EventPrimaryCaregiverAssigned,
			rawEvent: mustJSON(map[string]any{
				"id":          "evt-pii-caregiver",
				"occurredAt":  "2025-06-15T10:00:00Z",
				"actorId":     "actor-001",
				"patientId":   "pat-pii-caregiver",
				"caregiverId": "caregiver-uuid-must-vanish",
			}),
			piiValues: []string{"caregiver-uuid-must-vanish"},
		},
		{
			name:      "referredPersonId absent from referral",
			eventType: domain.EventReferralCreated,
			rawEvent: mustJSON(map[string]any{
				"id":                 "evt-pii-referral",
				"occurredAt":         "2025-06-15T10:00:00Z",
				"actorId":            "actor-001",
				"patientId":          "pat-pii-referral",
				"referralId":         "referral-uuid-must-vanish",
				"referredPersonId":   "referred-uuid-must-vanish",
				"destinationService": "CRAS",
				"status":             "pending",
			}),
			piiValues: []string{"referral-uuid-must-vanish", "referred-uuid-must-vanish"},
		},
	}

	for _, tt := range piiFields {
		t.Run(tt.name, func(t *testing.T) {
			rec, err := anonymizer.Anonymize(context.Background(), tt.eventType, tt.rawEvent)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			recJSON, _ := json.Marshal(rec)
			serialized := string(recJSON)

			for _, pii := range tt.piiValues {
				if strings.Contains(serialized, pii) {
					t.Errorf("PII value %q must not appear in anonymized record", pii)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test: Empty salt produces error
// ---------------------------------------------------------------------------

func TestAnonymize_EmptySaltError(t *testing.T) {
	geoLookup := newFakeGeographyLookup()

	anonymizer := NewAnonymizer(geoLookup, "")

	evt := rawPatientEvent("pat-empty-salt", "30-34", "masculino", "3515")

	_, err := anonymizer.Anonymize(context.Background(), domain.EventPatientCreated, evt)
	if err == nil {
		t.Fatal("expected error when salt is empty")
	}
}

// ---------------------------------------------------------------------------
// Test: Period derived from OccurredAt
// ---------------------------------------------------------------------------

func TestAnonymize_PeriodDerivedFromOccurredAt(t *testing.T) {
	geoLookup := newFakeGeographyLookup()
	salt := "test-salt"

	anonymizer := NewAnonymizer(geoLookup, salt)

	tests := []struct {
		name       string
		occurredAt string
		wantYear   int
		wantMonth  int
	}{
		{"january 2025", "2025-01-15T10:00:00Z", 2025, 1},
		{"december 2024", "2024-12-31T23:59:59Z", 2024, 12},
		{"june 2025", "2025-06-01T00:00:00Z", 2025, 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt := rawPatientEventWithOccurredAt("pat-period-test", "30-34", "masculino", "3515", tt.occurredAt)

			rec, err := anonymizer.Anonymize(context.Background(), domain.EventPatientCreated, evt)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if rec.Period.Year != tt.wantYear {
				t.Errorf("Period.Year = %d, want %d", rec.Period.Year, tt.wantYear)
			}
			if rec.Period.Month != tt.wantMonth {
				t.Errorf("Period.Month = %d, want %d", rec.Period.Month, tt.wantMonth)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test: Sex mapping
// ---------------------------------------------------------------------------

func TestAnonymize_SexMapping(t *testing.T) {
	geoLookup := newFakeGeographyLookup()
	salt := "test-salt"

	anonymizer := NewAnonymizer(geoLookup, salt)

	tests := []struct {
		name    string
		sex     string
		wantSex domain.Sex
	}{
		{"male", "MALE", domain.SexMale},
		{"female", "FEMALE", domain.SexFemale},
		{"unknown", "UNKNOWN", domain.SexUnknown},
		{"empty defaults to unknown", "", domain.SexUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt := rawPatientEvent("pat-sex-test", "30-34", tt.sex, "3515")

			rec, err := anonymizer.Anonymize(context.Background(), domain.EventPatientCreated, evt)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if rec.Snapshot == nil {
				t.Fatal("expected Snapshot payload to be non-nil")
			}
			if rec.Snapshot.Sex != tt.wantSex {
				t.Errorf("Sex = %q, want %q", rec.Snapshot.Sex, tt.wantSex)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// rawPatientEvent builds a PatientCreated payload in the CURRENT contract:
// social-care sends quasi-identifiers already generalized. The parameters are
// named ageBand/mesoregion (not birthDate/cep) because neither PII field
// crosses the boundary any more.
func rawPatientEvent(patientID, ageBand, sex, mesoregion string) []byte {
	return rawPatientEventWithOccurredAt(patientID, ageBand, sex, mesoregion, "2025-06-15T10:00:00Z")
}

func rawPatientEventWithOccurredAt(patientID, ageBand, sex, mesoregion, occurredAt string) []byte {
	data, _ := json.Marshal(map[string]any{
		"id":             "evt-anon-" + patientID,
		"occurredAt":     occurredAt,
		"actorId":        "actor-001",
		"patientId":      patientID,
		"personId":       "person-" + patientID,
		"ageBand":        ageBand,
		"sex":            sex,
		"mesoregionCode": mesoregion,
		"mesoregionName": "Campinas",
		"stateCode":      "35",
	})
	return data
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic("mustJSON: " + err.Error())
	}
	return data
}

// ---------------------------------------------------------------------------
// Test: care-pathway lifecycle (ADR-002)
// ---------------------------------------------------------------------------

func TestAnonymize_LifecycleTransitions(t *testing.T) {
	anonymizer := NewAnonymizer(newFakeGeographyLookup(), "test-salt")

	for _, tt := range []struct {
		name       string
		eventType  domain.EventType
		reason     string
		wantStatus domain.LifecycleStatus
		wantReason string
	}{
		{"admitted", domain.EventPatientAdmitted, "", domain.LifecycleAdmitted, ""},
		{"discharged", domain.EventPatientDischarged, "objetivos alcancados", domain.LifecycleDischarged, "objetivos alcancados"},
		{"readmitted", domain.EventPatientReadmitted, "", domain.LifecycleReadmitted, ""},
		{"withdrawn", domain.EventPatientWithdrawnFromWaitlist, "mudanca de municipio", domain.LifecycleWithdrawn, "mudanca de municipio"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evt := mustJSON(map[string]any{
				"id":         "evt-lc-" + tt.name,
				"occurredAt": "2025-06-15T10:00:00Z",
				"actorId":    "actor-001",
				"patientId":  "pat-lc",
				"personId":   "person-lc",
				"reason":     tt.reason,
			})

			rec, err := anonymizer.Anonymize(context.Background(), tt.eventType, evt)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rec.Kind != FactKindLifecycle {
				t.Errorf("Kind = %q, want %q", rec.Kind, FactKindLifecycle)
			}
			if rec.Lifecycle == nil {
				t.Fatal("expected Lifecycle payload")
			}
			if rec.Lifecycle.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", rec.Lifecycle.Status, tt.wantStatus)
			}
			if rec.Lifecycle.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", rec.Lifecycle.Reason, tt.wantReason)
			}
		})
	}
}

func TestAnonymize_LifecycleNeverCarriesNotes(t *testing.T) {
	anonymizer := NewAnonymizer(newFakeGeographyLookup(), "test-salt")

	// `notes` is free text written by a caseworker and may name people. The
	// event carries it; this service must not surface it anywhere in the record.
	secret := "mae relatou violencia domestica na rua das Flores 123"
	evt := mustJSON(map[string]any{
		"id":         "evt-lc-notes",
		"occurredAt": "2025-06-15T10:00:00Z",
		"actorId":    "actor-001",
		"patientId":  "pat-lc",
		"personId":   "person-lc",
		"reason":     "encaminhado",
		"notes":      secret,
	})

	rec, err := anonymizer.Anonymize(context.Background(), domain.EventPatientDischarged, evt)
	if err != nil {
		t.Fatalf("an unknown field must not break ingestion: %v", err)
	}

	recJSON, _ := json.Marshal(rec)
	if strings.Contains(string(recJSON), "violencia") || strings.Contains(string(recJSON), "Flores") {
		t.Error("free-text notes leaked into the anonymized record")
	}
}

func TestAnonymize_PIIAnonymizedIsAcknowledgedNotDropped(t *testing.T) {
	anonymizer := NewAnonymizer(newFakeGeographyLookup(), "test-salt")

	// ADR-002: this service holds nothing to erase, so the event has no effect.
	// But it must be RECOGNIZED — falling through to the default branch would
	// report "unknown event type", which is indistinguishable from a bug.
	evt := mustJSON(map[string]any{
		"id":         "evt-erasure",
		"occurredAt": "2025-06-15T10:00:00Z",
		"actorId":    "actor-001",
		"patientId":  "pat-erased",
		"personId":   "person-erased",
	})

	rec, err := anonymizer.Anonymize(context.Background(), domain.EventPatientPIIAnonymized, evt)
	if err != nil {
		t.Fatalf("erasure event must be recognized, got: %v", err)
	}
	if rec.Kind != FactKindNone {
		t.Errorf("Kind = %q, want %q", rec.Kind, FactKindNone)
	}
	if rec.Snapshot != nil || rec.Lifecycle != nil {
		t.Error("erasure event must not materialize any payload")
	}
}
