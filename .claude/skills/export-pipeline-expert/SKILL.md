---
name: export-pipeline-expert
description: >
  Expert skill for the 8-format export pipeline. Covers CSV, JSON, XML, Parquet,
  DBF, DBC, ODS, and FHIR Bundle encoders. Encoder interface design, streaming,
  Content-Type headers, Content-Disposition, and BR Core FHIR profiles.
  Use when the user mentions: export, encoder, CSV, JSON, XML, Parquet, DBF, DBC,
  ODS, FHIR, Bundle, BR Core, RNDS, DataSUS.
user_invocable: true
---

# Export Pipeline Expert -- 8 Format Encoders

You are the export pipeline specialist. You design and implement encoders that transform analytical indicator data into 8 different output formats for diverse consumers.

> ## Zero external dependencies — all 8 encoders are stdlib
>
> `go.mod` has exactly three direct requires: **chi, pgx, nats.go**. Not one of
> them is an encoding library. All 8 formats — including Parquet, DBF, DBC and
> ODS — are implemented by hand in `internal/export/`, already working, with
> tests in `encoder_test.go`.
>
> **Never `go get` an encoding library for this pipeline.** If a task looks like
> it needs `segmentio/parquet-go`, `go-dbf` or `excelize`, the answer is to
> extend the existing encoder, not to add a dependency and rewrite it. Adding one
> also breaks the build contract: `CGO_ENABLED=0` + `FROM scratch`.
>
> Read the target encoder in `internal/export/` before proposing any change —
> the file is the source of truth, this skill is the map.

## Encoder Interface

```go
package export

import (
    "context"
    "io"
)

type Encoder interface {
    Encode(ctx context.Context, data ExportData, w io.Writer) error
    ContentType() string
    FileExtension() string
}

type ExportData struct {
    Dataset    string                   // full, demographics, epidemiological, ...
    PeriodStart string                  // YYYY-MM
    PeriodEnd   string                  // YYYY-MM
    Columns    []ColumnDef
    Rows       []map[string]interface{}
    Meta       ExportMeta
}

type ColumnDef struct {
    Name     string
    Type     string // string, int, float, bool, date
    Label    string // human-readable label
}

type ExportMeta struct {
    GeneratedAt     string
    KThreshold      int
    SuppressedGroups int
    TotalRecords    int
    Source          string // "ACDG Brasil - svc-analysis-bi"
}
```

All encoders write to `io.Writer` for streaming. No full dataset buffering in memory.

## Format Details

### 1. CSV
```go
type CSVEncoder struct{}

func (e *CSVEncoder) Encode(ctx context.Context, data ExportData, w io.Writer) error {
    writer := csv.NewWriter(w)
    defer writer.Flush()

    // Header row
    headers := make([]string, len(data.Columns))
    for i, col := range data.Columns {
        headers[i] = col.Label
    }
    writer.Write(headers)

    // Data rows
    for _, row := range data.Rows {
        record := make([]string, len(data.Columns))
        for i, col := range data.Columns {
            record[i] = fmt.Sprintf("%v", row[col.Name])
        }
        writer.Write(record)
    }
    return nil
}

func (e *CSVEncoder) ContentType() string    { return "text/csv; charset=utf-8" }
func (e *CSVEncoder) FileExtension() string  { return "csv" }
```
- **Content-Type**: `text/csv; charset=utf-8`
- **Audience**: Universal
- UTF-8 BOM for Excel compatibility (optional)

### 2. JSON
```go
type JSONEncoder struct{}

func (e *JSONEncoder) ContentType() string    { return "application/json" }
func (e *JSONEncoder) FileExtension() string  { return "json" }
```
- Standard `encoding/json` with `json.NewEncoder(w)`
- Wraps data in the standard response envelope

### 3. XML
```go
type XMLEncoder struct{}

func (e *XMLEncoder) ContentType() string    { return "application/xml" }
func (e *XMLEncoder) FileExtension() string  { return "xml" }
```
- Standard `encoding/xml` with `xml.NewEncoder(w)`
- Root element: `<dataset>`
- No PII in XML comments or processing instructions

### 4. Parquet
```go
type ParquetEncoder struct{}

func (e *ParquetEncoder) ContentType() string    { return "application/octet-stream" }
func (e *ParquetEncoder) FileExtension() string  { return "parquet" }
```
- **Hand-rolled with stdlib** (`bytes`, `encoding/binary`, `io`) — there is NO
  Parquet dependency in `go.mod`. Do not `go get segmentio/parquet-go`.
- Minimal writer: `PAR1` magic, single row group, plain encoding, no compression,
  then file metadata + footer. See the `TODO` in `internal/export/parquet.go` for
  the known Thrift compact-protocol limitations before extending it.
- Columnar format optimized for analytics tools
- **No PII in file metadata** (created_by, key-value metadata)

### 5. DBF (DataSUS / TABWIN)
```go
type DBFEncoder struct{}

func (e *DBFEncoder) ContentType() string    { return "application/octet-stream" }
func (e *DBFEncoder) FileExtension() string  { return "dbf" }
```
- **Hand-rolled with stdlib** (`bytes`, `encoding/binary`, `io`) — there is NO
  `go-dbf` dependency in `go.mod`. The dBASE III header and field descriptors
  are written byte by byte in `internal/export/dbf.go`.
