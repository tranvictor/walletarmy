package walletarmy

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/KyberNetwork/logger"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	jarviscommon "github.com/tranvictor/jarvis/common"
	"github.com/tranvictor/jarvis/networks"
)

func (wm *WalletManager) BuildTx(
	txType uint8,
	from, to common.Address,
	nonce *big.Int,
	value *big.Int,
	gasLimit uint64,
	extraGasLimit uint64,
	gasPrice float64,
	extraGasPrice float64,
	tipCapGwei float64,
	extraTipCapGwei float64,
	data []byte,
	network networks.Network,
) (tx *types.Transaction, err error) {
	// Track whether we acquired the nonce (for cleanup on failure)
	var acquiredNonce bool
	var nonceValue uint64

	// Cleanup function to release nonce if we fail after acquiring it
	defer func() {
		if err != nil && acquiredNonce {
			wm.ReleaseNonce(from, network, nonceValue)
		}
	}()

	if gasLimit == 0 {
		r, readerErr := wm.Reader(network)
		if readerErr != nil {
			return nil, errors.Join(ErrEstimateGasFailed, fmt.Errorf("couldn't get reader: %w", readerErr))
		}
		gasLimit, err = r.EstimateExactGas(
			from.Hex(), to.Hex(),
			gasPrice,
			value,
			data,
		)

		if err != nil {
			return nil, errors.Join(ErrEstimateGasFailed, fmt.Errorf("couldn't estimate gas. The tx is meant to revert or network error. Detail: %w", err))
		}
	}

	if nonce == nil {
		nonce, err = wm.nonce(from, network)
		if err != nil {
			return nil, errors.Join(ErrAcquireNonceFailed, fmt.Errorf("couldn't get nonce of the wallet from any nodes: %w", err))
		}
		acquiredNonce = true
		nonceValue = nonce.Uint64()
	}

	if gasPrice == 0 {
		gasInfo, gasErr := wm.GasSetting(network)
		if gasErr != nil {
			err = errors.Join(ErrGetGasSettingFailed, fmt.Errorf("couldn't get gas price info from any nodes: %w", gasErr))
			return nil, err
		}
		gasPrice = gasInfo.GasPrice
		tipCapGwei = gasInfo.MaxPriorityPrice
	}

	gasPriceToUse := gasPrice + extraGasPrice
	tipCapGweiToUse := tipCapGwei + extraTipCapGwei
	if tipCapGweiToUse > gasPriceToUse {
		gasPriceToUse = tipCapGweiToUse
	}

	// Success — transfer nonce ownership to the caller.
	// From this point the caller is responsible for either broadcasting a tx
	// that uses this nonce (which registers it via registerBroadcastedTx) or
	// calling ReleaseNonce explicitly if the tx is never broadcast.
	acquiredNonce = false

	return jarviscommon.BuildExactTx(
		txType,
		nonce.Uint64(),
		to.Hex(),
		value,
		gasLimit+extraGasLimit,
		gasPriceToUse,
		tipCapGweiToUse,
		data,
		network.GetChainID(),
	), nil
}

func (wm *WalletManager) SignTx(
	wallet common.Address,
	tx *types.Transaction,
	network networks.Network,
) (signedAddr common.Address, signedTx *types.Transaction, err error) {
	acc := wm.Account(wallet)
	if acc == nil {
		acc, err = wm.UnlockAccount(wallet)
		if err != nil {
			return common.Address{}, nil, fmt.Errorf(
				"the wallet to sign txs is not registered in context manager",
			)
		}
	}
	return acc.SignTx(tx, big.NewInt(int64(network.GetChainID())))
}

func (wm *WalletManager) registerBroadcastedTx(tx *types.Transaction, network networks.Network) error {
	wallet, err := jarviscommon.GetSignerAddressFromTx(tx, big.NewInt(int64(network.GetChainID())))
	if err != nil {
		return fmt.Errorf("couldn't derive sender from the tx data in context manager: %s", err)
	}
	// update nonce
	wm.setPendingNonce(wallet, network, tx.Nonce())
	// update txs
	wm.setTx(wallet, network, tx)
	// persist for crash recovery
	wm.persistTxBroadcast(tx, wallet, network.GetChainID())
	return nil
}

func (wm *WalletManager) BroadcastTx(
	tx *types.Transaction,
) (hash string, broadcasted bool, err BroadcastError) {
	network, networkErr := wm.getNetworkByChainID(tx.ChainId().Uint64())
	// TODO: handle chainId 0 for old txs
	if networkErr != nil {
		return "", false, BroadcastError(fmt.Errorf("tx is encoded with unsupported ChainID: %w", networkErr))
	}
	b, broadcasterErr := wm.Broadcaster(network)
	if broadcasterErr != nil {
		return "", false, BroadcastError(fmt.Errorf("couldn't get broadcaster: %w", broadcasterErr))
	}
	hash, broadcasted, allErrors := b.BroadcastTx(tx)
	if broadcasted {
		regErr := wm.registerBroadcastedTx(tx, network)
		if regErr != nil {
			return "", false, BroadcastError(fmt.Errorf("couldn't register broadcasted tx in context manager: %w", regErr))
		}
	}
	return hash, broadcasted, NewBroadcastError(allErrors)
}

func (wm *WalletManager) BroadcastTxSync(
	tx *types.Transaction,
) (receipt *types.Receipt, err error) {
	network, networkErr := wm.getNetworkByChainID(tx.ChainId().Uint64())
	if networkErr != nil {
		return nil, fmt.Errorf("tx is encoded with unsupported ChainID: %w", networkErr)
	}
	b, broadcasterErr := wm.Broadcaster(network)
	if broadcasterErr != nil {
		return nil, NewBroadcastError(fmt.Errorf("couldn't get broadcaster: %w", broadcasterErr))
	}
	receipt, err = b.BroadcastTxSync(tx)
	if err != nil {
		return nil, NewBroadcastError(fmt.Errorf("couldn't broadcast sync tx: %w", err))
	}
	err = wm.registerBroadcastedTx(tx, network)
	if err != nil {
		return nil, NewBroadcastError(fmt.Errorf("couldn't register broadcasted tx in context manager: %w", err))
	}
	return receipt, nil
}

// createErrorDecoder creates an error decoder from ABIs if available
func (wm *WalletManager) createErrorDecoder(abis []abi.ABI) *ErrorDecoder {
	if len(abis) == 0 {
		return nil
	}

	errDecoder, err := NewErrorDecoder(abis...)
	if err != nil {
		logger.WithFields(logger.Fields{
			"error": err,
		}).Error("Failed to create error decoder. Ignore and continue")
		return nil
	}
	return errDecoder
}

// MonitorTx non-blocking way to monitor the tx status, it returns a channel that will be closed when the tx monitoring is done
// the channel is supposed to receive the following values:
//  1. "mined" if the tx is mined
//  2. "slow" if the tx is too slow to be mined (so receiver might want to retry with higher gas price)
//  3. other strings if the tx failed and the reason is returned by the node or other debugging error message that the node can return
//
// Deprecated: Use MonitorTxContext instead for better cancellation support.
func (wm *WalletManager) MonitorTx(tx *types.Transaction, network networks.Network, txCheckInterval time.Duration) <-chan TxInfo {
	return wm.MonitorTxContext(context.Background(), tx, network, txCheckInterval)
}

