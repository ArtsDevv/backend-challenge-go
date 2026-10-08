//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"backend-challenge-go/internal/ports"
)

func fakeMessageHash(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func TestInboxIdempotency_SameMessageIdSameHashIsIdempotent(t *testing.T) {
	skipIfUnavailable(t)
	ctx := context.Background()

	consumerName := "consumer-" + uuid.NewString()
	messageID := "message-" + uuid.NewString()

	var firstReserved bool
	err := uow.WithinTx(ctx, func(ctx context.Context) error {
		var txErr error
		_, firstReserved, txErr = inboxStore.ReserveOrGet(ctx, consumerName, messageID, fakeMessageHash("hash-A"))
		return txErr
	})
	require.NoError(t, err)
	require.True(t, firstReserved)

	var (
		secondExisting ports.InboxRecord
		secondReserved bool
	)
	err = uow.WithinTx(ctx, func(ctx context.Context) error {
		var txErr error
		secondExisting, secondReserved, txErr = inboxStore.ReserveOrGet(ctx, consumerName, messageID, fakeMessageHash("hash-A"))
		return txErr
	})
	require.NoError(t, err)
	require.False(t, secondReserved)
	require.Equal(t, fakeMessageHash("hash-A"), secondExisting.MessageHash)
	require.Equal(t, messageID, secondExisting.MessageID)
	require.False(t, secondExisting.Completed())

	err = uow.WithinTx(ctx, func(ctx context.Context) error {
		return inboxStore.MarkCompleted(ctx, consumerName, messageID, time.Now().UTC())
	})
	require.NoError(t, err)

	var thirdExisting ports.InboxRecord
	err = uow.WithinTx(ctx, func(ctx context.Context) error {
		var txErr error
		thirdExisting, _, txErr = inboxStore.ReserveOrGet(ctx, consumerName, messageID, fakeMessageHash("hash-A"))
		return txErr
	})
	require.NoError(t, err)
	require.True(t, thirdExisting.Completed())
}

func TestInboxIdempotency_SameMessageIdDifferentHashIsRejected(t *testing.T) {
	skipIfUnavailable(t)
	ctx := context.Background()

	consumerName := "consumer-" + uuid.NewString()
	messageID := "message-" + uuid.NewString()

	var firstReserved bool
	err := uow.WithinTx(ctx, func(ctx context.Context) error {
		var txErr error
		_, firstReserved, txErr = inboxStore.ReserveOrGet(ctx, consumerName, messageID, fakeMessageHash("hash-B"))
		return txErr
	})
	require.NoError(t, err)
	require.True(t, firstReserved)

	err = uow.WithinTx(ctx, func(ctx context.Context) error {
		_, _, txErr := inboxStore.ReserveOrGet(ctx, consumerName, messageID, fakeMessageHash("hash-C"))
		return txErr
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ports.ErrInboxHashMismatch))
}

func TestInboxIdempotency_SameMessageIdDifferentConsumersDoNotConflict(t *testing.T) {
	skipIfUnavailable(t)
	ctx := context.Background()

	consumerNameOne := "consumer-" + uuid.NewString()
	consumerNameTwo := "consumer-" + uuid.NewString()
	messageID := "message-" + uuid.NewString()

	var firstReserved bool
	err := uow.WithinTx(ctx, func(ctx context.Context) error {
		var txErr error
		_, firstReserved, txErr = inboxStore.ReserveOrGet(ctx, consumerNameOne, messageID, fakeMessageHash("hash-D"))
		return txErr
	})
	require.NoError(t, err)
	require.True(t, firstReserved)

	var secondReserved bool
	err = uow.WithinTx(ctx, func(ctx context.Context) error {
		var txErr error
		_, secondReserved, txErr = inboxStore.ReserveOrGet(ctx, consumerNameTwo, messageID, fakeMessageHash("hash-D"))
		return txErr
	})
	require.NoError(t, err)
	require.True(t, secondReserved)
}
