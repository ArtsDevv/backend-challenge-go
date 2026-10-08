CREATE TABLE wallets (
    id            UUID PRIMARY KEY,
    player_id     TEXT NOT NULL,
    currency      CHAR(3) NOT NULL,
    balance_minor BIGINT NOT NULL,
    version       BIGINT NOT NULL DEFAULT 1,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT wallets_balance_non_negative CHECK (balance_minor >= 0),
    CONSTRAINT wallets_version_positive CHECK (version >= 1),
    CONSTRAINT wallets_player_currency_unique UNIQUE (player_id, currency)
);

CREATE INDEX wallets_player_id_idx ON wallets (player_id);

COMMENT ON TABLE wallets IS
    'Aggregate root for a player wallet. balance_minor is authoritative and must never go negative; version is incremented only when balance_minor changes, for optimistic-read / lost-update detection on top of the FOR UPDATE row lock.';
COMMENT ON COLUMN wallets.balance_minor IS
    'Current balance in minimal currency units (e.g. cents). Never FLOAT.';
COMMENT ON COLUMN wallets.version IS
    'Starts at 1 (set by the OPENING transaction), increments by exactly 1 on every balance-changing commit.';

CREATE TABLE wager_transactions (
    id                                  UUID PRIMARY KEY,
    external_transaction_id             TEXT,
    provider_id                         TEXT,
    idempotency_key                     TEXT,
    payload_hash                        CHAR(64),
    wallet_id                           UUID NOT NULL REFERENCES wallets (id),
    player_id                           TEXT NOT NULL,
    round_id                            TEXT,
    game_id                             TEXT,
    kind                                TEXT NOT NULL,
    amount_minor                        BIGINT NOT NULL,
    currency                            CHAR(3) NOT NULL,
    reference_external_transaction_id   TEXT,
    reference_transaction_id            UUID REFERENCES wager_transactions (id),
    status                              TEXT NOT NULL,
    failure_code                        TEXT,
    result_balance_minor                BIGINT,
    result_wallet_version               BIGINT,
    pending_reference_attempts          INT NOT NULL DEFAULT 0,
    pending_reference_next_attempt_at   TIMESTAMPTZ,
    pending_since                       TIMESTAMPTZ,
    created_at                          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                          TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at                        TIMESTAMPTZ,

    CONSTRAINT wt_kind_check CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    CONSTRAINT wt_status_check CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    CONSTRAINT wt_amount_non_negative CHECK (amount_minor >= 0),

    CONSTRAINT wt_opening_has_no_external_fields CHECK (
        kind <> 'OPENING' OR (
            provider_id IS NULL AND external_transaction_id IS NULL AND
            idempotency_key IS NULL AND payload_hash IS NULL AND
            round_id IS NULL AND game_id IS NULL AND
            reference_external_transaction_id IS NULL
        )
    ),
    CONSTRAINT wt_external_requires_identity CHECK (
        kind = 'OPENING' OR (
            provider_id IS NOT NULL AND external_transaction_id IS NOT NULL AND
            idempotency_key IS NOT NULL AND payload_hash IS NOT NULL
        )
    ),
    CONSTRAINT wt_reversal_requires_reference CHECK (
        kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_transaction_id IS NOT NULL
    )
);

CREATE UNIQUE INDEX wt_provider_idempotency_key_uidx
    ON wager_transactions (provider_id, idempotency_key);

CREATE UNIQUE INDEX wt_provider_external_tx_uidx
    ON wager_transactions (provider_id, external_transaction_id);

CREATE UNIQUE INDEX wt_single_opening_per_wallet_uidx
    ON wager_transactions (wallet_id) WHERE kind = 'OPENING';

CREATE UNIQUE INDEX wt_single_successful_reversal_per_kind_uidx
    ON wager_transactions (provider_id, reference_external_transaction_id, kind)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');

CREATE INDEX wt_wallet_id_idx ON wager_transactions (wallet_id);

CREATE INDEX wt_pending_reference_poll_idx
    ON wager_transactions (pending_reference_next_attempt_at)
    WHERE status = 'PENDING_REFERENCE';

COMMENT ON TABLE wager_transactions IS
    'A wagering operation (external BET/WIN/LOSS/REFUND/ROLLBACK, or internal OPENING) and its state machine: PENDING -> (PENDING_REFERENCE | PROCESSED | REJECTED | FAILED). PROCESSED/REJECTED/FAILED are terminal and immutable; replay of a terminal row only reads result_balance_minor/result_wallet_version, it never recomputes them.';
