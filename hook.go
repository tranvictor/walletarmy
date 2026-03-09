package walletarmy

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/core/types"
)

// Hook is called before signing/broadcasting and after signing/broadcasting.
//
// Before sign and broadcast (BeforeSignAndBroadcast):
//   - tx: the built (unsigned) transaction, always non-nil.
//   - err: always nil (reserved for future use).
//   - Return a non-nil error to abort the entire EnsureTx flow immediately.
//
// After sign and broadcast (AfterSignAndBroadcast):
//   - tx: the signed transaction that was successfully broadcast, always non-nil.
//   - err: the broadcast error if any nodes rejected the tx, or nil on full success.
//   - Return a non-nil error to abort the entire EnsureTx flow immediately.
//     The transaction was already broadcast, so returning an error here does NOT
//     prevent it from being mined.
type Hook func(tx *types.Transaction, err error) error

// TxMinedHook is called when a transaction is mined (either successfully or reverted).
//
// Parameters:
//   - tx: the mined transaction, always non-nil.
//   - receipt: the transaction receipt, always non-nil. Check receipt.Status
//     (types.ReceiptStatusSuccessful or types.ReceiptStatusFailed) to determine
//     whether the transaction succeeded or reverted.
//
// Return a non-nil error to propagate it to the EnsureTx caller. The transaction
// and receipt are still returned alongside the error.
type TxMinedHook func(tx *types.Transaction, receipt *types.Receipt) error

// SimulationFailedHook is called when eth_call simulation detects that the transaction
// would revert. This is called AFTER the transaction is built but BEFORE it is signed
// or broadcast.
//
// This hook is distinct from GasEstimationFailedHook: gas estimation failure means the
// node could not estimate gas at all, while simulation failure means the tx was
// estimated successfully but eth_call showed it would revert at the current state.
//
// Parameters:
//   - tx: the built (unsigned) transaction that would revert, always non-nil.
//   - revertData: raw revert bytes from the node, always non-nil (may be empty).
//   - abiError: the decoded ABI error if ABIs were provided and decoding succeeded,
//     nil otherwise.
//   - revertParams: the decoded revert parameters if ABIs were provided and decoding
//     succeeded, nil otherwise.
//   - err: the wrapped simulation error (always non-nil, wraps ErrSimulatedTxReverted).
//
// Return values:
//   - shouldRetry=true: the execution will retry (counts against MaxAttempts).
//   - shouldRetry=false: the execution stops and returns the simulation error.
//   - retErr non-nil: the execution stops immediately with retErr, regardless of
//     shouldRetry.
//
// Note: each retry still counts against MaxAttempts even when shouldRetry is true.
// The nonce acquired for this attempt is preserved for the retry.
type SimulationFailedHook func(tx *types.Transaction, revertData []byte, abiError *abi.Error, revertParams any, err error) (shouldRetry bool, retErr error)

// GasEstimationFailedHook is called when gas estimation fails during transaction building.
// This typically means the transaction would revert or there is a network error.
//
// This hook is always called when set, regardless of whether ABIs were provided.
//
// Parameters:
//   - tx: always nil (the transaction could not be built due to gas estimation failure).
//   - abiError: the decoded ABI error if ABIs were provided via SetAbis() and the error
//     could be decoded, nil otherwise (including when no ABIs are set).
//   - revertParams: the decoded revert parameters, nil when abiError is nil.
//   - revertMsgError: the error from ABI decoding itself, nil when no ABIs are set or
//     decoding was not attempted.
//   - gasEstimationError: the original gas estimation error, always non-nil
//     (wraps ErrEstimateGasFailed).
//
// Return values:
//   - gasLimit non-nil: the returned gas limit overrides the estimate for the next
//     retry attempt, allowing the caller to force a specific gas limit.
//   - gasLimit nil: the next retry will attempt gas estimation again.
//   - err non-nil: the execution stops immediately with this error.
type GasEstimationFailedHook func(tx *types.Transaction, abiError *abi.Error, revertParams any, revertMsgError, gasEstimationError error) (gasLimit *big.Int, err error)
