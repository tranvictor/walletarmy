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
	DefaultGasPriceIncreasePercent = 1.2 // 20% increase when tx is slow
	DefaultTipCapIncreasePercent   = 1.1 // 10% increase when tx is slow
	MaxCapMultiplier               = 5.0 // Multiplier for default max gas price/tip cap when not set
)

// TxExecutionResult represents the outcome of a transaction execution step
type TxExecutionResult struct {
	Transaction  *types.Transaction
	Receipt      *types.Receipt
	ShouldRetry  bool
	ShouldReturn bool
	Error        error
}

// ManagerDefaults holds default configuration values that are inherited by TxRequest
type ManagerDefaults struct {
	// Retry configuration
	NumRetries      int
	SleepDuration   time.Duration
	TxCheckInterval time.Duration
	SlowTxTimeout   time.Duration // Time before considering a tx "slow" during monitoring

	// Gas configuration
	ExtraGasLimit   uint64
	ExtraGasPrice   float64
	ExtraTipCapGwei float64
	MaxGasPrice     float64
	MaxTipCap       float64

	// Gas bumping configuration (for slow tx retry)
	GasPriceIncreasePercent float64 // Multiplier for gas price when tx is slow (e.g., 1.2 = 20% increase)
	TipCapIncreasePercent   float64 // Multiplier for tip cap when tx is slow (e.g., 1.1 = 10% increase)

	// Default network (if not specified in request)
	Network networks.Network

	// Default transaction type
	TxType uint8
}
