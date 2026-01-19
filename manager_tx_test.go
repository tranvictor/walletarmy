package walletarmy

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient/gethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tranvictor/jarvis/networks"
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
	assert.Equal(t, "mined", status.Status)
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
	assert.Equal(t, "reverted", status.Status)
	assert.Equal(t, receipt, status.Receipt)
}

func TestMonitorTxContext_ReturnsCancelledOnContextCancel(t *testing.T) {
	setup := newTestSetup(t)

	// Set a delay so we can cancel before status is returned
	setup.Monitor.Delay = 5 * time.Second

	tx := newTestTx(0, testAddr2, oneEth)

	ctx, cancel := context.WithCancel(context.Background())

	statusChan := setup.WM.MonitorTxContext(ctx, tx, networks.EthereumMainnet, time.Second)

	// Cancel context immediately
	cancel()

	status := <-statusChan
	assert.Equal(t, "cancelled", status.Status)
	assert.Nil(t, status.Receipt)
}

// ============================================================
// TxExecutionContext Tests
// ============================================================

func TestTxExecutionContext_AdjustGasPricesForSlowTx_Success(t *testing.T) {
	ctx := &TxExecutionContext{
		MaxGasPrice: 200.0, // High limit
		MaxTipCap:   100.0, // High limit
	}

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei tip
		big.NewInt(20000000000), // 20 gwei max fee
		big.NewInt(1),
	)

	adjusted := ctx.AdjustGasPricesForSlowTx(tx)

	assert.True(t, adjusted)
	// Gas price should be increased by GasPriceIncreasePercent (1.2)
	expectedGasPrice := 20.0 * GasPriceIncreasePercent
	assert.InDelta(t, expectedGasPrice, ctx.RetryGasPrice, 0.001)
	// Tip cap should be increased by TipCapIncreasePercent (1.1)
	expectedTipCap := 2.0 * TipCapIncreasePercent
	assert.InDelta(t, expectedTipCap, ctx.RetryTipCap, 0.001)
	// Nonce should be preserved
	assert.Equal(t, big.NewInt(5), ctx.RetryNonce)
}

func TestTxExecutionContext_AdjustGasPricesForSlowTx_HitsGasPriceLimit(t *testing.T) {
	ctx := &TxExecutionContext{
		MaxGasPrice: 22.0, // Low limit - 20 * 1.2 = 24 would exceed
		MaxTipCap:   100.0,
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
		MaxGasPrice: 200.0,
		MaxTipCap:   2.1, // Low limit - 2 * 1.1 = 2.2 would exceed
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
				NumRetries:       tt.numRetries,
				ActualRetryCount: tt.currentRetries,
			}

			result := ctx.IncrementRetryCountAndCheck("test error")

			if tt.expectResult {
				assert.NotNil(t, result)
				assert.True(t, result.ShouldReturn)
				assert.False(t, result.ShouldRetry)
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
		NumRetries: 5,
		OldTxs: map[string]*types.Transaction{
			oldTx.Hash().Hex(): oldTx,
		},
		Network: networks.EthereumMainnet,
	}

	newTx := newTestTx(6, testAddr2, oneEth)

	result := setup.WM.handleNonceIsLowError(newTx, execCtx)

	// Should return the old tx that was mined
	assert.True(t, result.ShouldReturn)
	assert.False(t, result.ShouldRetry)
	assert.Equal(t, oldTx, result.Transaction)
	assert.Nil(t, result.Error)
}

func TestHandleBroadcastError_TxIsKnown_RetriesWithSameNonce(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		NumRetries: 5,
		OldTxs:     make(map[string]*types.Transaction),
		Network:    networks.EthereumMainnet,
	}

	result := setup.WM.handleBroadcastError(ErrTxIsKnown, tx, execCtx)

	assert.False(t, result.ShouldReturn)
	assert.True(t, result.ShouldRetry)
	assert.Equal(t, big.NewInt(5), execCtx.RetryNonce)
}

