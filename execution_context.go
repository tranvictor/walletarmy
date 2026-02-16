package walletarmy

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	jarviscommon "github.com/tranvictor/jarvis/common"
	"github.com/tranvictor/jarvis/networks"
)

// TxParams holds immutable transaction parameters (set once, never mutated).
type TxParams struct {
	TxType         uint8
	From           common.Address
	To             common.Address
	Value          *big.Int
	Data           []byte
	Network        networks.Network
	// SkipSimulation, when true, skips the eth_call simulation that normally runs
	// after BuildTx succeeds but before signing and broadcasting. This allows
	// forcing a transaction through even when the simulation indicates it would
	// revert. The caller should typically also provide a manual gas limit
	// (via TxRequest.SetGasLimit or the GasLimit field in TxRetryState), since
	// gas estimation may fail for the same reason the simulation would revert.
	SkipSimulation bool
}

// RetryConfig holds immutable retry/timing configuration.
type RetryConfig struct {
	MaxAttempts     int           // Maximum number of retry attempts (renamed from NumRetries)
	SleepDuration   time.Duration // Sleep between retry attempts
	TxCheckInterval time.Duration // Interval for checking tx status
	SlowTxTimeout   time.Duration // Time before considering a tx "slow" during monitoring
}

// GasBounds holds immutable gas configuration and protection limits.
type GasBounds struct {
	ExtraGasLimit        uint64  // Extra gas limit added to estimates
	GasLimitBufferPercent uint64 // Percentage multiplier for estimated gas limit (e.g., 200 = 2x, 220 = 2.2x, 0 = no buffer)
	ExtraGasPrice        float64 // Extra gas price added to suggestions (gwei)
	ExtraTipCap          float64 // Extra tip cap added to suggestions (gwei)
	MaxGasPrice          float64 // Maximum gas price protection limit (gwei)
	MaxTipCap            float64 // Maximum tip cap protection limit (gwei)
	GasPriceBumpFactor   float64 // Multiplier for gas price when tx is slow (e.g., 1.2 = 20% increase)
	TipCapBumpFactor     float64 // Multiplier for tip cap when tx is slow (e.g., 1.1 = 10% increase)
}

// TxHooks holds all callback hooks (set once at construction).
type TxHooks struct {
	BeforeSignAndBroadcast Hook
	AfterSignAndBroadcast  Hook
	GasEstimationFailed    GasEstimationFailedHook
	SimulationFailed       SimulationFailedHook
	TxMined                TxMinedHook
	ABIs                   []abi.ABI
}

// TxRetryState holds all mutable state that changes during the retry loop.
// This is the ONLY part of TxExecutionContext that should be mutated during execution.
type TxRetryState struct {
	AttemptCount int              // Number of retry attempts so far
	GasPrice     float64          // Current gas price for next attempt (gwei)
	TipCap       float64          // Current tip cap for next attempt (gwei)
	GasLimit     uint64           // May be overridden by hooks
	Nonce        *big.Int         // nil = acquire new, non-nil = reuse this nonce
	OldTxs       map[string]*types.Transaction
	ResumeWith   *types.Transaction // If non-nil, skip build/broadcast and go straight to monitoring
}

// TxExecutionContext holds the state and parameters for transaction execution.
// It composes immutable configuration (Params, Retry, Gas, Hooks) with mutable
// retry state (State). Only State fields should be mutated during execution.
type TxExecutionContext struct {
	Params TxParams
	Retry  RetryConfig
	Gas    GasBounds
	Hooks  TxHooks
	State  TxRetryState // The only mutable part
}

