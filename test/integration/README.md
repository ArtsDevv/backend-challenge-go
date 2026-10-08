# Testes de integração

Esta pasta contém os testes que exercitam dependências reais (Postgres, LocalStack e Keycloak) em
vez de mocks/fakes — ver a regra eliminatória do edital:
"substituir Postgres/SQS/IdP por mocks nos testes de integração" nunca é aceitável aqui. Os
testes unitários do projeto (em `internal/...`), que usam fakes das interfaces de
`internal/ports`, continuam vivendo ao lado do código que testam, como de costume em Go.

## Build tag

Todo arquivo `_test.go` nesta pasta carrega a tag `integration`:

```go
//go:build integration
```

Isso garante que `go test ./...` (o alvo `make test`) **nunca** sobe um container Docker nem
depende de nada externo — só os testes unitários rodam. Os testes de integração só executam
quando a tag é passada explicitamente:

```bash
go test -tags=integration ./test/integration/...
```

## Abordagem

### Postgres — testcontainers-go + golang-migrate

- `wallet_concurrency_test.go` sobe **um único** container `postgres:16-alpine` via
  `github.com/testcontainers/testcontainers-go/modules/postgres` (`TestMain`), compartilhado por
  todos os testes do pacote, em vez de um container por teste — evita pagar o custo de subida do
  container repetidamente.
- O schema é aplicado contra esse container exatamente como em qualquer ambiente real: chamando o
  binário externo `migrate` (golang-migrate) — `migrate -database <url-do-container> -path
  migrations up` — nunca reimplementando a lógica de migração em Go nem importando
  `golang-migrate` como biblioteca (o projeto fixou `golang-migrate` como ferramenta de CLI
  externa; ver `ARCHITECTURE.md`/`Makefile`, alvo `migrate-up`). Isso também significa que
  qualquer migration nova em `/migrations` é automaticamente coberta por este teste, sem precisar
  tocar neste arquivo.
- Se o binário `migrate` não estiver no `PATH`, ou o Docker não estiver disponível/acessível,
  `TestMain` **pula toda a suíte** com uma mensagem clara em vez de falhar — rodar
  `go test -tags=integration ./...` em uma máquina sem Docker não deve parecer uma regressão de
  código.
- A partir do container já migrado, os testes usam os adapters reais do projeto
  (`internal/adapters/postgres`) e os casos de uso reais (`internal/application/...`) — nunca uma
  reimplementação paralela da lógica de negócio só para teste.

### LocalStack e Keycloak — testcontainers efêmeros, por teste

`TestEndToEnd_HTTPAndSQSWithAuthAndMessaging` (em `wallet_concurrency_test.go`, com as funções de
apoio em `e2e_infra_test.go`) sobe seus **próprios** containers Keycloak e LocalStack via
`testcontainers.GenericContainer` — efêmeros, isolados dos demais testes do pacote e do
`docker-compose.yml` de desenvolvimento — em vez de reutilizar um ambiente compartilhado. O
Keycloak importa o mesmo `deployments/keycloak/realm-export.json` usado em desenvolvimento; o
LocalStack tem as duas filas FIFO (`wager-transactions.fifo`/`wager-events.fifo`) criadas via
chamadas diretas da AWS SDK (`CreateQueue`), sem depender de
`deployments/localstack/init-queues.sh`. Nenhuma dessas dependências externas é mockada — a mesma
regra eliminatória citada acima também se aplica aqui.

Continua valendo subir `docker compose -f deployments/docker-compose.yml up -d localstack keycloak`
para rodar a aplicação localmente (`make run`, Postman, etc.); os testes desta pasta nunca dependem
desse compose, nem o reutilizam.

## Pré-requisitos para rodar `wallet_concurrency_test.go`

1. Docker (ou outro runtime compatível com testcontainers-go, ex. Docker Desktop/Colima) em
   execução e acessível pelo usuário atual.
