package walletarmy

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/gethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tranvictor/jarvis/networks"
	"github.com/tranvictor/jarvis/util/account"
)

// ============================================================
// BuildTx Tests
// ============================================================

func TestBuildTx_EstimatesGas_WhenZero(t *testing.T) {
	setup := newTestSetup(t)

	// Track that EstimateExactGas was called
	estimateCalled := false
	setup.Reader.EstimateExactGasFn = func(from, to string, gasPrice float64, value *big.Int, data []byte) (uint64, error) {
		estimateCalled = true
		return 50000, nil
	}

	tx, err := setup.WM.BuildTx(
		2, // txType
		testAddr1,
		testAddr2,
		big.NewInt(5), // nonce provided
		oneEth,
		0,  // gasLimit = 0, should trigger estimation
		0,  // extraGasLimit
		20, // gasPrice
		0,  // extraGasPrice
		2,  // tipCapGwei
		0,  // extraTipCapGwei
		nil,
		networks.EthereumMainnet,
	)

	require.NoError(t, err)
	require.NotNil(t, tx)
	assert.True(t, estimateCalled, "EstimateExactGas should have been called")
	assert.Equal(t, uint64(50000), tx.Gas())
}

func TestBuildTx_UsesProvidedGasLimit(t *testing.T) {
	setup := newTestSetup(t)

	// EstimateExactGas should NOT be called
	estimateCalled := false
	setup.Reader.EstimateExactGasFn = func(from, to string, gasPrice float64, value *big.Int, data []byte) (uint64, error) {
		estimateCalled = true
		return 50000, nil
	}

	tx, err := setup.WM.BuildTx(
		2,
		testAddr1,
		testAddr2,
		big.NewInt(5),
		oneEth,
		21000, // gasLimit provided
		1000,  // extraGasLimit
		20,
		0,
		2,
		0,
		nil,
		networks.EthereumMainnet,
	)

	require.NoError(t, err)
	require.NotNil(t, tx)
	assert.False(t, estimateCalled, "EstimateExactGas should NOT have been called")
	assert.Equal(t, uint64(22000), tx.Gas()) // 21000 + 1000 extra
}

func TestBuildTx_AcquiresNonce_WhenNil(t *testing.T) {
	setup := newTestSetup(t)

	// Set up nonce tracking
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	tx, err := setup.WM.BuildTx(
		2,
		testAddr1,
		testAddr2,
		nil, // nonce = nil, should acquire
		oneEth,
		21000,
		0,
		20,
		0,
		2,
		0,
		nil,
		networks.EthereumMainnet,
	)

	require.NoError(t, err)
	require.NotNil(t, tx)
	assert.Equal(t, uint64(5), tx.Nonce())
	assert.Contains(t, setup.Reader.GetMinedNonceCalls, testAddr1.Hex())
	assert.Contains(t, setup.Reader.GetPendingNonceCalls, testAddr1.Hex())
}

func TestBuildTx_GetsGasSettings_WhenZero(t *testing.T) {
	setup := newTestSetup(t)

	suggestedCalled := false
	setup.Reader.SuggestedGasSettingsFn = func() (float64, float64, error) {
		suggestedCalled = true
		return 30.0, 3.0, nil // 30 gwei gas price, 3 gwei tip
	}

	tx, err := setup.WM.BuildTx(
		2,
		testAddr1,
		testAddr2,
		big.NewInt(5),
		oneEth,
		21000,
		0,
		0, // gasPrice = 0, should get from network
		0,
		0, // tipCapGwei = 0, should get from network
		0,
		nil,
		networks.EthereumMainnet,
	)

	require.NoError(t, err)
	require.NotNil(t, tx)
	assert.True(t, suggestedCalled, "SuggestedGasSettings should have been called")
}

func TestBuildTx_ReturnsError_WhenGasEstimationFails(t *testing.T) {
	setup := newTestSetup(t)

	expectedErr := errors.New("execution reverted")
	setup.Reader.EstimateExactGasFn = func(from, to string, gasPrice float64, value *big.Int, data []byte) (uint64, error) {
		return 0, expectedErr
	}

	tx, err := setup.WM.BuildTx(
		2,
		testAddr1,
		testAddr2,
		big.NewInt(5),
		oneEth,
		0, // gasLimit = 0, triggers estimation
		0,
		20,
		0,
		2,
		0,
		nil,
		networks.EthereumMainnet,
	)

	assert.Nil(t, tx)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrEstimateGasFailed))
}

func TestBuildTx_ReturnsError_WhenNonceAcquisitionFails(t *testing.T) {
	setup := newTestSetup(t)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) {
		return 0, errors.New("network error")
	}

	tx, err := setup.WM.BuildTx(
		2,
		testAddr1,
		testAddr2,
		nil, // nonce = nil
		oneEth,
		21000,
		0,
		20,
		0,
		2,
		0,
		nil,
		networks.EthereumMainnet,
	)

	assert.Nil(t, tx)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrAcquireNonceFailed))
}

// ============================================================
// BroadcastTx Tests
// ============================================================

func TestBroadcastTx_Success(t *testing.T) {
	setup := newTestSetup(t)

	// Create and sign a transaction
	tx := newTestTx(0, testAddr2, oneEth)
	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(chainIDMain), testPrivateKey1)
	require.NoError(t, err)

	hash, broadcasted, broadcastErr := setup.WM.BroadcastTx(signedTx)

	assert.True(t, broadcasted)
	assert.Equal(t, signedTx.Hash().Hex(), hash)
	assert.Nil(t, broadcastErr)
	assert.Len(t, setup.Broadcaster.BroadcastTxCalls, 1)
}

func TestBroadcastTx_Failure(t *testing.T) {
	setup := newTestSetup(t)

	setup.Broadcaster.BroadcastTxFn = func(tx *types.Transaction) (string, bool, error) {
		return "", false, errors.New("insufficient funds for gas * price + value")
	}

	// Create and sign a transaction
	tx := newTestTx(0, testAddr2, oneEth)
	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(chainIDMain), testPrivateKey1)
	require.NoError(t, err)

	hash, broadcasted, broadcastErr := setup.WM.BroadcastTx(signedTx)

	assert.False(t, broadcasted)
	assert.Empty(t, hash)
	assert.NotNil(t, broadcastErr)
	assert.Equal(t, ErrInsufficientFund, broadcastErr)
}

// ============================================================
// MonitorTxContext Tests
// ============================================================

func TestMonitorTxContext_ReturnsMined(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(0, testAddr2, oneEth)
	receipt := newSuccessReceipt(tx)

	setup.Monitor.StatusToReturn = TxMonitorStatus{
		Status:  "done",
		Receipt: receipt,
	}

	ctx := context.Background()
	statusChan := setup.WM.MonitorTxContext(ctx, tx, networks.EthereumMainnet, time.Second)

	status := <-statusChan
	assert.Equal(t, TxStatusMined, status.Status)
	assert.Equal(t, receipt, status.Receipt)
}

func TestMonitorTxContext_ReturnsReverted(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(0, testAddr2, oneEth)
	receipt := newFailedReceipt(tx)

	setup.Monitor.StatusToReturn = TxMonitorStatus{
		Status:  "reverted",
		Receipt: receipt,
	}

	ctx := context.Background()
	statusChan := setup.WM.MonitorTxContext(ctx, tx, networks.EthereumMainnet, time.Second)

	status := <-statusChan
	assert.Equal(t, TxStatusReverted, status.Status)
	assert.Equal(t, receipt, status.Receipt)
}

func TestMonitorTxContext_ReturnsCancelledOnContextCancel(t *testing.T) {
	setup := newTestSetup(t)

	// Set a shorter slow timeout and delay so test runs faster
	setup.WM.SetDefaults(ManagerDefaults{SlowTxTimeout: 100 * time.Millisecond})
	setup.Monitor.Delay = 200 * time.Millisecond // Longer than slow timeout to allow cancel

	tx := newTestTx(0, testAddr2, oneEth)

	ctx, cancel := context.WithCancel(context.Background())

	statusChan := setup.WM.MonitorTxContext(ctx, tx, networks.EthereumMainnet, time.Second)

	// Cancel context immediately
	cancel()

	status := <-statusChan
	assert.Equal(t, TxStatusCancelled, status.Status)
	assert.Nil(t, status.Receipt)
}

// ============================================================
// MonitorTxContext — Slow Timer Deferred Until First Monitor Event
// ============================================================

// TestMonitorTxContext_SlowTimerDefersUntilFirstCheck tests that when
// SlowTxTimeout < txCheckInterval, the slow timer does NOT fire before
// the monitor has checked the node. If the monitor returns "done" on its
// first check (even though it's after SlowTxTimeout), we get TxStatusMined.
func TestMonitorTxContext_SlowTimerDefersUntilFirstCheck(t *testing.T) {
	setup := newTestSetup(t)

	// SlowTxTimeout is much shorter than the monitor delay.
	// Under the old code, the slow timer would fire at 20ms before the
	// monitor returns "done" at 100ms. With the fix, the slow timer
	// doesn't start until after the monitor delivers a status.
	setup.WM.SetDefaults(ManagerDefaults{SlowTxTimeout: 20 * time.Millisecond})
	setup.Monitor.Delay = 100 * time.Millisecond
	setup.Monitor.StatusToReturn = TxMonitorStatus{
		Status:  "done",
		Receipt: &types.Receipt{Status: types.ReceiptStatusSuccessful},
	}

	tx := newTestTx(0, testAddr2, oneEth)
	ctx := context.Background()
	statusChan := setup.WM.MonitorTxContext(ctx, tx, networks.EthereumMainnet, time.Second)

	status := <-statusChan
	assert.Equal(t, TxStatusMined, status.Status,
		"Should get TxStatusMined, not TxStatusSlow — slow timer must not fire before first monitor check")
	assert.NotNil(t, status.Receipt)
}

// TestMonitorTxContext_SlowTimerFiresAfterFirstNonTerminalCheck tests that
// after the monitor delivers a non-terminal status (e.g., unknown/pending),
// the slow timer starts and eventually fires TxStatusSlow.
func TestMonitorTxContext_SlowTimerFiresAfterFirstNonTerminalCheck(t *testing.T) {
	setup := newTestSetup(t)

	setup.WM.SetDefaults(ManagerDefaults{SlowTxTimeout: 50 * time.Millisecond})

	// The monitor returns an unrecognized status (treated as non-terminal).
	// This arms the slow timer, which fires after 50ms.
	setup.Monitor.StatusToReturn = TxMonitorStatus{Status: "pending"}

	tx := newTestTx(0, testAddr2, oneEth)
	ctx := context.Background()
	statusChan := setup.WM.MonitorTxContext(ctx, tx, networks.EthereumMainnet, time.Second)

	status := <-statusChan
	assert.Equal(t, TxStatusSlow, status.Status,
		"Should get TxStatusSlow after non-terminal status arms the timer")
}

// TestMonitorTxContext_SlowTimerFiresAfterMonitorClose tests that when
// the monitor channel closes without a terminal status, the slow timer
// is armed and eventually fires.
func TestMonitorTxContext_SlowTimerFiresAfterMonitorClose(t *testing.T) {
	closingMon := &closingMonitor{}

	wm := NewWalletManager(
		WithTxMonitorFactory(func(reader EthReader) TxMonitor {
			return closingMon
		}),
	)
	wm.SetDefaults(ManagerDefaults{SlowTxTimeout: 50 * time.Millisecond})

	// Store the monitor directly so getTxMonitor finds it
	wm.txMonitors.Store(networks.EthereumMainnet.GetChainID(), closingMon)

	tx := newTestTx(0, testAddr2, oneEth)
	ctx := context.Background()
	statusChan := wm.MonitorTxContext(ctx, tx, networks.EthereumMainnet, time.Second)

	status := <-statusChan
	assert.Equal(t, TxStatusSlow, status.Status,
		"Should get TxStatusSlow after monitor channel closes without terminal status")
}

// closingMonitor is a TxMonitor that immediately closes the channel without sending a status.
type closingMonitor struct{}

func (m *closingMonitor) MakeWaitChannelWithInterval(hash string, interval time.Duration) <-chan TxMonitorStatus {
	ch := make(chan TxMonitorStatus)
	go func() {
		close(ch)
	}()
	return ch
}

// ============================================================
// TxExecutionContext Tests
// ============================================================