func TestHandleBroadcastError_InsufficientFunds_IncrementsRetry(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		NumRetries:       5,
		ActualRetryCount: 0,
		OldTxs:           make(map[string]*types.Transaction),
		Network:          networks.EthereumMainnet,
	}

	result := setup.WM.handleBroadcastError(ErrInsufficientFund, tx, execCtx)

	assert.False(t, result.ShouldReturn)
	assert.True(t, result.ShouldRetry)
	assert.Equal(t, 1, execCtx.ActualRetryCount)
	assert.Equal(t, big.NewInt(5), execCtx.RetryNonce)
}

func TestHandleBroadcastError_ExceedsRetries(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		NumRetries:       2,
		ActualRetryCount: 2, // Already at limit
		OldTxs:           make(map[string]*types.Transaction),
		Network:          networks.EthereumMainnet,
	}

	result := setup.WM.handleBroadcastError(ErrInsufficientFund, tx, execCtx)

	assert.True(t, result.ShouldReturn)
	assert.False(t, result.ShouldRetry)
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

	result := setup.WM.handleTransactionStatus(TxStatus{Status: "mined", Receipt: receipt}, tx, execCtx)

	assert.True(t, result.ShouldReturn)
	assert.False(t, result.ShouldRetry)
	assert.Nil(t, result.Error)
	assert.Equal(t, tx, result.Transaction)
	assert.Equal(t, receipt, result.Receipt)
}

func TestHandleTransactionStatus_Reverted(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newFailedReceipt(tx)

	execCtx := &TxExecutionContext{}

	result := setup.WM.handleTransactionStatus(TxStatus{Status: "reverted", Receipt: receipt}, tx, execCtx)

	assert.True(t, result.ShouldReturn)
	assert.False(t, result.ShouldRetry)
	assert.Nil(t, result.Error)
	assert.Equal(t, tx, result.Transaction)
	assert.Equal(t, receipt, result.Receipt)
}

func TestHandleTransactionStatus_Lost_Retries(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)

	execCtx := &TxExecutionContext{
		NumRetries:       5,
		ActualRetryCount: 0,
	}

	result := setup.WM.handleTransactionStatus(TxStatus{Status: "lost"}, tx, execCtx)

	assert.False(t, result.ShouldReturn)
	assert.True(t, result.ShouldRetry)
	assert.Equal(t, 1, execCtx.ActualRetryCount)
	assert.Nil(t, execCtx.RetryNonce) // Should get new nonce
}

func TestHandleTransactionStatus_Slow_BumpsGas(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),  // 2 gwei
		big.NewInt(20000000000), // 20 gwei
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		MaxGasPrice: 100.0,
		MaxTipCap:   50.0,
	}

	result := setup.WM.handleTransactionStatus(TxStatus{Status: "slow"}, tx, execCtx)

	assert.False(t, result.ShouldReturn)
	assert.True(t, result.ShouldRetry)
	assert.Greater(t, execCtx.RetryGasPrice, 20.0) // Should be bumped
	assert.Greater(t, execCtx.RetryTipCap, 2.0)    // Should be bumped
}

func TestHandleTransactionStatus_Slow_HitsLimit(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestDynamicTx(5, testAddr2, oneEth, 21000,
		big.NewInt(2000000000),
		big.NewInt(20000000000),
		big.NewInt(1),
	)

	execCtx := &TxExecutionContext{
		MaxGasPrice: 22.0, // Low limit
		MaxTipCap:   50.0,
	}

	result := setup.WM.handleTransactionStatus(TxStatus{Status: "slow"}, tx, execCtx)

	assert.True(t, result.ShouldReturn)
	assert.False(t, result.ShouldRetry)
	assert.True(t, errors.Is(result.Error, ErrGasPriceLimitReached))
}

// ============================================================
// handleGasEstimationFailure Tests
// ============================================================