2. O binário `migrate` (golang-migrate) no `PATH`. Instalação:
   ```bash
   go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
   ```
   (ou via um dos binários pré-compilados do projeto golang-migrate — ver seu README oficial.)
3. Go 1.23+ com os módulos do projeto resolvidos (`go mod tidy` já rodado ao menos uma vez).

## Rodando

```bash
go test -tags=integration ./test/integration/...
```

Para rodar com mais verbosidade (útil para acompanhar a subida do container):

```bash
go test -tags=integration -v ./test/integration/...
```

## O que `wallet_concurrency_test.go` cobre

- **Cenário obrigatório do edital**: uma carteira aberta com 100.00 BRL recebe, simultaneamente
  (duas goroutines liberadas pela mesma barreira), duas apostas (`BET`) de 80.00 BRL cada. O teste
  afirma:
  - exatamente uma delas termina `PROCESSED` e a outra `REJECTED` com `failureCode`
    `INSUFFICIENT_BALANCE` — nenhuma das duas retorna um `error` Go (uma rejeição de negócio nunca
    é pânico/erro de transporte, por desenho);
  - o saldo final da carteira é exatamente 20.00 BRL, com `version` incrementada uma única vez;
    nunca uma corrida silenciosa (`FOR UPDATE` serializa a segunda tentativa atrás do commit da
    primeira, de forma determinística, não aleatória);
  - existe exatamente **um** lançamento de débito no ledger (`wallet_ledger_entries`), nunca dois;
  - reenviar qualquer uma das duas requisições (mesma `idempotencyKey` + mesmo payload) depois do
    resultado assentado devolve `idempotentReplay=true` com o resultado já persistido, sem mutar o
    saldo nem o ledger novamente — cobre "reenvios não alteram o resultado".
- **Carteiras diferentes avançam em paralelo**: várias carteiras distintas processam apostas
  concorrentes entre si sem qualquer contenção global, demonstrando que o lock é escopado por
  linha (`wallets.id`), nunca à tabela inteira — consistente com a proibição de locks globais.
- **`TestEndToEnd_HTTPAndSQSWithAuthAndMessaging`**: fluxo HTTP+SQS ponta a ponta contra Keycloak e
  LocalStack efêmeros reais (ver a seção acima). Abre uma carteira e processa um `BET` por dois
  caminhos de transporte distintos, nunca reimplementando lógica de produção: via HTTP
  (`httptest.NewServer` sobre o `chi.Router` real, com um token OAuth2 `client_credentials` real do
  client `provider-a`) e via SQS (uma mensagem real publicada em `wager-transactions.fifo` e
  consumida pelo `sqsadapter.Consumer` real). Confirma 201/`PROCESSED` no caminho HTTP, 401 sem
  `Authorization`, e 404 (not-found mascarando cross-provider) quando `provider-b` tenta ler a
  transação de `provider-a` — via `ports.TokenValidator` real (`fxplatform.AuthModule`, descoberta
  OIDC real contra o Keycloak efêmero). Depois, confirma que o `workers.OutboxPublisher` real
  publica os eventos `WagerTransactionProcessed`/`WalletBalanceChanged` das duas apostas em
  `wager-events.fifo`, lidos de volta via `ReceiveMessage` real.

## O que os demais arquivos cobrem

Todos compartilham o mesmo container Postgres e o mesmo `TestMain` de
`wallet_concurrency_test.go` (mesma tag `integration`, mesmo `skipIfUnavailable(t)` como primeira
linha de cada teste).

- **`ledger_immutability_test.go`** (`TestLedgerImmutability_UpdateAndDeleteAreRejected`): abre
  uma carteira (gerando um lançamento `CREDIT` em `wallet_ledger_entries`) e tenta, via SQL cru
  (`pool.Exec`, fora de qualquer repositório), um `UPDATE` e um `DELETE` diretos sobre essa linha.
  Confirma que o trigger `forbid_ledger_mutation` do banco rejeita as duas operações com uma
  mensagem contendo "append-only" (SQLSTATE `23000`) e que o lançamento, relido em seguida,
  permanece byte a byte igual ao original — o ledger é append-only de verdade, não só por
  convenção na camada de aplicação.
