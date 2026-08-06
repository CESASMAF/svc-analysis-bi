package ingestion

import "encoding/json"

// ---------------------------------------------------------------------------
// Internal JSON deserialization structs matching the NATS payload format
// published by svc-social-care's NATSEventPublisher (Swift/Codable).
//
// Swift serializes events as FLAT objects with top-level fields:
//   - "id" (UUID string) — event identifier
//   - "occurredAt" (ISO 8601) — timestamp
//   - "patientId", "actorId", etc. — camelCase field names
//
// There is NO "metadata" wrapper — id and occurredAt are top-level.
// The "actorId" field is present in all events but discarded (PII).
// ---------------------------------------------------------------------------

// jsonEventBase contains the fields common to ALL events from svc-social-care.
// Swift's DomainEvent protocol requires id, occurredAt, and actorId.
type jsonEventBase struct {
	ID         string `json:"id"`
	OccurredAt string `json:"occurredAt"`
	ActorID    string `json:"actorId"` // PII — always discarded
	PatientID  string `json:"patientId"`
}

// jsonPatientCreated carries quasi-identifiers that social-care ALREADY
// generalized. This service never receives birthDate or CEP: they are PII, and
// generalizing at the source is what makes "no PII reaches analysis-bi" true in
// code rather than only in the docs (see DemographicGeneralization in the Swift
// side, and scripts/gen-ibge-table.py for the shared IBGE table).
//
// Every field is optional because a patient may be registered without personal
// data or without an address. Empty means "unknown" and must stay
// distinguishable from a real category — never coerce it to a default.
type jsonPatientCreated struct {
	jsonEventBase
	PersonID string `json:"personId"`

	AgeBand        string `json:"ageBand"`        // "0-4" … "75-79", "80+"
	Sex            string `json:"sex"`            // masculino | feminino | outro
	MesoregionCode string `json:"mesoregionCode"` // IBGE
	MesoregionName string `json:"mesoregionName"`
	StateCode      string `json:"stateCode"`
}

// NOTE — lacuna conhecida (auditoria 2026-08-06). Esta struct declarava
// `housingType` e `totalIncomeCents`, que social-care NUNCA envia no
// PatientCreated: moradia e renda mudam por avaliação social, não no cadastro,
// e viajam em HousingConditionUpdatedEvent / SocioEconomicSituationUpdatedEvent.
// Campos removidos por serem promessa falsa — ficavam sempre no zero-value.
//
// O buraco de verdade é outro e continua aberto: anonymizeGenericAssessment
// produz FactKindPatientSnapshot mas não extrai `housingType` nem renda do
// `after` desses eventos. Enquanto isso, Snapshot.HousingType e
// Snapshot.IncomeBand nunca são populados por ninguém.

type jsonAssessmentUpdated struct {
	jsonEventBase
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

type jsonHealthStatusAfter struct {
	ICDCode  string `json:"icdCode"`
	ICDLabel string `json:"icdLabel"`
	Chapter  string `json:"chapter"`
	Block    string `json:"block"`
}

type jsonAppointmentRegistered struct {
	jsonEventBase
	AppointmentID          string `json:"appointmentId"`
	ProfessionalInChargeID string `json:"professionalInChargeId"`
	Type                   string `json:"type"` // Swift uses "type", not "appointmentType"
}

type jsonReferralCreated struct {
	jsonEventBase
	ReferralID         string `json:"referralId"`
	ReferredPersonID   string `json:"referredPersonId"`
	DestinationService string `json:"destinationService"`
	Status             string `json:"status"`
}

type jsonRightsViolationReported struct {
	jsonEventBase
	ReportID      string `json:"reportId"`
	VictimID      string `json:"victimId"`
	ViolationType string `json:"violationType"`
}

type jsonFamilyMemberAdded struct {
	jsonEventBase
	MemberID     string `json:"memberId"`
	Relationship string `json:"relationship"`
}

type jsonFamilyMemberRemoved struct {
	jsonEventBase
	MemberID string `json:"memberId"`
}

type jsonPrimaryCaregiverAssigned struct {
	jsonEventBase
	CaregiverID string `json:"caregiverId"`
}

// jsonGenericAssessment is used for assessment events that produce a
// SnapshotPayload (housing, education, socioeconomic, etc.).
type jsonGenericAssessment struct {
	jsonEventBase
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

// jsonLifecycle reads a care-pathway transition.
//
// `notes` is absent BY DESIGN. The source events (Discharged, Readmitted,
// Withdrawn) carry a free-text `notes` written by a caseworker, which can name
// people, addresses or conditions. Declaring the field — even unused — would
// make it one edit away from being persisted. `reason` is categorical and safe.
type jsonLifecycle struct {
	jsonEventBase
	PersonID string `json:"personId"`
	Reason   string `json:"reason"`
}
