# Arquitetura Explicada

Este documento descreve decisões de arquitetura do `backend-challenge-go`,
organizado nas seções exigidas pelo read.me do desafio. Ele descreve o que o código em
`internal/**`/`migrations/**` efetivamente implementa hoje.

O domínio (`internal/domain/**`) é independente de Fx, HTTP, SQS e de qualquer outra
biblioteca de persistência. A camada de aplicação (`internal/application/**`) depende apenas
de `internal/ports` (interfaces), nunca de um adapter concreto.

## 1. Dinheiro (Money)

`internal/domain/money.Money` (`internal/domain/money/money.go`) é um value object imutável: um
`int64` de unidades mínimas (`amountMinor`), mais um código de moeda de 3
letras. Não existem campos exportados nem setters, toda operação devolve um novo valor. 
Dinheiro nunca é representado como `float32`/`float64` em lugar nenhum do sistema.

- **Contrato externo**: `{"amount":"25.00","currency":"BRL"}`, via `MarshalJSON`/`UnmarshalJSON`.
  `NewMoneyFromString`/`UnmarshalJSON` é o único ponto de entrada para dado externo e rejeita: 
  valor vazio (`ErrEmptyAmount`), formato inválido, `Infinity`,
  notação científica, texto solto (`ErrInvalidFormat`), escala maior que 2 casas
  (`ErrScaleExceeded`) e valor negativo (`ErrNegativeNotAllowed`). A moeda é validada como 3
  letras alfabéticas (`ErrInvalidCurrency`) e normalizada para maiúsculas, o tipo não restringe a
  um registro fechado de moedas, mas sempre carrega a moeda.
- **Construtor interno** `FromMinorUnits` permite negativos, usado para reidratar estado
  persistido e para o resultado de `Sub`. Nunca para analisar entrada externa não confiável.
  Negativos só existem em diferenças/cálculos internos (ex. `difference` da reconciliação), nunca
  como saldo de carteira.
- **Operações** (`Add`, `Sub`, `Negate`, `Compare`) exigem a mesma moeda nos dois operandos.
  A divergência retorna `ErrCurrencyMismatch` e são checadas contra overflow de `int64`
  (`ErrOverflow`), inclusive na conversão string→minor-units e em
  `Negate(math.MinInt64)`.
- **Persistência**:(`BIGINT _minor`) + moeda (`CHAR(3)`), nunca tipo float.
  Ver `wallets.balance_minor`, `wager_transactions.amount_minor`,
  `wallet_ledger_entries.amount_minor/balance_before_minor/balance_after_minor` na migration.
- **Testes**: `internal/domain/money/money_test.go` cobre BRL e uma segunda moeda, comprovando que
  o tipo carrega moeda de verdade e os casos de incompatibilidade entre
  moedas.

## 2. Transações (WagerTransaction e sua máquina de estados)

`internal/domain/wagering.WagerTransaction` (`transaction.go`) carrega identidade interna
(`InternalID`) e externa (`ExternalTransactionID`, `ProviderID`, `IdempotencyKey`,
`PayloadHash`), `WalletID`/`PlayerID`/`RoundID`/`GameID`, `Kind`, `Money`, a referência opcional de
reversão, `Status`, `FailureCode`, o snapshot de resultado (`ResultBalanceMinor`/
`ResultWalletVersion`) e os timestamps. Todos os campos são privados, acessados de fora do pacote
só via método getter (`InternalID()`, `Status()`, etc.), o mesmo padrão de encapsulamento de
`internal/domain/wallet.Wallet`. Dois construtores: `NewExternalTransaction`/
`NewOpeningTransaction` para criação (validam as regras de negócio abaixo), `Rehydrate` para
reconstrução a partir de uma linha já persistida (`internal/adapters/postgres/transaction_repository.go`),
que só confere campos obrigatórios presentes e nunca reaplica validação de negócio ou reemite
eventos.

- **Tipos**: `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK` + interno `OPENING`.
  `NewExternalTransaction` rejeita `OPENING` e qualquer kind fora do vocabulário externo com
  `ErrInvalidKindForExternal`. `OPENING` só nasce via `NewOpeningTransaction`, o qual
  parâmetro nem sequer possui campos de provider/externalId/key/hash/round/game/reference, uma a
  ausência é estrutural, não apenas validada.
- **Regra de valor por tipos** (`validateAmountForKind`): `LOSS` exige `amount == 0`; `BET`, `WIN`,
  `REFUND`, `ROLLBACK` exigem `amount > 0`; `OPENING` aceita zero ou positivo, nunca negativo.
