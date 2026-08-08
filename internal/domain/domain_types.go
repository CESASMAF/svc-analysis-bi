package domain

import "errors"

// PatientHash is an opaque, irreversible SHA-256 digest of a patientId
// combined with a secret salt.
type PatientHash string

// Sex represents the biological sex used as a quasi-identifier.
type Sex string

const (
	SexMale    Sex = "MALE"
	SexFemale  Sex = "FEMALE"
	SexUnknown Sex = "UNKNOWN"
)

// AgeBand represents a 5-year age bracket used to generalize exact birth
// dates (0-4, 5-9, ..., 75-79, 80+).
type AgeBand struct {
	Label  string
	MinAge int
	MaxAge int
}

// IncomeBand classifies total household income into coarse brackets
// relative to the Brazilian minimum wage.
type IncomeBand string

const (
	IncomeBand0to05SM IncomeBand = "0-0.5SM"
	IncomeBand05to1SM IncomeBand = "0.5-1SM"
	IncomeBand1to2SM  IncomeBand = "1-2SM"
	IncomeBand2to3SM  IncomeBand = "2-3SM"
	IncomeBand3to5SM  IncomeBand = "3-5SM"
	IncomeBand5PlusSM IncomeBand = "5+SM"
)

// Period represents a single month in a time series.
type Period struct {
	Year  int
	Month int
}

// TimeGranularity controls how time-series data is aggregated.
type TimeGranularity string

const (
	GranularityMonthly   TimeGranularity = "monthly"
	GranularityQuarterly TimeGranularity = "quarterly"
	GranularityYearly    TimeGranularity = "yearly"
)

// MesoregionCode is a 4-digit IBGE mesoregion code.
type MesoregionCode string

// Geography holds the full IBGE geographic hierarchy resolved from a CEP.
type Geography struct {
	MesoregionCode  MesoregionCode
	MesoregionName  string
	MicroregionCode string
	MicroregionName string
	StateCode       string
	StateName       string
	Region          string
}

// QuasiIdentifier groups the three generalized attributes used for
// K-anonymity equivalence classes.
type QuasiIdentifier struct {
	AgeBand    AgeBand
	Sex        Sex
	Mesoregion MesoregionCode
}

// IndicatorAxis identifies which thematic axis an indicator belongs to.
type IndicatorAxis string

const (
	AxisDemographics    IndicatorAxis = "demographics"
	AxisEpidemiological IndicatorAxis = "epidemiological"
	AxisSocioeconomic   IndicatorAxis = "socioeconomic"
	AxisProtection      IndicatorAxis = "protection"
	AxisCare            IndicatorAxis = "care"
)

// IndicatorGroup represents a single cell in an indicator result.
type IndicatorGroup struct {
	Labels         map[string]string
	Value          int
	BelowThreshold bool
}

// IndicatorResult is the generic envelope returned by indicator queries.
type IndicatorResult struct {
	Axis       IndicatorAxis
	Period     Period
	Groups     []IndicatorGroup
	Suppressed int
}

// TimeSeriesPoint is a single data point in a time series.
type TimeSeriesPoint struct {
	Period string
	Value  float64
}

// KAnonymityNotice is attached to indicator responses when cells have been
// suppressed.
type KAnonymityNotice struct {
	Suppressed       bool
	Message          string
	SuppressedFields []string
}

// DatasetScope identifies the scope of a dataset for export or query.
type DatasetScope string

const (
	DatasetScopeFull            DatasetScope = "full"
	DatasetScopeDemographics    DatasetScope = "demographics"
	DatasetScopeEpidemiological DatasetScope = "epidemiological"
	DatasetScopeSocioeconomic   DatasetScope = "socioeconomic"
	DatasetScopeProtection      DatasetScope = "protection"
	DatasetScopeCare            DatasetScope = "care"
)

// ExportFormat represents a supported file format for bulk data export.
type ExportFormat string

const (
	ExportFormatCSV     ExportFormat = "csv"
	ExportFormatJSON    ExportFormat = "json"
	ExportFormatXML     ExportFormat = "xml"
	ExportFormatParquet ExportFormat = "parquet"
	ExportFormatDBF     ExportFormat = "dbf"
	ExportFormatDBC     ExportFormat = "dbc"
	ExportFormatODS     ExportFormat = "ods"
	ExportFormatFHIR    ExportFormat = "fhir"
)

var (
	ErrCEPWrongLength      = errors.New("CEP must be exactly 8 characters")
	ErrCEPNonDigit         = errors.New("CEP must contain only digits")
	ErrCEPNotFound         = errors.New("CEP not found in geography lookup")
	ErrInvalidBirthDate    = errors.New("invalid birth date: must be in the past")
	ErrEmptySalt           = errors.New("anonymization salt must not be empty")
	ErrInvalidPeriod       = errors.New("invalid period: month must be 1-12 and year must be positive")
	ErrInvalidAge          = errors.New("age must be non-negative")
	ErrInvalidGranularity  = errors.New("invalid time granularity")
	ErrInvalidExportFormat = errors.New("invalid export format")
	ErrInvalidDatasetScope = errors.New("invalid dataset scope")
)

// LifecycleStatus is where a patient stands in the care pathway.
//
// It is a small closed set on purpose: it comes from domain events, not from
// free text, and it feeds a dimension that must stay countable.
type LifecycleStatus string

const (
	LifecycleAdmitted   LifecycleStatus = "ADMITTED"
	LifecycleDischarged LifecycleStatus = "DISCHARGED"
	LifecycleReadmitted LifecycleStatus = "READMITTED"
	LifecycleWithdrawn  LifecycleStatus = "WITHDRAWN"
)
