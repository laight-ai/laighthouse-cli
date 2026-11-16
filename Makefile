.PHONY: build test vet gen

# 백엔드의 openapi.json을 잘라 넣고 internal/api/gen.go를 다시 만든다. make gen SPEC=<openapi.json 경로>
gen:
	sh scripts/update-openapi.sh $(SPEC)

build:
	go build -o bin/laighthouse-cli ./cmd/laighthouse-cli

test:
	go test ./...

vet:
	go vet ./...
