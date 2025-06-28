package walletarmy

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/KyberNetwork/logger"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/tranvictor/jarvis/accounts"
	jarviscommon "github.com/tranvictor/jarvis/common"
	"github.com/tranvictor/jarvis/networks"
	"github.com/tranvictor/jarvis/txanalyzer"
	"github.com/tranvictor/jarvis/util"
	"github.com/tranvictor/jarvis/util/account"
	"github.com/tranvictor/jarvis/util/broadcaster"
	"github.com/tranvictor/jarvis/util/monitor"
	"github.com/tranvictor/jarvis/util/reader"
)

const (
	DefaultNumRetries    = 9
	DefaultSleepDuration = 5 * time.Second
)

var (
	ErrEstimateGasFailed    = fmt.Errorf("estimate gas failed")
	ErrAcquireNonceFailed   = fmt.Errorf("acquire nonce failed")
	ErrGetGasSettingFailed  = fmt.Errorf("get gas setting failed")
	ErrEnsureTxOutOfRetries = fmt.Errorf("ensure tx out of retries")
)

// WalletManager manages
//  1. multiple wallets and their informations in its
//     life time. It basically gives next nonce to do transaction for specific
//     wallet and specific network.
//     It queries the node to check the nonce in lazy maner, it also takes mining
//     txs into account.
//  2. multiple networks gas price. The gas price will be queried lazily prior to txs
//     and will be stored as cache for a while
//  3. txs in the context manager's life time
type WalletManager struct {
	lock sync.RWMutex

	// readers stores all reader instances for all networks that ever interacts
	// with accounts manager. ChainID of the network is used as the key.
	readers      map[uint64]*reader.EthReader
	broadcasters map[uint64]*broadcaster.Broadcaster
	analyzers    map[uint64]*txanalyzer.TxAnalyzer
	txMonitors   map[uint64]*monitor.TxMonitor
	accounts     map[common.Address]*account.Account

	// nonces map between (address, network) => last signed nonce (not mined nonces)
	pendingNonces map[common.Address]map[uint64]*big.Int
	// txs map between (address, network, nonce) => tx
	txs map[common.Address]map[uint64]map[uint64]*types.Transaction

	// gasPrices map between network => gasinfo
	gasSettings map[uint64]*GasInfo
}

func NewWalletManager() *WalletManager {
	return &WalletManager{
		lock:          sync.RWMutex{},
		readers:       map[uint64]*reader.EthReader{},
		broadcasters:  map[uint64]*broadcaster.Broadcaster{},
		analyzers:     map[uint64]*txanalyzer.TxAnalyzer{},
		txMonitors:    map[uint64]*monitor.TxMonitor{},
		accounts:      map[common.Address]*account.Account{},
		pendingNonces: map[common.Address]map[uint64]*big.Int{},
		txs:           map[common.Address]map[uint64]map[uint64]*types.Transaction{},
		gasSettings:   map[uint64]*GasInfo{},
	}
}

func (wm *WalletManager) SetAccount(acc *account.Account) {
	wm.lock.Lock()
	defer wm.lock.Unlock()
	wm.accounts[acc.Address()] = acc
}

func (wm *WalletManager) UnlockAccount(addr common.Address) (*account.Account, error) {
	accDesc, err := accounts.GetAccount(addr.Hex())
	if err != nil {
		return nil, fmt.Errorf("wallet %s doesn't exist in jarvis", addr.Hex())
	}
	acc, err := accounts.UnlockAccount(accDesc)
	if err != nil {
		return nil, fmt.Errorf("unlocking wallet failed: %w", err)
	}
	wm.SetAccount(acc)
	return acc, nil
}

func (wm *WalletManager) Account(wallet common.Address) *account.Account {
	wm.lock.RLock()
	defer wm.lock.RUnlock()
	return wm.accounts[wallet]
}

