# backend-challenge-go

Serviço Go de processamento distribuído de apostas: API HTTP + consumidor SQS que movimentam
carteiras de jogadores (débito/crédito) com garantias financeiras corretas sob múltiplas
instâncias concorrentes e falhas parciais (processo caindo, mensagem reentregue, rede
instável).

Este README cobre como preparar o ambiente, subir a infraestrutura local e rodar o serviço e os
testes. Para as explicações de arquitetura (dinheiro, concorrência, idempotência, mensageria, auth,
Fx, shutdown) veja **[ARCHITECTURE.md](ARCHITECTURE.md)**.

## Status atual

Este repositório já contém, com lógica de negócio real (não apenas assinaturas): todo o domínio
(`internal/domain/**`), os casos de uso de aplicação, os adapters Postgres/HTTP/SQS, os dois
workers de background, toda a composição Fx (`cmd/api/main.go` + `internal/platform/fx/**`), a
migration inicial completa e a infraestrutura local (docker-compose, Keycloak, LocalStack).
Também contém uma suíte de testes unitários (`go test ./...`, limpa sob `-race`) e uma suíte de
integração (`test/integration/`, tag `integration`) que roda contra Postgres/Keycloak/LocalStack
reais via `testcontainers-go` — ver a seção **Testes** abaixo.

