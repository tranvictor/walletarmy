// deps.go defines minimal interfaces for external dependencies.
// This allows for easy mocking in tests and decouples the library from specific implementations.
package walletarmy

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient/gethclient"
)

// EthReader defines the minimal interface for reading blockchain state.
// This abstracts away the concrete jarvis reader implementation.
type EthReader interface {
	// GetMinedNonce returns the nonce of the last mined transaction for the address
	GetMinedNonce(addr string) (uint64, error)

	// GetPendingNonce returns the nonce of the next pending transaction for the address
	GetPendingNonce(addr string) (uint64, error)

	// EstimateExactGas estimates the gas required for a transaction
	EstimateExactGas(from, to string, gasPrice float64, value *big.Int, data []byte) (uint64, error)

	// SuggestedGasSettings returns suggested gas price and tip cap in gwei
	SuggestedGasSettings() (gasPrice float64, tipCapGwei float64, err error)

	// EthCall simulates a transaction execution without sending it
	// The overrides parameter allows for state overrides during the call simulation
	EthCall(from, to string, data []byte, overrides *map[common.Address]gethclient.OverrideAccount) ([]byte, error)

	// TxInfoFromHash returns transaction info for a given hash
	TxInfoFromHash(hash string) (TxInfo, error)
}

// TxInfoStatus represents the status of a transaction in the monitoring/execution flow.
type TxInfoStatus string

const (
	// TxStatusMined indicates the transaction was mined successfully
	TxStatusMined TxInfoStatus = "mined"
	// TxStatusReverted indicates the transaction was mined but execution reverted
	TxStatusReverted TxInfoStatus = "reverted"
	// TxStatusLost indicates the transaction was dropped from the mempool
	TxStatusLost TxInfoStatus = "lost"
	// TxStatusSlow indicates the transaction is taking too long to be mined
	TxStatusSlow TxInfoStatus = "slow"
	// TxStatusCancelled indicates the monitoring was cancelled via context
	TxStatusCancelled TxInfoStatus = "cancelled"
	// TxStatusPending indicates the transaction is still pending
	TxStatusPending TxInfoStatus = "pending"
	// TxStatusDone is the raw status from jarvis monitor indicating success
	TxStatusDone TxInfoStatus = "done"
)

// TxInfo represents transaction information returned by the reader.
// This mirrors the essential fields from jarviscommon.TxInfo.
type TxInfo struct {
	Status  TxInfoStatus
	Receipt *types.Receipt
}

// EthBroadcaster defines the minimal interface for broadcasting transactions.
type EthBroadcaster interface {
	// BroadcastTx broadcasts a signed transaction to the network
	// Returns the tx hash, whether it was broadcast successfully, and any errors
	BroadcastTx(tx *types.Transaction) (hash string, broadcasted bool, err error)

	// BroadcastTxSync broadcasts and waits for the transaction to be mined (for L2s that support it)
	BroadcastTxSync(tx *types.Transaction) (receipt *types.Receipt, err error)
}

// TxMonitorStatus represents the status of a monitored transaction.
type TxMonitorStatus struct {
	Status  string
	Receipt *types.Receipt
}

// TxMonitor defines the minimal interface for monitoring transaction status.
type TxMonitor interface {
	// MakeWaitChannelWithInterval creates a channel that receives status updates
	MakeWaitChannelWithInterval(txHash string, interval time.Duration) <-chan TxMonitorStatus
}

// ReaderFactory creates an EthReader for a given network.
// This allows injecting mock readers for testing.
type ReaderFactory func(chainID uint64, networkName string) (EthReader, error)

// BroadcasterFactory creates an EthBroadcaster for a given network.
// This allows injecting mock broadcasters for testing.
type BroadcasterFactory func(chainID uint64, networkName string) (EthBroadcaster, error)

// TxMonitorFactory creates a TxMonitor for a given network.
// This allows injecting mock monitors for testing.
type TxMonitorFactory func(reader EthReader) TxMonitor
