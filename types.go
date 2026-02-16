package walletarmy

import (
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/tranvictor/jarvis/networks"
)

// Constants for transaction execution
const (
	DefaultNumRetries      = 9
	DefaultSleepDuration   = 5 * time.Second
	DefaultTxCheckInterval = 5 * time.Second
	DefaultSlowTxTimeout   = 5 * time.Second // Time before considering a tx "slow"

	// Default gas adjustment values (can be overridden via ManagerDefaults)
	DefaultGasPriceBumpFactor = 1.2 // 20% increase when tx is slow
	DefaultTipCapBumpFactor   = 1.1 // 10% increase when tx is slow
	MaxCapMultiplier          = 5.0 // Multiplier for default max gas price/tip cap when not set

	// Deprecated: Use DefaultGasPriceBumpFactor instead.
	DefaultGasPriceIncreasePercent = DefaultGasPriceBumpFactor
	// Deprecated: Use DefaultTipCapBumpFactor instead.
	DefaultTipCapIncreasePercent = DefaultTipCapBumpFactor
)

// LoopAction represents what the transaction execution loop should do next.
type LoopAction int

const (
	// ActionContinueToMonitor proceeds to the monitoring phase of the loop.
	ActionContinueToMonitor LoopAction = iota
	// ActionRetry goes back to the top of the loop for a new attempt.
	ActionRetry
	// ActionReturn exits the loop entirely, returning the result to the caller.
	ActionReturn
)

// TxExecutionResult represents the outcome of a transaction execution step.
type TxExecutionResult struct {
	Transaction *types.Transaction
	Receipt     *types.Receipt
	Action      LoopAction
	Error       error
}

// ManagerDefaults holds default configuration values that are inherited by TxRequest
type ManagerDefaults struct {
	// Retry configuration
	NumRetries      int
	SleepDuration   time.Duration
	TxCheckInterval time.Duration
	SlowTxTimeout   time.Duration // Time before considering a tx "slow" during monitoring

	// Gas configuration
	ExtraGasLimit         uint64
	GasLimitBufferPercent uint64 // Percentage multiplier for estimated gas limit (e.g., 200 = 2x, 220 = 2.2x, 0 = no buffer)
	ExtraGasPrice         float64
	ExtraTipCap           float64
	MaxGasPrice           float64
	MaxTipCap             float64

	// Gas bumping configuration (for slow tx retry)
	GasPriceBumpFactor float64 // Multiplier for gas price when tx is slow (e.g., 1.2 = 20% increase)
	TipCapBumpFactor   float64 // Multiplier for tip cap when tx is slow (e.g., 1.1 = 10% increase)

	// Default network (if not specified in request)
	Network networks.Network

	// Default transaction type
	TxType uint8
}
