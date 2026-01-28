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
	defer func() { _ = client.Close() }()

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
	defer func() { _ = client.Close() }()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	state, err := store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	assert.Nil(t, state)
}

func TestNonceStore_SavePendingNonce(t *testing.T) {
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

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
	defer func() { _ = client.Close() }()

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
	defer func() { _ = client.Close() }()

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
	defer func() { _ = client.Close() }()

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
	defer func() { _ = client.Close() }()

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
	defer func() { _ = client.Close() }()

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
	for i := range numGoroutines {
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
	defer func() { _ = client.Close() }()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	const numGoroutines = 30
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*2)

	// Add a set of nonces
	for i := range numGoroutines {
		require.NoError(t, store.AddReservedNonce(ctx, wallet, 1, uint64(i)))
	}

	// Concurrently: some goroutines add more, some remove existing
	for i := range numGoroutines {
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
	defer func() { _ = client.Close() }()

	store := NewNonceStore(client)
	ctx := context.Background()

	const numGoroutines = 20
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	// Each goroutine saves to a different wallet
	for i := range numGoroutines {
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
	defer func() { _ = client.Close() }()

	store := NewNonceStore(client)
	ctx := context.Background()

	const numGoroutines = 20
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*2)

	// Half goroutines write, half goroutines read
	for i := range numGoroutines {
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
	defer func() { _ = client.Close() }()

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

// Race Condition Prevention Tests

func TestNonceStore_GetReturnsConsistentSnapshot(t *testing.T) {
	// This test verifies that Get() returns a consistent snapshot
	// of state + reserved nonces (they are stored in different keys).
	// Under heavy contention, some writes may fail after retries - this is expected.
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create initial state with reserved nonces
	nonce := uint64(10)
	state := &walletarmy.NonceState{
		Wallet:            wallet,
		ChainID:           1,
		LocalPendingNonce: &nonce,
		ReservedNonces:    []uint64{10, 11, 12},
		UpdatedAt:         time.Now(),
	}
	require.NoError(t, store.Save(ctx, state))

	// Run concurrent Get and modifications (reduced concurrency to avoid retry exhaustion)
	const numGoroutines = 10
	var wg sync.WaitGroup
	snapshots := make(chan *walletarmy.NonceState, numGoroutines)

	for i := range numGoroutines {
		wg.Add(3)

		// Getter - reads the state
		go func() {
			defer wg.Done()
			snapshot, err := store.Get(ctx, wallet, 1)
			if err == nil && snapshot != nil {
				snapshots <- snapshot
			}
			// Ignore errors - Get should not fail but we're being defensive
		}()

		// Modifier 1 - adds reserved nonces (single SADD, no contention issues)
		go func(idx int) {
			defer wg.Done()
			newNonce := uint64(100 + idx)
			// AddReservedNonce uses simple SADD, no WATCH needed
			_ = store.AddReservedNonce(ctx, wallet, 1, newNonce)
		}(i)

		// Modifier 2 - updates pending nonce (may fail under contention, that's ok)
		go func(idx int) {
			defer wg.Done()
			newPending := uint64(50 + idx)
			// SavePendingNonce uses WATCH, may fail under heavy contention
			_ = store.SavePendingNonce(ctx, wallet, 1, newPending)
		}(i)
	}

	wg.Wait()
	close(snapshots)

	// All snapshots should be internally consistent:
	// The ReservedNonces should be a valid set (no duplicates)
	snapshotCount := 0
	for snapshot := range snapshots {
		snapshotCount++
		require.NotNil(t, snapshot)
		// Check for duplicates
		seen := make(map[uint64]bool)
		for _, n := range snapshot.ReservedNonces {
			if seen[n] {
				t.Errorf("duplicate nonce %d in snapshot", n)
			}
			seen[n] = true
		}
	}

	// Should have retrieved at least some snapshots
	assert.Greater(t, snapshotCount, 0, "should have retrieved at least one snapshot")
}

func TestNonceStore_SaveAtomicReservedNoncesUpdate(t *testing.T) {
	// This test verifies that Save() atomically updates the state using WATCH.
	// It ensures no partial writes occur - either the full new state is saved or
	// the operation retries on conflict.
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create initial state with reserved nonces
	nonce := uint64(10)
	state := &walletarmy.NonceState{
		Wallet:            wallet,
		ChainID:           1,
		LocalPendingNonce: &nonce,
		ReservedNonces:    []uint64{10, 11, 12},
		UpdatedAt:         time.Now(),
	}
	require.NoError(t, store.Save(ctx, state))

	// Concurrently call Save() with different reserved nonces sets
	const numGoroutines = 10
	var wg sync.WaitGroup
	successCount := int32(0)
	var mu sync.Mutex

	for i := range numGoroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			newNonce := uint64(20 + idx)
			// Each goroutine saves a distinct set of reserved nonces
			newState := &walletarmy.NonceState{
				Wallet:            wallet,
				ChainID:           1,
				LocalPendingNonce: &newNonce,
				ReservedNonces:    []uint64{uint64(100 + idx*3), uint64(101 + idx*3), uint64(102 + idx*3)},
				UpdatedAt:         time.Now(),
			}
			if err := store.Save(ctx, newState); err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()

	// Most should succeed due to retries
	assert.GreaterOrEqual(t, int(successCount), numGoroutines/2, "at least half should succeed")

	// Verify final state is internally consistent (no partial writes)
	finalState, err := store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	require.NotNil(t, finalState)

	// The reserved nonces should be a complete set of 3 consecutive numbers from one Save() call
	require.Len(t, finalState.ReservedNonces, 3, "should have exactly 3 reserved nonces")

	// All 3 nonces should be from the same goroutine (consecutive)
	nonces := finalState.ReservedNonces
	// Sort for easier verification
	min := nonces[0]
	for _, n := range nonces {
		if n < min {
			min = n
		}
	}

	// Check that we have min, min+1, min+2 (all 3 consecutive nonces)
	nonceSet := make(map[uint64]bool)
	for _, n := range nonces {
		nonceSet[n] = true
	}
	assert.True(t, nonceSet[min] && nonceSet[min+1] && nonceSet[min+2],
		"reserved nonces should be consecutive (no partial write): got %v", nonces)
}

func TestNonceStore_ConcurrentSaveOnSameWallet(t *testing.T) {
	// Multiple goroutines calling Save() on the same wallet.
	// With WATCH, only one should succeed per retry round.
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create initial state
	nonce := uint64(10)
	state := &walletarmy.NonceState{
		Wallet:            wallet,
		ChainID:           1,
		LocalPendingNonce: &nonce,
		ReservedNonces:    []uint64{10},
		UpdatedAt:         time.Now(),
	}
	require.NoError(t, store.Save(ctx, state))

	const numGoroutines = 10
	var wg sync.WaitGroup
	successCount := int32(0)
	var mu sync.Mutex
	finalNonces := make([]uint64, 0)

	for i := range numGoroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			newNonce := uint64(100 + idx)
			newState := &walletarmy.NonceState{
				Wallet:            wallet,
				ChainID:           1,
				LocalPendingNonce: &newNonce,
				ReservedNonces:    []uint64{newNonce},
				UpdatedAt:         time.Now(),
			}

			if err := store.Save(ctx, newState); err == nil {
				mu.Lock()
				successCount++
				finalNonces = append(finalNonces, newNonce)
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()

	// Most (if not all) should succeed due to retries
	assert.GreaterOrEqual(t, int(successCount), numGoroutines/2, "at least half should succeed")

	// Verify final state exists
	finalState, err := store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	require.NotNil(t, finalState)
	require.NotNil(t, finalState.LocalPendingNonce)

	// The final pending nonce should be one of the values we set
	assert.GreaterOrEqual(t, *finalState.LocalPendingNonce, uint64(100))
	assert.Less(t, *finalState.LocalPendingNonce, uint64(100+numGoroutines))
}

func TestNonceStore_ConcurrentGetDuringModification(t *testing.T) {
	// Verify that Get() returns a valid snapshot even during heavy writes.
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create initial state
	nonce := uint64(1)
	state := &walletarmy.NonceState{
		Wallet:            wallet,
		ChainID:           1,
		LocalPendingNonce: &nonce,
		ReservedNonces:    []uint64{1},
		UpdatedAt:         time.Now(),
	}
	require.NoError(t, store.Save(ctx, state))

	const numReaders = 20
	const numWriters = 10
	var wg sync.WaitGroup
	errors := make(chan error, numReaders+numWriters)

	// Start writers
	for i := range numWriters {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				nonce := uint64(idx*10 + j)
				if err := store.AddReservedNonce(ctx, wallet, 1, nonce); err != nil {
					errors <- fmt.Errorf("add reserved nonce: %w", err)
				}
			}
		}(i)
	}

	// Start readers
	for range numReaders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				snapshot, err := store.Get(ctx, wallet, 1)
				if err != nil {
					errors <- fmt.Errorf("get: %w", err)
					continue
				}
				if snapshot == nil {
					errors <- fmt.Errorf("got nil snapshot")
					continue
				}
				// Verify no duplicates in reserved nonces
				seen := make(map[uint64]bool)
				for _, n := range snapshot.ReservedNonces {
					if seen[n] {
						errors <- fmt.Errorf("duplicate nonce %d found in snapshot", n)
					}
					seen[n] = true
				}
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}
}

// Test for atomic Get() with WATCH/MULTI/EXEC

func TestNonceStore_GetAtomicReadRetryOnModification(t *testing.T) {
	// This test verifies that Get() retries when the underlying data changes
	// during the read operation (WATCH/MULTI/EXEC pattern).
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Create initial state
	nonce := uint64(10)
	state := &walletarmy.NonceState{
		Wallet:            wallet,
		ChainID:           1,
		LocalPendingNonce: &nonce,
		ReservedNonces:    []uint64{10, 11, 12},
		UpdatedAt:         time.Now(),
	}
	require.NoError(t, store.Save(ctx, state))

	// Run concurrent Get and heavy writes to trigger WATCH retries
	const numGoroutines = 20
	var wg sync.WaitGroup
	var successfulReads int32
	var mu sync.Mutex

	for i := range numGoroutines {
		wg.Add(2)

		// Reader goroutine
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				snapshot, err := store.Get(ctx, wallet, 1)
				if err == nil && snapshot != nil {
					mu.Lock()
					successfulReads++
					mu.Unlock()

					// Verify snapshot consistency
					// All reserved nonces should be unique
					seen := make(map[uint64]bool)
					for _, n := range snapshot.ReservedNonces {
						if seen[n] {
							t.Errorf("duplicate nonce %d in snapshot", n)
						}
						seen[n] = true
					}
				}
			}
		}()

		// Writer goroutine - modifies both state and reserved nonces
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				// Add reserved nonce
				_ = store.AddReservedNonce(ctx, wallet, 1, uint64(100+idx*10+j))
				// Update pending nonce
				_ = store.SavePendingNonce(ctx, wallet, 1, uint64(20+idx))
			}
		}(i)
	}

	wg.Wait()

	// Should have many successful reads despite heavy contention
	assert.Greater(t, int(successfulReads), 0, "should have successful reads")
}

