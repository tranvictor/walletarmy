package redis

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tranvictor/walletarmy"
)

func TestTxStore_SaveAndGet(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client, WithTxStoreKeyPrefix("test"))
	ctx := context.Background()

	// Create a test transaction
	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")
	to := common.HexToAddress("0x0987654321098765432109876543210987654321")

	ethTx := types.NewTransaction(
		1,                      // nonce
		to,                     // to
		big.NewInt(1000000),    // value
		21000,                  // gas
		big.NewInt(1000000000), // gasPrice
		nil,                    // data
	)

	pendingTx := &walletarmy.PendingTx{
		Hash:        ethTx.Hash(),
		Wallet:      wallet,
		ChainID:     1,
		Nonce:       1,
		Status:      walletarmy.PendingTxStatusBroadcasted,
		Transaction: ethTx,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Metadata:    map[string]string{"key": "value"},
	}

	// Save
	err := store.Save(ctx, pendingTx)
	require.NoError(t, err)

	// Get
	retrieved, err := store.Get(ctx, ethTx.Hash())
	require.NoError(t, err)
	require.NotNil(t, retrieved)

	assert.Equal(t, pendingTx.Hash, retrieved.Hash)
	assert.Equal(t, pendingTx.Wallet, retrieved.Wallet)
	assert.Equal(t, pendingTx.ChainID, retrieved.ChainID)
	assert.Equal(t, pendingTx.Nonce, retrieved.Nonce)
	assert.Equal(t, pendingTx.Status, retrieved.Status)
	assert.Equal(t, pendingTx.Metadata, retrieved.Metadata)
	assert.NotNil(t, retrieved.Transaction)
	assert.Equal(t, ethTx.Nonce(), retrieved.Transaction.Nonce())
}

func TestTxStore_GetNotFound(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	hash := common.HexToHash("0x1234567890123456789012345678901234567890123456789012345678901234")
	tx, err := store.Get(ctx, hash)

	require.NoError(t, err)
	assert.Nil(t, tx)
}

func TestTxStore_GetByNonce(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create two transactions with same nonce (replacement scenario)
	tx1 := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     5,
		Status:    walletarmy.PendingTxStatusBroadcasted,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	tx2 := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     5, // Same nonce
		Status:    walletarmy.PendingTxStatusBroadcasted,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	require.NoError(t, store.Save(ctx, tx1))
	require.NoError(t, store.Save(ctx, tx2))

	// Get by nonce
	txs, err := store.GetByNonce(ctx, wallet, 1, 5)
	require.NoError(t, err)
	assert.Len(t, txs, 2)
}

func TestTxStore_ListPending(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create pending and mined transactions
	pendingTx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusBroadcasted,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	minedTx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     2,
		Status:    walletarmy.PendingTxStatusMined,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	require.NoError(t, store.Save(ctx, pendingTx))
	require.NoError(t, store.Save(ctx, minedTx))

	// List pending
	txs, err := store.ListPending(ctx, wallet, 1)
	require.NoError(t, err)
	assert.Len(t, txs, 1)
	assert.Equal(t, pendingTx.Hash, txs[0].Hash)
}

func TestTxStore_ListAllPending(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	wallet2 := common.HexToAddress("0x2222222222222222222222222222222222222222")

	tx1 := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet1,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	tx2 := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		Wallet:    wallet2,
		ChainID:   137, // Different chain
		Nonce:     5,
		Status:    walletarmy.PendingTxStatusBroadcasted,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	require.NoError(t, store.Save(ctx, tx1))
	require.NoError(t, store.Save(ctx, tx2))

	// List all pending
	txs, err := store.ListAllPending(ctx)
	require.NoError(t, err)
	assert.Len(t, txs, 2)
}

func TestTxStore_UpdateStatus(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	tx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusBroadcasted,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	require.NoError(t, store.Save(ctx, tx))

	// Verify it's in pending list
	pending, err := store.ListPending(ctx, wallet, 1)
	require.NoError(t, err)
	assert.Len(t, pending, 1)

	// Update status to mined
	receipt := &types.Receipt{
		Status: types.ReceiptStatusSuccessful,
		Logs:   []*types.Log{}, // Required field
	}
	err = store.UpdateStatus(ctx, tx.Hash, walletarmy.PendingTxStatusMined, receipt)
	require.NoError(t, err)

	// Verify status updated
	updated, err := store.Get(ctx, tx.Hash)
	require.NoError(t, err)
	assert.Equal(t, walletarmy.PendingTxStatusMined, updated.Status)
	assert.NotNil(t, updated.Receipt)

	// Verify it's removed from pending list
	pending, err = store.ListPending(ctx, wallet, 1)
	require.NoError(t, err)
	assert.Len(t, pending, 0)
}

