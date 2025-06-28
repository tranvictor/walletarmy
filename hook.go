package walletarmy

import (
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"
)

// Hook is a function that is called before and after a transaction is signed and broadcasted
type Hook func(tx *types.Transaction, err error) error

// ContractRevertHook is a function that is called when a transaction is failed to estimate gas
// revertMsg is the revert message returned by the contract and parsed with the abi passed in the tx request
// This hook will NOT be called if the tx request doesn't have any abis set
// If the hook returns a non-nil gas limit, the gas limit will be used to do the next iteration
// The hook should return an error to stop the loop and return the error
type GasEstimationFailedHook func(tx *types.Transaction, revertMsgError, gasEstimationError error) (gasLimit *big.Int, err error)