// MonitorTxContext is a context-aware version of MonitorTx that supports cancellation.
// When the context is cancelled, the monitoring goroutine will exit and close the channel.
//
// Status mapping:
//   - The external TxMonitor returns raw statuses ("done", "reverted", "lost").
//     These are mapped to TxStatusMined, TxStatusReverted, TxStatusLost respectively.
//   - TxStatusSlow is NOT from the monitor. It is generated internally when the
//     monitor does not return a terminal status within SlowTxTimeout.
//   - TxStatusCancelled is generated when the caller's context is cancelled.
//   - Any unrecognized status from the monitor is treated as "still pending" and
//     the slow timeout is allowed to fire.
//
// Slow timer behavior:
//
//	The slow timer is NOT started immediately. It is deferred until the monitor
//	delivers its first non-terminal event (e.g., "pending", unknown status, or
//	channel close). This ensures the monitor has checked the node at least once
//	before judging the transaction as slow. Without this, a SlowTxTimeout shorter
//	than txCheckInterval would always fire before the first check, producing false
//	"slow" signals.
func (wm *WalletManager) MonitorTxContext(ctx context.Context, tx *types.Transaction, network networks.Network, txCheckInterval time.Duration) <-chan TxInfo {
	txMonitor := wm.getTxMonitor(network)
	statusChan := make(chan TxInfo, 1) // Buffered to avoid goroutine leak on context cancellation
	monitorChan := txMonitor.MakeWaitChannelWithInterval(tx.Hash().Hex(), txCheckInterval)

	// Defaults are guaranteed resolved — no fallback needed.
	slowTimeout := wm.Defaults().SlowTxTimeout

	go func() {
		defer close(statusChan)

		// slowCh is nil until the monitor delivers its first non-terminal event.
		// A nil channel blocks forever in select, so the slow case is effectively
		// disabled until we know the monitor has checked the node at least once.
		var slowCh <-chan time.Time
		var timer *time.Timer
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()

		// armSlowTimer starts the slow timer on the first non-terminal monitor event.
		// Subsequent calls are no-ops — the timer is not reset on later "pending" checks
		// because slowTimeout counts from the first check, not the last.
		armSlowTimer := func() {
			if timer == nil {
				timer = time.NewTimer(slowTimeout)
				slowCh = timer.C
			}
		}

		for {
			select {
			case <-ctx.Done():
				statusChan <- TxInfo{
					Status:  TxStatusCancelled,
					Receipt: nil,
				}
				return
			case status, ok := <-monitorChan:
				if !ok {
					// Monitor channel closed without a terminal status.
					// Disable it and arm the slow timer so it can fire.
					monitorChan = nil
					armSlowTimer()
					continue
				}
				switch status.Status {
				case "done":
					statusChan <- TxInfo{
						Status:  TxStatusMined,
						Receipt: status.Receipt,
					}
					return
				case "reverted":
					statusChan <- TxInfo{
						Status:  TxStatusReverted,
						Receipt: status.Receipt,
					}
					return
				case "lost":
					statusChan <- TxInfo{
						Status:  TxStatusLost,
						Receipt: nil,
					}
					return
				default:
					// Unrecognized or non-terminal status from monitor —
					// treat as "still pending". Disable the monitor channel
					// and arm the slow timer so it can fire.
					monitorChan = nil
					armSlowTimer()
					continue
				}
			case <-slowCh:
				statusChan <- TxInfo{
					Status:  TxStatusSlow,
					Receipt: nil,
				}
				return
			}
		}
	}()
	return statusChan
}

func (wm *WalletManager) getTxStatuses(oldTxs map[string]*types.Transaction, network networks.Network) (statuses map[string]TxInfo, err error) {
	result := map[string]TxInfo{}

	r, readerErr := wm.Reader(network)
	if readerErr != nil {
		return nil, fmt.Errorf("couldn't get reader for tx statuses: %w", readerErr)
	}

	for _, tx := range oldTxs {
		txInfo, _ := r.TxInfoFromHash(tx.Hash().Hex())
		result[tx.Hash().Hex()] = txInfo
	}

	return result, nil
}

// EnsureTxWithHooks ensures the tx is broadcasted and mined, it will retry until the tx is mined.
// This is a convenience wrapper that uses context.Background().
// For production use, prefer EnsureTxWithHooksContext to allow cancellation.
func (wm *WalletManager) EnsureTxWithHooks(
	numRetries int,
	sleepDuration time.Duration,
	txCheckInterval time.Duration,
	txType uint8,
	from, to common.Address,
	value *big.Int,
	gasLimit uint64, extraGasLimit uint64,
	gasPrice float64, extraGasPrice float64,
	tipCapGwei float64, extraTipCapGwei float64,
	maxGasPrice float64, maxTipCap float64,
	data []byte,
	network networks.Network,
	beforeSignAndBroadcastHook Hook,
	afterSignAndBroadcastHook Hook,
	abis []abi.ABI,
	gasEstimationFailedHook GasEstimationFailedHook,
) (tx *types.Transaction, receipt *types.Receipt, err error) {
	return wm.EnsureTxWithHooksContext(
		context.Background(),
		numRetries,
		sleepDuration,
		txCheckInterval,
		txType,
		from,
		to,
		value,
		gasLimit, extraGasLimit,
		gasPrice, extraGasPrice,
		tipCapGwei, extraTipCapGwei,
		maxGasPrice, maxTipCap,
		data,
		network,
		beforeSignAndBroadcastHook,
		afterSignAndBroadcastHook,
		abis,
		gasEstimationFailedHook,
		nil, // simulationFailedHook
		nil, // txMinedHook
	)
}

