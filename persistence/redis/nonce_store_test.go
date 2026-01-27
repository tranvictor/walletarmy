package redis

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tranvictor/walletarmy"
)

func TestNonceStore_SaveAndGet(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")
	nonce := uint64(10)

	state := &walletarmy.NonceState{
		Wallet:            wallet,
		ChainID:           1,
		LocalPendingNonce: &nonce,
		ReservedNonces:    []uint64{10, 11, 12},
		UpdatedAt:         time.Now(),
	}

	// Save
	err := store.Save(ctx, state)
	require.NoError(t, err)

	// Get
	retrieved, err := store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	require.NotNil(t, retrieved)

	assert.Equal(t, state.Wallet, retrieved.Wallet)
	assert.Equal(t, state.ChainID, retrieved.ChainID)
	assert.Equal(t, *state.LocalPendingNonce, *retrieved.LocalPendingNonce)
	assert.Equal(t, state.ReservedNonces, retrieved.ReservedNonces)
}

func TestNonceStore_GetNotFound(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	state, err := store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	assert.Nil(t, state)
}

func TestNonceStore_SavePendingNonce(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Save pending nonce (creates new state)
	err := store.SavePendingNonce(ctx, wallet, 1, 10)
	require.NoError(t, err)

	// Verify
	state, err := store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, uint64(10), *state.LocalPendingNonce)

	// Update pending nonce
	err = store.SavePendingNonce(ctx, wallet, 1, 15)
	require.NoError(t, err)

	// Verify updated
	state, err = store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(15), *state.LocalPendingNonce)
}

func TestNonceStore_ReservedNonces(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Add reserved nonces
	require.NoError(t, store.AddReservedNonce(ctx, wallet, 1, 10))
	require.NoError(t, store.AddReservedNonce(ctx, wallet, 1, 11))
	require.NoError(t, store.AddReservedNonce(ctx, wallet, 1, 12))

	// Verify
	state, err := store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	assert.Len(t, state.ReservedNonces, 3)
	assert.Contains(t, state.ReservedNonces, uint64(10))
	assert.Contains(t, state.ReservedNonces, uint64(11))
	assert.Contains(t, state.ReservedNonces, uint64(12))

	// Remove a reserved nonce
	require.NoError(t, store.RemoveReservedNonce(ctx, wallet, 1, 11))

	// Verify removed
	state, err = store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	assert.Len(t, state.ReservedNonces, 2)
	assert.NotContains(t, state.ReservedNonces, uint64(11))
}

func TestNonceStore_AddReservedNonceIdempotent(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Add same nonce twice
	require.NoError(t, store.AddReservedNonce(ctx, wallet, 1, 10))
	require.NoError(t, store.AddReservedNonce(ctx, wallet, 1, 10))

	// Should only have one entry
	state, err := store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	assert.Len(t, state.ReservedNonces, 1)
}

func TestNonceStore_ListAll(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	wallet2 := common.HexToAddress("0x2222222222222222222222222222222222222222")

	nonce1 := uint64(10)
	nonce2 := uint64(20)

	state1 := &walletarmy.NonceState{
		Wallet:            wallet1,
		ChainID:           1,
		LocalPendingNonce: &nonce1,
		UpdatedAt:         time.Now(),
	}

	state2 := &walletarmy.NonceState{
		Wallet:            wallet2,
		ChainID:           137, // Different chain
		LocalPendingNonce: &nonce2,
		UpdatedAt:         time.Now(),
	}

	require.NoError(t, store.Save(ctx, state1))
	require.NoError(t, store.Save(ctx, state2))

	// List all
	states, err := store.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, states, 2)
}

func TestNonceStore_WithKeyPrefix(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store1 := NewNonceStore(client, WithNonceStoreKeyPrefix("app1"))
	store2 := NewNonceStore(client, WithNonceStoreKeyPrefix("app2"))
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Save to store1
	require.NoError(t, store1.SavePendingNonce(ctx, wallet, 1, 10))

	// Should be visible in store1
	state, err := store1.Get(ctx, wallet, 1)
	require.NoError(t, err)
	assert.NotNil(t, state)

	// Should NOT be visible in store2
	state, err = store2.Get(ctx, wallet, 1)
	require.NoError(t, err)
	assert.Nil(t, state)
}

// Concurrency Tests - verify thread safety