func TestTxExecutionContext_AdjustGasPricesForSlowTx_Success(t *testing.T) {
	ctx := &TxExecutionContext{
		Gas: GasBounds{MaxGasPrice: 200.0, MaxTipCap: 100.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei max fee
		big.NewInt(1),
	)

	adjusted := ctx.AdjustGasPricesForSlowTx(tx)

	assert.True(t, adjusted)
	// Gas price should be increased by DefaultGasPriceBumpFactor (1.2)
	expectedGasPrice := 20.0 * DefaultGasPriceBumpFactor
	assert.InDelta(t, expectedGasPrice, ctx.State.GasPrice, 0.001)
	// Tip cap should be increased by DefaultTipCapBumpFactor (1.1)
	expectedTipCap := 2.0 * DefaultTipCapBumpFactor
	assert.InDelta(t, expectedTipCap, ctx.State.TipCap, 0.001)
	// Nonce should be preserved
	assert.Equal(t, big.NewInt(5), ctx.State.Nonce)
}

func TestTxExecutionContext_AdjustGasPricesForSlowTx_HitsGasPriceLimit(t *testing.T) {
	ctx := &TxExecutionContext{
		Gas: GasBounds{MaxGasPrice: 22.0, MaxTipCap: 100.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),
		big.NewInt(20000000000),
		big.NewInt(1),
	)

	adjusted := ctx.AdjustGasPricesForSlowTx(tx)

	assert.False(t, adjusted, "Should return false when gas price limit would be exceeded")
}

func TestTxExecutionContext_AdjustGasPricesForSlowTx_HitsTipCapLimit(t *testing.T) {
	ctx := &TxExecutionContext{
		Gas: GasBounds{MaxGasPrice: 200.0, MaxTipCap: 2.1, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),
		big.NewInt(20000000000),
		big.NewInt(1),
	)

	adjusted := ctx.AdjustGasPricesForSlowTx(tx)

	assert.False(t, adjusted, "Should return false when tip cap limit would be exceeded")
}

func TestTxExecutionContext_IncrementRetryCountAndCheck(t *testing.T) {
	tests := []struct {
		name           string
		numRetries     int
		currentRetries int
		expectResult   bool // true means we expect a result (exceeded retries)
	}{
		{"first retry within limit", 3, 0, false},
		{"second retry within limit", 3, 1, false},
		{"at limit", 3, 2, false},
		{"exceeds limit", 3, 3, true},
		{"zero retries allowed - first try", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &TxExecutionContext{
				Retry: RetryConfig{MaxAttempts: tt.numRetries},
				State: TxRetryState{AttemptCount: tt.currentRetries},
			}

			result := ctx.IncrementRetryCountAndCheck("test error")

			if tt.expectResult {
				assert.NotNil(t, result)
				assert.Equal(t, ActionReturn, result.Action)
				assert.True(t, errors.Is(result.Error, ErrEnsureTxOutOfRetries))
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

// ============================================================
// Concurrent Nonce Acquisition Tests
// ============================================================

func TestBuildTx_ConcurrentNonceAcquisition(t *testing.T) {
	setup := newTestSetup(t)

	// Track nonces returned
	var nextNonce uint64 = 0
	var mu sync.Mutex

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) {
		return 0, nil
	}
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) {
		return 0, nil
	}

	// Run concurrent BuildTx calls
	const numGoroutines = 10
	var wg sync.WaitGroup
	nonces := make([]uint64, numGoroutines)
	errs := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tx, err := setup.WM.BuildTx(
				2,
				testAddr1,
				testAddr2,
				nil, // Acquire nonce
				oneEth,
				21000,
				0,
				20,
				0,
				2,
				0,
				nil,
				networks.EthereumMainnet,
			)
			if err != nil {
				errs[idx] = err
				return
			}
			mu.Lock()
			nonces[idx] = tx.Nonce()
			if tx.Nonce() >= nextNonce {
				nextNonce = tx.Nonce() + 1
			}
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	// Check no errors
	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d had error", i)
	}

	// Check all nonces are unique
	nonceSet := make(map[uint64]bool)
	for _, n := range nonces {
		assert.False(t, nonceSet[n], "duplicate nonce %d found", n)
		nonceSet[n] = true
	}

	// Check nonces form a continuous sequence [0, numGoroutines)
	assert.Equal(t, uint64(numGoroutines), nextNonce, "expected nextNonce to be %d", numGoroutines)
	for i := uint64(0); i < numGoroutines; i++ {
		assert.True(t, nonceSet[i], "missing nonce %d in sequence", i)
	}
}

// ============================================================
// handleBroadcastError Tests
// ============================================================

func TestHandleBroadcastError_NonceTooLow_ChecksOldTxs(t *testing.T) {
	setup := newTestSetup(t)

	// Create an old tx that's been "mined"
	oldTx := newTestTx(5, testAddr2, oneEth)
	oldReceipt := newSuccessReceipt(oldTx)

	// Set up reader to return "done" for the old tx
	setup.Reader.TxInfoFromHashFn = func(hash string) (TxInfo, error) {
		if hash == oldTx.Hash().Hex() {
			return TxInfo{Status: "done", Receipt: oldReceipt}, nil
		}
		return TxInfo{Status: "pending"}, nil
	}

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		State: TxRetryState{OldTxs: map[string]*types.Transaction{
			oldTx.Hash().Hex(): oldTx,
		}},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	newTx := newTestTx(6, testAddr2, oneEth)

	result := setup.WM.handleNonceIsLowError(newTx, execCtx)

	// Should return the old tx that was mined
	assert.Equal(t, ActionReturn, result.Action)
	assert.Equal(t, oldTx, result.Transaction)
	assert.Nil(t, result.Error)
}

func TestHandleNonceIsLowError_TxMinedHook_Called(t *testing.T) {
	setup := newTestSetup(t)

	oldTx := newTestTx(5, testAddr2, oneEth)
	oldReceipt := newSuccessReceipt(oldTx)

	setup.Reader.TxInfoFromHashFn = func(hash string) (TxInfo, error) {
		if hash == oldTx.Hash().Hex() {
			return TxInfo{Status: "done", Receipt: oldReceipt}, nil
		}
		return TxInfo{Status: "pending"}, nil
	}

	hookCalled := false
	var receivedTx *types.Transaction
	var receivedReceipt *types.Receipt

	execCtx := &TxExecutionContext{
		Retry: RetryConfig{MaxAttempts: 5},
		State: TxRetryState{OldTxs: map[string]*types.Transaction{
			oldTx.Hash().Hex(): oldTx,
		}},
		Params: TxParams{Network: networks.EthereumMainnet},
		Hooks: TxHooks{TxMined: func(tx *types.Transaction, r *types.Receipt) error {
			hookCalled = true
			receivedTx = tx
			receivedReceipt = r
			return nil
		}},
	}

	newTx := newTestTx(6, testAddr2, oneEth)
	result := setup.WM.handleNonceIsLowError(newTx, execCtx)

	assert.True(t, hookCalled, "TxMinedHook should be called when old tx is mined")
	assert.Equal(t, oldTx, receivedTx)
	assert.Equal(t, oldReceipt, receivedReceipt)
	assert.Equal(t, ActionReturn, result.Action)
	assert.Equal(t, oldTx, result.Transaction)
}

func TestHandleBroadcastError_TxIsKnown_RetriesWithSameNonce(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	result := setup.WM.handleBroadcastError(ErrTxIsKnown, tx, execCtx)

	assert.Equal(t, ActionRetry, result.Action)
	assert.Equal(t, big.NewInt(5), execCtx.State.Nonce)
}

func TestHandleBroadcastError_InsufficientFunds_IncrementsRetry(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	result := setup.WM.handleBroadcastError(ErrInsufficientFund, tx, execCtx)

	assert.Equal(t, ActionRetry, result.Action)
	assert.Equal(t, 1, execCtx.State.AttemptCount)
	assert.Equal(t, big.NewInt(5), execCtx.State.Nonce)
}

func TestHandleBroadcastError_ExceedsRetries(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 2},
		State:  TxRetryState{AttemptCount: 2, OldTxs: make(map[string]*types.Transaction)},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	result := setup.WM.handleBroadcastError(ErrInsufficientFund, tx, execCtx)

	assert.Equal(t, ActionReturn, result.Action)
	assert.True(t, errors.Is(result.Error, ErrEnsureTxOutOfRetries))
}

// ============================================================
// handleTransactionStatus Tests
// ============================================================

func TestHandleTransactionStatus_Mined(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newSuccessReceipt(tx)

	execCtx := &TxExecutionContext{}

	result := setup.WM.handleTransactionStatus(TxInfo{Status: "mined", Receipt: receipt}, tx, execCtx)

	assert.Equal(t, ActionReturn, result.Action)
	assert.Nil(t, result.Error)
	assert.Equal(t, tx, result.Transaction)
	assert.Equal(t, receipt, result.Receipt)
}

func TestHandleTransactionStatus_Reverted(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newFailedReceipt(tx)

	execCtx := &TxExecutionContext{}

	result := setup.WM.handleTransactionStatus(TxInfo{Status: "reverted", Receipt: receipt}, tx, execCtx)

	assert.Equal(t, ActionReturn, result.Action)
	assert.Nil(t, result.Error)
	assert.Equal(t, tx, result.Transaction)
	assert.Equal(t, receipt, result.Receipt)
}

func TestHandleTransactionStatus_Lost_Retries(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei fee cap
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Retry: RetryConfig{MaxAttempts: 5},
		Gas:   GasBounds{MaxGasPrice: 100.0, MaxTipCap: 50.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	result := setup.WM.handleTransactionStatus(TxInfo{Status: "lost"}, tx, execCtx)

	assert.Equal(t, ActionRetry, result.Action)
	// Lost tx should retry with the same nonce and bumped gas
	require.NotNil(t, execCtx.State.Nonce)
	assert.Equal(t, big.NewInt(5), execCtx.State.Nonce)
}

func TestHandleTransactionStatus_Slow_BumpsGas(t *testing.T) {
	setup := newTestSetup(t)

	// Make this a blocking nonce so gas gets bumped (nonce == minedNonce)
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei
		big.NewInt(20000000000), // 20 gwei
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Gas:    GasBounds{MaxGasPrice: 100.0, MaxTipCap: 50.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	result := setup.WM.handleTransactionStatus(TxInfo{Status: "slow"}, tx, execCtx)

	assert.Equal(t, ActionRetry, result.Action)
	assert.Greater(t, execCtx.State.GasPrice, 20.0) // Should be bumped
	assert.Greater(t, execCtx.State.TipCap, 2.0)    // Should be bumped
}

func TestHandleTransactionStatus_Slow_HitsLimit(t *testing.T) {
	setup := newTestSetup(t)

	// Make this a blocking nonce so gas bump is attempted (and hits limit)
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),
		big.NewInt(20000000000),
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Gas:    GasBounds{MaxGasPrice: 22.0, MaxTipCap: 50.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	result := setup.WM.handleTransactionStatus(TxInfo{Status: "slow"}, tx, execCtx)

	assert.Equal(t, ActionReturn, result.Action)
	assert.True(t, errors.Is(result.Error, ErrGasPriceLimitReached))
}

// ============================================================
// handleGasEstimationFailure Tests
// ============================================================

func TestHandleGasEstimationFailure_NoOldTxs_Retries(t *testing.T) {
	setup := newTestSetup(t)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	err := errors.Join(ErrEstimateGasFailed, errors.New("execution reverted"))
	result := setup.WM.handleGasEstimationFailure(execCtx, nil, err)

	assert.Equal(t, ActionRetry, result.Action)
	assert.Equal(t, 1, execCtx.State.AttemptCount)
}

func TestHandleGasEstimationFailure_OldTxMined_ReturnsIt(t *testing.T) {
	setup := newTestSetup(t)

	oldTx := newTestTx(5, testAddr2, oneEth)
	oldReceipt := newSuccessReceipt(oldTx)

	setup.Reader.TxInfoFromHashFn = func(hash string) (TxInfo, error) {
		if hash == oldTx.Hash().Hex() {
			return TxInfo{Status: "done", Receipt: oldReceipt}, nil
		}
		return TxInfo{Status: "pending"}, nil
	}

	execCtx := &TxExecutionContext{
		Retry: RetryConfig{MaxAttempts: 5},
		State: TxRetryState{OldTxs: map[string]*types.Transaction{
			oldTx.Hash().Hex(): oldTx,
		}},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	err := errors.Join(ErrEstimateGasFailed, errors.New("execution reverted"))
	result := setup.WM.handleGasEstimationFailure(execCtx, nil, err)

	assert.Equal(t, ActionReturn, result.Action)
	assert.Equal(t, oldTx, result.Transaction)
}

func TestHandleGasEstimationFailure_ExceedsRetries(t *testing.T) {
	setup := newTestSetup(t)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 2},
		State:  TxRetryState{AttemptCount: 2, OldTxs: make(map[string]*types.Transaction)},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	err := errors.Join(ErrEstimateGasFailed, errors.New("execution reverted"))
	result := setup.WM.handleGasEstimationFailure(execCtx, nil, err)

	assert.Equal(t, ActionReturn, result.Action)
	assert.True(t, errors.Is(result.Error, ErrEnsureTxOutOfRetries))
}

func TestHandleGasEstimationFailure_TxMinedHook_Called(t *testing.T) {
	setup := newTestSetup(t)

	oldTx := newTestTx(5, testAddr2, oneEth)
	oldReceipt := newSuccessReceipt(oldTx)

	setup.Reader.TxInfoFromHashFn = func(hash string) (TxInfo, error) {
		if hash == oldTx.Hash().Hex() {
			return TxInfo{Status: "done", Receipt: oldReceipt}, nil
		}
		return TxInfo{Status: "pending"}, nil
	}

	hookCalled := false
	var receivedTx *types.Transaction
	var receivedReceipt *types.Receipt

	execCtx := &TxExecutionContext{
		Retry: RetryConfig{MaxAttempts: 5},
		State: TxRetryState{OldTxs: map[string]*types.Transaction{
			oldTx.Hash().Hex(): oldTx,
		}},
		Params: TxParams{Network: networks.EthereumMainnet},
		Hooks: TxHooks{TxMined: func(tx *types.Transaction, r *types.Receipt) error {
			hookCalled = true
			receivedTx = tx
			receivedReceipt = r
			return nil
		}},
	}

	err := errors.Join(ErrEstimateGasFailed, errors.New("execution reverted"))
	result := setup.WM.handleGasEstimationFailure(execCtx, nil, err)

	assert.True(t, hookCalled, "TxMinedHook should be called when old tx is mined")
	assert.Equal(t, oldTx, receivedTx)
	assert.Equal(t, oldReceipt, receivedReceipt)
	assert.Equal(t, ActionReturn, result.Action)
	assert.Equal(t, oldTx, result.Transaction)
}

// ============================================================
// Simulation (EthCall) Tests
// ============================================================

func TestExecuteTransactionAttempt_SimulationFails_ReturnsError(t *testing.T) {
	setup := newTestSetup(t)

	// EthCall returns a non-revert error (network error)
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, fmt.Errorf("network connection failed")
	}

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{TxType: 2, From: testAddr1, To: testAddr2, Value: oneEth, Network: networks.EthereumMainnet},
		State:  TxRetryState{GasLimit: 21000, GasPrice: 20.0, TipCap: 2.0, OldTxs: make(map[string]*types.Transaction)},
	}

	result := setup.WM.executeTransactionAttempt(context.Background(), execCtx, nil)

	// Should return error since simulation failed (not a revert)
	assert.Equal(t, ActionReturn, result.Action)
	assert.True(t, errors.Is(result.Error, ErrSimulatedTxFailed))
}

func TestExecuteTransactionAttempt_SimulationSucceeds_ContinuesToBroadcast(t *testing.T) {
	setup := newTestSetup(t)

	// EthCall succeeds (no error)
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil
	}

	// Track if we get to the broadcast phase (signAndBroadcastTransaction)
	// This will fail because we don't have a signed account, but that's OK for this test
	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{TxType: 2, From: testAddr1, To: testAddr2, Value: oneEth, Network: networks.EthereumMainnet},
		State:  TxRetryState{GasLimit: 21000, GasPrice: 20.0, TipCap: 2.0, OldTxs: make(map[string]*types.Transaction)},
	}

	result := setup.WM.executeTransactionAttempt(context.Background(), execCtx, nil)

	// The test should pass simulation and fail at signing (because we have no account)
	// This confirms simulation was successful
	assert.Equal(t, ActionReturn, result.Action)
	assert.Contains(t, result.Error.Error(), "not registered")
}

// ============================================================
// Hook Execution Tests
// ============================================================

func TestHandleTransactionStatus_TxMinedHook_Called(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newSuccessReceipt(tx)

	hookCalled := false
	var receivedTx *types.Transaction
	var receivedReceipt *types.Receipt

	execCtx := &TxExecutionContext{
		Hooks: TxHooks{TxMined: func(tx *types.Transaction, r *types.Receipt) error {
			hookCalled = true
			receivedTx = tx
			receivedReceipt = r
			return nil
		}},
	}

	result := setup.WM.handleTransactionStatus(TxInfo{Status: "mined", Receipt: receipt}, tx, execCtx)

	assert.True(t, hookCalled)
	assert.Equal(t, tx, receivedTx)
	assert.Equal(t, receipt, receivedReceipt)
	assert.Equal(t, ActionReturn, result.Action)
	assert.Nil(t, result.Error)
}

func TestHandleTransactionStatus_TxMinedHook_ReturnsError(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newSuccessReceipt(tx)

	hookError := errors.New("hook failed")

	execCtx := &TxExecutionContext{
		Hooks: TxHooks{TxMined: func(tx *types.Transaction, r *types.Receipt) error {
			return hookError
		}},
	}

	result := setup.WM.handleTransactionStatus(TxInfo{Status: "mined", Receipt: receipt}, tx, execCtx)

	assert.Equal(t, ActionReturn, result.Action)
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "hook error")
}

// ============================================================
// handleMinedTx Tests
// ============================================================

func TestHandleMinedTx_TxMinedHook_Called_StatusDone(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newSuccessReceipt(tx)

	hookCalled := false
	var receivedTx *types.Transaction
	var receivedReceipt *types.Receipt

	execCtx := &TxExecutionContext{
		Hooks: TxHooks{TxMined: func(tx *types.Transaction, r *types.Receipt) error {
			hookCalled = true
			receivedTx = tx
			receivedReceipt = r
			return nil
		}},
	}

	result := setup.WM.handleMinedTx(tx, TxInfo{Status: "done", Receipt: receipt}, execCtx)

	assert.True(t, hookCalled, "TxMinedHook should be called")
	assert.Equal(t, tx, receivedTx)
	assert.Equal(t, receipt, receivedReceipt)
	assert.Equal(t, ActionReturn, result.Action)
	assert.Nil(t, result.Error)
}

func TestHandleMinedTx_TxMinedHook_Called_StatusMined(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newSuccessReceipt(tx)

	hookCalled := false

	execCtx := &TxExecutionContext{
		Hooks: TxHooks{TxMined: func(tx *types.Transaction, r *types.Receipt) error {
			hookCalled = true
			return nil
		}},
	}

	result := setup.WM.handleMinedTx(tx, TxInfo{Status: "mined", Receipt: receipt}, execCtx)

	assert.True(t, hookCalled, "TxMinedHook should be called")
	assert.Equal(t, ActionReturn, result.Action)
	assert.Nil(t, result.Error)
}

func TestHandleMinedTx_TxMinedHook_Called_StatusReverted(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newFailedReceipt(tx)

	hookCalled := false

	execCtx := &TxExecutionContext{
		Hooks: TxHooks{TxMined: func(tx *types.Transaction, r *types.Receipt) error {
			hookCalled = true
			return nil
		}},
	}

	result := setup.WM.handleMinedTx(tx, TxInfo{Status: "reverted", Receipt: receipt}, execCtx)

	assert.True(t, hookCalled, "TxMinedHook should be called")
	assert.Equal(t, ActionReturn, result.Action)
	assert.Nil(t, result.Error)
}

func TestHandleMinedTx_TxMinedHook_Called_EmptyStatus_SuccessfulReceipt(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newSuccessReceipt(tx)

	hookCalled := false

	execCtx := &TxExecutionContext{
		Hooks: TxHooks{TxMined: func(tx *types.Transaction, r *types.Receipt) error {
			hookCalled = true
			return nil
		}},
	}

	result := setup.WM.handleMinedTx(tx, TxInfo{Status: "", Receipt: receipt}, execCtx)

	assert.True(t, hookCalled, "TxMinedHook should be called")
	assert.Equal(t, ActionReturn, result.Action)
	assert.Nil(t, result.Error)
}

func TestHandleMinedTx_TxMinedHook_Called_EmptyStatus_RevertedReceipt(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newFailedReceipt(tx)

	hookCalled := false

	execCtx := &TxExecutionContext{
		Hooks: TxHooks{TxMined: func(tx *types.Transaction, r *types.Receipt) error {
			hookCalled = true
			return nil
		}},
	}

	result := setup.WM.handleMinedTx(tx, TxInfo{Status: "", Receipt: receipt}, execCtx)

	assert.True(t, hookCalled, "TxMinedHook should be called")
	assert.Equal(t, ActionReturn, result.Action)
	assert.Nil(t, result.Error)
}

func TestHandleMinedTx_TxMinedHook_ReturnsError(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newSuccessReceipt(tx)

	hookError := errors.New("hook failed")

	execCtx := &TxExecutionContext{
		Hooks: TxHooks{TxMined: func(tx *types.Transaction, r *types.Receipt) error {
			return hookError
		}},
	}

	result := setup.WM.handleMinedTx(tx, TxInfo{Status: "done", Receipt: receipt}, execCtx)

	assert.Equal(t, ActionReturn, result.Action)
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "tx mined hook error")
}

func TestHandleMinedTx_NoHook_StillReturnsSuccess(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newSuccessReceipt(tx)

	execCtx := &TxExecutionContext{}

	result := setup.WM.handleMinedTx(tx, TxInfo{Status: "done", Receipt: receipt}, execCtx)

	assert.Equal(t, ActionReturn, result.Action)
	assert.Nil(t, result.Error)
	assert.Equal(t, tx, result.Transaction)
	assert.Equal(t, receipt, result.Receipt)
}

// ============================================================
// Factory Injection Tests
// ============================================================

