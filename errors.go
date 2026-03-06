package walletarmy

import (
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Transaction execution errors
var (
	ErrEstimateGasFailed    = fmt.Errorf("estimate gas failed")
	ErrAcquireNonceFailed   = fmt.Errorf("acquire nonce failed")
	ErrGetGasSettingFailed  = fmt.Errorf("get gas setting failed")
	ErrEnsureTxOutOfRetries = fmt.Errorf("ensure tx out of retries")
	ErrGasPriceLimitReached = fmt.Errorf("gas price protection limit reached")
	ErrFromAddressZero      = fmt.Errorf("from address cannot be zero")
	ErrNetworkNil           = fmt.Errorf("network cannot be nil")
	ErrSimulatedTxReverted  = fmt.Errorf("tx will be reverted")
	ErrSimulatedTxFailed    = fmt.Errorf("couldn't simulate tx at pending state")
	ErrCircuitBreakerOpen   = fmt.Errorf("circuit breaker is open: network temporarily unavailable")
	ErrSyncBroadcastTimeout = fmt.Errorf("sync broadcast timed out, falling back to async monitoring")
	ErrTxReverted           = fmt.Errorf("transaction was mined but reverted")
)

// DecodedError wraps a contract error with its decoded ABI information.
// Use errors.As(err, &decoded) to extract it from the error chain returned
// by Execute/ExecuteContext. This allows callers to inspect revert reasons
// without needing hooks or closure variables.
type DecodedError struct {
	// AbiError is the matched ABI error definition, or nil if decoding failed.
	AbiError *abi.Error
	// RevertParams contains the decoded parameters of the revert, or nil.
	RevertParams any
	// RevertData is the raw revert bytes from the node.
	RevertData []byte
	// Err is the underlying error being wrapped.
	Err error
}

func (e *DecodedError) Error() string {
	if e.AbiError != nil {
		return fmt.Sprintf("contract error %s(%v): %s",
			e.AbiError.Name, e.RevertParams, e.Err)
	}
	if len(e.RevertData) > 0 {
		return fmt.Sprintf("contract reverted (0x%s): %s",
			common.Bytes2Hex(e.RevertData), e.Err)
	}
	return e.Err.Error()
}

func (e *DecodedError) Unwrap() error {
	return e.Err
}

// GasEstimationError is a structured error returned when gas estimation fails.
// Callers can extract it with errors.As to access ABI-decoded error details.
//
// AbiError and RevertParams are populated only when ABIs are provided via
// SetAbis() and the RPC error contains decodable Solidity custom error data.
type GasEstimationError struct {
	AbiError     *abi.Error // decoded ABI error, nil if unavailable
	RevertParams any        // decoded parameters, nil if AbiError is nil
	Err          error      // underlying error chain (preserves ErrEstimateGasFailed, ErrEnsureTxOutOfRetries, etc.)
}

func (e *GasEstimationError) Error() string {
	if e.AbiError != nil {
		return fmt.Sprintf("gas estimation failed: %s(%v): %v", e.AbiError.Name, e.RevertParams, e.Err)
	}
	return fmt.Sprintf("gas estimation failed: %v", e.Err)
}

func (e *GasEstimationError) Unwrap() error { return e.Err }

// SimulationRevertError is a structured error returned when eth_call simulation
// detects that a transaction would revert. Callers can extract it with errors.As
// to access the built transaction, raw revert data, and ABI-decoded error details.
//
// AbiError and RevertParams are populated only when ABIs are provided via
// SetAbis() and the revert data contains a decodable Solidity custom error.
type SimulationRevertError struct {
	Tx           *types.Transaction // the built tx that would revert
	RevertData   []byte             // raw revert bytes from the node
	AbiError     *abi.Error         // decoded ABI error, nil if unavailable
	RevertParams any                // decoded parameters, nil if AbiError is nil
	Err          error              // underlying error chain (preserves ErrSimulatedTxReverted, ErrEnsureTxOutOfRetries, etc.)
}

func (e *SimulationRevertError) Error() string {
	if e.AbiError != nil {
		return fmt.Sprintf("simulation reverted: %s(%v): %v", e.AbiError.Name, e.RevertParams, e.Err)
	}
	return fmt.Sprintf("simulation reverted: 0x%x: %v", e.RevertData, e.Err)
}

func (e *SimulationRevertError) Unwrap() error { return e.Err }
