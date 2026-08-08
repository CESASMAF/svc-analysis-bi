package domain

// EventType enumerates all NATS subjects this service subscribes to.
type EventType string

// Event subjects match the NATS subjects published by svc-social-care's
// NATSEventPublisher, which uses "social-care.events.<SwiftTypeName>".
const (
	EventPatientCreated             EventType = "social-care.events.PatientCreatedEvent"
	EventFamilyMemberAdded          EventType = "social-care.events.FamilyMemberAddedEvent"
	EventFamilyMemberRemoved        EventType = "social-care.events.FamilyMemberRemovedEvent"
	EventPrimaryCaregiverAssigned   EventType = "social-care.events.PrimaryCaregiverAssignedEvent"
	EventSocialIdentityUpdated      EventType = "social-care.events.SocialIdentityUpdatedEvent"
	EventHousingConditionUpdated    EventType = "social-care.events.HousingConditionUpdatedEvent"
	EventSocioEconomicUpdated       EventType = "social-care.events.SocioEconomicSituationUpdatedEvent"
	EventWorkAndIncomeUpdated       EventType = "social-care.events.WorkAndIncomeUpdatedEvent"
	EventEducationalStatusUpdated   EventType = "social-care.events.EducationalStatusUpdatedEvent"
	EventHealthStatusUpdated        EventType = "social-care.events.HealthStatusUpdatedEvent"
	EventCommunitySupportUpdated    EventType = "social-care.events.CommunitySupportNetworkUpdatedEvent"
	EventSocialHealthSummaryUpdated EventType = "social-care.events.SocialHealthSummaryUpdatedEvent"
	EventAppointmentRegistered      EventType = "social-care.events.SocialCareAppointmentRegisteredEvent"
	EventIntakeInfoUpdated          EventType = "social-care.events.IntakeInfoUpdatedEvent"
	EventPlacementHistoryUpdated    EventType = "social-care.events.PlacementHistoryUpdatedEvent"
	EventRightsViolationReported    EventType = "social-care.events.RightsViolationReportedEvent"
	EventReferralCreated            EventType = "social-care.events.ReferralCreatedEvent"

	// Ciclo de vida do atendimento. Sem estes, o serviço não sabe responder
	// quando alguém entrou ou saiu — a pergunta mais básica do domínio.
	EventPatientAdmitted              EventType = "social-care.events.PatientAdmittedEvent"
	EventPatientDischarged            EventType = "social-care.events.PatientDischargedEvent"
	EventPatientReadmitted            EventType = "social-care.events.PatientReadmittedEvent"
	EventPatientWithdrawnFromWaitlist EventType = "social-care.events.PatientWithdrawnFromWaitlistEvent"

	// Erasure LGPD executada na origem (ADR-039 do social-care). Reconhecido de
	// propósito e SEM efeito — ver docs/adr/ADR-002-pii-anonymized-noop.md.
	EventPatientPIIAnonymized EventType = "social-care.events.PatientPIIAnonymizedEvent"
)

// EventMetadata contains traceability fields present in every event.
type EventMetadata struct {
	EventID       string
	OccurredAt    string
	SchemaVersion string
}

// PatientCreatedEvent is emitted when a new patient record is created.
type PatientCreatedEvent struct {
	Metadata  EventMetadata
	PatientID string
	PersonID  string
}

// FamilyMemberAddedEvent is emitted when a family member is added.
type FamilyMemberAddedEvent struct {
	Metadata     EventMetadata
	PatientID    string
	MemberID     string
	Relationship string
}

// FamilyMemberRemovedEvent is emitted when a family member is removed.
type FamilyMemberRemovedEvent struct {
	Metadata  EventMetadata
	PatientID string
	MemberID  string
}

// PrimaryCaregiverAssignedEvent is emitted when a primary caregiver is assigned.
type PrimaryCaregiverAssignedEvent struct {
	Metadata    EventMetadata
	PatientID   string
	CaregiverID string
}

// AssessmentUpdatedEvent is the generic payload for all assessment updates.
// Before/After are kept as []byte (raw JSON) because the concrete assessment
// type varies by NATS subject (10 different schemas). The ingestion layer
// deserializes them into typed structs based on the EventType.
type AssessmentUpdatedEvent struct {
	Metadata  EventMetadata
	PatientID string
	Before    []byte // json.RawMessage — nil on first assignment
	After     []byte // json.RawMessage — full assessment snapshot
}

// AppointmentRegisteredEvent is emitted when a social care appointment is registered.
type AppointmentRegisteredEvent struct {
	Metadata               EventMetadata
	PatientID              string
	AppointmentID          string
	ProfessionalInChargeID string
	AppointmentType        string
}

// RightsViolationReportedEvent is emitted when a rights violation report is filed.
type RightsViolationReportedEvent struct {
	Metadata      EventMetadata
	PatientID     string
	ReportID      string
	VictimID      string
	ViolationType string
}

// ReferralCreatedEvent is emitted when a referral is created.
type ReferralCreatedEvent struct {
	Metadata           EventMetadata
	PatientID          string
	ReferralID         string
	ReferredPersonID   string
	DestinationService string
	Status             string
}