- **`inbox_idempotency_test.go`** (três testes): exercita `ports.InboxStore.ReserveOrGet` contra o
  Postgres real. Cobre: (1) a mesma `(consumerName, messageID)` reaparecendo com o **mesmo** hash
  de conteúdo é idempotente (`reserved=false`, sem erro, registro existente devolvido); (2)
  reaparecer com um hash **diferente** é rejeitado com `ports.ErrInboxHashMismatch` — uma
  redelivery anômala da fila nunca é silenciosamente reprocessada; (3) o mesmo `messageID` sob
  dois `consumerName` distintos nunca colide, já que a dedupe é por par `(consumerName,
  messageID)`. Os hashes de teste são SHA-256 hex reais (64 caracteres) para respeitar o tipo
  `CHAR(64)` da coluna `message_hash` sem sofrer blank-padding do Postgres.
- **`outbox_publisher_dual_test.go`** (`TestOutboxPublisher_TwoConcurrentPublishersNeverDuplicate`):
  enfileira 24 eventos no outbox e sobe **duas** instâncias concorrentes de
  `workers.OutboxPublisher` apontando para um `ports.EventPublisher` fake que registra cada
  publicação. Confirma que o `FOR UPDATE SKIP LOCKED` de `LockPendingBatch` particiona o trabalho
  corretamente entre os dois publishers concorrentes: nenhum `MessageDeduplicationID` dos 24
  eventos do teste é publicado mais de uma vez, e todos os 24 são publicados exatamente uma vez
  dentro do prazo. A checagem é escopada aos IDs do próprio teste (em vez do total publicado pelo
  publisher fake) porque o container Postgres é compartilhado com os demais testes do pacote, que
  também enfileiram seus próprios eventos de outbox (ex.: abertura de carteira) nessa mesma tabela.
- **`pending_reference_resolver_test.go`**
  (`TestPendingReferenceResolver_GivesUpAfterMaxAttempts`): processa um `ROLLBACK` cuja
  `ReferenceExternalTransactionID` nunca existe, o que deixa a transação em
  `PENDING_REFERENCE`. Sobe um `workers.PendingReferenceResolver` real com uma config de
  backoff/`MaxAttempts` rápida e determinística, e confirma que, depois de esgotadas as
  tentativas, a transação é marcada `REJECTED` com `failureCode` `REFERENCE_NOT_FOUND` — e que o
  saldo da carteira permanece inalterado durante todo o processo, já que uma referência nunca
  resolvida não pode mover dinheiro.