- **Máquina de estados** (`state_machine.go`, único ponto de mutação: `transitionTo`):
  `PENDING -> {PENDING_REFERENCE, PROCESSED, REJECTED, FAILED}`;
  `PENDING_REFERENCE -> {PROCESSED, REJECTED, FAILED}` os três terminais não têm nenhuma aresta de
  saída. Uma transição inválida nunca gera panic e retorna `*InvalidTransitionError`/
  `*TerminalTransactionError`, classificáveis via `errors.Is`/`errors.As`. Replay de uma transação
  terminal (`PROCESSED`/`REJECTED`/`FAILED`) **apenas lê** `ResultBalanceMinor`/
  `ResultWalletVersion`/`FailureCode` já gravados, nunca reaplica a lógica de negócio (ver §3,
  idempotência).
- **BET**: débito (`Wallet.Debit`), valor > 0, exige saldo suficiente, senão
  `FailureCodeInsufficientBalance`. **WIN**: crédito (`Wallet.Credit`) pode opcionalmente carregar
  `ReferenceExternalTransactionID` da aposta da mesma rodada como dado de auditoria. **LOSS**: nenhuma 
   movimentação
  (`commitLoss` em `process_transaction.go` nunca toca `Wallets`/`Ledger`, nunca incrementa
  `version`), publica `WagerTransactionProcessed` **sem** `WalletBalanceChanged`.
- **Erros de domínio** (`internal/domain/wagering/errors.go`,
  `internal/domain/wallet/errors.go`) são sempre tipados e encadeiam para uma sentinela via
  `Unwrap`, nunca panic para representar uma rejeição de negócio.

## 3. Idempotência

Dois níveis persistidos, sem uma tabela genérica adicional. `wager_transactions` já é, por
desenho, o próprio registro de idempotência de negócio.

- **Nível de negócio (HTTP e SQS compartilham, pois chamam o mesmo caso de uso)**: hash canônico
  SHA-256 do payload de negócio (`providerId, externalTransactionId, playerId, walletId, roundId,
  gameId, kind, money, referenceExternalTransactionId`), calculado por
  `CanonicalHash`/`CanonicalHashInput` em
  `internal/application/wagering/process_transaction.go`, **excluindo** explicitamente a
  `idempotencyKey` e metadados de transporte (`messageId`, `occurredAt`). JSON canônico = chaves
  ordenadas, obtido de graça via `encoding/json` serializando um `map[string]interface{}`.
  > Nota de posicionamento: as decisões de arquitetura descrevem esta função como
  > `internal/domain/wagering.CanonicalHash`. Ela foi implementada em
  > `internal/application/wagering` porque o conjunto de arquivos desta tarefa não incluía tocar o
  > pacote de domínio; mover para lá depois é um reposicionamento puro (seu único chamador é
  > `ProcessTransactionUseCase.Execute`), não uma mudança de comportamento.
- Fluxo: `INSERT ... ON CONFLICT (provider_id, idempotency_key) DO NOTHING RETURNING id`
  (`TransactionRepository.InsertIfAbsent`). Se inseriu, processa normalmente. Se houve conflito, o
  próprio `INSERT` bloqueou no índice único até a transação concorrente commitar/abortar. Ao
  destravar, um `SELECT` localiza o resultado já persistido: hash igual devolve esse resultado com
  `idempotentReplay=true`, usando `result_balance_minor`/`result_wallet_version`. **o saldo da
  época**, nunca o saldo atual da carteira (`toReplayResult`); hash diferente retorna
  `IdempotencyKeyConflictError` → 409 `IDEMPOTENCY_KEY_CONFLICT`.
- `UNIQUE (provider_id, external_transaction_id)` (`wt_provider_external_tx_uidx`) impede que esse
  par seja reaplicado sob uma `idempotencyKey` diferente → 409
  `EXTERNAL_TRANSACTION_ALREADY_EXISTS` (checado também pela aplicação antes do insert, via
  `FindByProviderAndExternalTransactionID`, para a mensagem de erro chegar cedo quando possível).