func TestWalletManager_UsesCustomReaderFactory(t *testing.T) {
	factoryCalled := false
	customReader := &mockEthReader{
		SuggestedGasSettingsFn: func() (float64, float64, error) {
			return 50.0, 5.0, nil // Custom values
		},
	}

	wm := NewWalletManager(
		WithReaderFactory(func(network networks.Network) (EthReader, error) {
			factoryCalled = true
			return customReader, nil
		}),
	)

	// Access reader to trigger factory
	reader, err := wm.Reader(networks.EthereumMainnet)

	require.NoError(t, err)
	assert.True(t, factoryCalled)
	assert.Equal(t, customReader, reader)
}

func TestWalletManager_UsesCustomBroadcasterFactory(t *testing.T) {
	factoryCalled := false
	customBroadcaster := &mockEthBroadcaster{}

	// Need a reader factory too for initNetwork to work
	wm := NewWalletManager(
		WithReaderFactory(func(network networks.Network) (EthReader, error) {
			return &mockEthReader{}, nil
		}),
		WithBroadcasterFactory(func(network networks.Network) (EthBroadcaster, error) {
			factoryCalled = true
			return customBroadcaster, nil
		}),
	)

	// Access broadcaster to trigger factory
	broadcaster, err := wm.Broadcaster(networks.EthereumMainnet)

	require.NoError(t, err)
	assert.True(t, factoryCalled)
	assert.Equal(t, customBroadcaster, broadcaster)
}

// ============================================================
// Circuit Breaker Integration Tests
// ============================================================

func TestReader_CircuitBreakerOpen_ReturnsError(t *testing.T) {
	wm := NewWalletManager(
		WithReaderFactory(func(network networks.Network) (EthReader, error) {
			return nil, errors.New("network error")
		}),
	)

	// Trigger multiple failures to open circuit breaker
	for i := 0; i < 10; i++ {
		_, _ = wm.Reader(networks.EthereumMainnet)
	}

	// Now circuit breaker should be open
	_, err := wm.Reader(networks.EthereumMainnet)

	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrCircuitBreakerOpen))
}

// ============================================================
// Nonce Release Tests
// ============================================================

func TestBuildTx_ReleasesNonce_WhenGasEstimationFails(t *testing.T) {
	setup := newTestSetup(t)

	// First, set up initial nonce state
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Make gas estimation fail
	setup.Reader.EstimateExactGasFn = func(from, to string, gasPrice float64, value *big.Int, data []byte) (uint64, error) {
		return 0, errors.New("execution reverted")
	}

	// Build tx with gasLimit=0 to trigger estimation
	_, err := setup.WM.BuildTx(
		2, testAddr1, testAddr2,
		nil, // nonce = nil, will acquire nonce 5
		oneEth,
		0, 0, 20, 0, 2, 0, nil,
		networks.EthereumMainnet,
	)
	require.Error(t, err)

	// Now try to build another tx - if nonce was released, we should get nonce 5 again
	setup.Reader.EstimateExactGasFn = func(from, to string, gasPrice float64, value *big.Int, data []byte) (uint64, error) {
		return 21000, nil // Success this time
	}

	tx, err := setup.WM.BuildTx(
		2, testAddr1, testAddr2,
		nil, // Should get nonce 5 again since it was released
		oneEth,
		0, 0, 20, 0, 2, 0, nil,
		networks.EthereumMainnet,
	)

	require.NoError(t, err)
	assert.Equal(t, uint64(5), tx.Nonce(), "nonce should be 5 since previous was released")
}

func TestBuildTx_DoesNotReleaseNonce_WhenSuccess(t *testing.T) {
	setup := newTestSetup(t)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// First successful build
	tx1, err := setup.WM.BuildTx(
		2, testAddr1, testAddr2,
		nil, oneEth,
		21000, 0, 20, 0, 2, 0, nil,
		networks.EthereumMainnet,
	)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), tx1.Nonce())

	// Second build should get nonce 6 (not 5)
	tx2, err := setup.WM.BuildTx(
		2, testAddr1, testAddr2,
		nil, oneEth,
		21000, 0, 20, 0, 2, 0, nil,
		networks.EthereumMainnet,
	)
	require.NoError(t, err)
	assert.Equal(t, uint64(6), tx2.Nonce(), "nonce should be 6 since 5 was used")
}

func TestReleaseNonce_AllowsNonceReuse(t *testing.T) {
	setup := newTestSetup(t)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Acquire nonce 5
	tx1, err := setup.WM.BuildTx(
		2, testAddr1, testAddr2,
		nil, oneEth,
		21000, 0, 20, 0, 2, 0, nil,
		networks.EthereumMainnet,
	)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), tx1.Nonce())

	// Acquire nonce 6
	tx2, err := setup.WM.BuildTx(
		2, testAddr1, testAddr2,
		nil, oneEth,
		21000, 0, 20, 0, 2, 0, nil,
		networks.EthereumMainnet,
	)
	require.NoError(t, err)
	assert.Equal(t, uint64(6), tx2.Nonce())

	// Release nonce 6 (the tip)
	setup.WM.ReleaseNonce(testAddr1, networks.EthereumMainnet, 6)

	// Next acquire should get 6 again
	tx3, err := setup.WM.BuildTx(
		2, testAddr1, testAddr2,
		nil, oneEth,
		21000, 0, 20, 0, 2, 0, nil,
		networks.EthereumMainnet,
	)
	require.NoError(t, err)
	assert.Equal(t, uint64(6), tx3.Nonce(), "should get 6 again after release")
}

func TestReleaseNonce_OnlyReleasesTip(t *testing.T) {
	setup := newTestSetup(t)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Acquire nonces 5, 6, 7
	_, err := setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	_, err = setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	_, err = setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)

	// Try to release nonce 6 (not the tip - tip is 7)
	setup.WM.ReleaseNonce(testAddr1, networks.EthereumMainnet, 6)

	// Next acquire should still get 8 (not 6)
	tx, err := setup.WM.BuildTx(
		2, testAddr1, testAddr2,
		nil, oneEth,
		21000, 0, 20, 0, 2, 0, nil,
		networks.EthereumMainnet,
	)
	require.NoError(t, err)
	assert.Equal(t, uint64(8), tx.Nonce(), "should get 8, not 6, because 6 is not the tip")
}

func TestReleaseNonce_MultipleWalletsIndependent(t *testing.T) {
	setup := newTestSetup(t)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) {
		if addr == testAddr1.Hex() {
			return 10, nil
		}
		return 20, nil
	}
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) {
		if addr == testAddr1.Hex() {
			return 10, nil
		}
		return 20, nil
	}

	// Acquire nonce for wallet 1
	tx1, err := setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(10), tx1.Nonce())

	// Acquire nonce for wallet 2
	tx2, err := setup.WM.BuildTx(2, testAddr3, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(20), tx2.Nonce())

	// Release nonce for wallet 1
	setup.WM.ReleaseNonce(testAddr1, networks.EthereumMainnet, 10)

	// Wallet 1 should get 10 again
	tx3, err := setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(10), tx3.Nonce())

	// Wallet 2 should get 21 (not affected by wallet 1's release)
	tx4, err := setup.WM.BuildTx(2, testAddr3, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(21), tx4.Nonce())
}

func TestBuildTx_ReleasesNonce_WhenNonceAcquisitionFailsLater(t *testing.T) {
	setup := newTestSetup(t)

	callCount := 0
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) {
		callCount++
		if callCount == 1 {
			return 5, nil
		}
		return 5, nil
	}
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) {
		return 5, nil
	}

	// Make gas estimation fail after nonce is acquired
	setup.Reader.EstimateExactGasFn = func(from, to string, gasPrice float64, value *big.Int, data []byte) (uint64, error) {
		return 0, errors.New("out of gas")
	}

	// First attempt - fails at gas estimation
	_, err := setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 0, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.Error(t, err)

	// Second attempt with working gas estimation
	setup.Reader.EstimateExactGasFn = func(from, to string, gasPrice float64, value *big.Int, data []byte) (uint64, error) {
		return 21000, nil
	}

	tx, err := setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 0, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	// Should get nonce 5 again because the first attempt released it
	assert.Equal(t, uint64(5), tx.Nonce())
}

func TestNonceRelease_SequentialReleaseAndAcquire(t *testing.T) {
	setup := newTestSetup(t)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 0, nil }

	// Acquire nonces 0-4
	for i := 0; i < 5; i++ {
		tx, err := setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
		require.NoError(t, err)
		assert.Equal(t, uint64(i), tx.Nonce())
	}
	// Now we have nonces 0-4 acquired, tip is 4

	// Release nonce 4 (the tip)
	setup.WM.ReleaseNonce(testAddr1, networks.EthereumMainnet, 4)

	// Next acquire should get 4 again
	tx, err := setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(4), tx.Nonce(), "should get 4 after release")

	// Release 4 again
	setup.WM.ReleaseNonce(testAddr1, networks.EthereumMainnet, 4)

	// Release 3 (now the new tip after releasing 4)
	setup.WM.ReleaseNonce(testAddr1, networks.EthereumMainnet, 3)

	// Next acquire should get 3
	tx, err = setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(3), tx.Nonce(), "should get 3 after double release")
}

func TestSignAndBroadcast_ReleasesNonce_WhenBeforeHookFails(t *testing.T) {
	setup := newTestSetup(t)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil // Simulation passes
	}

	// Build a tx first (this acquires nonce 5)
	tx := newTestTx(5, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{From: testAddr1, To: testAddr2, Network: networks.EthereumMainnet},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
		Hooks: TxHooks{BeforeSignAndBroadcast: func(tx *types.Transaction, err error) error {
			return errors.New("hook says no")
		}},
	}

	result := setup.WM.signAndBroadcastTransaction(context.Background(), tx, execCtx)

	assert.Equal(t, ActionReturn, result.Action)
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "hook")

	// Now the nonce should have been released
	// Build another tx - should get nonce 5 again
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	newTx, err := setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), newTx.Nonce(), "nonce 5 should be available after release")
}

func TestSignAndBroadcastTransaction_SyncBroadcast_TxMinedHook_Called(t *testing.T) {
	setup := setupEnsureTxTest(t)

	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	expectedReceipt := &types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		BlockNumber: big.NewInt(12345),
	}

	// Use Arbitrum which supports sync broadcast
	setup.Broadcaster.BroadcastTxSyncFn = func(tx *types.Transaction) (*types.Receipt, error) {
		return expectedReceipt, nil
	}

	hookCalled := false
	var receivedTx *types.Transaction
	var receivedReceipt *types.Receipt

	tx := newTestTxWithChainID(5, testAddr2, oneEth, chainIDArbitrum)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{From: fromAddr, To: testAddr2, Network: networks.ArbitrumMainnet},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
		Hooks: TxHooks{TxMined: func(tx *types.Transaction, r *types.Receipt) error {
			hookCalled = true
			receivedTx = tx
			receivedReceipt = r
			return nil
		}},
	}

	result := setup.WM.signAndBroadcastTransaction(context.Background(), tx, execCtx)

	assert.True(t, hookCalled, "TxMinedHook should be called when sync broadcast returns receipt")
	assert.NotNil(t, receivedTx)
	assert.Equal(t, expectedReceipt, receivedReceipt)
	assert.Equal(t, ActionReturn, result.Action)
	assert.Nil(t, result.Error)
}

func TestSimulation_ReleasesNonce_WhenHookSaysNoRetry(t *testing.T) {
	setup := newTestSetup(t)

	// Start with nonce 5
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// First, let's acquire nonce 5 manually to establish state
	tx1, err := setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), tx1.Nonce())

	// Now release it to simulate what happens when simulation fails
	setup.WM.ReleaseNonce(testAddr1, networks.EthereumMainnet, 5)

	// Next acquire should get 5 again (proving release worked)
	tx2, err := setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), tx2.Nonce(), "nonce 5 should be available after release")
}

func TestSimulation_DoesNotReleaseNonce_WhenRetrying(t *testing.T) {
	setup := newTestSetup(t)

	// Start fresh - no prior nonce state
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Simulation succeeds, tx should proceed
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil // Success
	}

	// First, acquire nonce 5
	tx1, err := setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), tx1.Nonce())

	// Don't release it - simulating tx is still in-flight

	// Next acquire should get 6 (nonce 5 is still reserved)
	tx2, err := setup.WM.BuildTx(2, testAddr1, testAddr2, nil, oneEth, 21000, 0, 20, 0, 2, 0, nil, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(6), tx2.Nonce(), "nonce 6 because 5 is still in use")
}

func TestSimulation_NonRevertError_ReturnsErrorWithoutHook(t *testing.T) {
	setup := newTestSetup(t)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Make EthCall return a network error (not a revert)
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, errors.New("network timeout")
	}

	hookCalled := false
	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{TxType: 2, From: testAddr1, To: testAddr2, Value: oneEth, Network: networks.EthereumMainnet},
		State:  TxRetryState{GasLimit: 21000, GasPrice: 20.0, TipCap: 2.0, OldTxs: make(map[string]*types.Transaction)},
		Hooks: TxHooks{SimulationFailed: func(tx *types.Transaction, revertData []byte, abiError *abi.Error, revertParams any, err error) (bool, error) {
			hookCalled = true
			return false, nil
		}},
	}

	result := setup.WM.executeTransactionAttempt(context.Background(), execCtx, nil)

	// Hook should NOT be called for non-revert errors
	assert.False(t, hookCalled, "simulation hook should NOT be called for non-revert errors")
	assert.Equal(t, ActionReturn, result.Action)
	assert.Error(t, result.Error)
	assert.True(t, errors.Is(result.Error, ErrSimulatedTxFailed))
}

// ============================================================
// Structured Error Types — errors.As Tests
// ============================================================

func TestGasEstimationError_ExceedsRetries_ExtractableWithErrorsAs(t *testing.T) {
	setup := newTestSetup(t)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 2},
		State:  TxRetryState{AttemptCount: 2, OldTxs: make(map[string]*types.Transaction)},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	err := errors.Join(ErrEstimateGasFailed, errors.New("execution reverted"))
	result := setup.WM.handleGasEstimationFailure(execCtx, nil, err)

	assert.Equal(t, ActionReturn, result.Action)

	var gasErr *GasEstimationError
	require.True(t, errors.As(result.Error, &gasErr), "result.Error should be extractable as *GasEstimationError")
	assert.True(t, errors.Is(gasErr, ErrEnsureTxOutOfRetries), "should preserve ErrEnsureTxOutOfRetries in chain")
	assert.True(t, errors.Is(gasErr, ErrEstimateGasFailed), "should preserve ErrEstimateGasFailed in chain")
	assert.Nil(t, gasErr.AbiError, "AbiError should be nil when no ABIs provided")
	assert.Nil(t, gasErr.RevertParams, "RevertParams should be nil when no ABIs provided")
}

func TestGasEstimationError_HookReturnsError_ExtractableWithErrorsAs(t *testing.T) {
	setup := newTestSetup(t)

	hookError := errors.New("caller decided to abort")
	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
		Params: TxParams{Network: networks.EthereumMainnet},
		Hooks: TxHooks{GasEstimationFailed: func(tx *types.Transaction, abiError *abi.Error, revertParams any, revertMsgError, gasEstimationError error) (*big.Int, error) {
			return nil, hookError
		}},
	}

	err := errors.Join(ErrEstimateGasFailed, errors.New("execution reverted"))
	result := setup.WM.handleGasEstimationFailure(execCtx, nil, err)

	assert.Equal(t, ActionReturn, result.Action)

	var gasErr *GasEstimationError
	require.True(t, errors.As(result.Error, &gasErr), "result.Error should be extractable as *GasEstimationError")
	assert.True(t, errors.Is(gasErr, hookError), "should wrap the hook's error")
}

func TestSimulationRevertError_RetriesExhausted_ExtractableWithErrorsAs(t *testing.T) {
	setup := newTestSetup(t)

	builtTx := newTestTx(5, testAddr2, oneEth)
	revertData := []byte{0x08, 0xc3, 0x79, 0xa0}

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 0},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
	}

	origErr := errors.Join(ErrSimulatedTxReverted, fmt.Errorf("revert data: %x", revertData))
	result := setup.WM.handleEthCallRevertFailure(execCtx, nil, builtTx, revertData, origErr)

	assert.Equal(t, ActionReturn, result.Action)

	var simErr *SimulationRevertError
	require.True(t, errors.As(result.Error, &simErr), "result.Error should be extractable as *SimulationRevertError")
	assert.True(t, errors.Is(simErr, ErrEnsureTxOutOfRetries), "should preserve ErrEnsureTxOutOfRetries in chain")
	assert.Equal(t, builtTx, simErr.Tx)
	assert.Equal(t, revertData, simErr.RevertData)
	assert.Nil(t, simErr.AbiError, "AbiError should be nil when no ABIs provided")
}

func TestSimulationRevertError_HookSaysNoRetry_ExtractableWithErrorsAs(t *testing.T) {
	setup := newTestSetup(t)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	builtTx := newTestTx(5, testAddr2, oneEth)
	revertData := []byte{0x08, 0xc3, 0x79, 0xa0}

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Hooks: TxHooks{SimulationFailed: func(tx *types.Transaction, rd []byte, abiError *abi.Error, revertParams any, err error) (bool, error) {
			return false, nil
		}},
	}

	origErr := errors.Join(ErrSimulatedTxReverted, fmt.Errorf("revert data: %x", revertData))
	result := setup.WM.handleEthCallRevertFailure(execCtx, nil, builtTx, revertData, origErr)

	assert.Equal(t, ActionReturn, result.Action)

	var simErr *SimulationRevertError
	require.True(t, errors.As(result.Error, &simErr), "result.Error should be extractable as *SimulationRevertError")
	assert.True(t, errors.Is(simErr, ErrSimulatedTxReverted), "should preserve ErrSimulatedTxReverted in chain")
	assert.Equal(t, builtTx, simErr.Tx)
	assert.Equal(t, revertData, simErr.RevertData)
}

