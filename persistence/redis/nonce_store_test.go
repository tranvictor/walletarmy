package redis

import (
	"context"
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