- **Nível de transporte (somente SQS)**: `inbox_messages`, chave única
  `(consumer_name, message_id)`. `message_id` é o do envelope SQS, distinto da `idempotencyKey`
  de negócio. `InboxStore.ReserveOrGet` insere-ou-lê na MESMA transação SQL das mudanças de
  domínio/ledger/outbox (`internal/adapters/sqs/consumer.go`, dentro de `UnitOfWork.WithinTx`).
  `MarkCompleted` grava `completed_at` ao final da mesma transação, antes do commit. Reentrega do
  mesmo `message_id` já completo é apenas confirmada (mensagem deletada) sem reprocessar. hash do
  corpo divergente do hash armazenado (`message_hash`) é reportado como `ErrInboxHashMismatch`,
  nunca reprocessado silenciosamente.
- Tudo persistido em Postgres, nunca apenas em memória, logo sobrevive a restart e funciona
  igual sob N instâncias.

## 4. Locks / Concorrência

**Lock pessimista de linha (`SELECT ... FOR UPDATE`) como mecanismo primário**, com a coluna
`version` como cinturão e suspensório e `CHECK`s no banco como última linha de defesa. Implementado
em `internal/adapters/postgres/tx_manager.go` (`TxManager.WithinTx`, que satisfaz
`ports.UnitOfWork`) + `wallet_repository.go`/`transaction_repository.go`.

Fluxo único para toda mutação de carteira, dentro de uma única transação SQL:

1. `BEGIN`.
2. `SET LOCAL lock_timeout = '3s'` (constante `lockTimeout` em `tx_manager.go`).
3. `SELECT ... FROM wallets WHERE id = $1 FOR UPDATE` (`WalletRepository.LockForUpdate`).
4. Para `REFUND`/`ROLLBACK`: também `SELECT ... FROM wager_transactions WHERE id = $referenceId
   FOR UPDATE` (`TransactionRepository.LockForUpdate`). A transação referenciada sempre pertence
   à MESMA carteira já travada, então nunca há ordenação cruzada entre carteiras diferentes e,
   portanto, nenhum risco de deadlock entre wallets distintas nesse fluxo.
5. Validação em Go via `Money`/`Wallet.Debit`/`Wallet.Credit` (mesma moeda, saldo suficiente,
   aritmética com overflow checado, regras por `kind`).
6. `UPDATE wallets SET balance_minor=$novo, version=version+1, updated_at=now() WHERE id=$1 AND
   version=$old`. O `WHERE version=$old` é redundante com o `FOR UPDATE` do passo 3 em operação
   normal. É mantido como invariante auto-verificável, `RowsAffected()==0` vira
   `*wallet.VersionConflictError`, tratado como bug interno (log de erro), nunca como sucesso
   silencioso.
7. `INSERT` no ledger, inbox (quando SQS) e outbox.

**Por que isso não viola "locks globais proibidos"**: o lock é escopado à PK de uma única linha de
`wallets` (e, em reversão, também à linha da transação referenciada, que pertence à mesma wallet).
Nunca à tabela inteira nem a um lock advisory/distribuído. Carteiras diferentes nunca disputam a
mesma linha e avançam em paralelo mesmo com N instâncias contra o mesmo Postgres.

**Proteção contra pileup/picos**: `lock_timeout` de 3s por transação, estourar vira
`ports.ErrWalletLockTimeout` (SQLSTATE `55P03`, classificado em `classifyError`), mapeado para
HTTP 503 + `Retry-After` e, no consumidor SQS, tratado como falha transitória (retry via
visibilidade encurtada, nunca DLQ só por isso — `isTransient` em `consumer.go`).
`serialization_failure`/`deadlock_detected` (SQLSTATE `40001`/`40P01`) recebem retry limitado
interno dentro de `TxManager.WithinTx` (até 3 tentativas, backoff de 10–50ms + jitter,
`randomRetryDelay`) antes de propagar `ports.ErrSerializationFailure` como transitório — cenário
raro dado o escopo de lock por linha, tratado por completude.

**Backstops no banco**, independentes do lock (defesa em profundidade):

- `wallets_balance_non_negative CHECK (balance_minor >= 0)`.
- `wle_wallet_transaction_unique UNIQUE (wallet_id, transaction_id)` em
  `wallet_ledger_entries`. Impede duplo lançamento para a mesma transação mesmo sob bug de
  aplicação.
- `wle_arithmetic_check CHECK (balance_after = balance_before ± amount conforme direção)`.
- `wt_single_successful_reversal_per_kind_uidx`. Impede duas reversões bem-sucedidas do mesmo tipo
  contra a mesma referência.

