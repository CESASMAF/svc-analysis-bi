package configs

import _ "embed"

// IBGEMesoregionsCSV é o mapeamento CEP→mesorregião IBGE embedado no binário
// via go:embed. Garante que a imagem `scratch` (sem filesystem) resolva CEP
// sem depender de um arquivo externo no container. Pode ser sobrescrito por um
// arquivo em runtime via a env GEO_CSV_PATH (ver cmd/server/main.go).
//
//go:embed ibge_mesoregions.csv
var IBGEMesoregionsCSV string