// EnsureTxWithHooksContext ensures the tx is broadcasted and mined, it will retry until the tx is mined.
// The context allows the caller to cancel the operation at any point.
//
// It returns nil and error if:
//  1. the tx couldn't be built
//  2. the tx couldn't be broadcasted and get mined after numRetries retries
//  3. the context is cancelled
//
// It always returns the tx that was mined, either if the tx was successful or reverted.
//
// Possible errors:
//  1. ErrEstimateGasFailed
//  2. ErrAcquireNonceFailed
//  3. ErrGetGasSettingFailed
//  4. ErrEnsureTxOutOfRetries
//  5. ErrGasPriceLimitReached
//  6. context.Canceled or context.DeadlineExceeded
//
// # If the caller wants to know the reason of the error, they can use errors.Is to check if the error is one of the above
//
// After building the tx and before signing and broadcasting, the caller can provide a function hook to receive the tx and building error,
// if the hook returns an error, the process will be stopped and the error will be returned. If the hook returns nil, the process will continue
// even if the tx building failed, in this case, it will retry with the same data up to numRetries times and the hook will be called again.
//
// After signing and broadcasting successfully, the caller can provide a function hook to receive the signed tx and broadcast error,
// if the hook returns an error, the process will be stopped and the error will be returned. If the hook returns nil, the process will continue
// to monitor the tx to see if the tx is mined or not. If the tx is not mined, the process will retry either with a new nonce or with higher gas
// price and tip cap to ensure the tx is mined. Hooks will be called again in the retry process.
func (wm *WalletManager) EnsureTxWithHooksContext(
	ctx context.Context,
	numRetries int,
	sleepDuration time.Duration,
	txCheckInterval time.Duration,
	txType uint8,
	from, to common.Address,
	value *big.Int,
	gasLimit uint64, extraGasLimit uint64,
	gasPrice float64, extraGasPrice float64,
	tipCapGwei float64, extraTipCapGwei float64,
	maxGasPrice float64, maxTipCap float64,
	data []byte,
	network networks.Network,
	beforeSignAndBroadcastHook Hook,
	afterSignAndBroadcastHook Hook,
	abis []abi.ABI,
	gasEstimationFailedHook GasEstimationFailedHook,
	simulationFailedHook SimulationFailedHook,
	txMinedHook TxMinedHook,
) (tx *types.Transaction, receipt *types.Receipt, err error) {
	// Use resolved manager defaults — guaranteed non-zero by applyDefaultsResolution.
	defaults := wm.Defaults()

	execCtx, err := NewTxExecutionContext(
		TxParams{
			TxType:  txType,
			From:    from,
			To:      to,
			Value:   value,
			Data:    data,
			Network: network,
		},
		RetryConfig{
			MaxAttempts:     numRetries,
			SleepDuration:   sleepDuration,
			TxCheckInterval: txCheckInterval,
			SlowTxTimeout:   defaults.SlowTxTimeout,
		},
		GasBounds{
			ExtraGasLimit:      extraGasLimit,
			ExtraGasPrice:      extraGasPrice,
			ExtraTipCap:        extraTipCapGwei,
			MaxGasPrice:        maxGasPrice,
			MaxTipCap:          maxTipCap,
			GasPriceBumpFactor: defaults.GasPriceBumpFactor,
			TipCapBumpFactor:   defaults.TipCapBumpFactor,
		},
		TxHooks{
			BeforeSignAndBroadcast: beforeSignAndBroadcastHook,
			AfterSignAndBroadcast:  afterSignAndBroadcastHook,
			GasEstimationFailed:    gasEstimationFailedHook,
			SimulationFailed:       simulationFailedHook,
			TxMined:                txMinedHook,
			ABIs:                   abis,
		},
		gasPrice,
		tipCapGwei,
	)
	if err != nil {
		return nil, nil, err
	}

	// Store the initial gas limit in state (may be overridden by hooks)
	execCtx.State.GasLimit = gasLimit

	// Create error decoder
	errDecoder := wm.createErrorDecoder(execCtx.Hooks.ABIs)

	// Main execution loop - shared by both new transactions and resumed transactions
	return wm.executeTransactionLoop(ctx, execCtx, errDecoder)
}

// executeTransactionLoop is the main execution loop for transaction processing.
// It handles both new transactions (via executeTransactionAttempt) and resumed
// transactions (via State.ResumeWith). This ensures consistent retry and gas bumping logic.
func (wm *WalletManager) executeTransactionLoop(
	ctx context.Context,
	execCtx *TxExecutionContext,
	errDecoder *ErrorDecoder,
) (tx *types.Transaction, receipt *types.Receipt, err error) {
	// Safety-net nonce release.
	//
	// Nonce ownership flows: BuildTx acquires → caller owns → broadcast registers it.
	// If we exit the loop with an error before any tx was broadcast (OldTxs empty),
	// the nonce in State.Nonce is still reserved and must be released.
	//
	// Individual handlers (handleEthCallRevertFailure, signAndBroadcastTransaction, etc.)
	// release the nonce explicitly on their own exit paths. This defer is a last-resort
	// guard for any path that might set State.Nonce but forget to release it.
	defer func() {
		if err != nil && len(execCtx.State.OldTxs) == 0 && execCtx.State.Nonce != nil {
			wm.ReleaseNonce(execCtx.Params.From, execCtx.Params.Network, execCtx.State.Nonce.Uint64())
		}
	}()

	for {
		// Check for context cancellation before each iteration
		select {
		case <-ctx.Done():
			err = ctx.Err()
			return nil, nil, err
		default:
		}

		// Only sleep after actual retry attempts, not slow monitoring
		// Also skip sleep on first iteration when resuming with ResumeWith
		if execCtx.State.AttemptCount > 0 && execCtx.State.ResumeWith == nil {
			// Use a timer so we can also check for context cancellation during sleep
			sleepTimer := time.NewTimer(execCtx.Retry.SleepDuration)
			select {
			case <-ctx.Done():
				sleepTimer.Stop()
				err = ctx.Err()
				return nil, nil, err
			case <-sleepTimer.C:
			}
		}

		var result *TxExecutionResult

		// If resuming a pending transaction, skip executeTransactionAttempt on first iteration
		// and go straight to monitoring the existing transaction
		if execCtx.State.ResumeWith != nil {
			result = &TxExecutionResult{
				Transaction: execCtx.State.ResumeWith,
				Action:      ActionContinueToMonitor,
			}
			execCtx.State.ResumeWith = nil // Clear so subsequent iterations go through normal flow
		} else {
			// Execute transaction attempt (build/sign/broadcast)
			result = wm.executeTransactionAttempt(ctx, execCtx, errDecoder)
		}

		switch result.Action {
		case ActionReturn:
			err = result.Error
			return result.Transaction, result.Receipt, err
		case ActionRetry:
			continue
		case ActionContinueToMonitor:
			// Fall through to monitoring below
		}

		// Monitor and handle the transaction (only if we have a transaction to monitor)
		// in this case, result.Receipt can be filled already because of this rpc https://www.quicknode.com/docs/arbitrum/eth_sendRawTransactionSync
		if result.Transaction != nil && result.Receipt == nil {
			statusChan := wm.MonitorTxContext(ctx, result.Transaction, execCtx.Params.Network, execCtx.Retry.TxCheckInterval)

			// Wait for status from the context-aware monitor
			status := <-statusChan
			if status.Status == TxStatusCancelled {
				err = ctx.Err()
				return nil, nil, err
			}
			result = wm.handleTransactionStatus(status, result.Transaction, execCtx)
			switch result.Action {
			case ActionReturn:
				err = result.Error
				return result.Transaction, result.Receipt, err
			case ActionRetry:
				continue
			case ActionContinueToMonitor:
				// Should not happen after handleTransactionStatus, but handle gracefully
				continue
			}
		}
	}
}