func (wm *WalletManager) setTx(wallet common.Address, network networks.Network, tx *types.Transaction) {
	wm.lock.Lock()
	defer wm.lock.Unlock()

	if wm.txs[wallet] == nil {
		wm.txs[wallet] = map[uint64]map[uint64]*types.Transaction{}
	}

	if wm.txs[wallet][network.GetChainID()] == nil {
		wm.txs[wallet][network.GetChainID()] = map[uint64]*types.Transaction{}
	}

	wm.txs[wallet][network.GetChainID()][uint64(tx.Nonce())] = tx
}

func (wm *WalletManager) getBroadcaster(network networks.Network) *broadcaster.Broadcaster {
	wm.lock.RLock()
	defer wm.lock.RUnlock()
	return wm.broadcasters[network.GetChainID()]
}

func (wm *WalletManager) Broadcaster(network networks.Network) *broadcaster.Broadcaster {
	broadcaster := wm.getBroadcaster(network)
	if broadcaster == nil {
		err := wm.initNetwork(network)
		if err != nil {
			panic(
				fmt.Errorf(
					"couldn't init reader and broadcaster for network: %s, err: %s",
					network,
					err,
				),
			)
		}
		return wm.getBroadcaster(network)
	}
	return broadcaster
}

func (wm *WalletManager) getReader(network networks.Network) *reader.EthReader {
	wm.lock.RLock()
	defer wm.lock.RUnlock()
	return wm.readers[network.GetChainID()]
}

func (wm *WalletManager) Reader(network networks.Network) *reader.EthReader {
	reader := wm.getReader(network)
	if reader == nil {
		err := wm.initNetwork(network)
		if err != nil {
			panic(
				fmt.Errorf(
					"couldn't init reader and broadcaster for network: %s, err: %s",
					network,
					err,
				),
			)
		}
		return wm.getReader(network)
	}
	return reader
}

func (wm *WalletManager) getAnalyzer(network networks.Network) *txanalyzer.TxAnalyzer {
	wm.lock.RLock()
	defer wm.lock.RUnlock()
	return wm.analyzers[network.GetChainID()]
}

func (wm *WalletManager) getTxMonitor(network networks.Network) *monitor.TxMonitor {
	wm.lock.RLock()
	defer wm.lock.RUnlock()
	return wm.txMonitors[network.GetChainID()]
}

func (wm *WalletManager) Analyzer(network networks.Network) *txanalyzer.TxAnalyzer {
	analyzer := wm.getAnalyzer(network)
	if analyzer == nil {
		err := wm.initNetwork(network)
		if err != nil {
			panic(
				fmt.Errorf(
					"couldn't init reader and broadcaster for network: %s, err: %s",
					network,
					err,
				),
			)
		}
		return wm.getAnalyzer(network)
	}
	return analyzer
}

func (wm *WalletManager) initNetwork(network networks.Network) (err error) {
	wm.lock.Lock()
	defer wm.lock.Unlock()

	reader, found := wm.readers[network.GetChainID()]
	if !found {
		reader, err = util.EthReader(network)
		if err != nil {
			return err
		}
	}
	wm.readers[network.GetChainID()] = reader

	analyzer, found := wm.analyzers[network.GetChainID()]
	if !found {
		analyzer = txanalyzer.NewGenericAnalyzer(reader, network)
		if err != nil {
			return err
		}
	}
	wm.analyzers[network.GetChainID()] = analyzer

	broadcaster, found := wm.broadcasters[network.GetChainID()]
	if !found {
		broadcaster, err = util.EthBroadcaster(network)
		if err != nil {
			return err
		}
	}
	wm.broadcasters[network.GetChainID()] = broadcaster

	txMonitor, found := wm.txMonitors[network.GetChainID()]
	if !found {
		txMonitor = monitor.NewGenericTxMonitor(reader)
	}
	wm.txMonitors[network.GetChainID()] = txMonitor

	return nil
}

func (wm *WalletManager) setPendingNonce(wallet common.Address, network networks.Network, nonce uint64) {
	wm.lock.Lock()
	defer wm.lock.Unlock()
	walletNonces := wm.pendingNonces[wallet]
	if walletNonces == nil {
		walletNonces = map[uint64]*big.Int{}
		wm.pendingNonces[wallet] = walletNonces
	}
	oldNonce := walletNonces[network.GetChainID()]
	if oldNonce != nil && oldNonce.Cmp(big.NewInt(int64(nonce))) >= 0 {
		return
	}
	walletNonces[network.GetChainID()] = big.NewInt(int64(nonce))
}