**Instrumentação de conflitos**: toda tentativa que falha contra o Postgres por
`ports.ErrWalletLockTimeout` ou `ports.ErrSerializationFailure` passa por
`TxManager.recordConflict` (`tx_manager.go`, ligado via `WithMetrics`/`provideUnitOfWork`), que
incrementa `wagering_concurrency_conflicts_total{component="postgres", reason="lock_timeout"}` ou
`{..., reason="serialization_failure"}`. Como HTTP e SQS chamam o mesmo `WithinTx`, este é o único
ponto de instrumentação necessário, sem risco de contar o mesmo conflito duas vezes. **Comprovado
em execução** por `TestConcurrencyConflicts_LockTimeoutIncrementsMetric`
(`test/integration/concurrency_conflict_metric_test.go`): segura um `FOR UPDATE` manualmente por
mais de 3s, confirma `ports.ErrWalletLockTimeout` e o contador Prometheus (via
`prometheus/testutil`) em exatamente 1.

**Evidência com processos reais do SO, não só goroutines**: o cenário obrigatório acima
(`TestWalletConcurrency_TwoSimultaneousBetsOnlyOneSucceeds`) roda com goroutines dentro de um único
processo de teste. `TestMultiProcessConcurrency_ThreeIndependentProcesses`
(`test/integration/multi_process_concurrency_test.go` +
`test/integration/testdata/concurrency_worker/main.go`) repete o mesmo resultado com **três
processos do sistema operacional lançados via `exec.Cmd`** (todos `Start()` antes de qualquer
`Wait()`), cada um com seu próprio `pgxpool.Pool` e sua própria memória: dois disputando a mesma
carteira (mesmo cenário 100.00/2×80.00 BRL → 1 `PROCESSED` + 1 `REJECTED`, saldo final 20.00, um
único débito) e um terceiro totalmente independente numa segunda carteira, provando que a
contenção na primeira carteira não vaza para a segunda entre processos distintos. Satisfaz
literalmente a exigência de "pelo menos três processos independentes, cada um com suas próprias
conexões e memória" da seção 8 do edital.

## 5. Referências pendentes (PENDING_REFERENCE)

Estado 100% persistido em colunas de `wager_transactions`
(`pending_reference_attempts`, `pending_reference_next_attempt_at`, `pending_since`). Nenhum
timer em memória, qualquer instância ao subir volta a encontrar as linhas elegíveis.

- Um `REFUND`/`ROLLBACK` cuja referência não é encontrada sincronamente
  (`process_transaction.go:enterPendingReference`) entra em `PENDING_REFERENCE`, grava
  `pending_since` (na primeira entrada) e agenda a primeira tentativa via
  `computeNextAttempt`. 
- Worker `internal/workers/pending_reference_resolver.go` (`Run`/`Stop`, ciclo de poll
  configurável que dobra o intervalo, até um teto, quando o lote vem vazio, para não gerar
  hot-loop): a cada iteração, dentro de **uma** transação (`UnitOfWork.WithinTx`),
  `TransactionRepository.LockPendingReferenceBatch` executa
  `SELECT ... WHERE status='PENDING_REFERENCE' AND pending_reference_next_attempt_at <= now()
  ORDER BY pending_reference_next_attempt_at FOR UPDATE SKIP LOCKED LIMIT N`. `SKIP LOCKED`
  permite múltiplas instâncias competindo pelo mesmo lote sem contenção nem duplicação. Se uma
  instância cai no meio, o abort da transação libera o lock e a próxima varredura (de qualquer
  instância) repega a linha.
- `ResolvePendingReferenceUseCase.Resolve` (`internal/application/wagering/resolve_pending_reference.go`)
  roda **dentro da mesma transação** que o worker já abriu (nunca abre a sua própria
  `WithinTx`): tenta resolver `(providerId, referenceExternalTransactionId)`. Achou e válida →
  reprocessa a reversão normalmente, saindo para `PROCESSED`/`REJECTED` no mesmo commit do
  lançamento de ledger quando aplicável. Não achou → `RecordPendingReferenceAttempt` incrementa
  tentativas e reagenda via `computeNextAttempt`.
- **Corte duplo**: `WagerTransaction.PendingReferenceExceeded(maxAttempts, ttl, now)`. O que
  disparar primeiro entre `attempts >= maxAttempts` OU `now - pending_since >= ttl` vence →
  `REJECTED` com `failureCode=REFERENCE_NOT_FOUND` + evento `WagerTransactionRejected`, mesmo
  commit.
