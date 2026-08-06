package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// minimumWageCents is the Brazilian minimum wage in cents (R$1,412.00 as of 2024).
// Used for income band classification.
const minimumWageCents int64 = 141200

// allAgeBands returns the predefined 5-year age brackets.
// The 80+ band uses a conventional ceiling of 199.
// Returns a fresh slice each time to prevent accidental mutation.
func allAgeBands() []AgeBand {
	return []AgeBand{
		{Label: "0-4", MinAge: 0, MaxAge: 4},
		{Label: "5-9", MinAge: 5, MaxAge: 9},
		{Label: "10-14", MinAge: 10, MaxAge: 14},
		{Label: "15-19", MinAge: 15, MaxAge: 19},
		{Label: "20-24", MinAge: 20, MaxAge: 24},
		{Label: "25-29", MinAge: 25, MaxAge: 29},
		{Label: "30-34", MinAge: 30, MaxAge: 34},
		{Label: "35-39", MinAge: 35, MaxAge: 39},
		{Label: "40-44", MinAge: 40, MaxAge: 44},
		{Label: "45-49", MinAge: 45, MaxAge: 49},
		{Label: "50-54", MinAge: 50, MaxAge: 54},
		{Label: "55-59", MinAge: 55, MaxAge: 59},
		{Label: "60-64", MinAge: 60, MaxAge: 64},
		{Label: "65-69", MinAge: 65, MaxAge: 69},
		{Label: "70-74", MinAge: 70, MaxAge: 74},
		{Label: "75-79", MinAge: 75, MaxAge: 79},
		{Label: "80+", MinAge: 80, MaxAge: 199},
	}
}

// HashPatientID produces an irreversible HMAC-SHA256 digest of the patientID
// keyed with the provided salt. Using HMAC avoids ambiguous concatenation
// (e.g., "ab"+"c" vs "a"+"bc" producing the same digest).
func HashPatientID(patientID string, salt string) (PatientHash, error) {
	if salt == "" {
		return "", ErrEmptySalt
	}
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(patientID))
	return PatientHash(hex.EncodeToString(mac.Sum(nil))), nil
}

// GeneralizeAge computes the patient's age at referenceDate from birthDate,
// then maps it to the appropriate 5-year AgeBand.
func GeneralizeAge(birthDate time.Time, referenceDate time.Time) (AgeBand, error) {
	if birthDate.After(referenceDate) {
		return AgeBand{}, ErrInvalidBirthDate
	}

	age := computeAge(birthDate, referenceDate)
	if age < 0 {
		return AgeBand{}, ErrInvalidAge
	}

	bands := allAgeBands()
	for _, band := range bands {
		if age >= band.MinAge && age <= band.MaxAge {
			return band, nil
		}
	}

	// Should not reach here, but return 80+ as fallback for very old ages
	return bands[len(bands)-1], nil
}

// computeAge calculates age in completed years between birth and reference dates.
func computeAge(birth, ref time.Time) int {
	years := ref.Year() - birth.Year()
	if ref.Month() < birth.Month() ||
		(ref.Month() == birth.Month() && ref.Day() < birth.Day()) {
		years--
	}
	return years
}

// GeneralizeIncome classifies a total household income (in cents) into
// one of the predefined IncomeBand brackets relative to the minimum wage.
// Negative values are clamped to the lowest band.
func GeneralizeIncome(totalIncomeCents int64) IncomeBand {
	if totalIncomeCents < 0 {
		return IncomeBand0to05SM
	}

	// Thresholds in cents based on multiples of minimum wage
	half := minimumWageCents / 2
	one := minimumWageCents
	two := minimumWageCents * 2
	three := minimumWageCents * 3
	five := minimumWageCents * 5

	switch {
	case totalIncomeCents <= half:
		return IncomeBand0to05SM
	case totalIncomeCents < one:
		return IncomeBand05to1SM
	case totalIncomeCents < two:
		return IncomeBand1to2SM
	case totalIncomeCents < three:
		return IncomeBand2to3SM
	case totalIncomeCents < five:
		return IncomeBand3to5SM
	default:
		return IncomeBand5PlusSM
	}
}

// GeographyLookup resolves a raw 8-digit CEP string to its IBGE geographic
// hierarchy. This is the only interface in the domain package.
type GeographyLookup interface {
	FindByCEP(cep string) (Geography, error)
}

// validateCEP checks that a CEP string is exactly 8 digits.
// Returns ErrCEPWrongLength or ErrCEPNonDigit as appropriate.
func validateCEP(cep string) error {
	if len(cep) != 8 {
		return ErrCEPWrongLength
	}
	for _, c := range cep {
		if c < '0' || c > '9' {
			return ErrCEPNonDigit
		}
	}
	return nil
}

// AgeBandFromLabel resolves a band label produced upstream ("0-4", "80+") back
// into the full AgeBand, so the label never has to be parsed ad hoc.
//
// Since social-care started generalizing at the source (no birthDate crosses
// the boundary), this is how the band enters the pipeline. It is deliberately
// a lookup against allAgeBands and NOT a parser: the set of bands is a
// modelling decision shared by both services, and an unrecognized label must
// fail loudly rather than be reconstructed into something plausible.
func AgeBandFromLabel(label string) (AgeBand, bool) {
	for _, band := range allAgeBands() {
		if band.Label == label {
			return band, true
		}
	}
	return AgeBand{}, false
}