func (wm *WalletManager) pendingNonce(wallet common.Address, network networks.Network) *big.Int {
	wm.lock.RLock()
	defer wm.lock.RUnlock()
	walletPendingNonces := wm.pendingNonces[wallet]
	if walletPendingNonces == nil {
		return nil
	}
	result := walletPendingNonces[network.GetChainID()]
	if result != nil {
		// when there is a pending nonce, we add 1 to get the next nonce
		result = big.NewInt(0).Add(result, big.NewInt(1))
	}
	return result
}

//  1. get remote pending nonce
//  2. get local pending nonce
//  2. get mined nonce
//  3. if mined nonce == remote == local, all good, lets return the mined nonce
//  4. since mined nonce is always <= remote nonce, if mined nonce > local nonce,
//     this session doesn't catch up with mined txs (in case there are txs  that
//     were from other apps and they were mined), return max(mined none, remote nonce)
//     and set local nonce to max(mined none, remote nonce)
//  5. if not, means mined nonce is smaller than both remote and local pending nonce
//     5.1 if remote == local: means all pending txs are from this session, we return
//     local nonce
//     5.2 if remote > local: means there is pending txs from another app, we return
//     remote nonce in order not to mess up with the other txs, but give a warning
//     5.3 if local > remote: means txs from this session are not broadcasted to the
//     the notes, return local nonce and give warnings
func (wm *WalletManager) nonce(wallet common.Address, network networks.Network) (*big.Int, error) {

	reader := wm.Reader(network)
	minedNonce, err := reader.GetMinedNonce(wallet.Hex())
	if err != nil {
		return nil, fmt.Errorf("couldn't get mined nonce in context manager: %s", err)
	}

	remotePendingNonce, err := reader.GetPendingNonce(wallet.Hex())
	if err != nil {
		return nil, fmt.Errorf("couldn't get remote pending nonce in context manager: %s", err)
	}

	var localPendingNonce uint64
	localPendingNonceBig := wm.pendingNonce(wallet, network)

	if localPendingNonceBig == nil {
		wm.setPendingNonce(wallet, network, remotePendingNonce)
		localPendingNonce = remotePendingNonce
	} else {
		localPendingNonce = localPendingNonceBig.Uint64()
	}

	hasPendingTxsOnNodes := minedNonce < remotePendingNonce
	if !hasPendingTxsOnNodes {
		if minedNonce > remotePendingNonce {
			return nil, fmt.Errorf(
				"mined nonce is higher than pending nonce, this is abnormal data from nodes, retry again later",
			)
		}
		// in this case, minedNonce is supposed to == remotePendingNonce
		if localPendingNonce <= minedNonce {
			// in this case, minedNonce is more up to date, update localPendingNonce
			// and return minedNonce
			wm.setPendingNonce(wallet, network, minedNonce)
			return big.NewInt(int64(minedNonce)), nil
		} else {
			// in this case, local is more up to date, return pending nonce
			wm.setPendingNonce(wallet, network, localPendingNonce) // update local nonce to the latest
			return big.NewInt(int64(localPendingNonce)), nil
		}
	}

	if localPendingNonce <= minedNonce {
		// localPendingNonce <= minedNonce < remotePendingNonce
		// in this case, there are pending txs on nodes and they are
		// from other apps
		// TODO: put warnings
		// we don't have to update local pending nonce here since
		// it will be updated if the new tx is broadcasted with context manager
		wm.setPendingNonce(wallet, network, remotePendingNonce) // update local nonce to the latest
		return big.NewInt(int64(remotePendingNonce)), nil
	} else if localPendingNonce <= remotePendingNonce {
		// minedNonce < localPendingNonce <= remotePendingNonce
		// similar to the previous case, however, there are pending txs came from
		// jarvis as well. No need special treatments
		wm.setPendingNonce(wallet, network, remotePendingNonce) // update local nonce to the latest
		return big.NewInt(int64(remotePendingNonce)), nil
	}
	// minedNonce < remotePendingNonce < localPendingNonce
	// in this case, local has more pending txs, this is the case when
	// the node doesn't have full pending txs as local, something is
	// wrong with the local txs.
	// TODO: give warnings and check pending txs, see if they are not found and update
	// local pending nonce respectively and retry not found txs, need to figure out
	// a mechanism to stop trying as well.
	// For now, we will just go ahead with localPendingNonce
	wm.setPendingNonce(wallet, network, localPendingNonce) // update local nonce to the latest
	return big.NewInt(int64(localPendingNonce)), nil
}

