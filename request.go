package walletarmy

import (
	"context"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/tranvictor/jarvis/networks"
	"github.com/tranvictor/walletarmy/idempotency"
)

// TxRequest represents a transaction request with builder pattern
type TxRequest struct {
	wm *WalletManager

	numRetries      int
	sleepDuration   time.Duration
	txCheckInterval time.Duration

	// Transaction parameters
	txType          uint8
	from, to        common.Address
	value           *big.Int
	gasLimit              uint64
	extraGasLimit         uint64
	gasLimitBufferPercent uint64
	gasPrice        float64
	extraGasPrice   float64
	tipCapGwei      float64
	extraTipCapGwei float64
	maxGasPrice     float64
	maxTipCap       float64
	data            []byte
	network         networks.Network

	// Hooks
	beforeSignAndBroadcastHook Hook
	afterSignAndBroadcastHook  Hook
	gasEstimationFailedHook    GasEstimationFailedHook
	simulationFailedHook       SimulationFailedHook
	txMinedHook                TxMinedHook
	abis                       []abi.ABI

	// Skip eth_call simulation before broadcasting
	skipSimulation bool

	// Idempotency key for preventing duplicate transactions
	idempotencyKey string
}

// R creates a new transaction request (similar to go-resty's R() method).
// The request inherits default configuration from the WalletManager.
func (wm *WalletManager) R() *TxRequest {
	// Use Defaults() to safely read with lock.
	// All fields are guaranteed non-zero by applyDefaultsResolution.
	defaults := wm.Defaults()

	return &TxRequest{
		wm:              wm,
		value:           big.NewInt(0),
		network:         defaults.Network,
		numRetries:      defaults.NumRetries,
		sleepDuration:   defaults.SleepDuration,
		txCheckInterval: defaults.TxCheckInterval,
		txType:          defaults.TxType,
		extraGasLimit:         defaults.ExtraGasLimit,
		gasLimitBufferPercent: defaults.GasLimitBufferPercent,
		extraGasPrice:         defaults.ExtraGasPrice,
		extraTipCapGwei: defaults.ExtraTipCap,
		maxGasPrice:     defaults.MaxGasPrice,
		maxTipCap:       defaults.MaxTipCap,
	}
}

// SetNumRetries sets the number of retries
func (r *TxRequest) SetNumRetries(numRetries int) *TxRequest {
	r.numRetries = numRetries
	return r
}

// SetSleepDuration sets the sleep duration
func (r *TxRequest) SetSleepDuration(sleepDuration time.Duration) *TxRequest {
	r.sleepDuration = sleepDuration
	return r
}

// SetTxCheckInterval sets the transaction check interval
func (r *TxRequest) SetTxCheckInterval(txCheckInterval time.Duration) *TxRequest {
	r.txCheckInterval = txCheckInterval
	return r
}

// SetTxType sets the transaction type
func (r *TxRequest) SetTxType(txType uint8) *TxRequest {
	r.txType = txType
	return r
}

// SetFrom sets the from address
func (r *TxRequest) SetFrom(from common.Address) *TxRequest {
	r.from = from
	return r
}

// SetTo sets the to address
func (r *TxRequest) SetTo(to common.Address) *TxRequest {
	r.to = to
	return r
}

// SetValue sets the transaction value
func (r *TxRequest) SetValue(value *big.Int) *TxRequest {
	if value != nil {
		r.value = value
	}
	return r
}

// SetGasLimit sets the gas limit
func (r *TxRequest) SetGasLimit(gasLimit uint64) *TxRequest {
	r.gasLimit = gasLimit
	return r
}

// SetExtraGasLimit sets the extra gas limit
func (r *TxRequest) SetExtraGasLimit(extraGasLimit uint64) *TxRequest {
	r.extraGasLimit = extraGasLimit
	return r
}

// SetGasLimitBufferPercent sets a percentage multiplier for the estimated gas limit.
// The value represents a percentage where 100 = 1x (no change), 200 = 2x, 220 = 2.2x.
// This is only applied when gas is estimated (gasLimit is not explicitly set).
// A value of 0 means no buffer is applied.
func (r *TxRequest) SetGasLimitBufferPercent(percent uint64) *TxRequest {
	r.gasLimitBufferPercent = percent
	return r
}

