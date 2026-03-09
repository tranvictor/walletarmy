package walletarmy

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tranvictor/jarvis/util/account"
)

func TestWalletManager_SetAccount(t *testing.T) {
	wm := NewWalletManager()
	acc, err := account.NewPrivateKeyAccount(testPrivateKeyHex1)
	require.NoError(t, err)

	wm.SetAccount(acc)

	retrieved := wm.Account(acc.Address())
	assert.NotNil(t, retrieved)
	assert.Equal(t, acc.Address(), retrieved.Address())
}

func TestWalletManager_Account_ReturnsNil_WhenNotSet(t *testing.T) {
	wm := NewWalletManager()
	unknownAddr := common.HexToAddress("0x9999999999999999999999999999999999999999")

	acc := wm.Account(unknownAddr)

	assert.Nil(t, acc)
}

func TestWalletManager_Account_MultipleAccounts(t *testing.T) {
	wm := NewWalletManager()
	acc1, err := account.NewPrivateKeyAccount(testPrivateKeyHex1)
	require.NoError(t, err)
	acc2, err := account.NewPrivateKeyAccount(testPrivateKeyHex2)
	require.NoError(t, err)

	wm.SetAccount(acc1)
	wm.SetAccount(acc2)

	retrieved1 := wm.Account(acc1.Address())
	retrieved2 := wm.Account(acc2.Address())

	assert.NotNil(t, retrieved1)
	assert.NotNil(t, retrieved2)
	assert.Equal(t, acc1.Address(), retrieved1.Address())
	assert.Equal(t, acc2.Address(), retrieved2.Address())
}

func TestWalletManager_Defaults_ThreadSafe(t *testing.T) {
	wm := NewWalletManager()

	var wg sync.WaitGroup
	iterations := 100

	// Writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(val int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				wm.SetDefaults(ManagerDefaults{NumRetries: val + j})
			}
		}(i)
	}

	// Readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = wm.Defaults()
			}
		}()
	}

	wg.Wait()
	// Test passes if no race condition occurs
}

func TestWalletManager_SetDefaults(t *testing.T) {
	wm := NewWalletManager()
	defaults := ManagerDefaults{
		NumRetries:    10,
		ExtraGasLimit: 5000,
	}

	wm.SetDefaults(defaults)

	retrieved := wm.Defaults()
	assert.Equal(t, 10, retrieved.NumRetries)
	assert.Equal(t, uint64(5000), retrieved.ExtraGasLimit)
}

func TestWalletManager_IdempotencyStore_ReturnsNil_WhenNotConfigured(t *testing.T) {
	wm := NewWalletManager()

	store := wm.IdempotencyStore()

	assert.Nil(t, store)
}

func TestWalletManager_GetWalletLock_CreatesSeparateLocks(t *testing.T) {
	wm := NewWalletManager()
	wallet1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	wallet2 := common.HexToAddress("0x2222222222222222222222222222222222222222")

	lock1 := wm.getWalletLock(wallet1)
	lock2 := wm.getWalletLock(wallet2)

	// Different wallets should have different lock instances
	assert.True(t, lock1 != lock2, "Different wallets should have different locks")

	// Same wallet should return same lock
	lock1Again := wm.getWalletLock(wallet1)
	assert.True(t, lock1 == lock1Again, "Same wallet should return same lock")
}

func TestWalletManager_GetNetworkLock_CreatesSeparateLocks(t *testing.T) {
	wm := NewWalletManager()
	chainID1 := uint64(1)
	chainID2 := uint64(56)

	lock1 := wm.getNetworkLock(chainID1)
	lock2 := wm.getNetworkLock(chainID2)

	// Different networks should have different lock instances
	assert.True(t, lock1 != lock2, "Different networks should have different locks")

	// Same network should return same lock
	lock1Again := wm.getNetworkLock(chainID1)
	assert.True(t, lock1 == lock1Again, "Same network should return same lock")
}

func TestWalletManager_GetCircuitBreaker_CreatesSeparateBreakers(t *testing.T) {
	wm := NewWalletManager()
	chainID1 := uint64(1)
	chainID2 := uint64(56)

	cb1 := wm.getCircuitBreaker(chainID1)
	cb2 := wm.getCircuitBreaker(chainID2)

	// Different networks should have different circuit breaker instances
	assert.True(t, cb1 != cb2, "Different networks should have different circuit breakers")

	// Same network should return same circuit breaker
	cb1Again := wm.getCircuitBreaker(chainID1)
	assert.True(t, cb1 == cb1Again, "Same network should return same circuit breaker")
}

func TestWalletManager_R_CreatesNewRequestEachTime(t *testing.T) {
	wm := NewWalletManager()

	req1 := wm.R()
	req2 := wm.R()

	// Each call to R() should return a new request instance
	assert.True(t, req1 != req2, "Each R() call should return a new request instance")
}

func TestWalletManager_SetAccount_OverwritesPrevious(t *testing.T) {
	wm := NewWalletManager()
	acc1, err := account.NewPrivateKeyAccount(testPrivateKeyHex1)
	require.NoError(t, err)

	// Set account
	wm.SetAccount(acc1)

	// Create another account with same address (would be same key)
	acc2, err := account.NewPrivateKeyAccount(testPrivateKeyHex1)
	require.NoError(t, err)

	// Set again (should overwrite)
	wm.SetAccount(acc2)

	retrieved := wm.Account(acc1.Address())
	assert.NotNil(t, retrieved)
	// Both acc1 and acc2 have same address, so this should work
	assert.Equal(t, acc2.Address(), retrieved.Address())
}

func TestWalletManager_ConcurrentAccountAccess(t *testing.T) {
	wm := NewWalletManager()
	acc, err := account.NewPrivateKeyAccount(testPrivateKeyHex1)
	require.NoError(t, err)

	var wg sync.WaitGroup

	// Writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				wm.SetAccount(acc)
			}
		}()
	}

	// Readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = wm.Account(acc.Address())
			}
		}()
	}

	wg.Wait()
	// Test passes if no race condition occurs
}

func TestWalletManager_NonceTracker_Initialized(t *testing.T) {
	wm := NewWalletManager()

	// The nonce tracker should be initialized
	assert.NotNil(t, wm.nonceTracker)
}

// testPrivateKeyHex are hex-encoded test private keys
var (
	testPrivateKeyHex1 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testPrivateKeyHex2 = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)
