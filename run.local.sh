#!/usr/bin/env bash
# =============================================================================
# run.local.sh — roda o svc-analysis-bi lendo o .env local (Go não lê .env sozinho).
# Uso:  ./run.local.sh          (equivale a: source .env && go run ./cmd/server/)
# Suba a infra antes:  cd ../infra/stack/dev && ./up.sh
# =============================================================================
set -euo pipefail
cd "$(dirname "$0")"

if [ -f .env ]; then
  set -a            # exporta tudo que for definido a seguir
  . ./.env
  set +a
else
  echo "⚠  .env não encontrado — usando defaults do config.go" >&2
fi

exec go run ./cmd/server/