- **`multi_process_concurrency_test.go`**
  (`TestMultiProcessConcurrency_ThreeIndependentProcesses`): cobre a exigência do edital (seção 8,
  "as garantias devem ser demonstradas com pelo menos três processos independentes, cada um com
  suas próprias conexões e memória"; seção 13, item 4, "repita cenários relevantes com pelo menos
  três instâncias independentes") de um jeito que `TestWalletConcurrency_TwoSimultaneousBetsOnlyOneSucceeds`
  não cobre: aquele teste usa duas goroutines dentro do mesmo processo de teste, o que prova a
  correção da serialização por linha mas não isolamento real entre processos do sistema
  operacional. Este teste compila, uma única vez por execução, o binário auxiliar
  `testdata/concurrency_worker` (um `package main` standalone, com seu próprio `pgxpool.Pool`
  independente e sua própria instância dos adapters/caso de uso reais — nunca o pool nem os
  repositórios do processo de teste) e sobe três processos reais do SO via `os/exec` apontando
  para o mesmo Postgres efêmero do pacote: dois competindo pela mesma carteira recém-aberta com
  100.00 BRL (duas apostas de 80.00 BRL cada) e um terceiro operando sobre uma carteira
  independente, aberta com 50.00 BRL (aposta de 10.00 BRL), para também demonstrar isolamento
  entre carteiras com processos de verdade. Os três `exec.Cmd` são iniciados (`Start`) antes de
  qualquer `Wait`, garantindo que os três PIDs — cada um com sua própria memória e suas próprias
  conexões TCP ao Postgres — estejam de fato concorrentes em algum momento, não apenas
  sequenciais. Cada processo imprime um JSON de resultado em `stdout`, que o teste decodifica para
  afirmar: exatamente uma das duas apostas da carteira A termina `PROCESSED` (saldo 20.00 BRL,
  `version` 2) e a outra `REJECTED` com `failureCode` `INSUFFICIENT_BALANCE`; o saldo e a versão
  lidos diretamente do banco ao final batem com o resultado do processo vencedor, com exatamente
  um lançamento de débito no ledger; e o processo da carteira B termina `PROCESSED` com saldo
  40.00 BRL, provando que a contenção na carteira A não vazou para um processo do SO totalmente
  separado rodando ao mesmo tempo. O pacote `testdata/concurrency_worker` nunca entra em
  `go build ./...`, `go vet ./...` nem `go test ./...` — diretórios chamados `testdata` são
  ignorados pela ferramenta `go` por convenção — então esse binário auxiliar só é compilado quando
  este teste roda explicitamente.
- **`concurrency_conflict_metric_test.go`**
  (`TestConcurrencyConflicts_LockTimeoutIncrementsMetric`): fecha o gap entre a métrica
  `wagering_concurrency_conflicts_total` (`internal/platform/metrics`) já estar declarada e
  nenhum ponto do código realmente incrementá-la. Abre uma carteira, trava a linha correspondente
  com um `SELECT ... FOR UPDATE` feito via uma conexão/transação crua separada (fora de qualquer
  repositório) e, em paralelo, chama `pgadapter.NewTxManager(pool, pgadapter.WithMetrics(m))` para
  tentar um `LockForUpdate` sobre a mesma carteira com um `lock_timeout` curto. Confirma que o
  erro devolvido é `ports.ErrWalletLockTimeout` e que o contador
  `ConcurrencyConflicts{component="postgres",reason="lock_timeout"}` do `*metrics.Metrics` isolado
  do teste (um `prometheus.NewRegistry()` próprio, nunca o registry global) sobe para exatamente
  1 — provando que `TxManager.WithinTx`, caminho único compartilhado pelo HTTP e pelo SQS, agora
  instrumenta um conflito real de concorrência no Postgres.
- **`dlq_watcher_test.go`**
  (`TestDLQWatcher_ObservesMessageAndIncrementsMetricWithoutDeleting`): fecha o gap da métrica
  `wagering_dlq_messages_total`, cobrindo o `sqsadapter.DLQWatcher` novo. Sobe um LocalStack
  efêmero próprio, cria uma fila FIFO de teste e publica uma única mensagem nela. Sobe o watcher
  real com `WithPollInterval`/`WithWaitTimeSeconds` reduzidos (poll de 50ms, long-poll de 1s) só
  para o teste não depender dos defaults de produção (30s/10s) para terminar rápido, e aguarda,
  via `require.Eventually`, o contador `DLQMessages{queue="test-dlq.fifo"}` chegar a pelo menos 1.
  Em seguida confirma o ponto central do design do watcher — ele só observa, nunca consome: depois
  de aguardar a mensagem passar do `VisibilityTimeout` de observação, um `ReceiveMessage` direto
  contra a mesma fila ainda enxerga a mensagem, provando que o watcher a devolveu à fila
  (`ChangeMessageVisibility` com `VisibilityTimeout: 0`) em vez de `DeleteMessage`-la.