// executeTransactionAttempt handles building and broadcasting a single transaction attempt
func (wm *WalletManager) executeTransactionAttempt(ctx context.Context, execCtx *TxExecutionContext, errDecoder *ErrorDecoder) *TxExecutionResult {
	// Build transaction
	builtTx, err := wm.BuildTx(
		execCtx.Params.TxType,
		execCtx.Params.From,
		execCtx.Params.To,
		execCtx.State.Nonce,
		execCtx.Params.Value,
		execCtx.State.GasLimit,
		execCtx.Gas.ExtraGasLimit,
		execCtx.State.GasPrice,
		execCtx.Gas.ExtraGasPrice,
		execCtx.State.TipCap,
		execCtx.Gas.ExtraTipCap,
		execCtx.Params.Data,
		execCtx.Params.Network,
	)

	// Handle gas estimation failure
	if errors.Is(err, ErrEstimateGasFailed) {
		return wm.handleGasEstimationFailure(execCtx, errDecoder, err)
	}

	// Any other BuildTx error (nonce acquisition, gas setting, etc.) — fail fast
	if err != nil {
		return &TxExecutionResult{
			Action: ActionReturn,
			Error:  err,
		}
	}

	// Simulate the tx at pending state to see if it will be reverted.
	// When SkipSimulation is true (set via TxRequest.SetSkipSimulation), this
	// entire block is bypassed and we proceed directly to sign-and-broadcast.
	//
	// Nonce ownership: BuildTx succeeded, so the nonce embedded in builtTx is now
	// our responsibility. Every exit path below must either:
	//   (a) pass builtTx to signAndBroadcastTransaction (which handles release on failure), or
	//   (b) call ReleaseNonce explicitly, or
	//   (c) preserve it in State.Nonce for a retry (the loop defer acts as safety net).
	if !execCtx.Params.SkipSimulation {
		r, readerErr := wm.Reader(execCtx.Params.Network)
		if readerErr != nil {
			// Release the nonce since the tx was never broadcast
			wm.ReleaseNonce(execCtx.Params.From, execCtx.Params.Network, builtTx.Nonce())
			return &TxExecutionResult{
				Action: ActionReturn,
				Error:  errors.Join(ErrSimulatedTxFailed, fmt.Errorf("couldn't get reader for simulation: %w", readerErr)),
			}
		}
		_, err = r.EthCall(execCtx.Params.From.Hex(), execCtx.Params.To.Hex(), execCtx.Params.Data, nil) // nil overrides = use current state
		if err != nil {
			revertData, isRevert := ethclient.RevertErrorData(err)
			if isRevert {
				return wm.handleEthCallRevertFailure(execCtx, errDecoder, builtTx, revertData, err)
			} else {
				// Release the nonce since the tx was never broadcast
				wm.ReleaseNonce(execCtx.Params.From, execCtx.Params.Network, builtTx.Nonce())
				err = errors.Join(ErrSimulatedTxFailed, fmt.Errorf("couldn't simulate tx at pending state. Detail: %w", err))
				logger.WithFields(logger.Fields{
					"tx_hash":         builtTx.Hash().Hex(),
					"nonce":           builtTx.Nonce(),
					"gas_price":       builtTx.GasPrice().String(),
					"tip_cap":         builtTx.GasTipCap().String(),
					"max_fee_per_gas": builtTx.GasFeeCap().String(),
					"used_sync_tx":    execCtx.Params.Network.IsSyncTxSupported(),
					"error":           err,
				}).Debug("Tx simulation failed but not a revert error")
				return &TxExecutionResult{
					Action: ActionReturn,
					Error:  err,
				}
			}
		}
	}

	// Execute hooks and broadcast
	result := wm.signAndBroadcastTransaction(ctx, builtTx, execCtx)

	// If no transaction is set in result, use the built transaction
	if result.Transaction == nil && result.Action != ActionReturn {
		result.Transaction = builtTx
	}

	return result
}

func (wm *WalletManager) handleEthCallRevertFailure(execCtx *TxExecutionContext, errDecoder *ErrorDecoder, builtTx *types.Transaction, revertData []byte, err error) *TxExecutionResult {
	var abiError *abi.Error
	var revertParams any

	if errDecoder != nil {
		abiError, revertParams, _ = errDecoder.Decode(err)
	}

	if abiError != nil {
		err = errors.Join(ErrSimulatedTxReverted, fmt.Errorf("revert error: %s. revert params: %+v. Detail: %w", abiError.Name, revertParams, err))
	} else {
		err = errors.Join(ErrSimulatedTxReverted, fmt.Errorf("revert data: %s. Detail: %w", common.Bytes2Hex(revertData), err))
	}

	logger.WithFields(logger.Fields{
		"tx_hash":         builtTx.Hash().Hex(),
		"nonce":           builtTx.Nonce(),
		"gas_price":       builtTx.GasPrice().String(),
		"tip_cap":         builtTx.GasTipCap().String(),
		"max_fee_per_gas": builtTx.GasFeeCap().String(),
		"used_sync_tx":    execCtx.Params.Network.IsSyncTxSupported(),
		"error":           err,
		"revert_data":     revertData,
	}).Debug("Tx simulation showed a revert error")

	// Call simulation failed hook if set
	if execCtx.Hooks.SimulationFailed != nil {
		shouldRetry, hookErr := execCtx.Hooks.SimulationFailed(builtTx, revertData, abiError, revertParams, err)
		if hookErr != nil {
			// Release nonce since we're giving up and tx was never broadcast
			wm.ReleaseNonce(execCtx.Params.From, execCtx.Params.Network, builtTx.Nonce())
			return &TxExecutionResult{
				Action: ActionReturn,
				Error:  fmt.Errorf("simulation failed hook error: %w", hookErr),
			}
		}
		if !shouldRetry {
			// Release nonce since we're giving up and tx was never broadcast
			wm.ReleaseNonce(execCtx.Params.From, execCtx.Params.Network, builtTx.Nonce())
			return &TxExecutionResult{
				Action: ActionReturn,
				Error:  err,
			}
		}
	}

	// Increment retry count — without this, simulation reverts would retry forever
	if result := execCtx.IncrementRetryAndCheck("simulation reverted"); result != nil {
		wm.ReleaseNonce(execCtx.Params.From, execCtx.Params.Network, builtTx.Nonce())
		return result
	}

	// Preserve the nonce and gas limit for the retry attempt.
	// The nonce is NOT released here — ownership transfers to State.Nonce so the
	// next iteration reuses the same nonce. If all retries are eventually exhausted,
	// the safety-net defer in executeTransactionLoop releases it.
	execCtx.State.Nonce = big.NewInt(int64(builtTx.Nonce()))
	execCtx.State.GasLimit = builtTx.Gas()
	// we could persist the error here but then later we need to set it to nil, setting this to the error if the user is supposed to handle such error
	return &TxExecutionResult{
		Action: ActionRetry,
	}
}