- Defaults (configuráveis via `internal/config`, nunca fixos no código):
  `PENDING_REFERENCE_BACKOFF_BASE=2s`, fator `2`, `PENDING_REFERENCE_BACKOFF_MAX_INTERVAL=5m`,
  jitter `20%`, `PENDING_REFERENCE_MAX_ATTEMPTS=10`, `PENDING_REFERENCE_TTL=30m`.
- `fx.Lifecycle` (`internal/platform/fx/module_workers.go:registerPendingReferenceResolver`):
  `OnStart` sobe `go resolver.Run()` sem bloquear. `OnStop` chama `resolver.Stop(ctx)`, que sinaliza
  parada e aguarda a iteração/lote corrente commitar ou abortar, respeitando o orçamento de
  `fx.StopTimeout`, nunca mata a goroutine no meio de uma transação.

## 6. Reversões (REFUND / ROLLBACK)

`WagerTransaction.ValidateReversalAgainst` (`internal/domain/wagering/transaction.go`) é o choque
único de validação, chamado tanto no caminho síncrono (`process_transaction.go:processReversal`)
quanto no worker (`resolve_pending_reference.go:Resolve`):

- a referência deve existir e estar `PROCESSED` (terminal), senão `ErrReferenceNotTerminal`;
- `REFUND` só pode referenciar um `BET`; `ROLLBACK` pode referenciar `BET`, `WIN` ou `REFUND`,
  senão `ErrReferenceMismatch`.
- provider, player, wallet, moeda e rodada devem concordar entre operação e referência, senão
  `ErrReferenceMismatch`.
- o valor da reversão deve ser **exatamente igual** ao valor referenciado (`Money.Equal`, sem
  parciais), senão `ErrReferenceAmountMismatch`.

"Já reversado" é checado separadamente, pois exige olhar todas as transações (não só as duas em
memória): `TransactionRepository.HasSuccessfulReversal(providerId, referenceExternalTransactionId,
kind)`, reforçado no banco pelo índice parcial único
`wt_single_successful_reversal_per_kind_uidx` (`status='PROCESSED' AND kind IN
('REFUND','ROLLBACK')`). Uma referência nunca recebe duas reversões bem-sucedidas do **mesmo**
tipo.

`applyReversal` decide a direção do movimento a partir do `kind` da transação referenciada: se a
referência foi `WIN` ou `REFUND` (um crédito), reverter é um débito. Se foi `BET` (um débito),
reverter é um crédito. Um `ROLLBACK`/`REFUND` que debitaria mais que o saldo disponível é
rejeitado com `FailureCodeInsufficientBalanceForReversal`. **código distinto** do usado para
aposta sem saldo (`FailureCodeInsufficientBalance`).

## 7. Inbox / Outbox

**Inbox** (`inbox_messages`, `internal/adapters/postgres/inbox_repository.go` +
`ports.InboxStore`): dedup de transporte por `(consumer_name, message_id)`. `ReserveOrGet`
insere-ou-lê. `MarkCompleted` grava a conclusão. Ambos chamados de dentro da mesma transação SQL
das mudanças de domínio. Redelivery de um `message_id` já completo: apenas confirma
(deleta da fila), nunca reprocessa. Hash de corpo divergente: `ErrInboxHashMismatch`.

**Outbox** (`outbox_events`, `internal/adapters/postgres/outbox_repository.go` +
`ports.OutboxStore`): todo evento de domínio é inserido na MESMA transação SQL da mudança que o
originou (`persistence.enqueueEvents` em `process_transaction.go`, chamado a partir de
`commitMovement`/`commitLoss`/`rejectAndReturn`/`enterPendingReference`, e também em
`open_wallet.go`). "publicação só após commit da transação de origem" é garantido por
construção, nunca por um passo manual separado. `event_id` é gerado uma única vez na criação da
linha e preservado em toda republicação.

