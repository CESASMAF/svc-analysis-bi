package ingestion

import (
	"context"

	"github.com/acdgbrasil/svc-analysis-bi/internal/domain"
)

// NewEventHandlerRegistry creates an EventHandlerRegistry with handlers for
// all 17 event types. Each handler delegates to a shared Anonymizer instance
// for PII removal and quasi-identifier generalization.
func NewEventHandlerRegistry(geo domain.GeographyLookup, salt string) EventHandlerRegistry {
	anonymizer := NewAnonymizer(geo, salt)

	registry := make(EventHandlerRegistry)

	// Helper to register all event types with the shared anonymizer.
	register := func(eventType domain.EventType) {
		et := eventType // capture for closure
		registry[et] = func(ctx context.Context, data []byte) (AnonymizedRecord, error) {
			return anonymizer.Anonymize(ctx, et, data)
		}
	}

	// Registry / Demographics
	register(domain.EventPatientCreated)
	register(domain.EventFamilyMemberAdded)
	register(domain.EventFamilyMemberRemoved)
	register(domain.EventPrimaryCaregiverAssigned)
	register(domain.EventSocialIdentityUpdated)

	// Assessment
	register(domain.EventHousingConditionUpdated)
	register(domain.EventSocioEconomicUpdated)
	register(domain.EventWorkAndIncomeUpdated)
	register(domain.EventEducationalStatusUpdated)
	register(domain.EventHealthStatusUpdated)
	register(domain.EventCommunitySupportUpdated)
	register(domain.EventSocialHealthSummaryUpdated)

	// Care
	register(domain.EventAppointmentRegistered)
	register(domain.EventIntakeInfoUpdated)

	// Protection
	register(domain.EventPlacementHistoryUpdated)
	register(domain.EventRightsViolationReported)
	register(domain.EventReferralCreated)

	// Ciclo de vida do atendimento (ADR-002). Sem registro aqui o evento nem
	// chega ao anonymizer: o pipeline consulta ESTE mapa e manda para a DLQ o
	// que não encontra. Era por isto que os cinco caíam em silêncio.
	register(domain.EventPatientAdmitted)
	register(domain.EventPatientDischarged)
	register(domain.EventPatientReadmitted)
	register(domain.EventPatientWithdrawnFromWaitlist)

	// Erasure na origem — reconhecido, sem efeito local (ADR-002 §4).
	register(domain.EventPatientPIIAnonymized)

	return registry
}