func (wm *WalletManager) getGasSettingInfo(network networks.Network) *GasInfo {
	wm.lock.RLock()
	defer wm.lock.RUnlock()
	return wm.gasSettings[network.GetChainID()]
}

func (wm *WalletManager) setGasInfo(network networks.Network, info *GasInfo) {
	wm.lock.Lock()
	defer wm.lock.Unlock()
	wm.gasSettings[network.GetChainID()] = info
}

// implement a cache mechanism to be more efficient
func (wm *WalletManager) GasSetting(network networks.Network) (*GasInfo, error) {
	gasInfo := wm.getGasSettingInfo(network)
	if gasInfo == nil || time.Since(gasInfo.Timestamp) >= GAS_INFO_TTL {
		// gasInfo is not initiated or outdated
		reader := wm.Reader(network)
		gasPrice, gasTipCapGwei, err := reader.SuggestedGasSettings()
		if err != nil {
			return nil, fmt.Errorf("couldn't get gas settings in context manager: %w", err)
		}

		info := GasInfo{
			GasPrice:         gasPrice,
			BaseGasPrice:     nil,
			MaxPriorityPrice: gasTipCapGwei,
			FeePerGas:        gasPrice,
			Timestamp:        time.Now(),
		}
		wm.setGasInfo(network, &info)
		return &info, nil
	}
	return wm.getGasSettingInfo(network), nil
}