COMMENT ON COLUMN wager_transactions.payload_hash IS
    'SHA-256 (hex) of the canonical JSON business payload, excluding idempotency_key and transport metadata (messageId, occurredAt). Used to detect idempotency-key reuse with a different payload (409 IDEMPOTENCY_KEY_CONFLICT).';
COMMENT ON COLUMN wager_transactions.result_balance_minor IS
    'Wallet balance snapshot at the moment this transaction reached a terminal state. Written once; what idempotent replay returns instead of the current wallet balance.';
COMMENT ON COLUMN wager_transactions.result_wallet_version IS
    'Wallet version snapshot at the moment this transaction reached a terminal state (see result_balance_minor).';


CREATE TABLE wallet_ledger_entries (
    id                   UUID PRIMARY KEY,
    wallet_id            UUID NOT NULL REFERENCES wallets (id),
    transaction_id       UUID NOT NULL REFERENCES wager_transactions (id),
    direction            TEXT NOT NULL,
    amount_minor         BIGINT NOT NULL,
    currency             CHAR(3) NOT NULL,
    balance_before_minor BIGINT NOT NULL,
    balance_after_minor  BIGINT NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),


    CONSTRAINT wle_direction_check CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT wle_amount_positive CHECK (amount_minor > 0),
    CONSTRAINT wle_balances_non_negative CHECK (balance_before_minor >= 0 AND balance_after_minor >= 0),
    CONSTRAINT wle_arithmetic_check CHECK (
        (direction = 'CREDIT' AND balance_after_minor = balance_before_minor + amount_minor) OR
        (direction = 'DEBIT' AND balance_after_minor = balance_before_minor - amount_minor)
    ),
    CONSTRAINT wle_wallet_transaction_unique UNIQUE (wallet_id, transaction_id)
);

CREATE INDEX wle_wallet_id_created_at_idx ON wallet_ledger_entries (wallet_id, created_at, id);

CREATE FUNCTION forbid_ledger_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only' USING ERRCODE = 'integrity_constraint_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER ledger_immutable
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION forbid_ledger_mutation();

COMMENT ON TABLE wallet_ledger_entries IS
    'Append-only audit ledger: one row per balance-changing movement, written in the same commit as the wallets.balance_minor update it reflects. UPDATE/DELETE are rejected unconditionally by the ledger_immutable trigger. LOSS and rejected/failed transactions never produce a row here.';
COMMENT ON COLUMN wallet_ledger_entries.balance_after_minor IS
    'Enforced by wle_arithmetic_check to equal balance_before_minor plus/minus amount_minor according to direction.';


CREATE TABLE inbox_messages (
    id            BIGSERIAL PRIMARY KEY,
    consumer_name TEXT NOT NULL,
    message_id    TEXT NOT NULL,
    message_hash  CHAR(64) NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at  TIMESTAMPTZ,

    CONSTRAINT inbox_consumer_message_unique UNIQUE (consumer_name, message_id)
);

COMMENT ON TABLE inbox_messages IS
    'SQS transport-level dedup (at-least-once delivery), independent of the business-level idempotency carried by wager_transactions.idempotency_key. consumer_name identifies the logical consumer group, so distinct consumers never collide on the same message_id.';
COMMENT ON COLUMN inbox_messages.message_hash IS
    'SHA-256 (hex) of the raw message body, used to detect an anomalous redelivery of the same message_id with different content (never silently reprocessed).';


CREATE TABLE outbox_events (
    event_id        UUID PRIMARY KEY,
    aggregate_id    UUID NOT NULL,
    event_type      TEXT NOT NULL,
    correlation_id  UUID NOT NULL,
    causation_id    UUID,
    payload         JSONB NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX outbox_pending_poll_idx
    ON outbox_events (next_attempt_at)
    WHERE published_at IS NULL;

COMMENT ON TABLE outbox_events IS
    'Transactional outbox: inserted in the same SQL transaction as the domain change that produced the event, guaranteeing publication only ever happens after that transaction commits.';
COMMENT ON COLUMN outbox_events.event_id IS
    'Generated once, at row creation, and never regenerated on retry/republish. Sent as the SQS MessageDeduplicationId on every (re)publish attempt.';
COMMENT ON COLUMN outbox_events.payload IS
    'Immutable, already-serialized snapshot of the typed event envelope. Never recomputed from current aggregate state at publish time.';