Worker `internal/workers/outbox_publisher.go` (mesmo padrão de poll +
`SELECT ... FOR UPDATE SKIP LOCKED LIMIT N ORDER BY occurred_at` via
`OutboxStore.LockPendingBatch`, dentro de uma transação): publica cada evento via
`ports.EventPublisher` (`internal/adapters/sqs/outbound_publisher.go`, reaproveitando o MESMO
client/config SQS apontado ao LocalStack — segunda fila FIFO dedicada `wager-events.fifo` +
`wager-events-dlq.fifo`, nenhuma tecnologia de mensageria nova). `MessageGroupId = aggregate_id`
(ordena por carteira/transação). `MessageDeduplicationId = event_id` (preservado em toda
republicação). Sucesso → `MarkPublished` (`UPDATE ... WHERE published_at IS NULL`, idempotente).
falha → `RecordFailedAttempt` com o **mesmo** backoff exponencial com jitter do resolver de
pending-reference (`computeNextAttempt`, deliberadamente a mesma função/config em ambos os
workers, uma política de retry só no projeto). A outbox não tem corte por TTL/max-attempts:
eventos devem eventualmente ser publicados, nunca "desistir" — `attempts` alto é só observabilidade
(métrica, não decisão de negócio).

**Tradeoff assumido e documentado aqui**: se o processo cair exatamente entre um `SendMessage`
bem-sucedido e o `UPDATE` que marca `published_at`, o evento é reenviado na próxima varredura,
entrega at-least-once para o consumidor downstream, mitigada pela dedup nativa de 5 minutos do SQS
FIFO via `MessageDeduplicationId=event_id`. Comportamento padrão aceito do padrão Outbox, não um
defeito.

**DLQWatcher (observabilidade de dead-letter, não um componente de negócio)**:
`internal/adapters/sqs/dlq_watcher.go` fecha a métrica `DLQMessages`, que antes existia só como
definição sem nenhum produtor. É um observador periódico e **não-destrutivo** das duas filas de
dead-letter (`wager-transactions-dlq.fifo`, `wager-events-dlq.fifo`, quando as respectivas URLs
estão configuradas): a cada `pollInterval` (default 30s) faz `ReceiveMessage` com uma
`VisibilityTimeout` curta (5s), incrementa `DLQMessages{queue=...}` pelo tamanho do lote observado
e imediatamente devolve a visibilidade de cada mensagem (`ChangeMessageVisibility(0)`) — nunca
deleta nem reprocessa o conteúdo, a mensagem segue disponível para inspeção ou redrive manual.
Registrado em `internal/platform/fx/module_workers.go:registerDLQWatcher`, seguindo exatamente o
mesmo padrão `OnStart: go watcher.Run()` / `OnStop: watcher.Stop(ctx)` dos demais workers (ver
§10). O roteamento efetivo de uma mensagem para a DLQ continua sendo a `RedrivePolicy` nativa da
fila (SQS/LocalStack, `maxReceiveCount` configurado em `deployments/localstack/init-queues.sh`); o
watcher não participa dessa decisão, só observa o resultado depois de ocorrido. **Comprovado em
execução** por `TestDLQWatcher_ObservesMessageAndIncrementsMetricWithoutDeleting`
(`test/integration/dlq_watcher_test.go`, LocalStack efêmero real): publica uma mensagem direto na
fila de DLQ, confirma a métrica em ≥1 e confirma que a mensagem continua na fila depois de
observada.

## 8. Autenticação

OAuth2/OIDC via Keycloak, `client_credentials` entre serviços.
`internal/platform/fx/module_auth.go` constrói o único `ports.TokenValidator` real do projeto
(`oidcTokenValidator`, baseado em `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2`): o
restante do código (`internal/adapters/http/middleware/auth.go`) depende só da interface
`ports.TokenValidator`, nunca de um tipo concreto do go-oidc.

- `OnStart` (passo de "init com validação de config/dependências") faz a descoberta OIDC
  (`oidc.NewProvider` contra `OIDC_ISSUER_URL/.well-known/openid-configuration`) e constrói o
  `*oidc.IDTokenVerifier`, um Keycloak inalcançável/mal configurado falha `app.Start` rápido, antes
  do servidor HTTP aceitar qualquer conexão.
- `ValidateToken` verifica assinatura/emissor/expiração (`verifier.Verify`) e deriva
  `ports.Claims{Subject, ProviderID, Roles}` das claims cruas: `sub` → `Subject`.
  `azp` (com fallback para `client_id`) identifica o client OAuth2 que obteve o token. O token é
  tratado como identidade interna se carregar a role de realm `internal-service` OU se seu
  `azp`/`client_id` for igual a `OIDC_CLIENT_ID` (o próprio client desta aplicação), caso
  contrário, com `azp`/`client_id` não vazio, é tratado como identidade de provider, com
  `ProviderID` tomado de uma claim custom opcional `provider_id` (snake_case, como o realm a
  emite) ou, na ausência dela, do próprio `azp`/`client_id`.