func (wm *WalletManager) buildTx(
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
	if gasLimit == 0 {
		gasLimit, err = wm.Reader(network).EstimateExactGas(
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
	}

	if gasPrice == 0 {
		gasInfo, err := wm.GasSetting(network)
		if err != nil {
			return nil, errors.Join(ErrGetGasSettingFailed, fmt.Errorf("couldn't get gas price info from any nodes: %w", err))
		}
		gasPrice = gasInfo.GasPrice
		tipCapGwei = gasInfo.MaxPriorityPrice
	}

	return jarviscommon.BuildExactTx(
		txType,
		nonce.Uint64(),
		to.Hex(),
		value,
		gasLimit+extraGasLimit,
		gasPrice+extraGasPrice,
		tipCapGwei+extraTipCapGwei,
		data,
		network.GetChainID(),
	), nil
}

func (wm *WalletManager) signTx(
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

func (wm *WalletManager) signTxAndBroadcast(
	wallet common.Address,
	tx *types.Transaction,
	network networks.Network,
) (signedTx *types.Transaction, successful bool, err BroadcastError) {
	signedAddr, tx, err := wm.signTx(wallet, tx, network)
	if err != nil {
		return tx, false, err
	}
	if signedAddr.Cmp(wallet) != 0 {
		return tx, false, fmt.Errorf(
			"signed from wrong address. You could use wrong hw or passphrase. Expected wallet: %s, signed wallet: %s",
			wallet.Hex(),
			signedAddr.Hex(),
		)
	}
	_, broadcasted, allErrors := wm.broadcastTx(tx)
	return tx, broadcasted, allErrors
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
	return nil
}

func (wm *WalletManager) broadcastTx(
	tx *types.Transaction,
) (hash string, broadcasted bool, err BroadcastError) {
	network, err := networks.GetNetworkByID(tx.ChainId().Uint64())
	// TODO: handle chainId 0 for old txs
	if err != nil {
		return "", false, BroadcastError(fmt.Errorf("tx is encoded with unsupported ChainID: %w", err))
	}
	hash, broadcasted, allErrors := wm.Broadcaster(network).BroadcastTx(tx)
	if broadcasted {
		wm.registerBroadcastedTx(tx, network)
	}
	return hash, broadcasted, NewBroadcastError(allErrors)
}

// MonitorTx non-blocking way to monitor the tx status, it returns a channel that will be closed when the tx monitoring is done
// the channel is supposed to receive the following values:
//  1. "mined" if the tx is mined
//  2. "slow" if the tx is too slow to be mined (so receiver might want to retry with higher gas price)
//  3. other strings if the tx failed and the reason is returned by the node or other debugging error message that the node can return
func (wm *WalletManager) MonitorTx(tx *types.Transaction, network networks.Network) <-chan string {
	txMonitor := wm.getTxMonitor(network)
	statusChan := make(chan string)
	monitorChan := txMonitor.MakeWaitChannel(tx.Hash().Hex())
	go func() {
		select {
		case status := <-monitorChan:
			switch status.Status {
			case "done":
				statusChan <- "mined"
			case "reverted":
				// TODO: analyze to see what is the reason
				statusChan <- "reverted"
			case "lost":
				statusChan <- "lost"
			default:
				// ignore other statuses
			}
		case <-time.After(10 * time.Second):
			statusChan <- "slow"
		}
		close(statusChan)
	}()
	return statusChan
}

func (wm *WalletManager) getTxStatuses(oldTxs map[string]*types.Transaction, network networks.Network) (statuses map[string]jarviscommon.TxInfo, err error) {
	result := map[string]jarviscommon.TxInfo{}

	for _, tx := range oldTxs {
		txInfo, _ := wm.Reader(network).TxInfoFromHash(tx.Hash().Hex())
		result[tx.Hash().Hex()] = txInfo
	}

	return result, nil
}

// EnsureTx ensures the tx is broadcasted and mined, it will retry until the tx is mined
// it returns nil and error if:
//  1. the tx couldn't be built
//  2. the tx couldn't be broadcasted and get mined after 10 retries
//
// It always returns the tx that was mined, either if the tx was successful or reverted
// Possible errors:
//  1. ErrEstimateGasFailed
//  2. ErrAcquireNonceFailed
//  3. ErrGetGasSettingFailed
//  4. ErrEnsureTxOutOfRetries
//
// # If the caller wants to know the reason of the error, they can use errors.Is to check if the error is one of the above
//
// After building the tx and before signing and broadcasting, the caller can provide a function hook to receive the tx and building error,
// if the hook returns an error, the process will be stopped and the error will be returned. If the hook returns nil, the process will continue
// even if the tx building failed, in this case, it will retry with the same data up to 10 times and the hook will be called again.
//
// After signing and broadcasting successfully, the caller can provide a function hook to receive the signed tx and broadcast error,
// if the hook returns an error, the process will be stopped and the error will be returned. If the hook returns nil, the process will continue
// to monitor the tx to see if the tx is mined or not. If the tx is not mined, the process will retry either with a new nonce or with higher gas
// price and tip cap to ensure the tx is mined. Hooks will be called again in the retry process.
func (wm *WalletManager) EnsureTxWithHooks(
	numRetries int,
	sleepDuration time.Duration,
	txType uint8,
	from, to common.Address,
	value *big.Int,
	gasLimit uint64, extraGasLimit uint64,
	gasPrice float64, extraGasPrice float64,
	tipCapGwei float64, extraTipCapGwei float64,
	data []byte,
	network networks.Network,
	beforeSignAndBroadcastHook Hook,
	afterSignAndBroadcastHook Hook,
	abis []abi.ABI,
	gasEstimationFailedHook GasEstimationFailedHook,
) (tx *types.Transaction, err error) {
	// set default values for sleepDuration if not provided
	if sleepDuration == 0 {
		sleepDuration = DefaultSleepDuration
	}

	var errDecoder *ErrorDecoder

	if len(abis) > 0 {
		errDecoder, err = NewErrorDecoder(abis...)
		if err != nil {
			logger.WithFields(logger.Fields{
				"error": err,
			}).Error("Failed to create error decoder. Ignore and continue")
		}
	}

	var oldTxs map[string]*types.Transaction = map[string]*types.Transaction{}
	var retryNonce *big.Int
	var retryGasPrice float64 = gasPrice
	var retryTipCap float64 = tipCapGwei
	var signedTx *types.Transaction

	// Always run at least once (initial attempt) + numRetries additional attempts
	for i := range numRetries + 1 {
		// always sleep for sleepDuration before retrying
		if i > 0 {
			time.Sleep(sleepDuration)
		}

		tx, err = wm.buildTx(
			txType,          // tx type
			from,            // from address
			to,              // to address
			retryNonce,      // nonce (if nil means context manager will determine the nonce)
			value,           // amount
			gasLimit,        // gas limit
			extraGasLimit,   // extra gas limit
			retryGasPrice,   // gas price
			extraGasPrice,   // extra gas price
			retryTipCap,     // tip cap
			extraTipCapGwei, // extra tip cap
			data,            // data
			network,         // network
		)

		var successful bool
		var broadcastErr BroadcastError

		if errors.Is(err, ErrEstimateGasFailed) {
			// there is a chance that all of the nodes returned errors but one of them accepted the tx
			// making the tx being broadcasted successfully. In this case, the last iteration will continue
			// and the tx might not be able to build successfully because the Gas estimation might revert.
			// we should check the tx status of all signedTx.

			statuses, err := wm.getTxStatuses(oldTxs, network)
			if err != nil {
				logger.WithFields(logger.Fields{
					"error": err,
				}).Debug("Getting tx statuses in case where tx wasn't broadcasted because nonce is too low. Ignore and continue the retry loop")
				// ignore the error and retry
				// we do nothing and continue the loop
			} else {
				// if it is mined, we don't need to do anything, just stop the loop and return
				for txhash, status := range statuses {
					switch status.Status {
					case "done", "reverted":
						return oldTxs[txhash], nil
					}
				}

				// there is no done or reverted tx, we check to see all pending txs and get the one
				// with the highest gas price
				highestGasPrice := big.NewInt(0)
				for txhash, status := range statuses {
					switch status.Status {
					case "pending":
						if oldTxs[txhash].GasPrice().Cmp(highestGasPrice) > 0 {
							highestGasPrice = oldTxs[txhash].GasPrice()
							signedTx = oldTxs[txhash]
							successful = true
							broadcastErr = nil
						}
					}
				}
			}
		}

		// If tx is nil because estimate gas failed, we skip the current iteration
		if tx == nil {
			if errDecoder != nil && gasEstimationFailedHook != nil {
				revertMsgErr := errDecoder.Decode(err)
				hookGasLimit, hookErr := gasEstimationFailedHook(tx, revertMsgErr, err)
				if hookErr != nil {
					// when the hook returns an error, we stop the loop and return the error
					return nil, hookErr
				}
				if hookGasLimit != nil {
					// in case the hook returns a non-nil gas limit, we use it for the next iteration
					gasLimit = hookGasLimit.Uint64()
				}
			}
			continue
		}

		if signedTx == nil {
			// if in the above process, we didn't find any pending tx for the task, we proceed to
			// normal process
			if beforeSignAndBroadcastHook != nil {
				hookError := beforeSignAndBroadcastHook(tx, err)
				if hookError != nil {
					return nil, fmt.Errorf("after tx building and before signing and broadcasting hook error: %w", hookError)
				}
			}
			signedTx, successful, broadcastErr = wm.signTxAndBroadcast(from, tx, network)
		}

		if signedTx != nil {
			oldTxs[signedTx.Hash().Hex()] = signedTx
		}

		if !successful {
			logger.WithFields(logger.Fields{
				"tx_hash":         signedTx.Hash().Hex(),
				"nonce":           tx.Nonce(),
				"gas_price":       tx.GasPrice().String(),
				"tip_cap":         tx.GasTipCap().String(),
				"max_fee_per_gas": tx.GasFeeCap().String(),
				"error":           broadcastErr,
			}).Debug("Unsuccessful signing and broadcasting transaction")

			// there are a few cases we should handle
			// 1. insufficient fund
			if broadcastErr == ErrInsufficientFund {
				// wait for 5 seconds and retry with the same nonce because the nonce is already acquired from
				// the context manager
				retryNonce = big.NewInt(int64(tx.Nonce()))
				continue
			}

			// 2. nonce is low
			if broadcastErr == ErrNonceIsLow {
				// in this case, we need to check if the last transaction is mined or it is lost
				statuses, err := wm.getTxStatuses(oldTxs, network)
				if err != nil {
					logger.WithFields(logger.Fields{
						"error": err,
					}).Debug("Getting tx statuses in case where tx wasn't broadcasted because nonce is too low. Ignore and continue the retry loop")
					// ignore the error and retry
					continue
				}
				// if it is mined, we don't need to do anything, just stop the loop and return
				for txhash, status := range statuses {
					switch status.Status {
					case "done", "reverted":
						return oldTxs[txhash], nil
					}
				}

				// in this case, old txes weren't mined but the nonce is already used, it means
				// a different tx is with the same nonce was mined somewhere else
				// so we need to retry with a new nonce
				retryNonce = nil
				continue
			}

			// 3. gas limit is too low
			if broadcastErr == ErrGasLimitIsTooLow {
				// in this case, we just rely on the loop to hope it will finally have a better gas limit estimation
				// however, the same nonce must be used since it is acquired from the context manager already
				retryNonce = big.NewInt(int64(tx.Nonce()))
				continue
			}

			// 4. tx is known
			if broadcastErr == ErrTxIsKnown {
				// in this case, we need to speed up the tx by increasing the gas price and tip cap
				// however, it should be handled by the slow status gotten from the monitor tx below
				// so we just need to retry with the same nonce
				retryNonce = big.NewInt(int64(tx.Nonce()))
				continue
			}

			retryNonce = big.NewInt(int64(tx.Nonce()))
			continue
		} else {
			logger.WithFields(logger.Fields{
				"tx_hash":         signedTx.Hash().Hex(),
				"nonce":           tx.Nonce(),
				"gas_price":       tx.GasPrice().String(),
				"tip_cap":         tx.GasTipCap().String(),
				"max_fee_per_gas": tx.GasFeeCap().String(),
			}).Info("Signed and broadcasted transaction")

			if afterSignAndBroadcastHook != nil {
				hookError := afterSignAndBroadcastHook(signedTx, broadcastErr)
				if hookError != nil {
					return signedTx, fmt.Errorf("after signing and broadcasting hook error: %w", hookError)
				}
			}
		}

		statusChan := wm.MonitorTx(signedTx, network)
		status := <-statusChan
		switch status {
		case "mined", "reverted":
			return signedTx, nil
		case "lost":
			logger.WithFields(logger.Fields{
				"tx_hash": signedTx.Hash().Hex(),
			}).Info("Transaction lost, retrying...")
			retryNonce = nil
			time.Sleep(5 * time.Second)
		case "slow":
			logger.WithFields(logger.Fields{
				"tx_hash": signedTx.Hash().Hex(),
			}).Info("Transaction slow, retrying with the same nonce and increasing gas price by 20%% and tip cap by 10%%...")
			retryGasPrice = jarviscommon.BigToFloat(tx.GasPrice(), 9)*1.2 - extraGasPrice
			retryTipCap = jarviscommon.BigToFloat(tx.GasTipCap(), 9)*1.1 - extraTipCapGwei
			retryNonce = big.NewInt(int64(tx.Nonce()))
			time.Sleep(5 * time.Second)
		}
	}

	return nil, errors.Join(ErrEnsureTxOutOfRetries, err)
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
) (tx *types.Transaction, err error) {
	return wm.EnsureTxWithHooks(
		DefaultNumRetries,
		DefaultSleepDuration,
		txType,
		from,
		to,
		value,
		gasLimit, extraGasLimit,
		gasPrice, extraGasPrice,
		tipCapGwei, extraTipCapGwei,
		data,
		network,
		nil,
		nil,
		nil,
		nil,
	)
}