func TestTxStore_Delete(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	tx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusBroadcasted,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	require.NoError(t, store.Save(ctx, tx))

	// Delete
	err := store.Delete(ctx, tx.Hash)
	require.NoError(t, err)

	// Verify deleted
	deleted, err := store.Get(ctx, tx.Hash)
	require.NoError(t, err)
	assert.Nil(t, deleted)

	// Verify removed from indexes
	pending, err := store.ListPending(ctx, wallet, 1)
	require.NoError(t, err)
	assert.Len(t, pending, 0)
}

func TestTxStore_DeleteOlderThan(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	oldTx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusMined,
		CreatedAt: time.Now().Add(-2 * time.Hour), // 2 hours ago
		UpdatedAt: time.Now().Add(-2 * time.Hour),
	}

	newTx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     2,
		Status:    walletarmy.PendingTxStatusMined,
		CreatedAt: time.Now(), // Just now
		UpdatedAt: time.Now(),
	}

	require.NoError(t, store.Save(ctx, oldTx))
	require.NoError(t, store.Save(ctx, newTx))

	// Delete older than 1 hour
	deleted, err := store.DeleteOlderThan(ctx, 1*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)

	// Verify old tx deleted
	tx, err := store.Get(ctx, oldTx.Hash)
	require.NoError(t, err)
	assert.Nil(t, tx)

	// Verify new tx still exists
	tx, err = store.Get(ctx, newTx.Hash)
	require.NoError(t, err)
	assert.NotNil(t, tx)
}

func TestTxStore_TransactionWithReceipt(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create receipt with logs
	receipt := &types.Receipt{
		Status:            types.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		Bloom:             types.Bloom{},
		Logs: []*types.Log{
			{
				Address: wallet,
				Topics:  []common.Hash{common.HexToHash("0x1234")},
				Data:    []byte("test"),
			},
		},
		TxHash:           common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		GasUsed:          21000,
		BlockNumber:      big.NewInt(12345),
		TransactionIndex: 0,
	}

	tx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusMined,
		Receipt:   receipt,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	require.NoError(t, store.Save(ctx, tx))

	// Retrieve and verify receipt
	retrieved, err := store.Get(ctx, tx.Hash)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	require.NotNil(t, retrieved.Receipt)

	assert.Equal(t, receipt.Status, retrieved.Receipt.Status)
	assert.Equal(t, receipt.GasUsed, retrieved.Receipt.GasUsed)
}

func TestTxStore_WithKeyPrefix(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store1 := NewTxStore(client, WithTxStoreKeyPrefix("app1"))
	store2 := NewTxStore(client, WithTxStoreKeyPrefix("app2"))
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	tx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusBroadcasted,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Save to store1
	require.NoError(t, store1.Save(ctx, tx))

	// Should be visible in store1
	retrieved, err := store1.Get(ctx, tx.Hash)
	require.NoError(t, err)
	assert.NotNil(t, retrieved)

	// Should NOT be visible in store2
	retrieved, err = store2.Get(ctx, tx.Hash)
	require.NoError(t, err)
	assert.Nil(t, retrieved)
}

// Concurrency Tests - verify thread safety