- Um token ausente, malformado, expirado ou com assinatura/issuer inválidos nunca chega a um
  handler: `middleware.Authenticate` intercepta e responde 401 antes disso.

> **Nota de implementação (corrigida)**: uma versão anterior deste documento descrevia um
> descompasso entre o nome da claim emitida pelo realm (`provider_id`, snake_case, via protocol
> mapper em `provider-a`/`provider-b`) e o nome procurado por `claimsFromRaw` em `module_auth.go`
> (`providerId`, camelCase) — isso já foi corrigido no código. `claimsFromRaw` hoje lê
> `stringClaim(raw, "provider_id")` diretamente, batendo com o que o realm emite. O fallback para
> `clientID` (`azp`) continua existindo como segunda linha de defesa para um client sem esse
> protocol mapper, mas deixou de ser o único caminho funcional. O mecanismo é comprovado em
> execução real (não só por leitura de código) por
> `TestEndToEnd_HTTPAndSQSWithAuthAndMessaging` (`test/integration/e2e_infra_test.go`): dois tokens
> `client_credentials` reais de `provider-a`/`provider-b`, obtidos de um Keycloak efêmero real via
> `testcontainers`, validados pelo `ports.TokenValidator` de produção — a leitura correta de
> `provider_id` é o que torna possível o isolamento cross-provider comprovado no mesmo teste (ver
> §9).

## 9. Autorização

Duas checagens distintas, ambas em `internal/adapters/http/middleware/auth.go`:

- **`RequireInternalService`** — montado só no sub-router `/wallets` (`router.go`): rejeita com
  403 qualquer chamador cujas `Claims` não carreguem a role `internal-service`. Implementa
  "operações de carteira restritas ao serviço interno", um provider nunca alcança um handler de
  administração de carteira, independente do que colocar no corpo da requisição.
- **`AuthorizeProvider(claims, providerID)`** — chamado inline pelos handlers de wagering
  (`wagering_handler.go`): a identidade interna está isenta (pode agir em nome de qualquer
  provider); uma identidade de provider só passa se `claims.ProviderID == providerID`. A
  identidade autenticada determina o `providerId` autorizado, "um provedor só acessa suas
  próprias transações, inclusive em replay".
- **Mascaramento em leitura**: `GetByID`/`GetByProviderAndExternalID` mapeiam uma falha de
  `AuthorizeProvider` para **404**, nunca 403, um provider nunca consegue distinguir "não é meu"
  de "não existe" olhando o status HTTP. Já em escrita (`POST /wagering/transactions` com um
  `providerId` de outro dono no corpo), a mesma falha vira **403** — não há recurso a mascarar, é
  uma tentativa de escrita fora de escopo.

## 10. Uso do Fx

`cmd/api/main.go` monta o grafo com `fx.New(fx.Supply(cfg), fx.StopTimeout(cfg.HTTP.ShutdownTimeout),
fxplatform.Module)` e dirige `app.Start(ctx)`/`app.Stop(stopCtx)` explicitamente ao redor de
`signal.NotifyContext(SIGINT, SIGTERM)`, nunca o `app.Run()` implícito do Fx, logando em `slog`
cada fase (`starting`/`started`/`stopping`/`stopped`).

`internal/platform/fx/module_app.go` apenas agrega módulos via `fx.Options` (`ConfigModule`,
`LoggingModule`, `DBModule`, `SQSModule`, `AuthModule`, `RepositoriesModule`, `UseCasesModule`,
`HTTPModule`, `WorkersModule`) + `fx.WithLogger` — **nunca registra hooks de lifecycle próprios**.
Cada `OnStart`/`OnStop` vive dentro do construtor (ou `fx.Invoke`) do próprio recurso que o possui:

| Recurso | Arquivo | OnStart | OnStop |
|---|---|---|---|
| Pool Postgres | `module_db.go` | `pool.Ping` síncrono | `pool.Close()` |
| Client SQS | `module_sqs.go` | `GetQueueAttributes` síncrono | — (sem estado a liberar) |
| Validador OIDC | `module_auth.go` | descoberta OIDC | — |
| Servidor HTTP da API | `module_http.go` | `net.Listen` síncrono + `Serve` em goroutine | `server.Shutdown(ctx)` |
| Servidor de métricas | `module_http.go` | idem, porta separada | `server.Shutdown(ctx)` |
| Consumidor SQS | `module_workers.go` | `go consumer.Run()` | `consumer.Stop(ctx)` |
| Outbox publisher | `module_workers.go` | `go publisher.Run()` | `publisher.Stop(ctx)` |
| Pending-reference resolver | `module_workers.go` | `go resolver.Run()` | `resolver.Stop(ctx)` |
| DLQ watcher | `module_workers.go` | `go watcher.Run()` | `watcher.Stop(ctx)` |

