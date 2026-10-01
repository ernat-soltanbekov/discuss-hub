.PHONY: run build test audit demo docker
run:
	go run ./cmd/server
build:
	go build -trimpath -o bin/discuss-hub ./cmd/server
test:
	go test ./...
audit:
	go vet ./...
	go test -race -count=1 ./...
demo:
	DB_PATH=data/demo.db go run ./cmd/server -demo
docker:
	docker compose up --build
