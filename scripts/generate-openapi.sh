#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

mkdir -p server/internal/generated/openapi src/api
compatibility_spec=$(mktemp "${TMPDIR:-/tmp}/history-wiki-openapi.XXXXXX")
trap 'rm -f "$compatibility_spec"' EXIT HUP INT TERM
node scripts/openapi-for-oapi-codegen.mjs server/openapi/openapi.yaml "$compatibility_spec"
(
  cd server
  go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.5.0 \
    --config openapi/oapi-codegen.yaml "$compatibility_spec"
)
./node_modules/.bin/openapi-typescript server/openapi/openapi.yaml \
  --output src/api/openapi.gen.ts