Como o Fx registra `OnStart` na ordem de construção (que segue o grafo de dependências em
profundidade) e roda `OnStop` na ordem **exatamente reversa**, e como todo repositório/caso de
uso/handler depende transitivamente do pool Postgres e/ou do client SQS, o resultado sem
nenhuma orquestração manual é: pool e client SQS sobem primeiro e só fecham por último. Servidor
HTTP, consumidor e workers sobem depois e param primeiro. Isso satisfaz "fechar dependências só
depois dos componentes que as usam" por construção.

## 11. Shutdown

`SHUTDOWN_TIMEOUT` (default 25s) vira `fx.StopTimeout`, com margem antes de um SIGKILL típico de
orquestrador em 30s.

- **HTTP** (API e métricas): `server.Shutdown(ctx)` para de aceitar novas conexões, aguarda as
  em andamento dentro do prazo.
- **Workers** (`outbox_publisher.go`, `pending_reference_resolver.go`): `Stop(ctx)` sinaliza o
  loop para sair e aguarda a iteração corrente (lote `FOR UPDATE SKIP LOCKED` já aberto) commitar
  ou abortar, nunca interrompida no meio de uma transação.
- **Consumidor SQS** (`internal/adapters/sqs/consumer.go`): o loop de
  `ReceiveMessage` para IMEDIATAMENTE ao sinal de stop, sem novo long-poll (`serveCtx`).
  Mensagens em processamento são rastreadas por `sync.WaitGroup`; O contexto de shutdown
  (`workCtx`) só é cancelado se o orçamento do `fx.StopTimeout` se esgota antes de todo handler em
  voo retornar sozinho, nesse caso a transação pgx em andamento é cancelada e sofre rollback
  automático. Adicionalmente, mensagens já recebidas mas cujo processamento ainda não começou quando
  o prazo se esgota são liberadas
  explicitamente via `ChangeMessageVisibility(0)` (`releaseVisibility`), em vez de depender só do
  visibility timeout natural (~30s) expirar sozinho.
- **Postgres**: `pool.Close()` só roda depois que HTTP, consumidor e workers já pararam.

## 12. Limitações e trabalho não concluído

Registrado aqui para que nenhuma decisão fique só na cabeça de quem escreveu o código:

- **`lock_timeout` não é configurável via env var**: é a constante `lockTimeout = "3s"` em
  `internal/adapters/postgres/tx_manager.go`, não um valor lido de `internal/config`. Não existe
  `WALLET_LOCK_TIMEOUT` em `internal/config/config.go` hoje (ver a nota equivalente no `README.md`).
  Plugar esse valor à config é trabalho futuro, não uma mudança de
  comportamento esperada no curto prazo.
- **Sem teste unitário isolado para `classifyError`/mapeamento de SQLSTATE**: a tradução de
  `55P03`/`40001`/`40P01`/`23505` para as sentinelas de `ports.*` (`internal/adapters/postgres/tx_manager.go`)
  é coberta indiretamente pelos testes de integração (que de fato provocam os SQLSTATEs reais contra
  um Postgres real) e pelos testes de aplicação com fakes (que simulam o erro já classificado), mas
  não existe um `*_test.go` dedicado e isolado para essa função.
- **Outbox sem corte por TTL/max-attempts**: diferente do resolver de referências pendentes
  (que expira para `REJECTED` após `maxAttempts`/`ttl`), um evento de outbox nunca é descartado —
  `attempts` alto é só sinal de observabilidade. Decisão deliberada, não uma lacuna.
- **DLQWatcher é só observador**: incrementa `DLQMessages` e devolve a visibilidade da mensagem,
  mas não republica, não move a mensagem de volta para a fila principal nem aplica nenhuma política
  de redrive — isso continua sendo responsabilidade da `RedrivePolicy` nativa da fila SQS/LocalStack
  (ver §7). Uma reentrega automática de mensagens presas em DLQ, se desejada, é trabalho futuro.
- **Sem rate limiting/payload size limit explícito** nos handlers HTTP além do que o roteador
  `chi` e o `net/http` padrão já oferecem — não exigido textualmente pelo edital, citado aqui por
  completude.