// handleGasEstimationFailure processes gas estimation failures and pending transaction checks
func (wm *WalletManager) handleGasEstimationFailure(execCtx *TxExecutionContext, errDecoder *ErrorDecoder, err error) *TxExecutionResult {
	// Only check old transactions if we have any
	if len(execCtx.State.OldTxs) > 0 {
		// Check if previous transactions might have been successful
		statuses, statusErr := wm.getTxStatuses(execCtx.State.OldTxs, execCtx.Params.Network)
		if statusErr != nil {
			logger.WithFields(logger.Fields{
				"error": statusErr,
			}).Debug("Getting tx statuses after gas estimation failure. Ignore and continue the retry loop")
			// Don't return immediately, proceed to hook handling
		} else {
			// Check for completed transactions
			for txhash, status := range statuses {
				if status.Status == TxStatusDone || status.Status == TxStatusReverted {
					if tx, exists := execCtx.State.OldTxs[txhash]; exists && tx != nil {
						return wm.handleMinedTx(tx, status, execCtx)
					}
				}
			}

			// Find highest gas price pending transaction to monitor
			highestGasPrice := big.NewInt(0)
			var bestTx *types.Transaction
			for txhash, status := range statuses {
				if status.Status == TxStatusPending {
					if tx, exists := execCtx.State.OldTxs[txhash]; exists && tx != nil {
						if tx.GasPrice().Cmp(highestGasPrice) > 0 {
							highestGasPrice = tx.GasPrice()
							bestTx = tx
						}
					}
				}
			}

			if bestTx != nil {
				// We have a pending transaction, monitor it instead of retrying
				logger.WithFields(logger.Fields{
					"tx_hash": bestTx.Hash().Hex(),
					"nonce":   bestTx.Nonce(),
				}).Info("Found pending transaction during gas estimation failure, monitoring it instead")
				// Return the transaction but with ActionContinueToMonitor so it goes to monitoring
				return &TxExecutionResult{
					Transaction: bestTx,
					Action:      ActionContinueToMonitor,
				}
			}
		}
	}

	// Handle gas estimation failed hook
	if execCtx.Hooks.GasEstimationFailed != nil {
		var abiError *abi.Error
		var revertParams any
		var revertMsgErr error
		if errDecoder != nil {
			abiError, revertParams, revertMsgErr = errDecoder.Decode(err)
		}
		hookGasLimit, hookErr := execCtx.Hooks.GasEstimationFailed(nil, abiError, revertParams, revertMsgErr, err)
		if hookErr != nil {
			return &TxExecutionResult{
				Action: ActionReturn,
				Error:  hookErr,
			}
		}
		if hookGasLimit != nil {
			execCtx.State.GasLimit = hookGasLimit.Uint64()
		}
	}

	// Increment retry count for gas estimation failure
	execCtx.State.AttemptCount++
	if execCtx.State.AttemptCount > execCtx.Retry.MaxAttempts {
		return &TxExecutionResult{
			Action: ActionReturn,
			Error:  errors.Join(ErrEnsureTxOutOfRetries, fmt.Errorf("gas estimation failed after %d retries", execCtx.Retry.MaxAttempts)),
		}
	}

	return &TxExecutionResult{
		Action: ActionRetry,
	}
}

// SyncBroadcastTimeout is the maximum time to wait for BroadcastTxSync before falling back to async monitoring.
// This is a variable (not const) to allow tests to override it for faster testing.
var SyncBroadcastTimeout = 5 * time.Second

// signAndBroadcastTransaction handles the signing and broadcasting process
func (wm *WalletManager) signAndBroadcastTransaction(ctx context.Context, tx *types.Transaction, execCtx *TxExecutionContext) *TxExecutionResult {
	// Release nonce when we fail before any broadcast attempt.
	// Called only on pre-broadcast exit paths (hook error, sign error, address
	// mismatch). The OldTxs guard is defensive: it ensures we never release a
	// nonce for a tx that was already tracked (shouldn't happen in practice
	// since releaseNonce is only called before broadcast).
	releaseNonce := func() {
		if _, exists := execCtx.State.OldTxs[tx.Hash().Hex()]; !exists {
			wm.ReleaseNonce(execCtx.Params.From, execCtx.Params.Network, tx.Nonce())
		}
	}

	// Execute before hook
	if execCtx.Hooks.BeforeSignAndBroadcast != nil {
		if hookError := execCtx.Hooks.BeforeSignAndBroadcast(tx, nil); hookError != nil {
			releaseNonce()
			return &TxExecutionResult{
				Action: ActionReturn,
				Error:  fmt.Errorf("after tx building and before signing and broadcasting hook error: %w", hookError),
			}
		}
	}

	// Sign transaction
	signedAddr, signedTx, err := wm.SignTx(execCtx.Params.From, tx, execCtx.Params.Network)
	if err != nil {
		releaseNonce()
		return &TxExecutionResult{
			Action: ActionReturn,
			Error:  fmt.Errorf("failed to sign transaction: %w", err),
		}
	}

	// Verify signed address matches expected address
	if signedAddr.Cmp(execCtx.Params.From) != 0 {
		releaseNonce()
		return &TxExecutionResult{
			Action: ActionReturn,
			Error: fmt.Errorf(
				"signed from wrong address. You could use wrong hw or passphrase. Expected wallet: %s, signed wallet: %s",
				execCtx.Params.From.Hex(),
				signedAddr.Hex(),
			),
		}
	}

	var receipt *types.Receipt
	var broadcastErr BroadcastError
	var successful bool
	var syncBroadcastTimedOut bool

	// Broadcast transaction
	if execCtx.Params.Network.IsSyncTxSupported() {
		// Use timeout wrapper for sync broadcast to handle slow L2s.
		// If the broadcast takes too long, we fall back to async monitoring with gas bump support.
		//
		// Important: the goroutine calls the broadcaster directly (not BroadcastTxSync)
		// to avoid registerBroadcastedTx running in the background after a timeout.
		// If the goroutine completed after we had already bumped gas and broadcast a
		// replacement tx, the stale registerBroadcastedTx would overwrite the newer
		// pending nonce and tx tracking. Instead, we register the tx ourselves on the
		// main goroutine in every path (success, timeout, error).
		type syncResult struct {
			receipt *types.Receipt
			err     error
		}
		resultCh := make(chan syncResult, 1)

		b, broadcasterErr := wm.Broadcaster(execCtx.Params.Network)
		if broadcasterErr != nil {
			releaseNonce()
			return &TxExecutionResult{
				Action: ActionReturn,
				Error:  fmt.Errorf("couldn't get broadcaster: %w", broadcasterErr),
			}
		}

		go func() {
			r, e := b.BroadcastTxSync(signedTx)
			resultCh <- syncResult{r, e}
		}()

		select {
		case result := <-resultCh:
			receipt = result.receipt
			if result.err != nil {
				broadcastErr = NewBroadcastError(result.err)
			}
			if receipt != nil {
				successful = true
			}
			// Register on the main goroutine now that we have the result
			if successful {
				_ = wm.registerBroadcastedTx(signedTx, execCtx.Params.Network)
			}
		case <-time.After(SyncBroadcastTimeout):
			// Timeout: treat as slow tx, fall back to async monitoring.
			// The tx was already sent to the network, so we mark it as successful
			// but with no receipt, which triggers the monitor flow.
			// Register eagerly since the tx is in-flight on the network.
			syncBroadcastTimedOut = true
			successful = true
			_ = wm.registerBroadcastedTx(signedTx, execCtx.Params.Network)
			logger.WithFields(logger.Fields{
				"tx_hash":         signedTx.Hash().Hex(),
				"nonce":           signedTx.Nonce(),
				"timeout_seconds": SyncBroadcastTimeout.Seconds(),
			}).Warn("Sync broadcast timed out, falling back to async monitoring with gas bump support")
		case <-ctx.Done():
			// Context cancelled - return immediately
			return &TxExecutionResult{
				Transaction: signedTx,
				Action:      ActionReturn,
				Error:       ctx.Err(),
			}
		}
	} else {
		_, successful, broadcastErr = wm.BroadcastTx(signedTx)
	}

	// Always record signed txs in OldTxs, even when broadcast reports failure.
	// Nodes may silently accept the tx despite returning an error (network
	// timeouts, partial propagation, etc.), so we must track every signed tx
	// to detect if it gets mined later. This is checked by handleNonceIsLowError
	// and handleGasEstimationFailure to avoid orphaning txs.
	if signedTx != nil {
		execCtx.State.OldTxs[signedTx.Hash().Hex()] = signedTx
	}

	if !successful {
		// Only log if signedTx is not nil to avoid panic
		if signedTx != nil {
			logger.WithFields(logger.Fields{
				"tx_hash":         signedTx.Hash().Hex(),
				"nonce":           signedTx.Nonce(),
				"gas_price":       signedTx.GasPrice().String(),
				"tip_cap":         signedTx.GasTipCap().String(),
				"max_fee_per_gas": signedTx.GasFeeCap().String(),
				"used_sync_tx":    execCtx.Params.Network.IsSyncTxSupported(),
				"receipt":         receipt,
				"error":           broadcastErr,
			}).Debug("Unsuccessful signing and broadcasting transaction")
		} else {
			logger.WithFields(logger.Fields{
				"nonce":        tx.Nonce(),
				"gas_price":    tx.GasPrice().String(),
				"used_sync_tx": execCtx.Params.Network.IsSyncTxSupported(),
				"receipt":      receipt,
				"error":        broadcastErr,
			}).Debug("Unsuccessful signing and broadcasting transaction (no signed tx)")
		}

		return wm.handleBroadcastError(broadcastErr, tx, execCtx)
	}

	// Log successful broadcast
	logger.WithFields(logger.Fields{
		"tx_hash":                signedTx.Hash().Hex(),
		"nonce":                  signedTx.Nonce(),
		"gas_price":              signedTx.GasPrice().String(),
		"tip_cap":                signedTx.GasTipCap().String(),
		"max_fee_per_gas":        signedTx.GasFeeCap().String(),
		"used_sync_tx":           execCtx.Params.Network.IsSyncTxSupported(),
		"sync_broadcast_timeout": syncBroadcastTimedOut,
		"receipt":                receipt,
	}).Info("Signed and broadcasted transaction")

	// Execute after hook - convert BroadcastError to error for hook
	var hookErr error
	if broadcastErr != nil {
		hookErr = broadcastErr
	}

	if execCtx.Hooks.AfterSignAndBroadcast != nil {
		if hookError := execCtx.Hooks.AfterSignAndBroadcast(signedTx, hookErr); hookError != nil {
			return &TxExecutionResult{
				Transaction: signedTx,
				Receipt:     receipt,
				Action:      ActionReturn,
				Error:       fmt.Errorf("after signing and broadcasting hook error: %w", hookError),
			}
		}
	}

	// in case receipt is not nil, it means the tx is broadcasted and mined using eth_sendRawTransactionSync
	if receipt != nil {
		return wm.handleMinedTx(signedTx, TxInfo{Receipt: receipt}, execCtx)
	}

	return &TxExecutionResult{
		Transaction: signedTx,
		Receipt:     receipt,
		Action:      ActionContinueToMonitor,
	}
}