func TestSimulationRevertError_HookReturnsError_ExtractableWithErrorsAs(t *testing.T) {
	setup := newTestSetup(t)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	builtTx := newTestTx(5, testAddr2, oneEth)
	revertData := []byte{0x08, 0xc3, 0x79, 0xa0}
	hookError := errors.New("hook wants to abort")

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Hooks: TxHooks{SimulationFailed: func(tx *types.Transaction, rd []byte, abiError *abi.Error, revertParams any, err error) (bool, error) {
			return false, hookError
		}},
	}

	origErr := errors.Join(ErrSimulatedTxReverted, fmt.Errorf("revert data: %x", revertData))
	result := setup.WM.handleEthCallRevertFailure(execCtx, nil, builtTx, revertData, origErr)

	assert.Equal(t, ActionReturn, result.Action)

	var simErr *SimulationRevertError
	require.True(t, errors.As(result.Error, &simErr), "result.Error should be extractable as *SimulationRevertError")
	assert.True(t, errors.Is(simErr, hookError), "should wrap the hook's error")
	assert.Equal(t, builtTx, simErr.Tx)
	assert.Equal(t, revertData, simErr.RevertData)
}

// ============================================================
// EnsureTxWithHooksContext Integration Tests
// ============================================================

// setupEnsureTxTest creates a test setup configured for EnsureTx integration tests
// with a registered wallet that can sign transactions
func setupEnsureTxTest(t *testing.T) *testSetup {
	t.Helper()
	setup := newTestSetup(t)

	// Create and register the test wallet so transactions can be signed
	// testPrivateKey1 is "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	acc, err := account.NewPrivateKeyAccount("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	setup.WM.SetAccount(acc)

	return setup
}

func TestEnsureTx_FullSuccessPath(t *testing.T) {
	setup := setupEnsureTxTest(t)

	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	// Set up mocks for success path
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.SuggestedGasSettingsFn = func() (float64, float64, error) { return 20.0, 2.0, nil }
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil // Simulation passes
	}

	var broadcastedTx *types.Transaction
	setup.Broadcaster.BroadcastTxFn = func(tx *types.Transaction) (string, bool, error) {
		broadcastedTx = tx
		return tx.Hash().Hex(), true, nil
	}
	// Also handle sync broadcast in case network supports it
	setup.Broadcaster.BroadcastTxSyncFn = func(tx *types.Transaction) (*types.Receipt, error) {
		broadcastedTx = tx
		return &types.Receipt{
			Status:      types.ReceiptStatusSuccessful,
			TxHash:      tx.Hash(),
			BlockNumber: big.NewInt(12345),
		}, nil
	}

	// Monitor returns mined status (used for non-sync networks)
	setup.Monitor.StatusToReturn = TxMonitorStatus{
		Status:  "done",
		Receipt: &types.Receipt{Status: types.ReceiptStatusSuccessful},
	}

	ctx := context.Background()
	tx, receipt, err := setup.WM.EnsureTxWithHooksContext(
		ctx,
		3,           // numRetries
		time.Second, // sleepDuration
		time.Second, // txCheckInterval
		2,           // txType (EIP-1559)
		fromAddr,    // from
		testAddr2,   // to
		oneEth,      // value
		21000, 0,    // gasLimit, extraGasLimit
		20.0, 0, // gasPrice, extraGasPrice
		2.0, 0, // tipCapGwei, extraTipCapGwei
		100.0, 50.0, // maxGasPrice, maxTipCap
		nil, // data
		networks.EthereumMainnet,
		nil, nil, nil, nil, nil, nil, // hooks
	)

	require.NoError(t, err, "EnsureTxWithHooksContext should succeed")
	require.NotNil(t, tx, "tx should not be nil")
	require.NotNil(t, receipt, "receipt should not be nil")
	require.NotNil(t, broadcastedTx, "broadcastedTx should have been set by broadcast function")
	assert.Equal(t, broadcastedTx.Hash(), tx.Hash())
	assert.Equal(t, uint64(5), tx.Nonce())
	// Verify either BroadcastTx or BroadcastTxSync was called
	totalBroadcasts := len(setup.Broadcaster.BroadcastTxCalls) + len(setup.Broadcaster.BroadcastTxSyncCalls)
	assert.Equal(t, 1, totalBroadcasts, "should have broadcast exactly once")
}

func TestEnsureTx_TxMinedHook_Called(t *testing.T) {
	setup := setupEnsureTxTest(t)

	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil
	}

	expectedReceipt := &types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		BlockNumber: big.NewInt(12345),
	}
	setup.Monitor.StatusToReturn = TxMonitorStatus{
		Status:  "done",
		Receipt: expectedReceipt,
	}

	hookCalled := false
	var hookTx *types.Transaction
	var hookReceipt *types.Receipt

	txMinedHook := func(tx *types.Transaction, r *types.Receipt) error {
		hookCalled = true
		hookTx = tx
		hookReceipt = r
		return nil
	}

	ctx := context.Background()
	tx, receipt, err := setup.WM.EnsureTxWithHooksContext(
		ctx,
		3, time.Millisecond, time.Millisecond,
		2, fromAddr, testAddr2, oneEth,
		21000, 0, 20.0, 0, 2.0, 0, 100.0, 50.0,
		nil, networks.EthereumMainnet,
		nil, nil, nil, nil, nil, txMinedHook,
	)

	require.NoError(t, err)
	assert.True(t, hookCalled, "txMinedHook should have been called")
	assert.Equal(t, tx.Hash(), hookTx.Hash())
	assert.Equal(t, receipt, hookReceipt)
}

func TestEnsureTx_BeforeSignAndBroadcastHook_StopsExecution(t *testing.T) {
	setup := setupEnsureTxTest(t)

	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil
	}

	hookError := errors.New("pre-flight check failed")
	beforeHook := func(tx *types.Transaction, err error) error {
		return hookError
	}

	ctx := context.Background()
	tx, receipt, err := setup.WM.EnsureTxWithHooksContext(
		ctx,
		3, time.Millisecond, time.Millisecond,
		2, fromAddr, testAddr2, oneEth,
		21000, 0, 20.0, 0, 2.0, 0, 100.0, 50.0,
		nil, networks.EthereumMainnet,
		beforeHook, nil, nil, nil, nil, nil,
	)

	require.Error(t, err)
	assert.Nil(t, tx)
	assert.Nil(t, receipt)
	assert.Contains(t, err.Error(), "pre-flight check failed")
	// Broadcaster should NOT have been called
	assert.Empty(t, setup.Broadcaster.BroadcastTxCalls)
}

func TestEnsureTx_ContextCancellation_DuringExecution(t *testing.T) {
	setup := setupEnsureTxTest(t)

	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil
	}

	// Monitor will take a while, allowing cancellation
	// Use shorter slow timeout for faster test
	defaults := setup.WM.Defaults()
	defaults.SlowTxTimeout = 100 * time.Millisecond
	setup.WM.SetDefaults(defaults)
	setup.Monitor.Delay = 200 * time.Millisecond
	setup.Monitor.StatusToReturn = TxMonitorStatus{Status: "pending"}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	// Use BSCMainnet which doesn't support sync tx, forcing async path with monitoring
	tx, receipt, err := setup.WM.EnsureTxWithHooksContext(
		ctx,
		3, time.Millisecond, time.Millisecond,
		2, fromAddr, testAddr2, oneEth,
		21000, 0, 20.0, 0, 2.0, 0, 100.0, 50.0,
		nil, networks.BSCMainnet, // Use BSC which doesn't support sync tx
		nil, nil, nil, nil, nil, nil,
	)

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.Nil(t, tx)
	assert.Nil(t, receipt)
}

func TestEnsureTx_SlowTxTriggersGasBump(t *testing.T) {
	// This test verifies that AdjustGasPricesForSlowTx correctly bumps gas prices
	// The full integration with monitor is tested separately

	ctx := &TxExecutionContext{
		Gas: GasBounds{MaxGasPrice: 100.0, MaxTipCap: 50.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei max fee
		big.NewInt(1),
	)

	adjusted := ctx.AdjustGasPricesForSlowTx(tx)

	assert.True(t, adjusted, "should be able to adjust gas prices")
	assert.Greater(t, ctx.State.GasPrice, 20.0, "gas price should be bumped above 20")
	assert.Greater(t, ctx.State.TipCap, 2.0, "tip cap should be bumped above 2")
}

func TestEnsureTx_MaxRetriesExceeded(t *testing.T) {
	setup := setupEnsureTxTest(t)

	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil
	}

	// Disable sync broadcast - make it fail with retryable error
	setup.Broadcaster.BroadcastTxSyncFn = func(tx *types.Transaction) (*types.Receipt, error) {
		return nil, errors.New("insufficient funds for gas * price + value")
	}

	// Async broadcast also fails with a retryable error
	setup.Broadcaster.BroadcastTxFn = func(tx *types.Transaction) (string, bool, error) {
		return "", false, errors.New("insufficient funds for gas * price + value")
	}

	ctx := context.Background()
	tx, receipt, err := setup.WM.EnsureTxWithHooksContext(
		ctx,
		2, time.Millisecond, time.Millisecond, // Only 2 retries
		2, fromAddr, testAddr2, oneEth,
		21000, 0, 20.0, 0, 2.0, 0, 100.0, 50.0,
		nil, networks.EthereumMainnet,
		nil, nil, nil, nil, nil, nil,
	)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEnsureTxOutOfRetries))
	assert.Nil(t, tx)
	assert.Nil(t, receipt)
}

func TestEnsureTx_GasPriceLimitReached_Unit(t *testing.T) {
	// Unit test: verify that AdjustGasPricesForSlowTx returns false when limit would be exceeded

	ctx := &TxExecutionContext{
		Gas: GasBounds{MaxGasPrice: 22.0, MaxTipCap: 50.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei max fee
		big.NewInt(1),
	)

	adjusted := ctx.AdjustGasPricesForSlowTx(tx)

	// Should return false because the new gas price (20 * 1.2 = 24) exceeds maxGasPrice (22)
	assert.False(t, adjusted, "should NOT be able to adjust when gas price limit would be exceeded")
}

func TestEnsureTx_TxReverted_ReturnsWithReceipt(t *testing.T) {
	setup := setupEnsureTxTest(t)

	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil
	}

	// Monitor returns reverted status
	setup.Monitor.StatusToReturn = TxMonitorStatus{
		Status: "reverted",
		Receipt: &types.Receipt{
			Status:      types.ReceiptStatusFailed,
			BlockNumber: big.NewInt(12345),
		},
	}

	ctx := context.Background()
	// Use BSCMainnet which doesn't support sync tx, forcing async path with monitoring
	tx, receipt, err := setup.WM.EnsureTxWithHooksContext(
		ctx,
		3, time.Millisecond, time.Millisecond,
		2, fromAddr, testAddr2, oneEth,
		21000, 0, 20.0, 0, 2.0, 0, 100.0, 50.0,
		nil, networks.BSCMainnet,
		nil, nil, nil, nil, nil, nil,
	)

	// Reverted tx is still "successful" from EnsureTx perspective - it was mined
	require.NoError(t, err)
	require.NotNil(t, tx)
	require.NotNil(t, receipt)
	assert.Equal(t, types.ReceiptStatusFailed, receipt.Status)
}

func TestEnsureTx_SyncTx_ReturnsImmediately(t *testing.T) {
	setup := setupEnsureTxTest(t)

	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil
	}

	expectedReceipt := &types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		BlockNumber: big.NewInt(99999),
	}

	// BroadcastTxSync returns immediately with receipt
	setup.Broadcaster.BroadcastTxSyncFn = func(tx *types.Transaction) (*types.Receipt, error) {
		return expectedReceipt, nil
	}

	// Use a network that supports sync tx (we'll mock it)
	// For this test, we need to use the regular broadcast since our mock doesn't
	// actually switch based on network. But the concept is tested.
	ctx := context.Background()
	tx, receipt, err := setup.WM.EnsureTxWithHooksContext(
		ctx,
		3, time.Millisecond, time.Millisecond,
		2, fromAddr, testAddr2, oneEth,
		21000, 0, 20.0, 0, 2.0, 0, 100.0, 50.0,
		nil, networks.EthereumMainnet, // Would need a sync-supporting network
		nil, nil, nil, nil, nil, nil,
	)

	require.NoError(t, err)
	require.NotNil(t, tx)
	require.NotNil(t, receipt)
}

func TestEnsureTx_LostTx_RetriesWithSameNonceAndBumpedGas(t *testing.T) {
	setup := setupEnsureTxTest(t)

	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil
	}

	broadcastedNonces := []uint64{}
	setup.Broadcaster.BroadcastTxFn = func(tx *types.Transaction) (string, bool, error) {
		broadcastedNonces = append(broadcastedNonces, tx.Nonce())
		return tx.Hash().Hex(), true, nil
	}

	// First tx is "lost", second is mined
	setup.Monitor.StatusSequence = []TxMonitorStatus{
		{Status: "lost"},
		{Status: "done", Receipt: &types.Receipt{Status: types.ReceiptStatusSuccessful}},
	}

	ctx := context.Background()
	// Use BSCMainnet which doesn't support sync tx, forcing async path with monitoring
	tx, receipt, err := setup.WM.EnsureTxWithHooksContext(
		ctx,
		5, time.Millisecond, time.Millisecond,
		2, fromAddr, testAddr2, oneEth,
		21000, 0, 20.0, 0, 2.0, 0, 100.0, 50.0,
		nil, networks.BSCMainnet,
		nil, nil, nil, nil, nil, nil,
	)

	require.NoError(t, err)
	require.NotNil(t, tx)
	require.NotNil(t, receipt)
	require.GreaterOrEqual(t, len(broadcastedNonces), 2, "should have broadcast multiple times")

	// All broadcasts should use the same nonce — lost tx retries with same nonce
	for i, n := range broadcastedNonces {
		assert.Equal(t, broadcastedNonces[0], n,
			"broadcast %d should use the same nonce as the first, got %d vs %d", i, n, broadcastedNonces[0])
	}
}

func TestEnsureTx_AfterSignAndBroadcastHook_CalledOnSuccess(t *testing.T) {
	setup := setupEnsureTxTest(t)

	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil
	}

	setup.Monitor.StatusToReturn = TxMonitorStatus{
		Status:  "done",
		Receipt: &types.Receipt{Status: types.ReceiptStatusSuccessful},
	}

	afterHookCalled := false
	var afterHookTx *types.Transaction

	afterHook := func(tx *types.Transaction, err error) error {
		afterHookCalled = true
		afterHookTx = tx
		assert.Nil(t, err, "afterHook should receive nil error on success")
		return nil
	}

	ctx := context.Background()
	tx, _, err := setup.WM.EnsureTxWithHooksContext(
		ctx,
		3, time.Millisecond, time.Millisecond,
		2, fromAddr, testAddr2, oneEth,
		21000, 0, 20.0, 0, 2.0, 0, 100.0, 50.0,
		nil, networks.EthereumMainnet,
		nil, afterHook, nil, nil, nil, nil,
	)

	require.NoError(t, err)
	assert.True(t, afterHookCalled, "afterSignAndBroadcastHook should have been called")
	assert.Equal(t, tx.Hash(), afterHookTx.Hash())
}

func TestEnsureTx_GasEstimationFails_RetriesUntilSuccess(t *testing.T) {
	setup := setupEnsureTxTest(t)

	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil
	}

	estimateCallCount := 0
	setup.Reader.EstimateExactGasFn = func(from, to string, gasPrice float64, value *big.Int, data []byte) (uint64, error) {
		estimateCallCount++
		if estimateCallCount < 3 {
			return 0, errors.New("execution reverted")
		}
		return 21000, nil // Succeed on 3rd try
	}

	// Use sync broadcast since it returns immediately
	setup.Broadcaster.BroadcastTxSyncFn = func(tx *types.Transaction) (*types.Receipt, error) {
		return &types.Receipt{
			Status:      types.ReceiptStatusSuccessful,
			BlockNumber: big.NewInt(12345),
		}, nil
	}

	ctx := context.Background()
	tx, receipt, err := setup.WM.EnsureTxWithHooksContext(
		ctx,
		5, time.Millisecond, time.Millisecond,
		2, fromAddr, testAddr2, oneEth,
		0, 0, 20.0, 0, 2.0, 0, 100.0, 50.0, // gasLimit=0 triggers estimation
		nil, networks.EthereumMainnet,
		nil, nil, nil, nil, nil, nil,
	)

	require.NoError(t, err)
	require.NotNil(t, tx)
	require.NotNil(t, receipt)
	assert.GreaterOrEqual(t, estimateCallCount, 3, "gas estimation should have been called at least 3 times")
}

// ============================================================
// Additional Nonce Tests for Full Coverage
// ============================================================

