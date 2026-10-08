.PHONY: build run test test-race vet fmt migrate-up migrate-down compose-up compose-down

BINARY_NAME    := api
BUILD_DIR      := bin
CMD_DIR        := ./cmd/api
MIGRATIONS_DIR := migrations
COMPOSE_FILE   := deployments/docker-compose.yml
DATABASE_URL   ?=

## build: compila o binário da API em bin/api
build:
	go build -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)

## run: roda a API diretamente via go run (sem gerar binário)
run:
	go run $(CMD_DIR)

## test: executa os testes unitários (exclui testes com build tag "integration")
test:
	go test ./...

## test-race: executa os testes com o detector de race conditions habilitado
test-race:
	go test -race ./...

## vet: roda go vet em todos os pacotes
vet:
	go vet ./...

## fmt: formata todo o código Go do módulo (falha silenciosamente nunca; só reescreve arquivos)
fmt:
	gofmt -l -w .

## migrate-up: aplica todas as migrations pendentes contra $DATABASE_URL
migrate-up:
	@if [ -z "$(DATABASE_URL)" ]; then echo "DATABASE_URL não definido"; exit 1; fi
	migrate -database "$(DATABASE_URL)" -path $(MIGRATIONS_DIR) up

## migrate-down: reverte a última migration aplicada em $DATABASE_URL
migrate-down:
	@if [ -z "$(DATABASE_URL)" ]; then echo "DATABASE_URL não definido"; exit 1; fi
	migrate -database "$(DATABASE_URL)" -path $(MIGRATIONS_DIR) down

## compose-up: sobe a infraestrutura local (Postgres, LocalStack, Keycloak) em background
compose-up:
	docker compose -f $(COMPOSE_FILE) up -d

## compose-down: derruba a infraestrutura local subida pelo compose-up
compose-down:
	docker compose -f $(COMPOSE_FILE) down