// SetGasPrice sets the gas price
func (r *TxRequest) SetGasPrice(gasPrice float64) *TxRequest {
	r.gasPrice = gasPrice
	return r
}

// SetExtraGasPrice sets the extra gas price
func (r *TxRequest) SetExtraGasPrice(extraGasPrice float64) *TxRequest {
	r.extraGasPrice = extraGasPrice
	return r
}

// SetTipCapGwei sets the tip cap in gwei
func (r *TxRequest) SetTipCapGwei(tipCapGwei float64) *TxRequest {
	r.tipCapGwei = tipCapGwei
	return r
}

// SetExtraTipCapGwei sets the extra tip cap in gwei
func (r *TxRequest) SetExtraTipCapGwei(extraTipCapGwei float64) *TxRequest {
	r.extraTipCapGwei = extraTipCapGwei
	return r
}

// SetMaxGasPrice sets the maximum gas price limit
func (r *TxRequest) SetMaxGasPrice(maxGasPrice float64) *TxRequest {
	r.maxGasPrice = maxGasPrice
	return r
}

// SetMaxTipCap sets the maximum tip cap limit
func (r *TxRequest) SetMaxTipCap(maxTipCap float64) *TxRequest {
	r.maxTipCap = maxTipCap
	return r
}

// SetData sets the transaction data
func (r *TxRequest) SetData(data []byte) *TxRequest {
	r.data = data
	return r
}

// SetNetwork sets the network
func (r *TxRequest) SetNetwork(network networks.Network) *TxRequest {
	r.network = network
	return r
}

// SetAbis sets the abis
func (r *TxRequest) SetAbis(abis ...abi.ABI) *TxRequest {
	r.abis = abis
	return r
}

// SetBeforeSignAndBroadcastHook sets the hook to be called before signing and broadcasting
func (r *TxRequest) SetBeforeSignAndBroadcastHook(hook Hook) *TxRequest {
	r.beforeSignAndBroadcastHook = hook
	return r
}

// SetAfterSignAndBroadcastHook sets the hook to be called after signing and broadcasting
func (r *TxRequest) SetAfterSignAndBroadcastHook(hook Hook) *TxRequest {
	r.afterSignAndBroadcastHook = hook
	return r
}

// SetGasEstimationFailedHook sets the hook to be called when gas estimation fails
func (r *TxRequest) SetGasEstimationFailedHook(hook GasEstimationFailedHook) *TxRequest {
	r.gasEstimationFailedHook = hook
	return r
}

// SetSimulationFailedHook sets the hook to be called when eth_call simulation fails.
// This hook is called when the transaction would revert, allowing the caller to decide
// whether to retry or handle the error.
func (r *TxRequest) SetSimulationFailedHook(hook SimulationFailedHook) *TxRequest {
	r.simulationFailedHook = hook
	return r
}

// SetTxMinedHook sets the hook to be called when a transaction is mined.
// This hook is called for both successful and reverted transactions.
func (r *TxRequest) SetTxMinedHook(hook TxMinedHook) *TxRequest {
	r.txMinedHook = hook
	return r
}

// SetSkipSimulation skips the eth_call simulation that runs before signing and broadcasting.
// When set to true, the transaction will be signed and broadcast even if it would revert.
// This is useful when you know the simulation will fail but want to force the transaction through.
// Note: you likely also need to set a manual gas limit via SetGasLimit, since gas estimation
// may fail for the same reason the simulation would revert.
func (r *TxRequest) SetSkipSimulation(skip bool) *TxRequest {
	r.skipSimulation = skip
	return r
}

// SetIdempotencyKey sets a unique key to prevent duplicate transaction submissions.
// If the same key is used again, the previous transaction result will be returned
// instead of submitting a new transaction.
// Requires an IdempotencyStore to be configured on the WalletManager.
func (r *TxRequest) SetIdempotencyKey(key string) *TxRequest {
	r.idempotencyKey = key
	return r
}

// Execute executes the transaction request using a background context.
// For production use, prefer ExecuteContext to allow cancellation.
func (r *TxRequest) Execute() (*types.Transaction, *types.Receipt, error) {
	return r.ExecuteContext(context.Background())
}