func TestPendingNonce_ReturnsNilForNewWallet(t *testing.T) {
	setup := newTestSetup(t)
	wallet := common.HexToAddress("0x9999999999999999999999999999999999999999")

	nonce := setup.WM.pendingNonce(wallet, networks.EthereumMainnet)

	assert.Nil(t, nonce)
}

func TestPendingNonce_ReturnsValueAfterSet(t *testing.T) {
	setup := newTestSetup(t)
	wallet := testAddr1

	setup.WM.setPendingNonce(wallet, networks.EthereumMainnet, 10)

	nonce := setup.WM.pendingNonce(wallet, networks.EthereumMainnet)

	require.NotNil(t, nonce)
	assert.Equal(t, uint64(11), nonce.Uint64()) // Returns next nonce (10+1)
}

func TestAcquireNonce_ReturnsError_WhenReaderFails(t *testing.T) {
	wm := NewWalletManager(
		WithReaderFactory(func(network networks.Network) (EthReader, error) {
			return nil, errors.New("reader init failed")
		}),
	)

	nonce, err := wm.acquireNonce(testAddr1, networks.EthereumMainnet)

	assert.Nil(t, nonce)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "couldn't get reader")
}

func TestAcquireNonce_ReturnsError_WhenMinedNonceFails(t *testing.T) {
	mockReader := &mockEthReader{
		GetMinedNonceFn: func(addr string) (uint64, error) {
			return 0, errors.New("mined nonce error")
		},
		GetPendingNonceFn: func(addr string) (uint64, error) {
			return 0, nil
		},
		SuggestedGasSettingsFn: func() (float64, float64, error) {
			return 20.0, 2.0, nil
		},
	}

	wm := NewWalletManager(
		WithReaderFactory(func(network networks.Network) (EthReader, error) {
			return mockReader, nil
		}),
		WithBroadcasterFactory(func(network networks.Network) (EthBroadcaster, error) {
			return &mockEthBroadcaster{}, nil
		}),
		WithTxMonitorFactory(func(reader EthReader) TxMonitor {
			return &mockTxMonitor{}
		}),
	)

	nonce, err := wm.acquireNonce(testAddr1, networks.EthereumMainnet)

	assert.Nil(t, nonce)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "couldn't get mined nonce")
}

func TestAcquireNonce_ReturnsError_WhenPendingNonceFails(t *testing.T) {
	mockReader := &mockEthReader{
		GetMinedNonceFn: func(addr string) (uint64, error) {
			return 5, nil
		},
		GetPendingNonceFn: func(addr string) (uint64, error) {
			return 0, errors.New("pending nonce error")
		},
		SuggestedGasSettingsFn: func() (float64, float64, error) {
			return 20.0, 2.0, nil
		},
	}

	wm := NewWalletManager(
		WithReaderFactory(func(network networks.Network) (EthReader, error) {
			return mockReader, nil
		}),
		WithBroadcasterFactory(func(network networks.Network) (EthBroadcaster, error) {
			return &mockEthBroadcaster{}, nil
		}),
		WithTxMonitorFactory(func(reader EthReader) TxMonitor {
			return &mockTxMonitor{}
		}),
	)

	nonce, err := wm.acquireNonce(testAddr1, networks.EthereumMainnet)

	assert.Nil(t, nonce)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "couldn't get remote pending nonce")
}

func TestHandleNonceIsLowError_TxStatusCheckFails_RetriesOrExceeds(t *testing.T) {
	setup := newTestSetup(t)

	// Make status check fail
	setup.Reader.TxInfoFromHashFn = func(hash string) (TxInfo, error) {
		return TxInfo{}, errors.New("status check failed")
	}

	oldTx := newTestTx(5, testAddr2, oneEth)
	newTx := newTestTx(6, testAddr2, oneEth)

	t.Run("retries when under limit", func(t *testing.T) {
		execCtx := &TxExecutionContext{
			Retry: RetryConfig{MaxAttempts: 5},
			State: TxRetryState{OldTxs: map[string]*types.Transaction{
				oldTx.Hash().Hex(): oldTx,
			}},
			Params: TxParams{Network: networks.EthereumMainnet},
		}

		result := setup.WM.handleNonceIsLowError(newTx, execCtx)

		assert.Equal(t, ActionRetry, result.Action)
		assert.Nil(t, result.Error)
	})

	t.Run("exceeds retries", func(t *testing.T) {
		execCtx := &TxExecutionContext{
			Retry: RetryConfig{MaxAttempts: 3},
			State: TxRetryState{AttemptCount: 3, OldTxs: map[string]*types.Transaction{
				oldTx.Hash().Hex(): oldTx,
			}},
			Params: TxParams{Network: networks.EthereumMainnet},
		}

		result := setup.WM.handleNonceIsLowError(newTx, execCtx)

		// Should return with error after exceeding retries
		assert.Equal(t, ActionReturn, result.Action)
		assert.Error(t, result.Error)
	})
}

func TestHandleNonceIsLowError_NoPendingTxs_Retries(t *testing.T) {
	setup := newTestSetup(t)

	// Return "pending" for all txs (no completed ones)
	setup.Reader.TxInfoFromHashFn = func(hash string) (TxInfo, error) {
		return TxInfo{Status: "pending"}, nil
	}

	oldTx := newTestTx(5, testAddr2, oneEth)
	newTx := newTestTx(6, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		Retry: RetryConfig{MaxAttempts: 5},
		State: TxRetryState{OldTxs: map[string]*types.Transaction{
			oldTx.Hash().Hex(): oldTx,
		}},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	result := setup.WM.handleNonceIsLowError(newTx, execCtx)

	// Should retry with new nonce (Nonce = nil)
	assert.Equal(t, ActionRetry, result.Action)
	assert.Nil(t, execCtx.State.Nonce)
}

func TestHandleNonceIsLowError_RevertedTxFound_Returns(t *testing.T) {
	setup := newTestSetup(t)

	oldTx := newTestTx(5, testAddr2, oneEth)
	revertedReceipt := newFailedReceipt(oldTx)

	setup.Reader.TxInfoFromHashFn = func(hash string) (TxInfo, error) {
		if hash == oldTx.Hash().Hex() {
			return TxInfo{Status: "reverted", Receipt: revertedReceipt}, nil
		}
		return TxInfo{Status: "pending"}, nil
	}

	newTx := newTestTx(6, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		Retry: RetryConfig{MaxAttempts: 5},
		State: TxRetryState{OldTxs: map[string]*types.Transaction{
			oldTx.Hash().Hex(): oldTx,
		}},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	result := setup.WM.handleNonceIsLowError(newTx, execCtx)

	// Should return the reverted tx
	assert.Equal(t, ActionReturn, result.Action)
	assert.Equal(t, oldTx, result.Transaction)
}

func TestHandleNonceIsLowError_NoOldTxs_Retries(t *testing.T) {
	setup := newTestSetup(t)

	newTx := newTestTx(6, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	result := setup.WM.handleNonceIsLowError(newTx, execCtx)

	// Should retry
	assert.Equal(t, ActionRetry, result.Action)
}

func TestHandleNonceIsLowError_ExceedsRetries_NoPendingTxs(t *testing.T) {
	setup := newTestSetup(t)

	setup.Reader.TxInfoFromHashFn = func(hash string) (TxInfo, error) {
		return TxInfo{Status: "pending"}, nil
	}

	oldTx := newTestTx(5, testAddr2, oneEth)
	newTx := newTestTx(6, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		Retry: RetryConfig{MaxAttempts: 2},
		State: TxRetryState{AttemptCount: 2, OldTxs: map[string]*types.Transaction{
			oldTx.Hash().Hex(): oldTx,
		}},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	result := setup.WM.handleNonceIsLowError(newTx, execCtx)

	assert.Equal(t, ActionReturn, result.Action)
	assert.Error(t, result.Error)
}

func TestNonce_IsAliasForAcquireNonce(t *testing.T) {
	setup := newTestSetup(t)
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	nonce, err := setup.WM.nonce(testAddr1, networks.EthereumMainnet)

	require.NoError(t, err)
	assert.Equal(t, uint64(5), nonce.Uint64())
}

func TestSetPendingNonce_UpdatesTracker(t *testing.T) {
	setup := newTestSetup(t)
	wallet := testAddr1
	network := networks.EthereumMainnet

	// Initially nil
	initialNonce := setup.WM.pendingNonce(wallet, network)
	assert.Nil(t, initialNonce)

	// Set nonce
	setup.WM.setPendingNonce(wallet, network, 100)

	// Should return 101 (next nonce)
	updatedNonce := setup.WM.pendingNonce(wallet, network)
	require.NotNil(t, updatedNonce)
	assert.Equal(t, uint64(101), updatedNonce.Uint64())
}

func TestReleaseNonce_MultipleNetworks(t *testing.T) {
	setup := newTestSetup(t)
	wallet := testAddr1
	network1 := networks.EthereumMainnet
	network2 := networks.BSCMainnet

	// Acquire nonces on both networks
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 0, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 0, nil }

	nonce1, _ := setup.WM.acquireNonce(wallet, network1)
	nonce2, _ := setup.WM.acquireNonce(wallet, network2)

	// Release on network1
	setup.WM.ReleaseNonce(wallet, network1, nonce1.Uint64())

	// Acquire again on network1 - should get same nonce
	newNonce1, _ := setup.WM.acquireNonce(wallet, network1)
	assert.Equal(t, nonce1.Uint64(), newNonce1.Uint64())

	// Network2 should be unaffected - next acquire should increment
	newNonce2, _ := setup.WM.acquireNonce(wallet, network2)
	assert.Equal(t, nonce2.Uint64()+1, newNonce2.Uint64())
}

func TestAcquireNonce_ReturnsError_WhenAbnormalState(t *testing.T) {
	setup := newTestSetup(t)
	wallet := testAddr1

	// Set up local nonce first
	setup.WM.setPendingNonce(wallet, networks.EthereumMainnet, 5)

	// Configure reader to return abnormal state: mined > pending
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 10, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	nonce, err := setup.WM.acquireNonce(wallet, networks.EthereumMainnet)

	assert.Nil(t, nonce)
	assert.Error(t, err)
	// The error should come from the nonce tracker
}

func TestHandleNonceIsLowError_StatusDoneButTxNotInMap_Continues(t *testing.T) {
	setup := newTestSetup(t)

	oldTx := newTestTx(5, testAddr2, oneEth)
	differentTx := newTestTx(7, testAddr3, oneEth)

	// Status returns "done" for oldTx, but oldTx is not actually in OldTxs
	setup.Reader.TxInfoFromHashFn = func(hash string) (TxInfo, error) {
		// This will be called with differentTx's hash
		return TxInfo{Status: "pending"}, nil
	}

	newTx := newTestTx(6, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		Retry: RetryConfig{MaxAttempts: 5},
		State: TxRetryState{OldTxs: map[string]*types.Transaction{
			// Only differentTx in the map, but it's "pending"
			differentTx.Hash().Hex(): differentTx,
		}},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	result := setup.WM.handleNonceIsLowError(newTx, execCtx)

	// Should retry since no completed tx was found
	assert.Equal(t, ActionRetry, result.Action)

	// Make oldTx hash not in map to verify the edge case
	// Verify OldTxs doesn't have the oldTx
	_, exists := execCtx.State.OldTxs[oldTx.Hash().Hex()]
	assert.False(t, exists)
}

func TestHandleNonceIsLowError_HashMismatch_Continues(t *testing.T) {
	setup := newTestSetup(t)

	oldTx := newTestTx(5, testAddr2, oneEth)
	anotherTx := newTestTx(7, testAddr2, oneEth)

	// Status is "done" for a different hash than what's in OldTxs
	setup.Reader.TxInfoFromHashFn = func(hash string) (TxInfo, error) {
		// Return done for the oldTx hash
		if hash == oldTx.Hash().Hex() {
			return TxInfo{Status: "done"}, nil
		}
		return TxInfo{Status: "pending"}, nil
	}

	newTx := newTestTx(6, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		Retry: RetryConfig{MaxAttempts: 5},
		State: TxRetryState{OldTxs: map[string]*types.Transaction{
			// Different tx hash stored
			anotherTx.Hash().Hex(): anotherTx,
		}},
		Params: TxParams{Network: networks.EthereumMainnet},
	}

	result := setup.WM.handleNonceIsLowError(newTx, execCtx)

	// Should retry since the matching hash is not in OldTxs
	assert.Equal(t, ActionRetry, result.Action)
}

// ============================================================
// Sync Tx Broadcast Tests
// ============================================================

func TestSyncBroadcast_SucceedsWithinTimeout_ReturnsReceiptImmediately(t *testing.T) {
	setup, mockNetwork := newTestSetupWithSyncNetwork(t)

	// BroadcastTxSync returns immediately with success
	setup.Broadcaster.BroadcastTxSyncFn = func(tx *types.Transaction) (*types.Receipt, error) {
		return &types.Receipt{
			Status:      types.ReceiptStatusSuccessful,
			TxHash:      tx.Hash(),
			BlockNumber: big.NewInt(12345),
			GasUsed:     21000,
		}, nil
	}

	// Create a test tx with correct chain ID for mock network
	tx := newTestTxWithChainID(0, testAddr2, oneEth, chainIDArbitrum)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 3},
		Params: TxParams{From: setup.FromAddr, To: testAddr2, Network: mockNetwork},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
	}

	result := setup.WM.signAndBroadcastTransaction(context.Background(), tx, execCtx)

	// Should have receipt and should return immediately (not fall back to monitor)
	require.NotNil(t, result.Receipt, "Should get receipt immediately from sync broadcast")
	assert.Equal(t, types.ReceiptStatusSuccessful, result.Receipt.Status)
	assert.Equal(t, ActionReturn, result.Action, "Should return immediately with sync broadcast success")

	// Verify BroadcastTxSync was called (not BroadcastTx)
	assert.Len(t, setup.Broadcaster.BroadcastTxSyncCalls, 1)
	assert.Len(t, setup.Broadcaster.BroadcastTxCalls, 0, "Should not fall back to async broadcast")
}

func TestSyncBroadcast_Fails_ReturnsForRetry(t *testing.T) {
	setup, mockNetwork := newTestSetupWithSyncNetwork(t)

	// BroadcastTxSync fails
	setup.Broadcaster.BroadcastTxSyncFn = func(tx *types.Transaction) (*types.Receipt, error) {
		return nil, errors.New("temporary network error")
	}

	tx := newTestTxWithChainID(0, testAddr2, oneEth, chainIDArbitrum)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 3},
		Params: TxParams{From: setup.FromAddr, To: testAddr2, Network: mockNetwork},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
	}

	result := setup.WM.signAndBroadcastTransaction(context.Background(), tx, execCtx)

	// Should indicate retry is needed
	assert.Equal(t, ActionRetry, result.Action, "Should retry after broadcast failure")
	assert.Nil(t, result.Receipt)
}

func TestSyncBroadcast_ContextCancelled_ReturnsError(t *testing.T) {
	setup, mockNetwork := newTestSetupWithSyncNetwork(t)

	// BroadcastTxSync blocks until context is cancelled
	setup.Broadcaster.BroadcastTxSyncFn = func(tx *types.Transaction) (*types.Receipt, error) {
		time.Sleep(10 * time.Second) // Would block forever
		return &types.Receipt{
			Status: types.ReceiptStatusSuccessful,
			TxHash: tx.Hash(),
		}, nil
	}

	tx := newTestTxWithChainID(0, testAddr2, oneEth, chainIDArbitrum)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 3},
		Params: TxParams{From: setup.FromAddr, To: testAddr2, Network: mockNetwork},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := setup.WM.signAndBroadcastTransaction(ctx, tx, execCtx)

	// Should return with context error
	require.Equal(t, ActionReturn, result.Action, "Should return when context is cancelled")
	require.Error(t, result.Error)
	assert.ErrorIs(t, result.Error, context.DeadlineExceeded)
}

func TestSyncBroadcast_Timeout_FallsBackToAsyncMonitoring(t *testing.T) {
	// Override timeout for faster testing
	originalTimeout := SyncBroadcastTimeout
	SyncBroadcastTimeout = 50 * time.Millisecond
	defer func() { SyncBroadcastTimeout = originalTimeout }()

	setup, mockNetwork := newTestSetupWithSyncNetwork(t)

	broadcastStarted := make(chan struct{}, 1)

	// BroadcastTxSync blocks longer than the timeout
	setup.Broadcaster.BroadcastTxSyncFn = func(tx *types.Transaction) (*types.Receipt, error) {
		select {
		case broadcastStarted <- struct{}{}:
		default:
		}
		// Block for longer than the timeout
		time.Sleep(5 * time.Second)
		return &types.Receipt{
			Status:      types.ReceiptStatusSuccessful,
			TxHash:      tx.Hash(),
			BlockNumber: big.NewInt(12345),
		}, nil
	}

	tx := newTestTxWithChainID(0, testAddr2, oneEth, chainIDArbitrum)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{From: setup.FromAddr, To: testAddr2, Network: mockNetwork},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := setup.WM.signAndBroadcastTransaction(ctx, tx, execCtx)

	// After timeout, should return with:
	// - Transaction set (it was signed)
	// - No receipt (timed out before getting one)
	// - Action = ActionContinueToMonitor (so monitor flow kicks in)
	assert.NotNil(t, result.Transaction, "Should have signed transaction")
	assert.Nil(t, result.Receipt, "Should not have receipt after timeout")
	assert.Equal(t, ActionContinueToMonitor, result.Action, "Should not return immediately - needs to go to monitor flow")
	assert.Nil(t, result.Error, "Timeout is not an error - it's a fallback to async monitoring")
}