// NewTxExecutionContext creates a new transaction execution context from structured sub-types.
// initialGasPrice and initialTipCap are the starting gas prices (gwei) for the first attempt.
func NewTxExecutionContext(
	params TxParams,
	retry RetryConfig,
	gas GasBounds,
	hooks TxHooks,
	initialGasPrice float64,
	initialTipCap float64,
) (*TxExecutionContext, error) {
	// Validate required fields
	if params.From == (common.Address{}) {
		return nil, ErrFromAddressZero
	}
	if params.Network == nil {
		return nil, ErrNetworkNil
	}

	// Sanitize optional fields.
	// When called via WalletManager entry points (EnsureTxWithHooksContext,
	// TxRequest.executeInternal, ResumePendingTransaction), these are already
	// resolved from ManagerDefaults. The fallbacks here are a safety net for
	// direct callers of NewTxExecutionContext.
	if retry.MaxAttempts < 0 {
		retry.MaxAttempts = 0
	}
	if retry.SleepDuration <= 0 {
		retry.SleepDuration = DefaultSleepDuration
	}
	if retry.TxCheckInterval <= 0 {
		retry.TxCheckInterval = DefaultTxCheckInterval
	}
	if retry.SlowTxTimeout <= 0 {
		retry.SlowTxTimeout = DefaultSlowTxTimeout
	}
	if params.Value == nil {
		params.Value = big.NewInt(0)
	}
	if gas.MaxGasPrice == 0 {
		gas.MaxGasPrice = initialGasPrice * MaxCapMultiplier
	}
	if gas.MaxTipCap == 0 {
		gas.MaxTipCap = initialTipCap * MaxCapMultiplier
	}
	if gas.GasPriceBumpFactor == 0 {
		gas.GasPriceBumpFactor = DefaultGasPriceBumpFactor
	}
	if gas.TipCapBumpFactor == 0 {
		gas.TipCapBumpFactor = DefaultTipCapBumpFactor
	}

	return &TxExecutionContext{
		Params: params,
		Retry:  retry,
		Gas:    gas,
		Hooks:  hooks,
		State: TxRetryState{
			GasPrice: initialGasPrice,
			TipCap:   initialTipCap,
			OldTxs:   make(map[string]*types.Transaction),
		},
	}, nil
}

// BumpGasForSlowTx adjusts gas prices when a transaction is slow.
// It reads from Gas (immutable bounds) and writes to State (mutable).
// Returns true if adjustment was applied, false if limits were reached.
func (ctx *TxExecutionContext) BumpGasForSlowTx(tx *types.Transaction) bool {
	if tx == nil {
		return false
	}

	// Gas.GasPriceBumpFactor and Gas.TipCapBumpFactor are guaranteed non-zero
	// by NewTxExecutionContext (which fills package defaults for direct callers)
	// and by applyDefaultsResolution (for WalletManager entry points).

	// Compute new values first, validate both, then commit atomically.
	// This avoids partial State mutation if only one limit is exceeded.
	currentGasPrice := jarviscommon.BigToFloat(tx.GasPrice(), 9)
	newGasPrice := currentGasPrice * ctx.Gas.GasPriceBumpFactor

	currentTipCap := jarviscommon.BigToFloat(tx.GasTipCap(), 9)
	newTipCap := currentTipCap * ctx.Gas.TipCapBumpFactor

	// Check both limits before committing any state changes
	if ctx.Gas.MaxGasPrice > 0 && newGasPrice > ctx.Gas.MaxGasPrice {
		return false
	}
	if ctx.Gas.MaxTipCap > 0 && newTipCap > ctx.Gas.MaxTipCap {
		return false
	}

	// Both within limits — commit all state changes together
	ctx.State.GasPrice = newGasPrice
	ctx.State.TipCap = newTipCap
	ctx.State.Nonce = big.NewInt(int64(tx.Nonce()))

	return true
}

// IncrementRetryAndCheck increments retry count and checks if we've exceeded max attempts.
// Returns a TxExecutionResult with ActionReturn if retries are exhausted, nil otherwise.
func (ctx *TxExecutionContext) IncrementRetryAndCheck(errorMsg string) *TxExecutionResult {
	ctx.State.AttemptCount++
	if ctx.State.AttemptCount > ctx.Retry.MaxAttempts {
		return &TxExecutionResult{
			Transaction: nil,
			Action:      ActionReturn,
			Error:       errors.Join(ErrEnsureTxOutOfRetries, fmt.Errorf("%s after %d retries", errorMsg, ctx.Retry.MaxAttempts)),
		}
	}
	return nil
}

// Deprecated aliases for backward compatibility.

// AdjustGasPricesForSlowTx is a deprecated alias for BumpGasForSlowTx.
func (ctx *TxExecutionContext) AdjustGasPricesForSlowTx(tx *types.Transaction) bool {
	return ctx.BumpGasForSlowTx(tx)
}

// IncrementRetryCountAndCheck is a deprecated alias for IncrementRetryAndCheck.
func (ctx *TxExecutionContext) IncrementRetryCountAndCheck(errorMsg string) *TxExecutionResult {
	return ctx.IncrementRetryAndCheck(errorMsg)
}