// handleBroadcastError processes various broadcast errors and determines retry strategy
func (wm *WalletManager) handleBroadcastError(broadcastErr BroadcastError, tx *types.Transaction, execCtx *TxExecutionContext) *TxExecutionResult {
	// Special case: replacement tx underpriced — original tx is still pending,
	// but the replacement gas price wasn't high enough. Bump gas and retry.
	if broadcastErr == ErrReplacementUnderpriced {
		return wm.handleReplacementUnderpricedError(tx, execCtx)
	}

	// Special case: nonce is low requires checking if transaction is already mined
	if broadcastErr == ErrNonceIsLow {
		return wm.handleNonceIsLowError(tx, execCtx)
	}

	// Special case: tx is known doesn't count as retry (we're just waiting for it to be mined)
	// in this case, we need to speed up the tx by increasing the gas price and tip cap
	// however, it should be handled by the slow status gotten from the monitor tx
	// so we just need to retry with the same nonce
	if broadcastErr == ErrTxIsKnown {
		execCtx.State.Nonce = big.NewInt(int64(tx.Nonce()))
		return &TxExecutionResult{
			Action: ActionRetry,
		}
	}

	// All other errors count as retry attempts
	var errorMsg string
	switch broadcastErr {
	case ErrInsufficientFund:
		errorMsg = "insufficient fund"
	case ErrGasLimitIsTooLow:
		errorMsg = "gas limit too low"
	default:
		errorMsg = fmt.Sprintf("broadcast error: %v", broadcastErr)
	}

	if result := execCtx.IncrementRetryAndCheck(errorMsg); result != nil {
		return result
	}

	// Keep the same nonce for retry
	execCtx.State.Nonce = big.NewInt(int64(tx.Nonce()))
	return &TxExecutionResult{
		Action: ActionRetry,
	}
}

// handleReplacementUnderpricedError handles the case where a replacement transaction
// was rejected because its gas price wasn't high enough to replace the pending tx.
// Unlike "nonce too low" (where the nonce is already mined), here the original tx is
// still pending in the mempool.
//
// Two cases:
//  1. The pending tx at this nonce is OURS (found in OldTxs) — keep the same nonce
//     and bump gas to replace our own slow tx.
//  2. The pending tx is NOT ours (e.g., another WalletManager instance using the same
//     wallet) — acquire a fresh nonce instead of engaging in a gas price bidding war
//     with the other instance.
func (wm *WalletManager) handleReplacementUnderpricedError(tx *types.Transaction, execCtx *TxExecutionContext) *TxExecutionResult {
	// Determine whether the pending tx in the mempool at this nonce is ours.
	//
	// signAndBroadcastTransaction always adds the signed tx to OldTxs before
	// calling handleBroadcastError, even when broadcast fails. So OldTxs will
	// always contain at least ONE tx at this nonce — the one we just tried.
	//
	// If there are 2+ txs at this nonce, at least one is from a PREVIOUS
	// iteration (a successfully broadcast tx that is now pending in the
	// mempool). In that case, we should bump gas to replace our own tx.
	//
	// If there is exactly 1 tx at this nonce, it's the one that was just
	// rejected — the pending tx in the mempool belongs to another instance
	// (or external process). We should acquire a fresh nonce instead.
	txsAtNonce := 0
	for _, oldTx := range execCtx.State.OldTxs {
		if oldTx.Nonce() == tx.Nonce() {
			txsAtNonce++
		}
	}
	ownTxPending := txsAtNonce > 1

	if !ownTxPending {
		// The pending tx belongs to another instance/process. Do not try to
		// replace it — acquire a fresh nonce on the next attempt instead.
		logger.WithFields(logger.Fields{
			"tx_hash":   tx.Hash().Hex(),
			"nonce":     tx.Nonce(),
			"gas_price": tx.GasPrice().String(),
			"tip_cap":   tx.GasTipCap().String(),
		}).Info("Replacement underpriced for foreign pending tx, acquiring new nonce")

		execCtx.State.Nonce = nil
		return &TxExecutionResult{
			Action: ActionRetry,
		}
	}

	logger.WithFields(logger.Fields{
		"tx_hash":   tx.Hash().Hex(),
		"nonce":     tx.Nonce(),
		"gas_price": tx.GasPrice().String(),
		"tip_cap":   tx.GasTipCap().String(),
	}).Info("Replacement transaction underpriced, bumping gas and retrying with same nonce")

	// Keep the same nonce — the original tx is still valid and pending
	execCtx.State.Nonce = big.NewInt(int64(tx.Nonce()))

	if execCtx.BumpGasForSlowTx(tx) {
		return &TxExecutionResult{
			Action: ActionRetry,
		}
	}

	// Gas limits reached — cannot bump further
	return &TxExecutionResult{
		Transaction: tx,
		Action:      ActionReturn,
		Error:       errors.Join(ErrGasPriceLimitReached, fmt.Errorf("replacement underpriced and gas limits reached, maxGasPrice: %f, maxTipCap: %f", execCtx.Gas.MaxGasPrice, execCtx.Gas.MaxTipCap)),
	}
}