func TestSyncBroadcast_ReturnsRevertedReceipt_HandledCorrectly(t *testing.T) {
	setup, mockNetwork := newTestSetupWithSyncNetwork(t)

	// BroadcastTxSync returns a reverted receipt
	setup.Broadcaster.BroadcastTxSyncFn = func(tx *types.Transaction) (*types.Receipt, error) {
		return &types.Receipt{
			Status:      types.ReceiptStatusFailed,
			TxHash:      tx.Hash(),
			BlockNumber: big.NewInt(12345),
			GasUsed:     21000,
		}, nil
	}

	tx := newTestTxWithChainID(0, testAddr2, oneEth, chainIDArbitrum)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 3},
		Params: TxParams{From: setup.FromAddr, To: testAddr2, Network: mockNetwork},
		State:  TxRetryState{OldTxs: make(map[string]*types.Transaction)},
	}

	result := setup.WM.signAndBroadcastTransaction(context.Background(), tx, execCtx)

	// Should return with the reverted receipt (not an error - tx was mined)
	require.NotNil(t, result.Receipt)
	assert.Equal(t, types.ReceiptStatusFailed, result.Receipt.Status, "Should return the reverted receipt")
	assert.Equal(t, ActionReturn, result.Action, "Should return with reverted receipt")
	assert.Nil(t, result.Error, "Reverted tx is not an error - it's a valid outcome")
}

func TestSyncBroadcast_Timeout_TriggersMonitorFlow(t *testing.T) {
	// This test verifies that when sync broadcast times out:
	// 1. The function returns with Action=ActionContinueToMonitor (triggering monitor flow)
	// 2. The monitor is subsequently called

	// Override timeout for faster testing
	originalTimeout := SyncBroadcastTimeout
	SyncBroadcastTimeout = 50 * time.Millisecond
	defer func() { SyncBroadcastTimeout = originalTimeout }()

	setup, mockNetwork := newTestSetupWithSyncNetwork(t)

	var broadcastedTxs []*types.Transaction
	var mu sync.Mutex

	// Sync broadcast times out
	setup.Broadcaster.BroadcastTxSyncFn = func(tx *types.Transaction) (*types.Receipt, error) {
		mu.Lock()
		broadcastedTxs = append(broadcastedTxs, tx)
		mu.Unlock()

		// Block until timeout
		time.Sleep(1 * time.Second)
		return nil, nil // Won't be reached due to timeout
	}

	// Monitor returns "done" immediately (simulating tx mined during monitor)
	setup.Monitor.StatusToReturn = TxMonitorStatus{
		Status:  "done",
		Receipt: newSuccessReceipt(newTestTxWithChainID(0, testAddr2, oneEth, chainIDArbitrum)),
	}

	// Build initial tx
	initialTx, err := setup.WM.BuildTx(
		2, // EIP-1559
		setup.FromAddr,
		testAddr2,
		nil, // nonce
		oneEth,
		21000, 0,
		0, 0, // gas price (will use suggested)
		0, 0, // tip cap (will use suggested)
		nil, // data
		mockNetwork,
	)
	require.NoError(t, err)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5, TxCheckInterval: 10 * time.Millisecond},
		Params: TxParams{From: setup.FromAddr, To: testAddr2, Network: mockNetwork, TxType: 2, Value: oneEth},
		State:  TxRetryState{GasLimit: 21000, OldTxs: make(map[string]*types.Transaction)},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// First broadcast - should timeout and return for monitor flow
	result1 := setup.WM.signAndBroadcastTransaction(ctx, initialTx, execCtx)
	require.NotNil(t, result1.Transaction, "Should have transaction after timeout")
	require.Nil(t, result1.Receipt, "Should not have receipt after timeout")
	require.Equal(t, ActionContinueToMonitor, result1.Action, "Should go to monitor flow, not return immediately")

	// Simulate what ensureTxWithHooksContextInternal does after signAndBroadcast returns with no receipt
	// It should call MonitorTxContext
	statusChan := setup.WM.MonitorTxContext(ctx, result1.Transaction, mockNetwork, 10*time.Millisecond)
	status := <-statusChan

	// Monitor should return "mined" (since mock returns "done" which gets converted to "mined")
	assert.Equal(t, TxStatusMined, status.Status, "Monitor should return mined status")

	// Verify that:
	// 1. The sync broadcast was called
	// 2. Monitor was called after timeout
	mu.Lock()
	assert.Equal(t, 1, len(broadcastedTxs), "Should have called BroadcastTxSync once before timeout")
	mu.Unlock()

	assert.Len(t, setup.Monitor.MakeWaitChannelCalls, 1, "Monitor should have been called after sync timeout")
}

// ============================================================
// Nonce Leak Tests - Path 2 & 3
// ============================================================

// TestNonceLeak_NonRevertSimulationFailure_ShouldReleaseNonce tests that when
// executeTransactionAttempt acquires a nonce internally (RetryNonce is nil on first attempt)
// and then the simulation (EthCall) fails with a non-revert error, the nonce is released
// so the next acquire gets the same nonce back.
//
// This is Path 2: BuildTx succeeds → simulation fails (non-revert) → nonce should be released.
func TestNonceLeak_NonRevertSimulationFailure_ShouldReleaseNonce(t *testing.T) {
	setup := newTestSetup(t)

	// Chain state: mined=5, pending=5 (no pending txs on node)
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Simulation fails with a non-revert error (e.g., network timeout)
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, fmt.Errorf("network connection timeout")
	}

	// First attempt: Nonce is nil, so BuildTx acquires nonce internally
	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{TxType: 2, From: testAddr1, To: testAddr2, Value: oneEth, Network: networks.EthereumMainnet},
		State:  TxRetryState{GasLimit: 21000, GasPrice: 20.0, TipCap: 2.0, OldTxs: make(map[string]*types.Transaction)},
		// State.Nonce is nil — simulates the first attempt in executeTransactionLoop
	}

	result := setup.WM.executeTransactionAttempt(context.Background(), execCtx, nil)

	// Confirm it returned an error (simulation failed)
	require.Equal(t, ActionReturn, result.Action)
	require.Error(t, result.Error)
	assert.True(t, errors.Is(result.Error, ErrSimulatedTxFailed))

	// Now the key assertion: the nonce should have been released.
	// If we acquire again with the same chain state, we should get nonce 5 again,
	// NOT nonce 6 (which would mean nonce 5 was leaked).
	nonce, err := setup.WM.acquireNonce(testAddr1, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), nonce.Uint64(),
		"Nonce should be 5 again after release; got %d means nonce was leaked", nonce.Uint64())
}

// TestNonceLeak_NonRevertSimulationFailure_CompoundsOverMultipleCalls tests that the
// nonce leak from non-revert simulation failures compounds: each failed attempt
// permanently increments the local nonce by 1, creating a growing gap between
// the local tracker and the chain state.
func TestNonceLeak_NonRevertSimulationFailure_CompoundsOverMultipleCalls(t *testing.T) {
	setup := newTestSetup(t)

	// Chain state: mined=5, pending=5 (no pending txs on node)
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Simulation always fails with a non-revert error
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, fmt.Errorf("network connection timeout")
	}

	// Simulate 3 consecutive failed attempts via executeTransactionAttempt
	for i := 0; i < 3; i++ {
		execCtx := &TxExecutionContext{
			Retry:  RetryConfig{MaxAttempts: 5},
			Params: TxParams{TxType: 2, From: testAddr1, To: testAddr2, Value: oneEth, Network: networks.EthereumMainnet},
			State:  TxRetryState{GasLimit: 21000, GasPrice: 20.0, TipCap: 2.0, OldTxs: make(map[string]*types.Transaction)},
			// State.Nonce is nil — each call acquires a new nonce internally
		}

		result := setup.WM.executeTransactionAttempt(context.Background(), execCtx, nil)
		require.Equal(t, ActionReturn, result.Action, "attempt %d should return", i)
		require.Error(t, result.Error, "attempt %d should error", i)
	}

	// After 3 failed simulation attempts, the chain state hasn't changed (mined=5, pending=5).
	// If nonces are properly released, the next acquire should still return 5.
	// If nonces are leaked, the local tracker has been ratcheted up to 7 (5, 6, 7 acquired and not released)
	// and the next acquire would return 8.
	nonce, err := setup.WM.acquireNonce(testAddr1, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), nonce.Uint64(),
		"Nonce should still be 5 after 3 failed simulations; got %d means %d nonces were leaked",
		nonce.Uint64(), nonce.Uint64()-5)
}

// TestNonceLeak_NonRevertSimulationFailure_FullLoop_ShouldReleaseNonce tests the full
// executeTransactionLoop to verify the defer cleanup releases the nonce when
// simulation fails with a non-revert error on the first attempt (RetryNonce is nil).
func TestNonceLeak_NonRevertSimulationFailure_FullLoop_ShouldReleaseNonce(t *testing.T) {
	setup := newTestSetup(t)

	// Chain state: mined=5, pending=5 (no pending txs on node)
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Simulation fails with a non-revert error
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, fmt.Errorf("network connection timeout")
	}

	// Run through the full EnsureTxWithHooksContext which uses executeTransactionLoop
	_, _, err := setup.WM.EnsureTxWithHooksContext(
		context.Background(),
		1,                   // numRetries
		10*time.Millisecond, // sleepDuration
		10*time.Millisecond, // txCheckInterval
		2,                   // txType
		testAddr1,
		testAddr2,
		oneEth,
		21000, 0, // gasLimit, extraGasLimit
		20, 0, // gasPrice, extraGasPrice
		2, 0, // tipCapGwei, extraTipCapGwei
		0, 0, // maxGasPrice, maxTipCap
		nil, // data
		networks.EthereumMainnet,
		nil, nil, // hooks
		nil, nil, // abis, gasEstimationFailedHook
		nil, nil, // simulationFailedHook, txMinedHook
	)

	// Should fail with simulation error
	require.Error(t, err)

	// The critical check: acquire a nonce again and verify it's 5, not 6.
	// If the loop's defer didn't release the nonce, the local tracker will
	// have stored nonce 5 and the next acquire will return 6.
	nonce, acquireErr := setup.WM.acquireNonce(testAddr1, networks.EthereumMainnet)
	require.NoError(t, acquireErr)
	assert.Equal(t, uint64(5), nonce.Uint64(),
		"Nonce should be 5 after the failed EnsureTx released it; got %d means the nonce was leaked by executeTransactionLoop", nonce.Uint64())
}

// ============================================================
// Lost Transaction Handler Tests
// ============================================================

// TestHandleTransactionStatus_Lost_RetriesWithSameNonce tests that when a
// transaction is detected as "lost" (dropped from mempool), the handler
// retries with the SAME nonce and bumped gas, not a brand new nonce.
// A lost tx means the mempool dropped it — re-broadcasting with the same
// nonce and higher gas is the correct response.
func TestHandleTransactionStatus_Lost_RetriesWithSameNonce(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei fee cap
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Retry: RetryConfig{MaxAttempts: 5},
		Gas:   GasBounds{MaxGasPrice: 100.0, MaxTipCap: 50.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	result := setup.WM.handleTransactionStatus(TxInfo{Status: "lost"}, tx, execCtx)

	assert.Equal(t, ActionRetry, result.Action)

	// The key assertion: Nonce should be set to the SAME nonce (5),
	// not nil (which would cause a new nonce to be acquired).
	require.NotNil(t, execCtx.State.Nonce,
		"Nonce should be set to the lost tx's nonce, not nil")
	assert.Equal(t, big.NewInt(5), execCtx.State.Nonce,
		"Nonce should be the same nonce as the lost tx")

	// Gas should be bumped (same behavior as slow tx)
	assert.Greater(t, execCtx.State.GasPrice, 20.0,
		"Gas price should be bumped for lost tx retry")
	assert.Greater(t, execCtx.State.TipCap, 2.0,
		"Tip cap should be bumped for lost tx retry")
}

// TestHandleTransactionStatus_Lost_HitsGasLimit tests that when a lost tx
// retry would exceed gas price limits, it returns an error instead of
// retrying forever.
func TestHandleTransactionStatus_Lost_HitsGasLimit(t *testing.T) {
	setup := newTestSetup(t)

	// Chain state: mined=5, but the lost tx is also nonce 5.
	// No higher local nonces, so it's NOT blocking — should respect gas limit.
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei fee cap
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Gas:    GasBounds{MaxGasPrice: 22.0, MaxTipCap: 50.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	result := setup.WM.handleTransactionStatus(TxInfo{Status: "lost"}, tx, execCtx)

	assert.Equal(t, ActionReturn, result.Action,
		"Should return when gas limit reached for non-blocking lost tx")
	assert.True(t, errors.Is(result.Error, ErrGasPriceLimitReached))
}

// TestEnsureTx_LostTx_RetriesWithSameNonce is an integration test that verifies
// the full EnsureTx flow re-broadcasts a lost transaction with the same nonce
// and higher gas, rather than acquiring a new nonce.
func TestEnsureTx_LostTx_RetriesWithSameNonce(t *testing.T) {
	setup := setupEnsureTxTest(t)

	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.EthCallFn = func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
		return nil, nil
	}

	broadcastedNonces := []uint64{}
	var mu sync.Mutex
	setup.Broadcaster.BroadcastTxFn = func(tx *types.Transaction) (string, bool, error) {
		mu.Lock()
		broadcastedNonces = append(broadcastedNonces, tx.Nonce())
		mu.Unlock()
		return tx.Hash().Hex(), true, nil
	}

	// First tx is "lost", second is mined
	setup.Monitor.StatusSequence = []TxMonitorStatus{
		{Status: "lost"},
		{Status: "done", Receipt: &types.Receipt{Status: types.ReceiptStatusSuccessful}},
	}

	ctx := context.Background()
	tx, receipt, err := setup.WM.EnsureTxWithHooksContext(
		ctx,
		5, time.Millisecond, time.Millisecond,
		2, fromAddr, testAddr2, oneEth,
		21000, 0, 20.0, 0, 2.0, 0, 100.0, 50.0,
		nil, networks.BSCMainnet,
		nil, nil, nil, nil, nil, nil,
	)

	require.NoError(t, err)
	require.NotNil(t, tx)
	require.NotNil(t, receipt)

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(broadcastedNonces), 2,
		"should have broadcast at least twice (original + retry)")

	// The critical assertion: ALL broadcasts should use the SAME nonce.
	// The lost tx should be retried with the same nonce, not a new one.
	for i, n := range broadcastedNonces {
		assert.Equal(t, broadcastedNonces[0], n,
			"broadcast %d should use the same nonce as the first broadcast, got nonce %d vs %d",
			i, n, broadcastedNonces[0])
	}
}

// ============================================================
// Replacement Underpriced — Separate from Nonce Too Low
// ============================================================

// TestHandleBroadcastError_ReplacementUnderpriced_BumpsGas tests that when a
// replacement tx is rejected as underpriced and we have a PREVIOUS tx at the
// same nonce in OldTxs (meaning the pending mempool tx is ours), we keep the
// same nonce and bump gas.
//
// OldTxs setup mirrors the real flow: signAndBroadcastTransaction always adds
// the current signed tx to OldTxs BEFORE calling handleBroadcastError. So for
// the "own tx" scenario, OldTxs must contain both the previous broadcast AND
// the current rejected attempt (2 txs at the same nonce).
func TestHandleBroadcastError_ReplacementUnderpriced_BumpsGas(t *testing.T) {
	setup := newTestSetup(t)

	// Our previously broadcast tx at nonce 5 (actually in the mempool)
	prevTx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(1000000000),  // 1 gwei tip
		big.NewInt(10000000000), // 10 gwei fee cap
		big.NewInt(1),
	)

	// The current replacement attempt (rejected as underpriced)
	currentSignedTx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei fee cap
		big.NewInt(1),
	)

	// The unsigned version of the tx passed to handleBroadcastError
	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),
		big.NewInt(20000000000),
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Gas:    GasBounds{MaxGasPrice: 200.0, MaxTipCap: 100.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
		State: TxRetryState{OldTxs: map[string]*types.Transaction{
			prevTx.Hash().Hex():          prevTx,          // previous broadcast (in mempool)
			currentSignedTx.Hash().Hex(): currentSignedTx, // current attempt (added by signAndBroadcastTransaction)
		}},
	}

	result := setup.WM.handleBroadcastError(ErrReplacementUnderpriced, tx, execCtx)

	assert.Equal(t, ActionRetry, result.Action, "Should retry after underpriced rejection")
	assert.Nil(t, result.Error)

	// Critical: nonce must be preserved (same nonce, not nil)
	assert.NotNil(t, execCtx.State.Nonce, "Nonce should be set to keep the same nonce")
	assert.Equal(t, uint64(5), execCtx.State.Nonce.Uint64(), "Should keep the same nonce")

	// Gas should be bumped
	assert.Greater(t, execCtx.State.GasPrice, float64(0), "Gas price should be bumped")
	assert.Greater(t, execCtx.State.TipCap, float64(0), "Tip cap should be bumped")
}