func TestNonceStore_ConcurrentSavePendingNonce(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Use moderate concurrency to avoid exhausting retries with race detector overhead
	const numGoroutines = 10
	var wg sync.WaitGroup
	successCount := int32(0)
	var mu sync.Mutex
	var lastErr error

	// All goroutines try to update the same wallet's pending nonce
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			nonce := uint64(idx * 10)
			if err := store.SavePendingNonce(ctx, wallet, 1, nonce); err != nil {
				mu.Lock()
				lastErr = err
				mu.Unlock()
			} else {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()

	// At least half should succeed even under high contention
	if successCount < numGoroutines/2 {
		t.Errorf("too many failures: only %d/%d succeeded, last error: %v", successCount, numGoroutines, lastErr)
	}

	// Verify state exists and has a valid nonce (one of the values we set)
	state, err := store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.NotNil(t, state.LocalPendingNonce)
	// The final value should be one of the values set by a goroutine
	assert.True(t, *state.LocalPendingNonce%10 == 0 && *state.LocalPendingNonce < uint64(numGoroutines*10))
}

func TestNonceStore_ConcurrentAddRemoveReserved(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	const numGoroutines = 30
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*2)

	// Add a set of nonces
	for i := 0; i < numGoroutines; i++ {
		require.NoError(t, store.AddReservedNonce(ctx, wallet, 1, uint64(i)))
	}

	// Concurrently: some goroutines add more, some remove existing
	for i := 0; i < numGoroutines; i++ {
		wg.Add(2)

		// Adder - adds new nonces (offset by numGoroutines to avoid overlap)
		go func(idx int) {
			defer wg.Done()
			nonce := uint64(idx + numGoroutines)
			if err := store.AddReservedNonce(ctx, wallet, 1, nonce); err != nil {
				errors <- fmt.Errorf("add %d: %w", idx, err)
			}
		}(i)

		// Remover - removes existing nonces
		go func(idx int) {
			defer wg.Done()
			nonce := uint64(idx)
			if err := store.RemoveReservedNonce(ctx, wallet, 1, nonce); err != nil {
				errors <- fmt.Errorf("remove %d: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	// Verify state - should have only the newly added nonces
	state, err := store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	require.NotNil(t, state)

	// All original nonces (0 to numGoroutines-1) should be removed
	// All new nonces (numGoroutines to 2*numGoroutines-1) should be present
	for _, nonce := range state.ReservedNonces {
		assert.GreaterOrEqual(t, nonce, uint64(numGoroutines), "old nonce %d should be removed", nonce)
		assert.Less(t, nonce, uint64(2*numGoroutines), "nonce %d out of expected range", nonce)
	}
	assert.Len(t, state.ReservedNonces, numGoroutines)
}

func TestNonceStore_ConcurrentSave(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store := NewNonceStore(client)
	ctx := context.Background()

	const numGoroutines = 20
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	// Each goroutine saves to a different wallet
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			wallet := common.HexToAddress(fmt.Sprintf("0x%040x", idx))
			nonce := uint64(idx * 10)
			state := &walletarmy.NonceState{
				Wallet:            wallet,
				ChainID:           1,
				LocalPendingNonce: &nonce,
				ReservedNonces:    []uint64{nonce, nonce + 1, nonce + 2},
				UpdatedAt:         time.Now(),
			}

			if err := store.Save(ctx, state); err != nil {
				errors <- fmt.Errorf("goroutine %d save failed: %w", idx, err)
				return
			}

			// Verify we can read it back
			retrieved, err := store.Get(ctx, wallet, 1)
			if err != nil {
				errors <- fmt.Errorf("goroutine %d get failed: %w", idx, err)
				return
			}
			if retrieved == nil {
				errors <- fmt.Errorf("goroutine %d: saved state not found", idx)
				return
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	// Verify all states exist
	states, err := store.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, states, numGoroutines)
}

func TestNonceStore_ConcurrentListAll(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store := NewNonceStore(client)
	ctx := context.Background()

	const numGoroutines = 20
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*2)

	// Half goroutines write, half goroutines read
	for i := 0; i < numGoroutines; i++ {
		wg.Add(2)

		// Writer
		go func(idx int) {
			defer wg.Done()
			wallet := common.HexToAddress(fmt.Sprintf("0x%040x", idx))
			nonce := uint64(idx)
			if err := store.SavePendingNonce(ctx, wallet, 1, nonce); err != nil {
				errors <- fmt.Errorf("write %d: %w", idx, err)
			}
		}(i)

		// Reader
		go func(idx int) {
			defer wg.Done()
			_, err := store.ListAll(ctx)
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

	// Verify we can list all states
	states, err := store.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, states, numGoroutines)
}

func TestNonceStore_Cleanup(t *testing.T) {
	client := testRedisClient(t)
	defer client.Close()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create a valid state
	nonce := uint64(10)
	state := &walletarmy.NonceState{
		Wallet:            wallet,
		ChainID:           1,
		LocalPendingNonce: &nonce,
		UpdatedAt:         time.Now(),
	}
	require.NoError(t, store.Save(ctx, state))

	// Manually add an orphaned entry to the index
	orphanKey := "0x0000000000000000000000000000000000000000:999"
	err := client.SAdd(ctx, store.key(nonceWalletByChainKey), orphanKey).Err()
	require.NoError(t, err)

	// Verify orphan is in index
	pairs, err := client.SMembers(ctx, store.key(nonceWalletByChainKey)).Result()
	require.NoError(t, err)
	assert.Len(t, pairs, 2) // valid + orphan

	// Run cleanup
	removed, err := store.Cleanup(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	// Verify orphan is removed, valid state remains
	pairs, err = client.SMembers(ctx, store.key(nonceWalletByChainKey)).Result()
	require.NoError(t, err)
	assert.Len(t, pairs, 1)

	// Verify valid state still accessible
	retrieved, err := store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
}