// handleNonceIsLowError specifically handles the nonce is low error case
func (wm *WalletManager) handleNonceIsLowError(tx *types.Transaction, execCtx *TxExecutionContext) *TxExecutionResult {

	statuses, err := wm.getTxStatuses(execCtx.State.OldTxs, execCtx.Params.Network)

	if err != nil {
		logger.WithFields(logger.Fields{
			"error": err,
		}).Debug("Getting tx statuses in case where tx wasn't broadcasted because nonce is too low. Ignore and continue the retry loop")

		if result := execCtx.IncrementRetryAndCheck("nonce is low and status check failed"); result != nil {
			return result
		}

		return &TxExecutionResult{
			Action: ActionRetry,
		}
	}

	// Check if any old transaction is completed
	for txhash, status := range statuses {
		if status.Status == TxStatusDone || status.Status == TxStatusReverted {
			if tx, exists := execCtx.State.OldTxs[txhash]; exists && tx != nil {
				return wm.handleMinedTx(tx, status, execCtx)
			}
		}
	}

	// No completed transactions found, retry with new nonce
	if result := execCtx.IncrementRetryAndCheck("nonce is low and no pending transactions"); result != nil {
		return result
	}

	execCtx.State.Nonce = nil

	return &TxExecutionResult{
		Action: ActionRetry,
	}
}

// handleMinedTx processes a mined/completed transaction by persisting its status,
// calling TxMinedHook if set, and returns the appropriate TxExecutionResult.
// If txInfo.Status is empty, derives from txInfo.Receipt.Status.
func (wm *WalletManager) handleMinedTx(tx *types.Transaction, txInfo TxInfo, execCtx *TxExecutionContext) *TxExecutionResult {
	// Persist transaction status for crash recovery
	switch txInfo.Status {
	case "":
		// Derive from receipt when status string is not available
		if txInfo.Receipt.Status == types.ReceiptStatusSuccessful {
			wm.persistTxMined(tx.Hash(), PendingTxStatusMined, txInfo.Receipt)
		} else {
			wm.persistTxMined(tx.Hash(), PendingTxStatusReverted, txInfo.Receipt)
		}
	case TxStatusDone, TxStatusMined:
		wm.persistTxMined(tx.Hash(), PendingTxStatusMined, txInfo.Receipt)
	case TxStatusReverted:
		wm.persistTxMined(tx.Hash(), PendingTxStatusReverted, txInfo.Receipt)
	}

	if execCtx.Hooks.TxMined != nil {
		if hookErr := execCtx.Hooks.TxMined(tx, txInfo.Receipt); hookErr != nil {
			return &TxExecutionResult{
				Transaction: tx,
				Receipt:     txInfo.Receipt,
				Action:      ActionReturn,
				Error:       fmt.Errorf("tx mined hook error: %w", hookErr),
			}
		}
	}
	return &TxExecutionResult{
		Transaction: tx,
		Receipt:     txInfo.Receipt,
		Action:      ActionReturn,
	}
}

// handleTransactionStatus processes different transaction statuses
func (wm *WalletManager) handleTransactionStatus(status TxInfo, signedTx *types.Transaction, execCtx *TxExecutionContext) *TxExecutionResult {
	switch status.Status {
	case TxStatusMined, TxStatusReverted:
		return wm.handleMinedTx(signedTx, status, execCtx)

	case TxStatusLost:
		// Transaction was dropped from the mempool. Re-broadcast with the same nonce
		// and higher gas, similar to the "slow" handler. Acquiring a new nonce would
		// create a gap that can never be filled.
		if execCtx.BumpGasForSlowTx(signedTx) {
			logger.WithFields(logger.Fields{
				"tx_hash": signedTx.Hash().Hex(),
				"nonce":   signedTx.Nonce(),
			}).Info(fmt.Sprintf("Transaction lost, retrying with same nonce and increased gas price by %.0f%% and tip cap by %.0f%%...",
				(execCtx.Gas.GasPriceBumpFactor-1)*100, (execCtx.Gas.TipCapBumpFactor-1)*100))

			return &TxExecutionResult{
				Action: ActionRetry,
			}
		}

		logger.WithFields(logger.Fields{
			"tx_hash":       signedTx.Hash().Hex(),
			"nonce":         signedTx.Nonce(),
			"max_gas_price": execCtx.Gas.MaxGasPrice,
			"max_tip_cap":   execCtx.Gas.MaxTipCap,
		}).Warn("Transaction lost but gas price protection limits reached. Stopping retry attempts.")

		return &TxExecutionResult{
			Transaction: signedTx,
			Action:      ActionReturn,
			Error:       errors.Join(ErrGasPriceLimitReached, fmt.Errorf("transaction lost, maxGasPrice: %f, maxTipCap: %f", execCtx.Gas.MaxGasPrice, execCtx.Gas.MaxTipCap)),
		}

	case TxStatusSlow:
		// Only bump gas if this nonce is blocking higher-nonce transactions.
		// Non-blocking slow txs just keep waiting — they'll get mined once
		// the blocking nonce ahead of them is resolved.
		if !wm.isBlockingNonce(signedTx.Nonce(), execCtx.Params.From, execCtx.Params.Network) {
			logger.WithFields(logger.Fields{
				"tx_hash": signedTx.Hash().Hex(),
				"nonce":   signedTx.Nonce(),
			}).Info("Transaction slow but not blocking, continuing to wait...")

			// Set ResumeWith so the next loop iteration re-monitors this same
			// transaction instead of building a new one with a fresh nonce.
			// Without this, the original tx would be orphaned and its mined
			// status would never be reported to the caller.
			execCtx.State.ResumeWith = signedTx

			return &TxExecutionResult{
				Action: ActionRetry,
			}
		}

		// This nonce is blocking — bump gas to speed it up
		if execCtx.BumpGasForSlowTx(signedTx) {
			logger.WithFields(logger.Fields{
				"tx_hash": signedTx.Hash().Hex(),
				"nonce":   signedTx.Nonce(),
			}).Info(fmt.Sprintf("Transaction slow and blocking, increasing gas price by %.0f%% and tip cap by %.0f%%...",
				(execCtx.Gas.GasPriceBumpFactor-1)*100, (execCtx.Gas.TipCapBumpFactor-1)*100))

			return &TxExecutionResult{
				Action: ActionRetry,
			}
		} else {
			// Limits reached - stop retrying and return error
			logger.WithFields(logger.Fields{
				"tx_hash":       signedTx.Hash().Hex(),
				"nonce":         signedTx.Nonce(),
				"max_gas_price": execCtx.Gas.MaxGasPrice,
				"max_tip_cap":   execCtx.Gas.MaxTipCap,
			}).Warn("Transaction slow and blocking but gas price protection limits reached. Stopping retry attempts.")

			return &TxExecutionResult{
				Transaction: signedTx,
				Action:      ActionReturn,
				Error:       errors.Join(ErrGasPriceLimitReached, fmt.Errorf("maxGasPrice: %f, maxTipCap: %f", execCtx.Gas.MaxGasPrice, execCtx.Gas.MaxTipCap)),
			}
		}

	default:
		// Unknown status — treat as transient but count against MaxAttempts
		// to prevent infinite loops if the monitor keeps returning unexpected values.
		logger.WithFields(logger.Fields{
			"tx_hash": signedTx.Hash().Hex(),
			"nonce":   signedTx.Nonce(),
			"status":  status.Status,
		}).Warn("Unknown transaction status from monitor, retrying")

		if result := execCtx.IncrementRetryAndCheck(fmt.Sprintf("unknown monitor status: %s", status.Status)); result != nil {
			return result
		}
		return &TxExecutionResult{
			Action: ActionRetry,
		}
	}
}