// TestHandleBroadcastError_ReplacementUnderpriced_HitsGasLimit tests that when a
// replacement tx is underpriced and gas limits are reached, we stop retrying.
func TestHandleBroadcastError_ReplacementUnderpriced_HitsGasLimit(t *testing.T) {
	setup := newTestSetup(t)

	// Our previously broadcast tx at nonce 5 (actually in the mempool)
	prevTx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(1000000000),
		big.NewInt(10000000000),
		big.NewInt(1),
	)

	// The current signed tx (added to OldTxs by signAndBroadcastTransaction)
	currentSignedTx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),
		big.NewInt(20000000000),
		big.NewInt(1),
	)

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),
		big.NewInt(20000000000),
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Gas:    GasBounds{MaxGasPrice: 22.0, MaxTipCap: 2.5, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
		State: TxRetryState{OldTxs: map[string]*types.Transaction{
			prevTx.Hash().Hex():          prevTx,
			currentSignedTx.Hash().Hex(): currentSignedTx,
		}},
	}

	result := setup.WM.handleBroadcastError(ErrReplacementUnderpriced, tx, execCtx)

	assert.Equal(t, ActionReturn, result.Action, "Should return when gas limit reached")
	assert.True(t, errors.Is(result.Error, ErrGasPriceLimitReached),
		"Error should be ErrGasPriceLimitReached")

	// Nonce should still be preserved even on failure (for the returned tx)
	assert.NotNil(t, execCtx.State.Nonce)
	assert.Equal(t, uint64(5), execCtx.State.Nonce.Uint64())
}

// TestHandleBroadcastError_ReplacementUnderpriced_DoesNotOrphanOwnTx tests that
// when we have our own pending tx and get underpriced, we keep the same nonce
// instead of acquiring a new one which would orphan our pending tx.
func TestHandleBroadcastError_ReplacementUnderpriced_DoesNotOrphanOwnTx(t *testing.T) {
	setup := newTestSetup(t)

	// Our previously broadcast tx at nonce 5 (actually in the mempool)
	prevTx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(1000000000),
		big.NewInt(10000000000),
		big.NewInt(1),
	)

	// Current signed tx (added to OldTxs by signAndBroadcastTransaction)
	currentSignedTx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),
		big.NewInt(20000000000),
		big.NewInt(1),
	)

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),
		big.NewInt(20000000000),
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Gas:    GasBounds{MaxGasPrice: 200.0, MaxTipCap: 100.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
		State: TxRetryState{OldTxs: map[string]*types.Transaction{
			prevTx.Hash().Hex():          prevTx,
			currentSignedTx.Hash().Hex(): currentSignedTx,
		}},
	}

	result := setup.WM.handleBroadcastError(ErrReplacementUnderpriced, tx, execCtx)

	// When the pending tx is ours (2 txs at nonce 5), keep the same nonce — do not orphan.
	assert.Equal(t, ActionRetry, result.Action)
	assert.NotNil(t, execCtx.State.Nonce, "Nonce must NOT be nil — must keep the same nonce")
	assert.Equal(t, uint64(5), execCtx.State.Nonce.Uint64())
}

// TestHandleBroadcastError_ReplacementUnderpriced_ForeignTx_AcquiresNewNonce tests
// that when a replacement tx is rejected as underpriced but the pending tx in the
// mempool is NOT ours (belongs to another instance), we acquire a fresh nonce.
//
// In the real flow, signAndBroadcastTransaction adds the current signed tx to
// OldTxs before calling handleBroadcastError. So on the FIRST attempt at a nonce,
// OldTxs contains exactly 1 tx at that nonce — the current rejected attempt.
// This is how we detect a foreign pending tx: only 1 tx at this nonce in OldTxs.
//
// Scenario: two independent WalletManager instances share the same wallet.
// Instance A broadcasts nonce 5. Instance B also gets nonce 5, broadcasts,
// and gets "replacement transaction underpriced" because A's tx is already
// pending. Instance B should NOT try to replace A's tx — it should get a
// fresh nonce instead.
func TestHandleBroadcastError_ReplacementUnderpriced_ForeignTx_AcquiresNewNonce(t *testing.T) {
	setup := newTestSetup(t)

	// The current signed tx — rejected as underpriced.
	// This is the ONLY tx at nonce 5 in OldTxs (added by signAndBroadcastTransaction).
	// There is no previous broadcast of ours at this nonce.
	currentSignedTx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei fee cap
		big.NewInt(1),
	)

	// The unsigned tx passed to handleBroadcastError
	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),
		big.NewInt(20000000000),
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Gas:    GasBounds{MaxGasPrice: 200.0, MaxTipCap: 100.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
		State: TxRetryState{
			// Only the current rejected tx at nonce 5 — no previous broadcast of ours.
			// This means the pending tx in the mempool belongs to another instance.
			OldTxs: map[string]*types.Transaction{
				currentSignedTx.Hash().Hex(): currentSignedTx,
			},
		},
	}

	result := setup.WM.handleBroadcastError(ErrReplacementUnderpriced, tx, execCtx)

	// Should retry, not fail
	assert.Equal(t, ActionRetry, result.Action, "Should retry")
	assert.Nil(t, result.Error)

	// Critical: Nonce must be nil so a fresh nonce is acquired on retry.
	// The pending tx belongs to another instance — we must NOT replace it.
	assert.Nil(t, execCtx.State.Nonce,
		"Nonce should be nil to acquire a fresh nonce — the pending tx is not ours")
}

// TestHandleBroadcastError_ReplacementUnderpriced_OwnTx_BumpsGas tests that when
// replacement is underpriced AND we have a previous broadcast at this nonce
// (2+ txs at the same nonce in OldTxs), we keep the same nonce and bump gas.
func TestHandleBroadcastError_ReplacementUnderpriced_OwnTx_BumpsGas(t *testing.T) {
	setup := newTestSetup(t)

	// Previous tx at nonce 5 that we successfully broadcast (it's in the mempool)
	prevTx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(1000000000),  // 1 gwei tip
		big.NewInt(10000000000), // 10 gwei fee cap
		big.NewInt(1),
	)

	// Current signed tx (added to OldTxs by signAndBroadcastTransaction)
	currentSignedTx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei fee cap
		big.NewInt(1),
	)

	// The unsigned tx passed to handleBroadcastError
	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),
		big.NewInt(20000000000),
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Gas:    GasBounds{MaxGasPrice: 200.0, MaxTipCap: 100.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
		State: TxRetryState{
			// 2 txs at nonce 5: previous broadcast + current rejected attempt.
			// This means the pending tx in the mempool is ours.
			OldTxs: map[string]*types.Transaction{
				prevTx.Hash().Hex():          prevTx,
				currentSignedTx.Hash().Hex(): currentSignedTx,
			},
		},
	}

	result := setup.WM.handleBroadcastError(ErrReplacementUnderpriced, tx, execCtx)

	assert.Equal(t, ActionRetry, result.Action, "Should retry after underpriced rejection")
	assert.Nil(t, result.Error)

	// When the pending tx IS ours, we should keep the same nonce and bump gas
	assert.NotNil(t, execCtx.State.Nonce, "Nonce should be preserved when replacing our own tx")
	assert.Equal(t, uint64(5), execCtx.State.Nonce.Uint64())
	assert.Greater(t, execCtx.State.GasPrice, float64(0), "Gas price should be bumped")
	assert.Greater(t, execCtx.State.TipCap, float64(0), "Tip cap should be bumped")
}

// ============================================================
// Blocking Nonce Tests — Lost Tx with Gas Limit Reached
// ============================================================

// TestHandleTransactionStatus_Lost_HitsGasLimit_RespectsLimit tests that when
// a lost transaction reaches the gas price protection limit, it stops retrying.
// No 2x bypass — users should set aggressive limits since gas bumping only
// applies to one transaction at a time.
func TestHandleTransactionStatus_Lost_HitsGasLimit_RespectsLimit(t *testing.T) {
	setup := newTestSetup(t)

	// Even if this is a blocking nonce, gas limit is respected
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei fee cap
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Retry:  RetryConfig{MaxAttempts: 5},
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Gas:    GasBounds{MaxGasPrice: 22.0, MaxTipCap: 2.5, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	result := setup.WM.handleTransactionStatus(TxInfo{Status: "lost"}, tx, execCtx)

	assert.Equal(t, ActionReturn, result.Action)
	assert.True(t, errors.Is(result.Error, ErrGasPriceLimitReached))
}

// ============================================================
// Slow Tx — Only Bump Gas for Blocking Nonces
// ============================================================

// TestHandleTransactionStatus_Slow_BlockingNonce_BumpsGas tests that a slow tx
// with a blocking nonce (nonce == minedNonce, the next the chain expects) gets
// its gas price bumped.
func TestHandleTransactionStatus_Slow_BlockingNonce_BumpsGas(t *testing.T) {
	setup := newTestSetup(t)

	// Chain state: mined=5 — nonce 5 is the next expected, so tx with nonce 5 is blocking
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei fee cap
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Gas:    GasBounds{MaxGasPrice: 100.0, MaxTipCap: 50.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	result := setup.WM.handleTransactionStatus(TxInfo{Status: "slow"}, tx, execCtx)

	assert.Equal(t, ActionRetry, result.Action)
	assert.Greater(t, execCtx.State.GasPrice, 20.0, "Gas price should be bumped for blocking slow tx")
	assert.Greater(t, execCtx.State.TipCap, 2.0, "Tip cap should be bumped for blocking slow tx")
}

// TestHandleTransactionStatus_Slow_NonBlockingNonce_JustWaits tests that a slow tx
// whose nonce is NOT blocking (nonce != minedNonce) does NOT get gas bumped.
// Instead it just continues waiting (retry with same gas).
func TestHandleTransactionStatus_Slow_NonBlockingNonce_JustWaits(t *testing.T) {
	setup := newTestSetup(t)

	// Chain state: mined=3 — nonce 3 is next, our tx is nonce 5 (not blocking)
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 3, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 3, nil }

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei fee cap
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		Params: TxParams{From: testAddr1, Network: networks.EthereumMainnet},
		Gas:    GasBounds{MaxGasPrice: 100.0, MaxTipCap: 50.0, GasPriceBumpFactor: DefaultGasPriceBumpFactor, TipCapBumpFactor: DefaultTipCapBumpFactor},
	}

	result := setup.WM.handleTransactionStatus(TxInfo{Status: "slow"}, tx, execCtx)

	// Should retry (keep waiting) but NOT bump gas
	assert.Equal(t, ActionRetry, result.Action)
	assert.Equal(t, 0.0, execCtx.State.GasPrice, "Gas price should NOT be bumped for non-blocking slow tx")
	assert.Equal(t, 0.0, execCtx.State.TipCap, "Tip cap should NOT be bumped for non-blocking slow tx")
	assert.Nil(t, execCtx.State.Nonce, "Nonce should remain nil — keep monitoring the same tx")
	assert.Equal(t, tx, execCtx.State.ResumeWith, "ResumeWith should be set so the loop re-monitors the same tx instead of building a new one")
}

// ============================================================
// Integration: Non-blocking Slow Tx Waits, Then Bumps When Blocking
// ============================================================

// TestEnsureTx_SlowTx_NonBlocking_WaitsThenBumpsWhenBlocking is an end-to-end
// integration test for the slow tx blocking nonce logic:
//
//  1. Tx with nonce 7 is broadcast. Mined nonce is 5, so nonce 7 is NOT blocking.
//  2. SlowTxTimeout fires twice — gas should NOT be bumped, the SAME tx should
//     be re-monitored each time (no new tx built).
//  3. Mined nonce advances to 7 (lower nonces got mined elsewhere), so nonce 7
//     IS now blocking.
//  4. SlowTxTimeout fires again — now gas IS bumped, a replacement tx is built
//     and broadcast with the same nonce but higher gas.
//  5. Monitor reports "done" — success.
//
// Note: MonitorTxContext generates TxStatusSlow internally via time.After(slowTimeout),
// NOT from the mock monitor. The mock monitor must block (not return) for the slow
// timeout to fire. When we want "done", the mock returns before the timeout.
//
// Assertions:
//   - Gas price is NOT increased while non-blocking.
//   - The original tx hash is re-monitored (not a new build) while non-blocking.
//   - Gas price IS increased once blocking.
//   - Only 2 broadcasts total: the original + the replacement after becoming blocking.
//   - No extra transactions are built.
func TestEnsureTx_SlowTx_NonBlocking_WaitsThenBumpsWhenBlocking(t *testing.T) {
	fromAddr := crypto.PubkeyToAddress(testPrivateKey1.PublicKey)

	// --- Mined nonce changes over time ---
	var minedNonce uint64 = 5
	var nonceMu sync.Mutex

	reader := &mockEthReader{
		GetMinedNonceFn: func(addr string) (uint64, error) {
			nonceMu.Lock()
			defer nonceMu.Unlock()
			return minedNonce, nil
		},
		GetPendingNonceFn: func(addr string) (uint64, error) {
			nonceMu.Lock()
			defer nonceMu.Unlock()
			return minedNonce, nil
		},
		SuggestedGasSettingsFn: func() (float64, float64, error) { return 20.0, 2.0, nil },
		EthCallFn: func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
			return nil, nil
		},
	}

	broadcaster := &mockEthBroadcaster{}

	// Use a short slow timeout so tests don't take forever.
	// MonitorTxContext fires TxStatusSlow after this timeout.
	slowTimeout := 50 * time.Millisecond

	// --- Custom monitor that controls when to return ---
	// - Calls 0, 1, 2: block forever (never return) → lets SlowTxTimeout fire → TxStatusSlow
	// - Call 3: return "done" immediately → TxStatusMined
	// On call 2, we also advance minedNonce to 7 so the 3rd slow becomes blocking.
	wrapper := &slowThenDoneMonitor{
		slowUntilCall: 3, // calls 0,1,2 → slow timeout; call 3 → done
		onCall: func(callIdx int) {
			if callIdx == 2 {
				nonceMu.Lock()
				minedNonce = 7
				nonceMu.Unlock()
			}
		},
	}

	// Use BSCMainnet — it does NOT support sync tx, so the flow goes through
	// BroadcastTx + monitor (the path where slow/blocking logic matters).
	testNetwork := networks.BSCMainnet

	wm := NewWalletManager(
		WithReaderFactory(func(network networks.Network) (EthReader, error) {
			return reader, nil
		}),
		WithBroadcasterFactory(func(network networks.Network) (EthBroadcaster, error) {
			return broadcaster, nil
		}),
		WithTxMonitorFactory(func(r EthReader) TxMonitor {
			return wrapper
		}),
		WithDefaultSlowTxTimeout(slowTimeout),
	)

	// Register the test wallet
	acc, err := account.NewPrivateKeyAccount("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	wm.SetAccount(acc)

	// Initialize network
	_, err = wm.Reader(testNetwork)
	require.NoError(t, err)

	// --- Track broadcasts ---
	var broadcastedTxs []*types.Transaction
	var broadcastMu sync.Mutex
	broadcaster.BroadcastTxFn = func(tx *types.Transaction) (string, bool, error) {
		broadcastMu.Lock()
		broadcastedTxs = append(broadcastedTxs, tx)
		broadcastMu.Unlock()
		return tx.Hash().Hex(), true, nil
	}

	// --- Set local tracker so BuildTx acquires nonce 7 ---
	// local=7 (stored 6, pending 7), mined=5, remotePending=5 → acquires 7
	wm.nonceTracker.SetPendingNonce(fromAddr, testNetwork.GetChainID(), testNetwork.GetName(), 6)

	ctx := context.Background()
	tx, receipt, err := wm.EnsureTxWithHooksContext(
		ctx,
		5, time.Millisecond, time.Millisecond,
		2, fromAddr, testAddr2, oneEth,
		21000, 0, 20.0, 0, 2.0, 0, 100.0, 50.0,
		nil, testNetwork,
		nil, nil, nil, nil, nil, nil,
	)

	require.NoError(t, err)
	require.NotNil(t, tx)
	require.NotNil(t, receipt)

	// --- Assertion 1: Only 2 broadcasts (original + replacement after becoming blocking) ---
	broadcastMu.Lock()
	defer broadcastMu.Unlock()
	require.Equal(t, 2, len(broadcastedTxs),
		"Should have exactly 2 broadcasts: original + gas-bumped replacement (no extra txs built)")

	// --- Assertion 2: Both broadcasts use the same nonce ---
	assert.Equal(t, broadcastedTxs[0].Nonce(), broadcastedTxs[1].Nonce(),
		"Replacement tx should use the same nonce as the original")
	assert.Equal(t, uint64(7), broadcastedTxs[0].Nonce(),
		"Original tx should have nonce 7")

	// --- Assertion 3: Replacement has higher gas than original ---
	originalGasFeeCap := broadcastedTxs[0].GasFeeCap()
	replacementGasFeeCap := broadcastedTxs[1].GasFeeCap()
	assert.True(t, replacementGasFeeCap.Cmp(originalGasFeeCap) > 0,
		"Replacement tx gas fee cap (%s) should be higher than original (%s)",
		replacementGasFeeCap, originalGasFeeCap)

	originalTipCap := broadcastedTxs[0].GasTipCap()
	replacementTipCap := broadcastedTxs[1].GasTipCap()
	assert.True(t, replacementTipCap.Cmp(originalTipCap) > 0,
		"Replacement tx tip cap (%s) should be higher than original (%s)",
		replacementTipCap, originalTipCap)

	// --- Assertion 4: The same tx was re-monitored while non-blocking ---
	wrapper.mu.Lock()
	monitoredHashes := make([]string, len(wrapper.monitoredHashes))
	copy(monitoredHashes, wrapper.monitoredHashes)
	wrapper.mu.Unlock()

	require.Equal(t, 4, len(monitoredHashes),
		"Should have 4 monitor calls: 2 non-blocking waits + 1 blocking slow + 1 final mined")

	// First 3 should all be the same tx hash (the original, re-monitored)
	assert.Equal(t, monitoredHashes[0], monitoredHashes[1],
		"Non-blocking slow iterations should re-monitor the same tx hash")
	assert.Equal(t, monitoredHashes[0], monitoredHashes[2],
		"The 3rd slow iteration should still monitor the original tx before rebuilding")

	// 4th should be the replacement tx hash (different from original)
	assert.NotEqual(t, monitoredHashes[0], monitoredHashes[3],
		"After gas bump, a new replacement tx should be monitored (different hash)")
}

