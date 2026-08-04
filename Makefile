.PHONY: build ml-setup db-up db-down dashboard-up dashboard-down dashboard-dev migrate-up migrate-down test test-race test-integration lint fmt

build:
	go build -o bin/mip ./cmd/mip

ml-setup:
	python3.12 -m venv .venv
	.venv/bin/python -m pip install -r requirements-ml.txt

db-up:
	docker compose up -d postgres

db-down:
	docker compose down

dashboard-up:
	docker compose up -d --build redis api dashboard

dashboard-down:
	docker compose stop dashboard api redis

dashboard-dev:
	cd web && npm run dev

migrate-up:
	docker compose --profile tools run --rm migrate

migrate-down:
	docker compose --profile tools run --rm migrate -path=/migrations \
		-database="postgres://$${POSTGRES_USER:-mip}:$${POSTGRES_PASSWORD:-change-me}@postgres:5432/$${POSTGRES_DB:-mip}?sslmode=disable" down 1

test:
	go test ./...

test-race:
	go test -race ./...

test-integration:
	TEST_DATABASE_URL="$${DATABASE_URL}" go test -tags=integration ./internal/postgres

lint:
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...