func TestHandleGasEstimationFailure_NoOldTxs_Retries(t *testing.T) {
	setup := newTestSetup(t)

	execCtx := &TxExecutionContext{
		NumRetries:       5,
		ActualRetryCount: 0,
		OldTxs:           make(map[string]*types.Transaction),
		Network:          networks.EthereumMainnet,
	}

	err := errors.Join(ErrEstimateGasFailed, errors.New("execution reverted"))
	result := setup.WM.handleGasEstimationFailure(execCtx, nil, err)

	assert.False(t, result.ShouldReturn)
	assert.True(t, result.ShouldRetry)
	assert.Equal(t, 1, execCtx.ActualRetryCount)
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
		NumRetries: 5,
		OldTxs: map[string]*types.Transaction{
			oldTx.Hash().Hex(): oldTx,
		},
		Network: networks.EthereumMainnet,
	}

	err := errors.Join(ErrEstimateGasFailed, errors.New("execution reverted"))
	result := setup.WM.handleGasEstimationFailure(execCtx, nil, err)

	assert.True(t, result.ShouldReturn)
	assert.False(t, result.ShouldRetry)
	assert.Equal(t, oldTx, result.Transaction)
}

func TestHandleGasEstimationFailure_ExceedsRetries(t *testing.T) {
	setup := newTestSetup(t)

	execCtx := &TxExecutionContext{
		NumRetries:       2,
		ActualRetryCount: 2, // Already at limit
		OldTxs:           make(map[string]*types.Transaction),
		Network:          networks.EthereumMainnet,
	}

	err := errors.Join(ErrEstimateGasFailed, errors.New("execution reverted"))
	result := setup.WM.handleGasEstimationFailure(execCtx, nil, err)

	assert.True(t, result.ShouldReturn)
	assert.False(t, result.ShouldRetry)
	assert.True(t, errors.Is(result.Error, ErrEnsureTxOutOfRetries))
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
		NumRetries:    5,
		TxType:        2,
		From:          testAddr1,
		To:            testAddr2,
		Value:         oneEth,
		GasLimit:      21000,
		RetryGasPrice: 20.0,
		RetryTipCap:   2.0,
		Data:          nil,
		Network:       networks.EthereumMainnet,
		OldTxs:        make(map[string]*types.Transaction),
	}

	result := setup.WM.executeTransactionAttempt(context.Background(), execCtx, nil)

	// Should return error since simulation failed (not a revert)
	assert.True(t, result.ShouldReturn)
	assert.False(t, result.ShouldRetry)
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
		NumRetries:    5,
		TxType:        2,
		From:          testAddr1,
		To:            testAddr2,
		Value:         oneEth,
		GasLimit:      21000,
		RetryGasPrice: 20.0,
		RetryTipCap:   2.0,
		Data:          nil,
		Network:       networks.EthereumMainnet,
		OldTxs:        make(map[string]*types.Transaction),
	}

	result := setup.WM.executeTransactionAttempt(context.Background(), execCtx, nil)

	// The test should pass simulation and fail at signing (because we have no account)
	// This confirms simulation was successful
	assert.True(t, result.ShouldReturn)
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
		TxMinedHook: func(tx *types.Transaction, r *types.Receipt) error {
			hookCalled = true
			receivedTx = tx
			receivedReceipt = r
			return nil
		},
	}

	result := setup.WM.handleTransactionStatus(TxStatus{Status: "mined", Receipt: receipt}, tx, execCtx)

	assert.True(t, hookCalled)
	assert.Equal(t, tx, receivedTx)
	assert.Equal(t, receipt, receivedReceipt)
	assert.True(t, result.ShouldReturn)
	assert.Nil(t, result.Error)
}

func TestHandleTransactionStatus_TxMinedHook_ReturnsError(t *testing.T) {
	setup := newTestSetup(t)

	tx := newTestTx(5, testAddr2, oneEth)
	receipt := newSuccessReceipt(tx)

	hookError := errors.New("hook failed")

	execCtx := &TxExecutionContext{
		TxMinedHook: func(tx *types.Transaction, r *types.Receipt) error {
			return hookError
		},
	}

	result := setup.WM.handleTransactionStatus(TxStatus{Status: "mined", Receipt: receipt}, tx, execCtx)

	assert.True(t, result.ShouldReturn)
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "hook error")
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