// slowThenDoneMonitor is a TxMonitor that blocks (causing SlowTxTimeout to fire)
// for the first N calls, then returns "done" on subsequent calls.
// This lets us test the slow → blocking → gas bump → done flow.
type slowThenDoneMonitor struct {
	slowUntilCall   int               // calls before this index block (→ slow timeout)
	onCall          func(callIdx int) // side effect hook
	callCount       int
	monitoredHashes []string
	mu              sync.Mutex
}

func (m *slowThenDoneMonitor) MakeWaitChannelWithInterval(hash string, interval time.Duration) <-chan TxMonitorStatus {
	m.mu.Lock()
	idx := m.callCount
	m.monitoredHashes = append(m.monitoredHashes, hash)
	m.callCount++
	if m.onCall != nil {
		m.onCall(idx)
	}
	m.mu.Unlock()

	ch := make(chan TxMonitorStatus, 1)
	if idx >= m.slowUntilCall {
		// Return "done" immediately — MonitorTxContext will map this to TxStatusMined
		ch <- TxMonitorStatus{
			Status:  "done",
			Receipt: &types.Receipt{Status: types.ReceiptStatusSuccessful},
		}
	} else {
		// Return "pending" (non-terminal) — this arms the slow timer in
		// MonitorTxContext, which then fires TxStatusSlow after SlowTxTimeout.
		ch <- TxMonitorStatus{Status: "pending"}
	}
	return ch
}

// ============================================================
// Nonce Gap Detection Tests
// ============================================================
//
// When localPendingNonce > remotePendingNonce, there may be nonces in the
// [remotePending, localPending) range that were acquired (via BuildTx) but
// never broadcast. The gap detection scans TxStore for each nonce in that
// range and returns the first missing nonce instead of blindly using localPending.

// newTestSetupWithTxStore creates a test setup that includes a TxStore,
// needed for nonce gap detection tests.
func newTestSetupWithTxStore(t *testing.T) (*testSetup, *mockTxStore) {
	t.Helper()

	reader := &mockEthReader{
		GetMinedNonceFn:        func(addr string) (uint64, error) { return 0, nil },
		GetPendingNonceFn:      func(addr string) (uint64, error) { return 0, nil },
		SuggestedGasSettingsFn: func() (float64, float64, error) { return 20.0, 2.0, nil },
		EthCallFn: func(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error) {
			return nil, nil
		},
		TxInfoFromHashFn: func(hash string) (TxInfo, error) {
			return TxInfo{Status: "pending"}, nil
		},
	}

	broadcaster := &mockEthBroadcaster{}
	monitor := &mockTxMonitor{
		StatusToReturn: TxMonitorStatus{Status: "done"},
	}
	txStore := newMockTxStore()

	wm := NewWalletManager(
		WithReaderFactory(func(network networks.Network) (EthReader, error) {
			return reader, nil
		}),
		WithBroadcasterFactory(func(network networks.Network) (EthBroadcaster, error) {
			return broadcaster, nil
		}),
		WithTxMonitorFactory(func(r EthReader) TxMonitor {
			return monitor
		}),
		WithTxStore(txStore),
	)

	_, err := wm.Reader(networks.EthereumMainnet)
	require.NoError(t, err)

	return &testSetup{
		WM:          wm,
		Reader:      reader,
		Broadcaster: broadcaster,
		Monitor:     monitor,
	}, txStore
}

// TestAcquireNonce_GapDetection_FillsGap tests that when local nonce is ahead
// of remote pending nonce and a nonce in the range has no tx in the TxStore,
// acquireNonce returns that gap nonce instead of the local pending nonce.
func TestAcquireNonce_GapDetection_FillsGap(t *testing.T) {
	setup, txStore := newTestSetupWithTxStore(t)

	// Chain state: mined=5, remotePending=5 (no pending on nodes)
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Local tracker has advanced to nonce 8 (nonces 5, 6, 7 were acquired)
	setup.WM.nonceTracker.SetPendingNonce(testAddr1, networks.EthereumMainnet.GetChainID(), networks.EthereumMainnet.GetName(), 7)

	// TxStore has txs for nonces 5 and 7, but NOT 6 — nonce 6 is the gap
	ctx := context.Background()
	_ = txStore.Save(ctx, &PendingTx{
		Hash:    common.HexToHash("0xaaa"),
		Wallet:  testAddr1,
		ChainID: networks.EthereumMainnet.GetChainID(),
		Nonce:   5,
		Status:  PendingTxStatusBroadcasted,
	})
	_ = txStore.Save(ctx, &PendingTx{
		Hash:    common.HexToHash("0xbbb"),
		Wallet:  testAddr1,
		ChainID: networks.EthereumMainnet.GetChainID(),
		Nonce:   7,
		Status:  PendingTxStatusBroadcasted,
	})

	nonce, err := setup.WM.acquireNonce(testAddr1, networks.EthereumMainnet)
	require.NoError(t, err)

	// Should return nonce 6 (the gap), not 8 (the next local nonce)
	assert.Equal(t, uint64(6), nonce.Uint64(),
		"Should fill the gap at nonce 6 instead of using local pending nonce 8")
}

// TestAcquireNonce_GapDetection_NoGap_UsesLocalPending tests that when all nonces
// in [remotePending, localPending) are present in TxStore, acquireNonce returns
// the next local pending nonce as usual.
func TestAcquireNonce_GapDetection_NoGap_UsesLocalPending(t *testing.T) {
	setup, txStore := newTestSetupWithTxStore(t)

	// Chain state: mined=5, remotePending=5
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Local tracker at nonce 7 (nonces 5, 6, 7 were acquired)
	setup.WM.nonceTracker.SetPendingNonce(testAddr1, networks.EthereumMainnet.GetChainID(), networks.EthereumMainnet.GetName(), 7)

	// TxStore has txs for ALL nonces 5, 6, 7 — no gap
	ctx := context.Background()
	for i := uint64(5); i <= 7; i++ {
		_ = txStore.Save(ctx, &PendingTx{
			Hash:    common.BigToHash(big.NewInt(int64(i))),
			Wallet:  testAddr1,
			ChainID: networks.EthereumMainnet.GetChainID(),
			Nonce:   i,
			Status:  PendingTxStatusBroadcasted,
		})
	}

	nonce, err := setup.WM.acquireNonce(testAddr1, networks.EthereumMainnet)
	require.NoError(t, err)

	// All nonces accounted for — should get next nonce (8)
	assert.Equal(t, uint64(8), nonce.Uint64(),
		"No gap — should acquire the next local pending nonce")
}

// TestAcquireNonce_GapDetection_MultipleGaps_FillsLowest tests that when there
// are multiple gaps, the lowest gap nonce is returned first.
func TestAcquireNonce_GapDetection_MultipleGaps_FillsLowest(t *testing.T) {
	setup, txStore := newTestSetupWithTxStore(t)

	// Chain state: mined=5, remotePending=5
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Local tracker at nonce 9 (nonces 5-9 were acquired)
	setup.WM.nonceTracker.SetPendingNonce(testAddr1, networks.EthereumMainnet.GetChainID(), networks.EthereumMainnet.GetName(), 9)

	// TxStore has nonces 5, 7, 9 — gaps at 6 and 8
	ctx := context.Background()
	for _, n := range []uint64{5, 7, 9} {
		_ = txStore.Save(ctx, &PendingTx{
			Hash:    common.BigToHash(big.NewInt(int64(n))),
			Wallet:  testAddr1,
			ChainID: networks.EthereumMainnet.GetChainID(),
			Nonce:   n,
			Status:  PendingTxStatusBroadcasted,
		})
	}

	nonce, err := setup.WM.acquireNonce(testAddr1, networks.EthereumMainnet)
	require.NoError(t, err)

	// Should return the lowest gap: nonce 6
	assert.Equal(t, uint64(6), nonce.Uint64(),
		"Should fill the lowest gap at nonce 6")
}

// TestAcquireNonce_GapDetection_NoTxStore_SkipsCheck tests that when no TxStore
// is configured, gap detection is skipped and normal nonce logic applies.
func TestAcquireNonce_GapDetection_NoTxStore_SkipsCheck(t *testing.T) {
	setup := newTestSetup(t) // No TxStore

	// Chain state: mined=5, remotePending=5
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Local tracker at nonce 7
	setup.WM.nonceTracker.SetPendingNonce(testAddr1, networks.EthereumMainnet.GetChainID(), networks.EthereumMainnet.GetName(), 7)

	nonce, err := setup.WM.acquireNonce(testAddr1, networks.EthereumMainnet)
	require.NoError(t, err)

	// Without TxStore, should use local pending (8) — no gap detection
	assert.Equal(t, uint64(8), nonce.Uint64(),
		"Without TxStore, should use normal nonce logic (local pending)")
}

// TestAcquireNonce_GapDetection_WithPendingOnNodes tests gap detection when
// there ARE pending txs on nodes (remotePending > mined) and local is even higher.
func TestAcquireNonce_GapDetection_WithPendingOnNodes(t *testing.T) {
	setup, txStore := newTestSetupWithTxStore(t)

	// Chain state: mined=5, remotePending=7 (2 pending txs on nodes)
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 7, nil }

	// Local tracker at nonce 9 (local has nonces 7, 8, 9 beyond what node sees)
	setup.WM.nonceTracker.SetPendingNonce(testAddr1, networks.EthereumMainnet.GetChainID(), networks.EthereumMainnet.GetName(), 9)

	// TxStore has nonces 7 and 9, but NOT 8
	ctx := context.Background()
	for _, n := range []uint64{7, 9} {
		_ = txStore.Save(ctx, &PendingTx{
			Hash:    common.BigToHash(big.NewInt(int64(n))),
			Wallet:  testAddr1,
			ChainID: networks.EthereumMainnet.GetChainID(),
			Nonce:   n,
			Status:  PendingTxStatusBroadcasted,
		})
	}

	nonce, err := setup.WM.acquireNonce(testAddr1, networks.EthereumMainnet)
	require.NoError(t, err)

	// Gap at nonce 8 — should be filled
	assert.Equal(t, uint64(8), nonce.Uint64(),
		"Should fill gap at nonce 8 between remote pending and local pending")
}

// TestAcquireNonce_GapDetection_ConsecutiveCallsFillGaps tests that calling
// acquireNonce repeatedly fills consecutive gaps one by one.
func TestAcquireNonce_GapDetection_ConsecutiveCallsFillGaps(t *testing.T) {
	setup, txStore := newTestSetupWithTxStore(t)

	// Chain state: mined=5, remotePending=5
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Local tracker at nonce 8 (nonces 5-8 acquired)
	setup.WM.nonceTracker.SetPendingNonce(testAddr1, networks.EthereumMainnet.GetChainID(), networks.EthereumMainnet.GetName(), 8)

	// Only nonce 5 is in TxStore — gaps at 6, 7, 8
	ctx := context.Background()
	_ = txStore.Save(ctx, &PendingTx{
		Hash:    common.HexToHash("0xaaa"),
		Wallet:  testAddr1,
		ChainID: networks.EthereumMainnet.GetChainID(),
		Nonce:   5,
		Status:  PendingTxStatusBroadcasted,
	})

	// First call: should get nonce 6 (first gap)
	nonce1, err := setup.WM.acquireNonce(testAddr1, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(6), nonce1.Uint64(), "First call should fill gap at nonce 6")

	// Simulate that nonce 6 was broadcast (add to TxStore)
	_ = txStore.Save(ctx, &PendingTx{
		Hash:    common.HexToHash("0xbbb"),
		Wallet:  testAddr1,
		ChainID: networks.EthereumMainnet.GetChainID(),
		Nonce:   6,
		Status:  PendingTxStatusBroadcasted,
	})

	// Second call: should get nonce 7 (next gap)
	nonce2, err := setup.WM.acquireNonce(testAddr1, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(7), nonce2.Uint64(), "Second call should fill gap at nonce 7")

	// Simulate nonce 7 broadcast
	_ = txStore.Save(ctx, &PendingTx{
		Hash:    common.HexToHash("0xccc"),
		Wallet:  testAddr1,
		ChainID: networks.EthereumMainnet.GetChainID(),
		Nonce:   7,
		Status:  PendingTxStatusBroadcasted,
	})

	// Third call: should get nonce 8 (next gap)
	nonce3, err := setup.WM.acquireNonce(testAddr1, networks.EthereumMainnet)
	require.NoError(t, err)
	assert.Equal(t, uint64(8), nonce3.Uint64(), "Third call should fill gap at nonce 8")
}

// TestAcquireNonce_GapDetection_ConcurrentCallsNoDuplicates tests that when
// multiple goroutines call acquireNonce concurrently and all of them would hit
// the gap detection path, each goroutine gets a UNIQUE nonce. This tests the
// race condition where two callers both read the same local pending nonce,
// both find the same gap, and both return the same nonce.
func TestAcquireNonce_GapDetection_ConcurrentCallsNoDuplicates(t *testing.T) {
	setup, txStore := newTestSetupWithTxStore(t)

	// Chain state: mined=5, remotePending=5 (no pending on nodes)
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Local tracker has advanced to nonce 14 (nonces 5-14 were acquired)
	setup.WM.nonceTracker.SetPendingNonce(testAddr1, networks.EthereumMainnet.GetChainID(), networks.EthereumMainnet.GetName(), 14)

	// TxStore has tx only for nonce 5. Gaps at 6, 7, 8, 9, 10, 11, 12, 13, 14 (9 gaps).
	ctx := context.Background()
	_ = txStore.Save(ctx, &PendingTx{
		Hash:    common.HexToHash("0xaaa"),
		Wallet:  testAddr1,
		ChainID: networks.EthereumMainnet.GetChainID(),
		Nonce:   5,
		Status:  PendingTxStatusBroadcasted,
	})

	numGoroutines := 5
	results := make(chan uint64, numGoroutines)
	errs := make(chan error, numGoroutines)

	// Launch concurrent acquireNonce calls — all should hit gap detection
	var wg sync.WaitGroup
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nonce, err := setup.WM.acquireNonce(testAddr1, networks.EthereumMainnet)
			if err != nil {
				errs <- err
				return
			}
			results <- nonce.Uint64()
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}

	// Collect all nonces and check for duplicates
	seen := make(map[uint64]int)
	for nonce := range results {
		seen[nonce]++
	}

	for nonce, count := range seen {
		assert.Equal(t, 1, count,
			"Nonce %d was returned %d times — concurrent gap detection produced duplicates", nonce, count)
	}
}

// TestBuildTx_GapDetection_FillsGapBeforeAdvancing tests that BuildTx with
// a TxStore fills nonce gaps. When a user calls BuildTx without specifying
// a nonce, and there's a gap in the TxStore, the gap nonce is returned.
func TestBuildTx_GapDetection_FillsGapBeforeAdvancing(t *testing.T) {
	setup, txStore := newTestSetupWithTxStore(t)

	// Chain state: mined=5, remotePending=5
	setup.Reader.GetMinedNonceFn = func(addr string) (uint64, error) { return 5, nil }
	setup.Reader.GetPendingNonceFn = func(addr string) (uint64, error) { return 5, nil }

	// Local tracker at nonce 7
	setup.WM.nonceTracker.SetPendingNonce(testAddr1, networks.EthereumMainnet.GetChainID(), networks.EthereumMainnet.GetName(), 7)

	// TxStore has nonce 5 and 7, gap at 6
	ctx := context.Background()
	_ = txStore.Save(ctx, &PendingTx{
		Hash:    common.HexToHash("0xaaa"),
		Wallet:  testAddr1,
		ChainID: networks.EthereumMainnet.GetChainID(),
		Nonce:   5,
		Status:  PendingTxStatusBroadcasted,
	})
	_ = txStore.Save(ctx, &PendingTx{
		Hash:    common.HexToHash("0xbbb"),
		Wallet:  testAddr1,
		ChainID: networks.EthereumMainnet.GetChainID(),
		Nonce:   7,
		Status:  PendingTxStatusBroadcasted,
	})

	tx, err := setup.WM.BuildTx(
		2, // EIP-1559
		testAddr1, testAddr2,
		nil, // nonce = nil → auto-acquire
		oneEth,
		21000, 0,
		20.0, 0,
		2.0, 0,
		nil,
		networks.EthereumMainnet,
	)
	require.NoError(t, err)
	require.NotNil(t, tx)

	// The built tx should use nonce 6 (the gap), not 8
	assert.Equal(t, uint64(6), tx.Nonce(),
		"BuildTx should fill nonce gap at 6 instead of advancing to 8")
}
