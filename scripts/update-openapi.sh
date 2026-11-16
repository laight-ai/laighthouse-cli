#!/usr/bin/env sh
# 백엔드의 openapi.json을 복사해 CLI가 쓰는 부분만 남기고 Go 클라이언트를 다시 생성한다.
# 사용법: scripts/update-openapi.sh <openapi.json 경로>
set -eu
cd "$(dirname "$0")/.."
src="${1:?openapi.json 경로를 적으세요}"
cp "$src" api/openapi.json
python3 -I scripts/gen-config.py
go tool oapi-codegen -config oapi-codegen.yaml api/openapi.json
gofmt -w internal/api/gen.go
echo "generated internal/api/gen.go from $src"