func TestTxStore_ConcurrentSave(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	const numGoroutines = 20
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	for i := range numGoroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			wallet := common.HexToAddress(fmt.Sprintf("0x%040x", idx))
			tx := &walletarmy.PendingTx{
				Hash:      common.HexToHash(fmt.Sprintf("0x%064x", idx)),
				Wallet:    wallet,
				ChainID:   1,
				Nonce:     uint64(idx),
				Status:    walletarmy.PendingTxStatusBroadcasted,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}

			if err := store.Save(ctx, tx); err != nil {
				errors <- fmt.Errorf("goroutine %d save failed: %w", idx, err)
				return
			}

			// Verify we can read it back
			retrieved, err := store.Get(ctx, tx.Hash)
			if err != nil {
				errors <- fmt.Errorf("goroutine %d get failed: %w", idx, err)
				return
			}
			if retrieved == nil {
				errors <- fmt.Errorf("goroutine %d: saved tx not found", idx)
				return
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	// Verify all transactions exist
	for i := range numGoroutines {
		hash := common.HexToHash(fmt.Sprintf("0x%064x", i))
		tx, err := store.Get(ctx, hash)
		require.NoError(t, err, "failed to get tx %d", i)
		assert.NotNil(t, tx, "tx %d not found", i)
	}
}

func TestTxStore_ConcurrentUpdateStatus(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	// Create a transaction
	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.Save(ctx, tx))

	// Concurrently update status from multiple goroutines
	const numGoroutines = 10
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	statuses := []walletarmy.PendingTxStatus{
		walletarmy.PendingTxStatusPending,
		walletarmy.PendingTxStatusBroadcasted,
		walletarmy.PendingTxStatusMined,
	}

	for i := range numGoroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			status := statuses[idx%len(statuses)]
			if err := store.UpdateStatus(ctx, tx.Hash, status, nil); err != nil {
				errors <- fmt.Errorf("goroutine %d update failed: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	// Verify transaction still exists and has a valid status
	retrieved, err := store.Get(ctx, tx.Hash)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Contains(t, statuses, retrieved.Status)
}

func TestTxStore_ConcurrentSaveAndDelete(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	const numTxs = 50
	var wg sync.WaitGroup
	errors := make(chan error, numTxs*2)

	// First, create all transactions
	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")
	for i := range numTxs {
		tx := &walletarmy.PendingTx{
			Hash:      common.HexToHash(fmt.Sprintf("0x%064x", i)),
			Wallet:    wallet,
			ChainID:   1,
			Nonce:     uint64(i),
			Status:    walletarmy.PendingTxStatusBroadcasted,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		require.NoError(t, store.Save(ctx, tx))
	}

	// Concurrently: half goroutines update transactions, half delete them
	for i := range numTxs {
		wg.Add(2)

		// Updater goroutine
		go func(idx int) {
			defer wg.Done()
			hash := common.HexToHash(fmt.Sprintf("0x%064x", idx))
			err := store.UpdateStatus(ctx, hash, walletarmy.PendingTxStatusMined, nil)
			// Ignore "not found" errors from race with delete
			if err != nil && err.Error() != "failed to update transaction status after 3 retries: redis: transaction failed" {
				// Only report non-race errors
				errors <- fmt.Errorf("update %d: %w", idx, err)
			}
		}(i)

		// Deleter goroutine
		go func(idx int) {
			defer wg.Done()
			hash := common.HexToHash(fmt.Sprintf("0x%064x", idx))
			if err := store.Delete(ctx, hash); err != nil {
				errors <- fmt.Errorf("delete %d: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	// All transactions should be deleted
	for i := range numTxs {
		hash := common.HexToHash(fmt.Sprintf("0x%064x", i))
		tx, err := store.Get(ctx, hash)
		require.NoError(t, err)
		assert.Nil(t, tx, "tx %d should be deleted", i)
	}
}

func TestTxStore_ConcurrentListPending(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	const numGoroutines = 20
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*2)

	// Half goroutines write, half goroutines read
	for i := range numGoroutines {
		wg.Add(2)

		// Writer
		go func(idx int) {
			defer wg.Done()
			tx := &walletarmy.PendingTx{
				Hash:      common.HexToHash(fmt.Sprintf("0x%064x", idx)),
				Wallet:    wallet,
				ChainID:   1,
				Nonce:     uint64(idx),
				Status:    walletarmy.PendingTxStatusBroadcasted,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			if err := store.Save(ctx, tx); err != nil {
				errors <- fmt.Errorf("write %d: %w", idx, err)
			}
		}(i)

		// Reader
		go func(idx int) {
			defer wg.Done()
			_, err := store.ListPending(ctx, wallet, 1)
			if err != nil {
				errors <- fmt.Errorf("list %d: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	// Verify we can list all pending
	pending, err := store.ListPending(ctx, wallet, 1)
	require.NoError(t, err)
	assert.Len(t, pending, numGoroutines)
}

// Race Condition Prevention Tests

func TestTxStore_SaveDoesNotOverwriteMoreFinalStatus(t *testing.T) {
	// This test verifies that Save() respects status priority:
	// Once a tx reaches a final status (mined/reverted/replaced/dropped),
	// it cannot be overwritten by a less final status (pending/broadcasted).
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")
	hash := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

	// Create receipt for mined tx
	receipt := &types.Receipt{
		Status: types.ReceiptStatusSuccessful,
		Logs:   []*types.Log{},
	}

	// Save tx as mined (final status)
	minedTx := &walletarmy.PendingTx{
		Hash:      hash,
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusMined,
		Receipt:   receipt,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.Save(ctx, minedTx))

	// Try to save the same tx with broadcasted status (less final)
	broadcastedTx := &walletarmy.PendingTx{
		Hash:      hash,
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusBroadcasted,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	// This should succeed but NOT overwrite the mined status
	require.NoError(t, store.Save(ctx, broadcastedTx))

	// Verify status is still mined
	retrieved, err := store.Get(ctx, hash)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, walletarmy.PendingTxStatusMined, retrieved.Status)
	assert.NotNil(t, retrieved.Receipt, "receipt should be preserved")
}

func TestTxStore_SaveStatusPriorityOrder(t *testing.T) {
	// Test the full priority order: pending < broadcasted < dropped/replaced < mined/reverted
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	testCases := []struct {
		name           string
		firstStatus    walletarmy.PendingTxStatus
		secondStatus   walletarmy.PendingTxStatus
		expectedStatus walletarmy.PendingTxStatus
	}{
		{
			name:           "pending can be overwritten by broadcasted",
			firstStatus:    walletarmy.PendingTxStatusPending,
			secondStatus:   walletarmy.PendingTxStatusBroadcasted,
			expectedStatus: walletarmy.PendingTxStatusBroadcasted,
		},
		{
			name:           "broadcasted can be overwritten by mined",
			firstStatus:    walletarmy.PendingTxStatusBroadcasted,
			secondStatus:   walletarmy.PendingTxStatusMined,
			expectedStatus: walletarmy.PendingTxStatusMined,
		},
		{
			name:           "mined cannot be overwritten by pending",
			firstStatus:    walletarmy.PendingTxStatusMined,
			secondStatus:   walletarmy.PendingTxStatusPending,
			expectedStatus: walletarmy.PendingTxStatusMined,
		},
		{
			name:           "mined cannot be overwritten by broadcasted",
			firstStatus:    walletarmy.PendingTxStatusMined,
			secondStatus:   walletarmy.PendingTxStatusBroadcasted,
			expectedStatus: walletarmy.PendingTxStatusMined,
		},
		{
			name:           "replaced cannot be overwritten by broadcasted",
			firstStatus:    walletarmy.PendingTxStatusReplaced,
			secondStatus:   walletarmy.PendingTxStatusBroadcasted,
			expectedStatus: walletarmy.PendingTxStatusReplaced,
		},
		{
			name:           "dropped cannot be overwritten by broadcasted",
			firstStatus:    walletarmy.PendingTxStatusDropped,
			secondStatus:   walletarmy.PendingTxStatusBroadcasted,
			expectedStatus: walletarmy.PendingTxStatusDropped,
		},
		{
			name:           "reverted cannot be overwritten by pending",
			firstStatus:    walletarmy.PendingTxStatusReverted,
			secondStatus:   walletarmy.PendingTxStatusPending,
			expectedStatus: walletarmy.PendingTxStatusReverted,
		},
	}

	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			wallet := common.HexToAddress(fmt.Sprintf("0x%040x", i))
			hash := common.HexToHash(fmt.Sprintf("0x%064x", i))

			// Save with first status
			tx1 := &walletarmy.PendingTx{
				Hash:      hash,
				Wallet:    wallet,
				ChainID:   1,
				Nonce:     uint64(i),
				Status:    tc.firstStatus,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			require.NoError(t, store.Save(ctx, tx1))

			// Save with second status
			tx2 := &walletarmy.PendingTx{
				Hash:      hash,
				Wallet:    wallet,
				ChainID:   1,
				Nonce:     uint64(i),
				Status:    tc.secondStatus,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			require.NoError(t, store.Save(ctx, tx2))

			// Verify expected status
			retrieved, err := store.Get(ctx, hash)
			require.NoError(t, err)
			require.NotNil(t, retrieved)
			assert.Equal(t, tc.expectedStatus, retrieved.Status)
		})
	}
}

func TestTxStore_ConcurrentSaveAndUpdateStatusSameTx(t *testing.T) {
	// This test simulates a race between Save() and UpdateStatus() on the same tx.
	// Both use WATCH/MULTI/EXEC to ensure atomicity.
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")
	hash := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

	// Create initial tx as pending
	tx := &walletarmy.PendingTx{
		Hash:      hash,
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.Save(ctx, tx))

	// Run concurrent Save and UpdateStatus operations
	const numGoroutines = 10
	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex

	for range numGoroutines {
		wg.Add(2)

		// Save goroutine - tries to save with broadcasted status
		go func() {
			defer wg.Done()
			saveTx := &walletarmy.PendingTx{
				Hash:      hash,
				Wallet:    wallet,
				ChainID:   1,
				Nonce:     1,
				Status:    walletarmy.PendingTxStatusBroadcasted,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			if err := store.Save(ctx, saveTx); err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}()

		// UpdateStatus goroutine - tries to update to mined
		go func() {
			defer wg.Done()
			receipt := &types.Receipt{
				Status: types.ReceiptStatusSuccessful,
				Logs:   []*types.Log{},
			}
			if err := store.UpdateStatus(ctx, hash, walletarmy.PendingTxStatusMined, receipt); err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// Some operations should succeed (at least a few)
	assert.Greater(t, successCount, 0, "at least some operations should succeed")

	// Verify tx still exists with a valid status
	retrieved, err := store.Get(ctx, hash)
	require.NoError(t, err)
	require.NotNil(t, retrieved)

	// Final status should be either broadcasted or mined (both are valid outcomes)
	validStatuses := []walletarmy.PendingTxStatus{
		walletarmy.PendingTxStatusBroadcasted,
		walletarmy.PendingTxStatusMined,
	}
	assert.Contains(t, validStatuses, retrieved.Status)

	// If it's mined, it should NOT be in the pending set
	if retrieved.Status == walletarmy.PendingTxStatusMined {
		pending, err := store.ListPending(ctx, wallet, 1)
		require.NoError(t, err)
		for _, p := range pending {
			assert.NotEqual(t, hash, p.Hash, "mined tx should not be in pending set")
		}
	}
}

func TestTxStore_SavePreservesReceiptWhenNotOverwriting(t *testing.T) {
	// When Save() doesn't overwrite due to status priority,
	// it should preserve the existing receipt.
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")
	hash := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

	// Create receipt
	receipt := &types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		GasUsed:     21000,
		BlockNumber: big.NewInt(12345),
		Logs:        []*types.Log{},
	}

	// Save tx as mined with receipt
	minedTx := &walletarmy.PendingTx{
		Hash:      hash,
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusMined,
		Receipt:   receipt,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.Save(ctx, minedTx))

	// Try to overwrite with broadcasted (no receipt)
	broadcastedTx := &walletarmy.PendingTx{
		Hash:      hash,
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusBroadcasted,
		Receipt:   nil, // No receipt
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.Save(ctx, broadcastedTx))

	// Verify receipt is preserved
	retrieved, err := store.Get(ctx, hash)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	require.NotNil(t, retrieved.Receipt, "receipt should be preserved")
	assert.Equal(t, uint64(21000), retrieved.Receipt.GasUsed)
	assert.Equal(t, big.NewInt(12345), retrieved.Receipt.BlockNumber)
}

// Tests for DeleteOlderThanWithOptions improvements

func TestTxStore_DeleteOlderThanWithBatchLimit(t *testing.T) {
	// Test that DeleteOlderThanWithOptions respects batch limits
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create many old transactions
	const numTxs = 25
	for i := range numTxs {
		tx := &walletarmy.PendingTx{
			Hash:      common.HexToHash(fmt.Sprintf("0x%064x", i)),
			Wallet:    wallet,
			ChainID:   1,
			Nonce:     uint64(i),
			Status:    walletarmy.PendingTxStatusMined,
			CreatedAt: time.Now().Add(-2 * time.Hour), // 2 hours ago
			UpdatedAt: time.Now().Add(-2 * time.Hour),
		}
		require.NoError(t, store.Save(ctx, tx))
	}

	// Delete with batch size of 10, no grace period
	deleted, err := store.DeleteOlderThanWithOptions(ctx, 1*time.Hour, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, numTxs, deleted, "should delete all old transactions across batches")

	// Verify all deleted
	for i := range numTxs {
		hash := common.HexToHash(fmt.Sprintf("0x%064x", i))
		tx, err := store.Get(ctx, hash)
		require.NoError(t, err)
		assert.Nil(t, tx, "tx %d should be deleted", i)
	}
}

func TestTxStore_DeleteOlderThanWithGracePeriod(t *testing.T) {
	// Test that recently updated transactions are skipped
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create an old transaction that was recently updated
	recentlyUpdatedTx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusMined,
		CreatedAt: time.Now().Add(-2 * time.Hour),   // Created 2 hours ago
		UpdatedAt: time.Now().Add(-1 * time.Minute), // Updated 1 minute ago
	}
	require.NoError(t, store.Save(ctx, recentlyUpdatedTx))

	// Create an old transaction that hasn't been updated recently
	oldTx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     2,
		Status:    walletarmy.PendingTxStatusMined,
		CreatedAt: time.Now().Add(-2 * time.Hour), // Created 2 hours ago
		UpdatedAt: time.Now().Add(-2 * time.Hour), // Also updated 2 hours ago
	}
	require.NoError(t, store.Save(ctx, oldTx))

	// Delete with 5 minute grace period - should skip recentlyUpdatedTx
	deleted, err := store.DeleteOlderThanWithOptions(ctx, 1*time.Hour, 0, 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted, "should only delete the old tx, not the recently updated one")

	// Verify recentlyUpdatedTx still exists
	tx, err := store.Get(ctx, recentlyUpdatedTx.Hash)
	require.NoError(t, err)
	assert.NotNil(t, tx, "recently updated tx should still exist")

	// Verify oldTx is deleted
	tx, err = store.Get(ctx, oldTx.Hash)
	require.NoError(t, err)
	assert.Nil(t, tx, "old tx should be deleted")
}

func TestTxStore_DeleteOlderThanCleansUpEmptyNonceIndexes(t *testing.T) {
	// Test that empty nonce index sets are cleaned up after deletion
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create transactions with same nonce (replacement scenario)
	tx1 := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     5,
		Status:    walletarmy.PendingTxStatusReplaced,
		CreatedAt: time.Now().Add(-2 * time.Hour),
		UpdatedAt: time.Now().Add(-2 * time.Hour),
	}
	tx2 := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     5, // Same nonce
		Status:    walletarmy.PendingTxStatusMined,
		CreatedAt: time.Now().Add(-2 * time.Hour),
		UpdatedAt: time.Now().Add(-2 * time.Hour),
	}
	require.NoError(t, store.Save(ctx, tx1))
	require.NoError(t, store.Save(ctx, tx2))

	// Verify nonce index has 2 entries
	nonceKey := store.nonceIndexKey(wallet, 1, 5)
	count, err := client.SCard(ctx, nonceKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(2), count, "nonce index should have 2 entries")

	// Delete all old transactions
	deleted, err := store.DeleteOlderThanWithOptions(ctx, 1*time.Hour, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)

	// Verify nonce index is cleaned up (empty set deleted)
	exists, err := client.Exists(ctx, nonceKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists, "empty nonce index should be deleted")
}

func TestTxStore_DeleteOlderThanDefaultBehavior(t *testing.T) {
	// Test that the default DeleteOlderThan uses reasonable defaults
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create an old transaction
	oldTx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusMined,
		CreatedAt: time.Now().Add(-2 * time.Hour),
		UpdatedAt: time.Now().Add(-2 * time.Hour),
	}
	require.NoError(t, store.Save(ctx, oldTx))

	// Create a recently updated transaction (within default 5 min grace period)
	recentTx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     2,
		Status:    walletarmy.PendingTxStatusMined,
		CreatedAt: time.Now().Add(-2 * time.Hour),
		UpdatedAt: time.Now().Add(-2 * time.Minute), // Updated 2 minutes ago
	}
	require.NoError(t, store.Save(ctx, recentTx))

	// Use default DeleteOlderThan (1000 batch, 5 min grace)
	deleted, err := store.DeleteOlderThan(ctx, 1*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted, "should delete only the old tx due to grace period")

	// Verify oldTx deleted
	tx, err := store.Get(ctx, oldTx.Hash)
	require.NoError(t, err)
	assert.Nil(t, tx)

	// Verify recentTx still exists
	tx, err = store.Get(ctx, recentTx.Hash)
	require.NoError(t, err)
	assert.NotNil(t, tx)
}

func TestTxStore_DeleteOlderThanUpdatesTimestampForSkippedTxs(t *testing.T) {
	// Test that skipped transactions have their sorted set score updated
	// to their UpdatedAt time to prevent repeated checking
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	oldCreatedAt := time.Now().Add(-2 * time.Hour)
	recentUpdatedAt := time.Now().Add(-1 * time.Minute)

	// Create a transaction that was created long ago but updated recently
	tx := &walletarmy.PendingTx{
		Hash:      common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Wallet:    wallet,
		ChainID:   1,
		Nonce:     1,
		Status:    walletarmy.PendingTxStatusMined,
		CreatedAt: oldCreatedAt,
		UpdatedAt: recentUpdatedAt,
	}
	require.NoError(t, store.Save(ctx, tx))

	// Get the initial score (should be CreatedAt)
	initialScore, err := client.ZScore(ctx, store.key(txTimestampSortedSet), tx.Hash.Hex()).Result()
	require.NoError(t, err)
	assert.InDelta(t, float64(oldCreatedAt.Unix()), initialScore, 1, "initial score should be CreatedAt")

	// Run delete with grace period - tx should be skipped
	deleted, err := store.DeleteOlderThanWithOptions(ctx, 1*time.Hour, 0, 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 0, deleted, "tx should be skipped")

	// Get the new score (should be UpdatedAt)
	newScore, err := client.ZScore(ctx, store.key(txTimestampSortedSet), tx.Hash.Hex()).Result()
	require.NoError(t, err)
	assert.InDelta(t, float64(recentUpdatedAt.Unix()), newScore, 1, "new score should be UpdatedAt")
}

func TestTxStore_CleanupEmptyNonceIndexes(t *testing.T) {
	// Test the cleanupEmptyNonceIndexes helper function
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create some nonce index keys - one empty, one with data
	emptyKey := store.nonceIndexKey(wallet, 1, 100)
	nonEmptyKey := store.nonceIndexKey(wallet, 1, 200)

	// Create empty key (just to ensure it exists as empty)
	require.NoError(t, client.SAdd(ctx, emptyKey, "temp").Err())
	require.NoError(t, client.SRem(ctx, emptyKey, "temp").Err())

	// Create non-empty key
	require.NoError(t, client.SAdd(ctx, nonEmptyKey, "0xhash1", "0xhash2").Err())

	// Verify setup
	emptyCount, _ := client.SCard(ctx, emptyKey).Result()
	nonEmptyCount, _ := client.SCard(ctx, nonEmptyKey).Result()
	// Empty set may not exist or have 0 count
	assert.Equal(t, int64(0), emptyCount)
	assert.Equal(t, int64(2), nonEmptyCount)

	// Cleanup
	nonceKeys := map[string]struct{}{
		emptyKey:    {},
		nonEmptyKey: {},
	}
	err := store.cleanupEmptyNonceIndexes(ctx, nonceKeys)
	require.NoError(t, err)

	// Verify empty key is deleted
	exists, err := client.Exists(ctx, emptyKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists, "empty key should be deleted")

	// Verify non-empty key still exists
	exists, err = client.Exists(ctx, nonEmptyKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists, "non-empty key should still exist")
}

func TestTxStore_DeleteOlderThanHandlesEmptyDatabase(t *testing.T) {
	// Test that delete works correctly on empty database
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	deleted, err := store.DeleteOlderThan(ctx, 1*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)

	deleted, err = store.DeleteOlderThanWithOptions(ctx, 1*time.Hour, 10, 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)
}

func TestTxStore_ConcurrentDeleteAndUpdate(t *testing.T) {
	// Test race condition protection: concurrent delete and status update
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	const numTxs = 20
	var wg sync.WaitGroup
	errors := make(chan error, numTxs*2)

	// Create old transactions
	for i := range numTxs {
		tx := &walletarmy.PendingTx{
			Hash:      common.HexToHash(fmt.Sprintf("0x%064x", i)),
			Wallet:    wallet,
			ChainID:   1,
			Nonce:     uint64(i),
			Status:    walletarmy.PendingTxStatusBroadcasted,
			CreatedAt: time.Now().Add(-2 * time.Hour),
			UpdatedAt: time.Now().Add(-2 * time.Hour),
		}
		require.NoError(t, store.Save(ctx, tx))
	}

	// Concurrently: one goroutine deletes, others update status
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Use 0 grace period to make race more likely
		_, err := store.DeleteOlderThanWithOptions(ctx, 1*time.Hour, 5, 0)
		if err != nil {
			errors <- fmt.Errorf("delete: %w", err)
		}
	}()

	for i := range numTxs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			hash := common.HexToHash(fmt.Sprintf("0x%064x", idx))
			receipt := &types.Receipt{
				Status: types.ReceiptStatusSuccessful,
				Logs:   []*types.Log{},
			}
			err := store.UpdateStatus(ctx, hash, walletarmy.PendingTxStatusMined, receipt)
			// Ignore "not found" type errors from race with delete
			if err != nil && !strings.Contains(err.Error(), "retries") {
				errors <- fmt.Errorf("update %d: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	// Verify no data corruption - remaining txs should be in valid state
	for i := range numTxs {
		hash := common.HexToHash(fmt.Sprintf("0x%064x", i))
		tx, err := store.Get(ctx, hash)
		require.NoError(t, err)
		// tx may be nil (deleted) or have a valid status
		if tx != nil {
			validStatuses := []walletarmy.PendingTxStatus{
				walletarmy.PendingTxStatusBroadcasted,
				walletarmy.PendingTxStatusMined,
			}
			assert.Contains(t, validStatuses, tx.Status, "tx %d has invalid status", i)
		}
	}
}

func TestTxStore_UpdateStatusRespectsStatusPriority(t *testing.T) {
	// This test verifies that UpdateStatus() respects status priority
	// and won't downgrade a more final status to a less final one.
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewTxStore(client)
	ctx := context.Background()

	testCases := []struct {
		name           string
		initialStatus  walletarmy.PendingTxStatus
		updateStatus   walletarmy.PendingTxStatus
		expectedStatus walletarmy.PendingTxStatus
	}{
		{
			name:           "can upgrade pending to broadcasted",
			initialStatus:  walletarmy.PendingTxStatusPending,
			updateStatus:   walletarmy.PendingTxStatusBroadcasted,
			expectedStatus: walletarmy.PendingTxStatusBroadcasted,
		},
		{
			name:           "can upgrade broadcasted to mined",
			initialStatus:  walletarmy.PendingTxStatusBroadcasted,
			updateStatus:   walletarmy.PendingTxStatusMined,
			expectedStatus: walletarmy.PendingTxStatusMined,
		},
		{
			name:           "cannot downgrade mined to broadcasted",
			initialStatus:  walletarmy.PendingTxStatusMined,
			updateStatus:   walletarmy.PendingTxStatusBroadcasted,
			expectedStatus: walletarmy.PendingTxStatusMined,
		},
		{
			name:           "cannot downgrade mined to pending",
			initialStatus:  walletarmy.PendingTxStatusMined,
			updateStatus:   walletarmy.PendingTxStatusPending,
			expectedStatus: walletarmy.PendingTxStatusMined,
		},
		{
			name:           "cannot downgrade reverted to broadcasted",
			initialStatus:  walletarmy.PendingTxStatusReverted,
			updateStatus:   walletarmy.PendingTxStatusBroadcasted,
			expectedStatus: walletarmy.PendingTxStatusReverted,
		},
		{
			name:           "cannot downgrade replaced to pending",
			initialStatus:  walletarmy.PendingTxStatusReplaced,
			updateStatus:   walletarmy.PendingTxStatusPending,
			expectedStatus: walletarmy.PendingTxStatusReplaced,
		},
	}

	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			wallet := common.HexToAddress(fmt.Sprintf("0x%040x", 1000+i))
			hash := common.HexToHash(fmt.Sprintf("0x%064x", 1000+i))

			// Create tx with initial status
			tx := &walletarmy.PendingTx{
				Hash:      hash,
				Wallet:    wallet,
				ChainID:   1,
				Nonce:     uint64(i),
				Status:    tc.initialStatus,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			require.NoError(t, store.Save(ctx, tx))

			// Try to update to new status
			err := store.UpdateStatus(ctx, hash, tc.updateStatus, nil)
			require.NoError(t, err)

			// Verify expected status
			retrieved, err := store.Get(ctx, hash)
			require.NoError(t, err)
			require.NotNil(t, retrieved)
			assert.Equal(t, tc.expectedStatus, retrieved.Status)
		})
	}
}