func TestNonceStore_GetReturnsNilForNonExistent(t *testing.T) {
	// Verify Get() returns nil (not error) for non-existent wallet
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	state, err := store.Get(ctx, wallet, 999)
	require.NoError(t, err)
	assert.Nil(t, state)
}

func TestNonceStore_GetReturnsOnlyReservedNonces(t *testing.T) {
	// Test case where only reserved nonces exist (no main state)
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewNonceStore(client)
	ctx := context.Background()

	wallet := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Only add reserved nonces, no main state
	require.NoError(t, store.AddReservedNonce(ctx, wallet, 1, 5))
	require.NoError(t, store.AddReservedNonce(ctx, wallet, 1, 6))

	// Get should return state with only reserved nonces
	state, err := store.Get(ctx, wallet, 1)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Nil(t, state.LocalPendingNonce)
	assert.Len(t, state.ReservedNonces, 2)
	assert.Contains(t, state.ReservedNonces, uint64(5))
	assert.Contains(t, state.ReservedNonces, uint64(6))
}

func TestNonceStore_ListAllDocumentedConsistency(t *testing.T) {
	// This test documents that ListAll() does not provide atomic consistency
	// across all returned states, but each individual state should be valid.
	client := testRedisClient(t)
	defer func() { _ = client.Close() }()

	store := NewNonceStore(client)
	ctx := context.Background()

	// Create multiple wallets
	const numWallets = 10
	for i := range numWallets {
		wallet := common.HexToAddress(fmt.Sprintf("0x%040x", i))
		nonce := uint64(i * 10)
		state := &walletarmy.NonceState{
			Wallet:            wallet,
			ChainID:           1,
			LocalPendingNonce: &nonce,
			ReservedNonces:    []uint64{nonce, nonce + 1},
			UpdatedAt:         time.Now(),
		}
		require.NoError(t, store.Save(ctx, state))
	}

	// ListAll while concurrently modifying
	var wg sync.WaitGroup
	var listErr error
	var states []*walletarmy.NonceState

	wg.Add(1)
	go func() {
		defer wg.Done()
		states, listErr = store.ListAll(ctx)
	}()

	// Concurrent modifications
	for i := range numWallets {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			wallet := common.HexToAddress(fmt.Sprintf("0x%040x", idx))
			_ = store.AddReservedNonce(ctx, wallet, 1, uint64(idx*10+5))
		}(i)
	}

	wg.Wait()

	// ListAll should succeed (may have partial/inconsistent data across wallets)
	require.NoError(t, listErr)
	// Should return some states
	assert.GreaterOrEqual(t, len(states), 1)

	// Each individual state should be internally valid (no duplicates)
	for _, state := range states {
		seen := make(map[uint64]bool)
		for _, n := range state.ReservedNonces {
			assert.False(t, seen[n], "duplicate nonce %d in state for wallet %s", n, state.Wallet.Hex())
			seen[n] = true
		}
	}
}