func (wm *WalletManager) EnsureTx(
	txType uint8,
	from, to common.Address,
	value *big.Int,
	gasLimit uint64,
	extraGasLimit uint64,
	gasPrice float64,
	extraGasPrice float64,
	tipCapGwei float64,
	extraTipCapGwei float64,
	data []byte,
	network networks.Network,
) (tx *types.Transaction, receipt *types.Receipt, err error) {
	return wm.EnsureTxWithHooks(
		DefaultNumRetries,
		DefaultSleepDuration,
		DefaultTxCheckInterval,
		txType,
		from,
		to,
		value,
		gasLimit, extraGasLimit,
		gasPrice, extraGasPrice,
		tipCapGwei, extraTipCapGwei,
		0, 0, // Default maxGasPrice and maxTipCap (0 means no limit)
		data,
		network,
		nil,
		nil,
		nil,
		nil,
	)
}

// getNetworkByChainID is a helper to get a network by chain ID.
// It uses the configured network resolver (defaults to jarvis networks.GetNetworkByID).
func (wm *WalletManager) getNetworkByChainID(chainID uint64) (networks.Network, error) {
	return wm.networkResolver(chainID)
}

// ResumePendingTransaction resumes the EnsureTx flow for a previously broadcast transaction.
// This is used during crash recovery to continue monitoring and retry logic (including gas bumping)
// for transactions that were pending when the application crashed.
//
// The pendingTx.Transaction field must be non-nil as it contains the original transaction data.
//
// This method enters the same execution flow as EnsureTxWithHooksContext, starting from the
// monitoring phase. If the transaction is slow, it will bump gas and retry. If it's lost,
// it will retry with a new nonce.
func (wm *WalletManager) ResumePendingTransaction(
	ctx context.Context,
	pendingTx *PendingTx,
	opts ResumeTransactionOptions,
) (*types.Transaction, *types.Receipt, error) {
	if pendingTx == nil {
		return nil, nil, fmt.Errorf("pendingTx cannot be nil")
	}
	if pendingTx.Transaction == nil {
		return nil, nil, fmt.Errorf("pendingTx.Transaction cannot be nil - transaction data is required for resume")
	}

	tx := pendingTx.Transaction
	network, err := wm.getNetworkByChainID(pendingTx.ChainID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get network for chainID %d: %w", pendingTx.ChainID, err)
	}

	// Apply defaults
	if opts.NumRetries <= 0 {
		opts.NumRetries = DefaultNumRetries
	}
	if opts.SleepDuration <= 0 {
		opts.SleepDuration = DefaultSleepDuration
	}
	if opts.TxCheckInterval <= 0 {
		opts.TxCheckInterval = DefaultTxCheckInterval
	}

	// Extract gas settings from the existing transaction
	gasPrice := float64(tx.GasPrice().Uint64()) / 1e9 // Convert wei to gwei
	tipCap := float64(tx.GasTipCap().Uint64()) / 1e9  // Convert wei to gwei

	// Set max limits based on original tx if not specified
	if opts.MaxGasPrice <= 0 {
		opts.MaxGasPrice = gasPrice * MaxCapMultiplier
	}
	if opts.MaxTipCap <= 0 {
		opts.MaxTipCap = tipCap * MaxCapMultiplier
	}

	// Use resolved manager defaults — guaranteed non-zero by applyDefaultsResolution.
	defaults := wm.Defaults()

	// Create execution context pre-populated with the existing transaction.
	// State.ResumeWith is set so the loop skips executeTransactionAttempt on first iteration
	// and goes straight to monitoring.
	execCtx := &TxExecutionContext{
		Params: TxParams{
			TxType:  uint8(tx.Type()),
			From:    pendingTx.Wallet,
			To:      *tx.To(),
			Value:   tx.Value(),
			Data:    tx.Data(),
			Network: network,
		},
		Retry: RetryConfig{
			MaxAttempts:     opts.NumRetries,
			SleepDuration:   opts.SleepDuration,
			TxCheckInterval: opts.TxCheckInterval,
			SlowTxTimeout:   defaults.SlowTxTimeout,
		},
		Gas: GasBounds{
			MaxGasPrice:        opts.MaxGasPrice,
			MaxTipCap:          opts.MaxTipCap,
			GasPriceBumpFactor: defaults.GasPriceBumpFactor,
			TipCapBumpFactor:   defaults.TipCapBumpFactor,
		},
		Hooks: TxHooks{
			TxMined: opts.TxMinedHook,
		},
		State: TxRetryState{
			GasPrice:   gasPrice,
			TipCap:     tipCap,
			GasLimit:   tx.Gas(),
			Nonce:      big.NewInt(int64(tx.Nonce())),
			OldTxs:     make(map[string]*types.Transaction),
			ResumeWith: tx, // Set ResumeWith to skip first executeTransactionAttempt
		},
	}

	// Pre-populate OldTxs with the pending transaction
	execCtx.State.OldTxs[tx.Hash().Hex()] = tx

	// Use the same execution loop as EnsureTxWithHooksContext
	errDecoder := wm.createErrorDecoder(execCtx.Hooks.ABIs)
	return wm.executeTransactionLoop(ctx, execCtx, errDecoder)
}

// persistTxBroadcast persists a broadcasted transaction to the tx store
func (wm *WalletManager) persistTxBroadcast(tx *types.Transaction, wallet common.Address, chainID uint64) {
	if wm.txStore == nil {
		return
	}
	ctx := context.Background()
	ptx := &PendingTx{
		Hash:        tx.Hash(),
		Wallet:      wallet,
		ChainID:     chainID,
		Nonce:       tx.Nonce(),
		Status:      PendingTxStatusBroadcasted,
		Transaction: tx,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	_ = wm.txStore.Save(ctx, ptx)
}

// persistTxMined persists a mined transaction status to the tx store
func (wm *WalletManager) persistTxMined(txHash common.Hash, status PendingTxStatus, receipt *types.Receipt) {
	if wm.txStore == nil {
		return
	}
	ctx := context.Background()
	_ = wm.txStore.UpdateStatus(ctx, txHash, status, receipt)
}
