package redis

import (
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tranvictor/walletarmy/idempotency"
)

func TestIdempotencyStore_CreateAndGet(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewIdempotencyStore(client)

	// Create a record
	record, err := store.Create("test-key-1")
	require.NoError(t, err)
	require.NotNil(t, record)

	assert.Equal(t, "test-key-1", record.Key)
	assert.Equal(t, idempotency.StatusPending, record.Status)
	assert.False(t, record.CreatedAt.IsZero())
	assert.False(t, record.UpdatedAt.IsZero())

	// Get the record
	retrieved, err := store.Get("test-key-1")
	require.NoError(t, err)
	require.NotNil(t, retrieved)

	assert.Equal(t, record.Key, retrieved.Key)
	assert.Equal(t, record.Status, retrieved.Status)
	assert.Equal(t, record.CreatedAt.UnixNano(), retrieved.CreatedAt.UnixNano())
}

func TestIdempotencyStore_GetNotFound(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewIdempotencyStore(client)

	_, err := store.Get("non-existent-key")
	assert.ErrorIs(t, err, idempotency.ErrKeyNotFound)
}

func TestIdempotencyStore_CreateDuplicate(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewIdempotencyStore(client)

	// Create first record
	record1, err := store.Create("test-key-1")
	require.NoError(t, err)
	require.NotNil(t, record1)

	// Try to create duplicate
	record2, err := store.Create("test-key-1")
	assert.ErrorIs(t, err, idempotency.ErrDuplicateKey)
	require.NotNil(t, record2) // Should return the existing record

	assert.Equal(t, record1.Key, record2.Key)
	assert.Equal(t, record1.CreatedAt.UnixNano(), record2.CreatedAt.UnixNano())
}

func TestIdempotencyStore_Update(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewIdempotencyStore(client)

	// Create a record
	record, err := store.Create("test-key-1")
	require.NoError(t, err)

	// Update the record
	record.Status = idempotency.StatusSubmitted
	record.TxHash = common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")

	err = store.Update(record)
	require.NoError(t, err)

	// Verify update
	retrieved, err := store.Get("test-key-1")
	require.NoError(t, err)

	assert.Equal(t, idempotency.StatusSubmitted, retrieved.Status)
	assert.Equal(t, record.TxHash, retrieved.TxHash)
	assert.True(t, retrieved.UpdatedAt.After(record.CreatedAt))
}

func TestIdempotencyStore_UpdateNotFound(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewIdempotencyStore(client)

	record := &idempotency.Record{
		Key:    "non-existent-key",
		Status: idempotency.StatusPending,
	}

	err := store.Update(record)
	assert.ErrorIs(t, err, idempotency.ErrKeyNotFound)
}

func TestIdempotencyStore_Delete(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewIdempotencyStore(client)

	// Create a record
	_, err := store.Create("test-key-1")
	require.NoError(t, err)

	// Verify it exists
	_, err = store.Get("test-key-1")
	require.NoError(t, err)

	// Delete
	err = store.Delete("test-key-1")
	require.NoError(t, err)

	// Verify it's gone
	_, err = store.Get("test-key-1")
	assert.ErrorIs(t, err, idempotency.ErrKeyNotFound)
}

func TestIdempotencyStore_DeleteNonExistent(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewIdempotencyStore(client)

	// Delete non-existent key should not error
	err := store.Delete("non-existent-key")
	assert.NoError(t, err)
}

func TestIdempotencyStore_WithKeyPrefix(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store1 := NewIdempotencyStore(client, WithIdempotencyStoreKeyPrefix("app-a"))
	store2 := NewIdempotencyStore(client, WithIdempotencyStoreKeyPrefix("app-b"))

	// Create in store1
	_, err := store1.Create("test-key")
	require.NoError(t, err)

	// Should be able to create same key in store2
	_, err = store2.Create("test-key")
	require.NoError(t, err)

	// Each store sees its own record
	record1, err := store1.Get("test-key")
	require.NoError(t, err)
	assert.Equal(t, "test-key", record1.Key)

	record2, err := store2.Get("test-key")
	require.NoError(t, err)
	assert.Equal(t, "test-key", record2.Key)
}

