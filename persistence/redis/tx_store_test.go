package redis

import (
	"context"
	"fmt"
	"math/big"
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
	defer client.Close()

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
	defer client.Close()

	store := NewTxStore(client)
	ctx := context.Background()

	hash := common.HexToHash("0x1234567890123456789012345678901234567890123456789012345678901234")
	tx, err := store.Get(ctx, hash)

	require.NoError(t, err)
	assert.Nil(t, tx)
}

func TestTxStore_GetByNonce(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

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
	defer client.Close()

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
	defer client.Close()

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
	defer client.Close()

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
	defer client.Close()

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
	defer client.Close()

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
	defer client.Close()

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
	defer client.Close()

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
	defer client.Close()

	store := NewTxStore(client)
	ctx := context.Background()

	const numGoroutines = 20
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
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
	for i := 0; i < numGoroutines; i++ {
		hash := common.HexToHash(fmt.Sprintf("0x%064x", i))
		tx, err := store.Get(ctx, hash)
		require.NoError(t, err, "failed to get tx %d", i)
		assert.NotNil(t, tx, "tx %d not found", i)
	}
}

func TestTxStore_ConcurrentUpdateStatus(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

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

	for i := 0; i < numGoroutines; i++ {
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
	defer client.Close()

	store := NewTxStore(client)
	ctx := context.Background()

	const numTxs = 50
	var wg sync.WaitGroup
	errors := make(chan error, numTxs*2)

	// First, create all transactions
	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")
	for i := 0; i < numTxs; i++ {
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
	for i := 0; i < numTxs; i++ {
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
	for i := 0; i < numTxs; i++ {
		hash := common.HexToHash(fmt.Sprintf("0x%064x", i))
		tx, err := store.Get(ctx, hash)
		require.NoError(t, err)
		assert.Nil(t, tx, "tx %d should be deleted", i)
	}
}

func TestTxStore_ConcurrentListPending(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store := NewTxStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	const numGoroutines = 20
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*2)

	// Half goroutines write, half goroutines read
	for i := 0; i < numGoroutines; i++ {
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