// ExecuteContext executes the transaction request with context support.
// The context allows the caller to cancel long-running retry loops.
func (r *TxRequest) ExecuteContext(ctx context.Context) (*types.Transaction, *types.Receipt, error) {
	// Validate required fields before starting
	if r.from == (common.Address{}) {
		return nil, nil, ErrFromAddressZero
	}
	if r.network == nil {
		return nil, nil, ErrNetworkNil
	}

	// Handle idempotency if a key is provided and store is configured
	if r.idempotencyKey != "" && r.wm.idempotencyStore != nil {
		return r.executeWithIdempotency(ctx)
	}

	return r.executeInternal(ctx)
}

// executeWithIdempotency handles idempotent execution
func (r *TxRequest) executeWithIdempotency(ctx context.Context) (*types.Transaction, *types.Receipt, error) {
	store := r.wm.idempotencyStore

	// Try to get existing record
	existing, err := store.Get(r.idempotencyKey)
	if err == nil {
		// Record exists - check status
		switch existing.Status {
		case idempotency.StatusConfirmed:
			// Already completed successfully
			return existing.Transaction, existing.Receipt, nil
		case idempotency.StatusFailed:
			// Previously failed - return the error
			return existing.Transaction, existing.Receipt, existing.Error
		case idempotency.StatusPending, idempotency.StatusSubmitted:
			// Still in progress - return duplicate error
			return existing.Transaction, existing.Receipt, idempotency.ErrDuplicateKey
		}
	}

	// Create new record
	record, err := store.Create(r.idempotencyKey)
	if err == idempotency.ErrDuplicateKey {
		// Race condition - another request created the record
		return record.Transaction, record.Receipt, idempotency.ErrDuplicateKey
	}
	if err != nil {
		return nil, nil, err
	}

	// Execute the transaction
	tx, receipt, txErr := r.executeInternal(ctx)

	// Update the record with results
	record.Transaction = tx
	record.Receipt = receipt
	record.Error = txErr

	if tx != nil {
		record.TxHash = tx.Hash()
	}

	if txErr != nil {
		record.Status = idempotency.StatusFailed
	} else if receipt != nil {
		record.Status = idempotency.StatusConfirmed
	} else {
		record.Status = idempotency.StatusSubmitted
	}

	// Best effort update - don't fail the transaction if update fails
	_ = store.Update(record)

	return tx, receipt, txErr
}

// executeInternal builds TxExecutionContext directly and runs the execution loop.
func (r *TxRequest) executeInternal(ctx context.Context) (*types.Transaction, *types.Receipt, error) {
	// Use resolved manager defaults — guaranteed non-zero by applyDefaultsResolution.
	defaults := r.wm.Defaults()

	execCtx, err := NewTxExecutionContext(
		TxParams{
			TxType:         r.txType,
			From:           r.from,
			To:             r.to,
			Value:          r.value,
			Data:           r.data,
			Network:        r.network,
			SkipSimulation: r.skipSimulation,
		},
		RetryConfig{
			MaxAttempts:     r.numRetries,
			SleepDuration:   r.sleepDuration,
			TxCheckInterval: r.txCheckInterval,
			SlowTxTimeout:   defaults.SlowTxTimeout,
		},
		GasBounds{
			ExtraGasLimit:         r.extraGasLimit,
			GasLimitBufferPercent: r.gasLimitBufferPercent,
			ExtraGasPrice:         r.extraGasPrice,
			ExtraTipCap:        r.extraTipCapGwei,
			MaxGasPrice:        r.maxGasPrice,
			MaxTipCap:          r.maxTipCap,
			GasPriceBumpFactor: defaults.GasPriceBumpFactor,
			TipCapBumpFactor:   defaults.TipCapBumpFactor,
		},
		TxHooks{
			BeforeSignAndBroadcast: r.beforeSignAndBroadcastHook,
			AfterSignAndBroadcast:  r.afterSignAndBroadcastHook,
			GasEstimationFailed:    r.gasEstimationFailedHook,
			SimulationFailed:       r.simulationFailedHook,
			TxMined:                r.txMinedHook,
			ABIs:                   r.abis,
		},
		r.gasPrice,
		r.tipCapGwei,
	)
	if err != nil {
		return nil, nil, err
	}

	// Store the initial gas limit in state (may be overridden by hooks)
	execCtx.State.GasLimit = r.gasLimit

	// Create error decoder
	errDecoder := r.wm.createErrorDecoder(execCtx.Hooks.ABIs)

	// Run the execution loop directly
	return r.wm.executeTransactionLoop(ctx, execCtx, errDecoder)
}