func TestIdempotencyStore_WithTTL(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	// Create store with very short TTL
	store := NewIdempotencyStore(client, WithIdempotencyStoreTTL(100*time.Millisecond))

	// Create a record
	_, err := store.Create("test-key-1")
	require.NoError(t, err)

	// Should exist immediately
	_, err = store.Get("test-key-1")
	require.NoError(t, err)

	// Wait for TTL to expire
	time.Sleep(150 * time.Millisecond)

	// Should be gone (expired by Redis TTL)
	_, err = store.Get("test-key-1")
	assert.ErrorIs(t, err, idempotency.ErrKeyNotFound)
}

func TestIdempotencyStore_ConcurrentCreate(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewIdempotencyStore(client)

	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	var successCount int
	var duplicateCount int
	var mu sync.Mutex

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			_, err := store.Create("concurrent-key")
			mu.Lock()
			switch err {
			case nil:
				successCount++
			case idempotency.ErrDuplicateKey:
				duplicateCount++
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	// Exactly one should succeed
	assert.Equal(t, 1, successCount)
	assert.Equal(t, numGoroutines-1, duplicateCount)
}

func TestIdempotencyStore_ConcurrentUpdate(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewIdempotencyStore(client)

	// Create a record
	_, err := store.Create("test-key")
	require.NoError(t, err)

	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(status idempotency.Status) {
			defer wg.Done()
			record, err := store.Get("test-key")
			if err != nil {
				return
			}
			record.Status = status
			_ = store.Update(record)
		}(idempotency.Status(i % 4))
	}

	wg.Wait()

	// Record should still be readable (no corruption)
	record, err := store.Get("test-key")
	require.NoError(t, err)
	assert.Equal(t, "test-key", record.Key)
}

func TestIdempotencyStore_RecordWithTransaction(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewIdempotencyStore(client)

	// Create a record
	record, err := store.Create("test-key-1")
	require.NoError(t, err)

	// Create a sample transaction
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    1,
		GasPrice: nil,
		Gas:      21000,
		To:       nil,
		Value:    nil,
		Data:     nil,
	})

	// Update with transaction
	record.Status = idempotency.StatusSubmitted
	record.TxHash = tx.Hash()
	record.Transaction = tx

	err = store.Update(record)
	require.NoError(t, err)

	// Retrieve and verify
	retrieved, err := store.Get("test-key-1")
	require.NoError(t, err)

	assert.Equal(t, idempotency.StatusSubmitted, retrieved.Status)
	assert.Equal(t, tx.Hash(), retrieved.TxHash)
	require.NotNil(t, retrieved.Transaction)
	assert.Equal(t, tx.Nonce(), retrieved.Transaction.Nonce())
}

func TestIdempotencyStore_RecordWithReceipt(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewIdempotencyStore(client)

	// Create a record
	record, err := store.Create("test-key-1")
	require.NoError(t, err)

	// Create a sample receipt
	receipt := &types.Receipt{
		Status:            types.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		Logs:              []*types.Log{},
		TxHash:            common.HexToHash("0x1234"),
		GasUsed:           21000,
		BlockNumber:       nil,
	}

	// Update with receipt
	record.Status = idempotency.StatusConfirmed
	record.Receipt = receipt

	err = store.Update(record)
	require.NoError(t, err)

	// Retrieve and verify
	retrieved, err := store.Get("test-key-1")
	require.NoError(t, err)

	assert.Equal(t, idempotency.StatusConfirmed, retrieved.Status)
	require.NotNil(t, retrieved.Receipt)
	assert.Equal(t, types.ReceiptStatusSuccessful, retrieved.Receipt.Status)
	assert.Equal(t, uint64(21000), retrieved.Receipt.GasUsed)
}

func TestIdempotencyStore_RecordWithError(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewIdempotencyStore(client)

	// Create a record
	record, err := store.Create("test-key-1")
	require.NoError(t, err)

	// Update with error
	record.Status = idempotency.StatusFailed
	record.Error = assert.AnError

	err = store.Update(record)
	require.NoError(t, err)

	// Retrieve and verify
	retrieved, err := store.Get("test-key-1")
	require.NoError(t, err)

	assert.Equal(t, idempotency.StatusFailed, retrieved.Status)
	require.NotNil(t, retrieved.Error)
	assert.Contains(t, retrieved.Error.Error(), "assert.AnError")
}