## Pré-requisitos


  1. Instale o Go 1.23+ ([https://go.dev/dl/](https://go.dev/dl/)).
  2. Confirme com `go version`.
  3. `go.sum` já está completo (dependências diretas e indiretas, com os checksums completos) e o
     projeto compila/testa limpo (`go build ./...`, `go vet ./...`, `gofmt -l .`, `go test ./...`
     sem nenhum erro). `go mod tidy` é opcional aqui, só útil como verificação extra ou depois de
     alterar `go.mod`.
- **Docker** e **Docker Compose** (para subir Postgres, LocalStack e Keycloak localmente).
- **[golang-migrate](https://github.com/golang-migrate/migrate) CLI** (ferramenta externa, não é
  uma dependência Go do módulo), usada para aplicar/reverter as migrations em `migrations/`.
  ```
  go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
  ```
- `curl` e `jq` são usados nos exemplos abaixo, mas são opcionais (qualquer cliente HTTP serve).

## Estrutura de pastas

```
backend-challenge-go/
├── cmd/api/main.go                      # entry point: config.Load + fx.New + Start/Stop
├── internal/
│   ├── config/config.go                 # carrega e valida toda a configuração via env vars
│   ├── platform/
│   │   ├── logging/logger.go            # slog JSON + helpers de correlação
│   │   ├── metrics/metrics.go           # coletores Prometheus
│   │   └── fx/                          # um module_*.go por recurso (ver ARCHITECTURE.md)
│   ├── domain/                          # zero dependência de Fx/HTTP/SQS/pgx
│   │   ├── money/                       # Money (value object imutável)
│   │   ├── wallet/                      # Wallet (aggregate root) + WalletLedgerEntry
│   │   ├── wagering/                    # WagerTransaction + máquina de estados + failureCodes
│   │   └── events/                      # envelope de evento + 4 eventos tipados
│   ├── ports/                           # interfaces entre aplicação e infraestrutura
│   ├── application/
│   │   ├── wallets/open_wallet.go       # abertura de carteira (+ OPENING quando saldo > 0)
│   │   └── wagering/                    # process_transaction (uso compartilhado HTTP+SQS),
│   │                                      resolve_pending_reference, reconciliation
│   ├── adapters/
│   │   ├── postgres/                    # pgxpool, TxManager, repositórios (SQL explícito)
│   │   ├── http/                        # router chi, handlers, DTOs, middleware de auth
│   │   └── sqs/                         # client, consumer (inbound), outbound publisher
│   └── workers/                         # outbox_publisher, pending_reference_resolver
├── migrations/                          # golang-migrate: 000001_init_schema.{up,down}.sql
├── deployments/
│   ├── docker-compose.yml               # Postgres + LocalStack (SQS) + Keycloak
│   ├── keycloak/realm-export.json       # realm, clients (provider-a/b, wallet-service), roles
│   └── localstack/init-queues.sh        # cria as 4 filas FIFO (+ DLQs) no LocalStack                    
├── test/integration/                    # suíte com build tag `integration` (testcontainers-go):
│                                           Postgres/Keycloak/LocalStack reais, nunca mocks —
│                                           ver seção "Testes" abaixo e test/integration/README.md
├── go.mod / go.sum
├── Makefile
├── .env.example
├── README.md / ARCHITECTURE.md
```

## Configuração (variáveis de ambiente)

Copie `.env.example` para `.env` e ajuste se necessário. Nenhum valor lá é um segredo real, só
credenciais de desenvolvimento local.

| Grupo | Variáveis | Observação |
|---|---|---|
| Aplicação | `APP_ENV`, `HTTP_PORT`, `METRICS_PORT`, `LOG_LEVEL`, `SHUTDOWN_TIMEOUT` | `SHUTDOWN_TIMEOUT` vira `fx.StopTimeout` (default 25s) |
| Postgres | `POSTGRES_HOST/PORT/USER/PASSWORD/DB`, `DATABASE_URL`, `DB_MAX_CONNS`, `DB_MIN_CONNS` | `DATABASE_URL` é usada tanto pela aplicação (pgxpool) quanto pela CLI `migrate` |
| AWS / LocalStack (SQS) | `AWS_REGION`, `AWS_SQS_ENDPOINT`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `SQS_WAGER_TRANSACTIONS_QUEUE_URL`, `SQS_WAGER_TRANSACTIONS_DLQ_URL`, `SQS_WAGER_EVENTS_QUEUE_URL`, `SQS_WAGER_EVENTS_DLQ_URL`, `SQS_CONSUMER_NAME`, `SQS_MAX_MESSAGES`, `SQS_WAIT_TIME_SECONDS`, `SQS_VISIBILITY_TIMEOUT` | `AWS_SQS_ENDPOINT` aponta para o LocalStack; sem ela o SDK tenta a AWS real |
| OIDC / Keycloak | `KEYCLOAK_PORT`, `KEYCLOAK_ADMIN`, `KEYCLOAK_ADMIN_PASSWORD`, `KEYCLOAK_REALM`, `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `PROVIDER_A_CLIENT_ID/SECRET`, `PROVIDER_B_CLIENT_ID/SECRET`, `OIDC_TEST_USERNAME/PASSWORD` | ver seção de autenticação abaixo |
| Worker PENDING_REFERENCE | `PENDING_REFERENCE_BACKOFF_BASE`, `PENDING_REFERENCE_BACKOFF_FACTOR`, `PENDING_REFERENCE_BACKOFF_MAX_INTERVAL`, `PENDING_REFERENCE_BACKOFF_JITTER`, `PENDING_REFERENCE_MAX_ATTEMPTS`, `PENDING_REFERENCE_TTL`, `PENDING_REFERENCE_POLL_INTERVAL`, `PENDING_REFERENCE_MAX_POLL_INTERVAL`, `PENDING_REFERENCE_BATCH_SIZE` | backoff exponencial com jitter, nunca fixo no código; `JITTER` é uma fração (0–1), não percentual |
| Worker Outbox | `OUTBOX_BACKOFF_BASE`, `OUTBOX_BACKOFF_FACTOR`, `OUTBOX_BACKOFF_MAX_INTERVAL`, `OUTBOX_BACKOFF_JITTER`, `OUTBOX_POLL_INTERVAL`, `OUTBOX_MAX_POLL_INTERVAL`, `OUTBOX_BATCH_SIZE` | mesmo esquema de backoff do worker acima |

Não existe hoje uma variável `WALLET_LOCK_TIMEOUT`: o timeout do `SELECT ... FOR UPDATE` em
`wallets` é a constante `lockTimeout = "3s"` em `internal/adapters/postgres/tx_manager.go`, ainda
não plugada em `internal/config`. Ver `ARCHITECTURE.md` (seção "Locks/Concorrência").


## Subindo a infraestrutura local

```bash
docker compose --env-file .env -f deployments/docker-compose.yml up -d
```

Isso sobe três serviços:

- **Postgres** (`localhost:5433` com o `.env.example`/`.env` padrão — `POSTGRES_PORT=5433`,
  escolhida para não colidir com outro Postgres local na porta `5432`; banco `backend_challenge`).
- **LocalStack** (`localhost:4566`), com um hook de init (`deployments/localstack/init-queues.sh`)
  que cria automaticamente as 4 filas FIFO (`wager-transactions.fifo` +
  `wager-transactions-dlq.fifo`, `wager-events.fifo` + `wager-events-dlq.fifo`) assim que o
  container fica pronto.
- **Keycloak** (`localhost:8081`), importando `deployments/keycloak/realm-export.json`
  automaticamente (`start-dev --import-realm`).

Para derrubar: `docker compose -f deployments/docker-compose.yml down` (ou `make compose-down`).

## Migrations (golang-migrate)

Com o Postgres do passo anterior no ar:

```bash
# aplicar todas as migrations pendentes
migrate -database "$DATABASE_URL" -path migrations up

# reverter a última migration aplicada
migrate -database "$DATABASE_URL" -path migrations down 1
```

Ou via `Makefile` (lê `DATABASE_URL` do ambiente): `make migrate-up` / `make migrate-down`.

A migration `000001_init_schema` cria `wallets`, `wager_transactions`, `wallet_ledger_entries`
(com o trigger `ledger_immutable` que rejeita `UPDATE`/`DELETE`), `inbox_messages` e
`outbox_events`, com todas as constraints/índices descritos em
**[ARCHITECTURE.md](ARCHITECTURE.md)**.

## Rodando a aplicação

```bash
go run ./cmd/api
# ou: make run
```

Em desenvolvimento (`APP_ENV` diferente de `production`, inclusive quando a variável nem existe),
o processo carrega `.env` automaticamente via `godotenv` antes de ler a configuração — não é
preciso exportar as variáveis manualmente no shell. Se `APP_ENV=production`, esse carregamento é
pulado e as variáveis precisam vir do ambiente real (container/orquestrador), nunca de um arquivo
`.env`.

O processo falha rápido no boot (antes de aceitar qualquer requisição) se `DATABASE_URL`,
Postgres, o endpoint SQS ou o issuer OIDC estiverem incorretos/indisponíveis. Ver
`internal/platform/fx/module_db.go`, `module_sqs.go` e `module_auth.go`. A API sobe em
`HTTP_PORT` (default `8080`). Métricas Prometheus ficam em um listener HTTP separado em
`METRICS_PORT` (default `9090`), em `GET /metrics`.

### Imagem Docker

```bash
docker build -t backend-challenge-go .
docker run --rm --env-file .env --network host backend-challenge-go
```

O `Dockerfile` é multi-stage: builder `golang:1.23-alpine` + runtime `distroless/static-debian12`
(sem shell, sem libc, usuário não-root), ~32MB. Um workflow do GitHub Actions
(`.github/workflows/docker-publish.yml`) builda e publica essa imagem em `ghcr.io/<repo>`
automaticamente a cada Release publicada no GitHub.

## Autenticação — obtendo um token de teste no Keycloak

Toda rota de negócio exige `Authorization: Bearer <token>` (client_credentials entre serviços).
O realm de desenvolvimento (`deployments/keycloak/realm-export.json`) já provisiona os clients
`provider-a`, `provider-b` (role `provider`, com a identidade de provedor derivada do próprio
client OAuth2) e `wallet-service` (role `internal-service`, único autorizado a abrir/administrar
carteiras).

```bash
# token do provider-a (só enxerga/opera as próprias transações, inclusive em replay)
curl -s -X POST \
  "$OIDC_ISSUER_URL/protocol/openid-connect/token" \
  -d grant_type=client_credentials \
  -d client_id="$PROVIDER_A_CLIENT_ID" \
  -d client_secret="$PROVIDER_A_CLIENT_SECRET" \
  | jq -r .access_token

# token do serviço interno (único que pode abrir carteira/reconciliar)
curl -s -X POST \
  "$OIDC_ISSUER_URL/protocol/openid-connect/token" \
  -d grant_type=client_credentials \
  -d client_id="$OIDC_CLIENT_ID" \
  -d client_secret="$OIDC_CLIENT_SECRET" \
  | jq -r .access_token
```

Também há um usuário de teste (`testuser`/`testpassword`) no client `wallet-service` com
Resource Owner Password Credentials habilitado só em desenvolvimento, útil para obter um token
manualmente sem `curl -d grant_type=client_credentials`:

```bash
curl -s -X POST \
  "$OIDC_ISSUER_URL/protocol/openid-connect/token" \
  -d grant_type=password \
  -d client_id="$OIDC_CLIENT_ID" \
  -d client_secret="$OIDC_CLIENT_SECRET" \
  -d username="$OIDC_TEST_USERNAME" \
  -d password="$OIDC_TEST_PASSWORD" \
  | jq -r .access_token
```

## Exemplos de chamadas HTTP

Os exemplos abaixo seguem exatamente o contrato do edital. `$TOKEN` é um dos tokens obtidos acima
(use o do `wallet-service` para `/wallets/**`; o de um provider para `/wagering/**`).

```bash
# POST /wallets — abrir carteira (serviço interno)
curl -s -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"playerId":"player-123","initialBalance":{"amount":"100.00","currency":"BRL"}}'
# -> 201 {"id":"...","playerId":"player-123","balance":{"amount":"100.00","currency":"BRL"},"version":1}

# GET /wallets/:walletId
curl -s http://localhost:8080/wallets/$WALLET_ID -H "Authorization: Bearer $TOKEN"

# GET /wallets/:walletId/ledger?cursor=...&limit=50
curl -s "http://localhost:8080/wallets/$WALLET_ID/ledger?limit=50" -H "Authorization: Bearer $TOKEN"

# POST /wagering/transactions — exige Idempotency-Key (provider)
curl -s -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_TOKEN" -H "Content-Type: application/json" \
  -H "Idempotency-Key: 8f14e45f-ceea-4..." \
  -d '{
    "providerId":"provider-a",
    "externalTransactionId":"ext-tx-001",
    "playerId":"player-123",
    "walletId":"'"$WALLET_ID"'",
    "roundId":"round-1",
    "gameId":"slots-1",
    "kind":"BET",
    "money":{"amount":"80.00","currency":"BRL"}
  }'
# -> 201 {"transactionId":"...","status":"PROCESSED","balance":{...},"walletVersion":2,"idempotentReplay":false}
# (ou 202 se kind for REFUND/ROLLBACK e a referência ainda não existir -> status PENDING_REFERENCE;
#  ou 200 com idempotentReplay=true no reenvio da mesma Idempotency-Key)

# GET /wagering/transactions/:transactionId
curl -s http://localhost:8080/wagering/transactions/$TX_ID -H "Authorization: Bearer $PROVIDER_TOKEN"

# GET /providers/:providerId/wagering/transactions/:externalTransactionId
curl -s http://localhost:8080/providers/provider-a/wagering/transactions/ext-tx-001 \
  -H "Authorization: Bearer $PROVIDER_TOKEN"

# POST /wallets/:walletId/reconciliation — nunca altera saldo
curl -s -X POST http://localhost:8080/wallets/$WALLET_ID/reconciliation -H "Authorization: Bearer $TOKEN"
# -> 200 {"walletId":"...","storedBalance":{...},"calculatedBalance":{...},"difference":{"amount":"0.00",...},"consistent":true,"checkedEntries":2}

# health checks (sem autenticação)
curl -s http://localhost:8080/health/live
curl -s http://localhost:8080/health/ready
```

Ver **[ARCHITECTURE.md](ARCHITECTURE.md)** para a tabela completa de mapeamento de status HTTP e
o catálogo normativo de `failureCode`.

## Testes

```bash
go test ./...                                  # unitários — nunca sobe Docker
go test -race ./...                            # idem, com o detector de race conditions
go vet ./...
gofmt -l .                                     # deve devolver vazio

go test -tags=integration ./test/integration/...        # integração — precisa de Docker (+ migrate no PATH)
go test -race -tags=integration ./test/integration/...  # idem, com -race
```

A suíte unitária cobre `internal/domain/money`, `internal/domain/wallet`,
`internal/domain/wagering` e os casos de uso em `internal/application/**` (com repositórios
fake/in-memory, nunca mocks de Postgres/SQS/IdP reais). `go build ./...`, `go vet ./...`,
`gofmt -l .`, `go test ./...` e `go test -race ./...` rodam limpos neste repositório, sem nenhum
container.

**A suíte de integração (`test/integration/`, tag de build `integration`, `testcontainers-go`)
existe e passa de ponta a ponta contra Postgres/Keycloak/LocalStack reais** (nunca mocks — ver a
regra eliminatória do edital), cobrindo, entre outros:

- `TestWalletConcurrency_TwoSimultaneousBetsOnlyOneSucceeds` — o cenário obrigatório da seção 13
  (carteira com 100.00 BRL, duas apostas de 80.00 BRL simultâneas → 1 `PROCESSED` + 1 `REJECTED`
  com `INSUFFICIENT_BALANCE`, saldo final 20.00, um único débito no ledger, reenvio idempotente
  sem alterar nada) e `TestWalletConcurrency_DifferentWalletsProceedInParallel`.
- `TestMultiProcessConcurrency_ThreeIndependentProcesses` — o mesmo cenário acima, mas com
  **três processos reais do SO** (`os/exec`, cada um com seu próprio `pgxpool.Pool`), cobrindo a
  exigência de "pelo menos três processos independentes" da seção 8.
- `TestEndToEnd_HTTPAndSQSWithAuthAndMessaging` (`e2e_infra_test.go`) — sobe Keycloak e LocalStack
  efêmeros via `testcontainers.GenericContainer`, obtém tokens `client_credentials` reais de
  `provider-a`/`provider-b`, exercita o roteador HTTP de produção (201 autenticado, 401 sem token,
  404 mascarando acesso cross-provider) e o consumidor/outbox publisher reais contra SQS real.
- `TestLedgerImmutability_UpdateAndDeleteAreRejected`, `TestInboxIdempotency_*` (3 cenários),
  `TestOutboxPublisher_TwoConcurrentPublishersNeverDuplicate`,
  `TestPendingReferenceResolver_GivesUpAfterMaxAttempts`,
  `TestConcurrencyConflicts_LockTimeoutIncrementsMetric` e
  `TestDLQWatcher_ObservesMessageAndIncrementsMetricWithoutDeleting`.

Pré-requisitos para a suíte de integração: Docker acessível (os testes sobem seus próprios
containers via `testcontainers-go`, independentes do `docker-compose.yml` de desenvolvimento) e o
binário `migrate` no `PATH` (usado para aplicar as migrations reais contra o Postgres descartável
do teste). Se Docker ou `migrate` não estiverem disponíveis, a suíte pula a si mesma com uma
mensagem clara em vez de falhar. Ver **[test/integration/README.md](test/integration/README.md)**
para o detalhe de cada teste (todos confirmados em execução real, inclusive sob `-race`).

## Observabilidade

- Logs estruturados em JSON (`log/slog`), com `correlationId`, `messageId`, `transactionId`,
  `walletId` e `providerId` quando aplicável — nunca credenciais ou o payload financeiro
  completo. Ver `internal/platform/logging/logger.go`.
- Métricas Prometheus em `GET :$METRICS_PORT/metrics` (default `9090`): transações por
  status/kind, duplicatas detectadas (replay idempotente / dedup de inbox), retries, mensagens em
  DLQ, conflitos de concorrência, atraso da outbox, latência de processamento e divergências de
  reconciliação. Ver `internal/platform/metrics/metrics.go`.

## Documentos relacionados

- **[ARCHITECTURE.md](ARCHITECTURE.md)** — decisões de arquitetura (dinheiro, transações,
  idempotência, locks/concorrência, referências pendentes, reversões, inbox/outbox, autenticação,
  autorização, uso do Fx, shutdown, limitações).