- dBASE III format (compatible with TABWIN)
- Field names truncated to 10 characters (dBASE spec)
- Character encoding: CP1252 for TABWIN compatibility

### 6. DBC (DataSUS compressed)
```go
type DBCEncoder struct{}

func (e *DBCEncoder) ContentType() string    { return "application/octet-stream" }
func (e *DBCEncoder) FileExtension() string  { return "dbc" }
```
- DBF compressed with PKZIP deflate, via stdlib `compress/flate` — compatible
  with DataSUS tooling. Structure: 8-byte header (original size + compressed
  size, little-endian) followed by the deflate stream.
- **CGo is not an option in this service.** The build is `CGO_ENABLED=0` and the
  runtime image is `FROM scratch`; a cgo-linked binary would not run. Do not
  propose `blast-dbf` or any CGo wrapper — pure Go is the only path.
- Same field structure as DBF encoder

### 7. ODS (Open Document Spreadsheet)
```go
type ODSEncoder struct{}

func (e *ODSEncoder) ContentType() string    { return "application/vnd.oasis.opendocument.spreadsheet" }
func (e *ODSEncoder) FileExtension() string  { return "ods" }
```
- **Hand-rolled with stdlib** (`archive/zip`, `strings`, `bytes`, `io`) — there
  is NO `excelize` dependency in `go.mod`. ODS is a ZIP archive; the encoder
  writes the minimal set: `mimetype`, `META-INF/manifest.xml`, `content.xml`.
- **No PII in document properties** (author, company, description)
- Sheet name: dataset name
- Header row with column labels
- Data typed correctly (numbers as numbers, not strings)

### 8. FHIR Bundle (BR Core)
```go
type FHIREncoder struct{}

func (e *FHIREncoder) ContentType() string    { return "application/fhir+json" }
func (e *FHIREncoder) FileExtension() string  { return "json" }
```
- FHIR R4 Bundle with `type: "collection"`
- BR Core profiles (RNDS):
  - `BRCorePatient`: age band (extension), sex, mesoregion (NO identifier, name, exact address)
  - `BRCoreObservation`: socioeconomic indicators, housing conditions
  - `BRCoreCondition`: diagnoses (ICD-10 codes)
  - `BRCoreEncounter`: social care appointments
- Patient references use anonymized hash (NOT real patient IDs)

## FHIR Resource Mapping

### Patient (anonymized)
```json
{
  "resourceType": "Patient",
  "id": "<patient_hash>",
  "meta": {
    "profile": ["http://www.saude.gov.br/fhir/r4/StructureDefinition/BRIndividuo-1.0"]
  },
  "extension": [
    {
      "url": "http://acdgbrasil.com.br/fhir/StructureDefinition/age-band",
      "valueString": "10-14"
    }
  ],
  "gender": "female",
  "address": [
    {
      "state": "MG",
      "extension": [
        {
          "url": "http://acdgbrasil.com.br/fhir/StructureDefinition/mesoregion",
          "valueString": "3106"
        }
      ]
    }
  ]
}
```
- **NO** `identifier` (no CPF, no CNS)
- **NO** `name`
- **NO** `birthDate` (replaced by age-band extension)
- **NO** exact `address` (only state + mesoregion extension)

## HTTP Response Headers

```go
func setExportHeaders(w http.ResponseWriter, encoder Encoder, dataset, period string) {
    w.Header().Set("Content-Type", encoder.ContentType())
    filename := fmt.Sprintf("acdg-%s-%s.%s", dataset, period, encoder.FileExtension())
    w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
    w.Header().Set("Cache-Control", "no-store")
}
```

## Encoder Registry

```go
var encoders = map[string]Encoder{
    "csv":     &CSVEncoder{},
    "json":    &JSONEncoder{},
    "xml":     &XMLEncoder{},
    "parquet": &ParquetEncoder{},
    "dbf":     &DBFEncoder{},
    "dbc":     &DBCEncoder{},
    "ods":     &ODSEncoder{},
    "fhir":    &FHIREncoder{},
}

func GetEncoder(format string) (Encoder, error) {
    enc, ok := encoders[format]
    if !ok {
        return nil, fmt.Errorf("unsupported format: %s", format)
    }
    return enc, nil
}
```

## Rules (non-negotiable)
1. **io.Writer streaming** -- all encoders write to io.Writer, never buffer full dataset
2. **No PII in metadata** -- no author, company, or comments with identifying info
3. **K-anonymity respected** -- export data already filtered by K threshold
4. **Content-Type correct** -- each format has its specific MIME type
5. **Content-Disposition** -- attachment with sanitized filename (no path traversal)
6. **FHIR compliance** -- BR Core profiles, anonymized Patient, no identifiers
7. **DataSUS compatibility** -- DBF/DBC follow TABWIN conventions
8. **Context cancellation** -- encoders respect `ctx.Done()` for large exports
