// Package nonce provides thread-safe nonce tracking for Ethereum wallets.
// This is an internal package and should not be imported directly by external code.
package nonce

import (
	"math/big"
	"sync"

	"github.com/KyberNetwork/logger"
	"github.com/ethereum/go-ethereum/common"
)

// Tracker manages nonce tracking for multiple wallets across multiple networks.
// It provides thread-safe operations for acquiring, releasing, and tracking nonces.
type Tracker struct {
	// pendingNonces maps wallet address -> (chainID -> last used nonce)
	pendingNonces sync.Map // map[common.Address]map[uint64]*big.Int

	// walletLocks provides per-wallet locking
	walletLocks sync.Map // map[common.Address]*sync.RWMutex

	// claimedGaps tracks gap nonces that have been claimed by AcquireNonce
	// but not yet confirmed (i.e., not yet in TxStore). This prevents two
	// sequential callers under the same lock from both claiming the same gap.
	// Structure: wallet -> (chainID -> set of claimed nonces)
	// MUST be accessed with wallet lock held.
	claimedGaps sync.Map // map[common.Address]map[uint64]map[uint64]bool
}

// NewTracker creates a new nonce tracker
func NewTracker() *Tracker {
	return &Tracker{}
}

// getWalletLock returns the lock for a specific wallet, creating it if necessary
func (t *Tracker) getWalletLock(wallet common.Address) *sync.RWMutex {
	lock, _ := t.walletLocks.LoadOrStore(wallet, &sync.RWMutex{})
	return lock.(*sync.RWMutex)
}

// getClaimedGaps returns the set of claimed gap nonces for a wallet/chain,
// creating the inner maps if necessary. MUST be called with wallet lock held.
func (t *Tracker) getClaimedGaps(wallet common.Address, chainID uint64) map[uint64]bool {
	raw, _ := t.claimedGaps.LoadOrStore(wallet, make(map[uint64]map[uint64]bool))
	chainGaps := raw.(map[uint64]map[uint64]bool)
	if chainGaps[chainID] == nil {
		chainGaps[chainID] = make(map[uint64]bool)
	}
	return chainGaps[chainID]
}

// claimGap marks a gap nonce as claimed. MUST be called with wallet lock held.
func (t *Tracker) claimGap(wallet common.Address, chainID uint64, nonce uint64) {
	gaps := t.getClaimedGaps(wallet, chainID)
	gaps[nonce] = true
}

// getOrCreateNonceMap returns the nonce map for a wallet, creating it if necessary.
// MUST be called with wallet lock held.
func (t *Tracker) getOrCreateNonceMap(wallet common.Address) map[uint64]*big.Int {
	noncesRaw, _ := t.pendingNonces.LoadOrStore(wallet, make(map[uint64]*big.Int))
	return noncesRaw.(map[uint64]*big.Int)
}

// SetPendingNonceUnlocked sets the pending nonce. MUST be called with wallet lock held.
func (t *Tracker) SetPendingNonceUnlocked(wallet common.Address, chainID uint64, networkName string, nonce uint64) {
	walletNonces := t.getOrCreateNonceMap(wallet)
	oldNonce := walletNonces[chainID]
	if oldNonce != nil && oldNonce.Cmp(big.NewInt(int64(nonce))) >= 0 {
		logger.WithFields(logger.Fields{
			"wallet":    wallet.Hex(),
			"network":   networkName,
			"chain_id":  chainID,
			"new_nonce": nonce,
			"old_nonce": oldNonce.Uint64(),
		}).Debug("setPendingNonce skipped: new nonce not higher than existing")
		return
	}

	var oldNonceVal string
	if oldNonce != nil {
		oldNonceVal = oldNonce.String()
	} else {
		oldNonceVal = "nil"
	}
	logger.WithFields(logger.Fields{
		"wallet":    wallet.Hex(),
		"network":   networkName,
		"chain_id":  chainID,
		"new_nonce": nonce,
		"old_nonce": oldNonceVal,
	}).Debug("setPendingNonce: updating local pending nonce")

	walletNonces[chainID] = big.NewInt(int64(nonce))
}

// SetPendingNonce sets the pending nonce with locking
func (t *Tracker) SetPendingNonce(wallet common.Address, chainID uint64, networkName string, nonce uint64) {
	lock := t.getWalletLock(wallet)
	lock.Lock()
	defer lock.Unlock()
	t.SetPendingNonceUnlocked(wallet, chainID, networkName, nonce)
}

// GetPendingNonceUnlocked returns the next nonce to use. MUST be called with wallet lock held.
// Returns nil if no local nonce is tracked.
func (t *Tracker) GetPendingNonceUnlocked(wallet common.Address, chainID uint64) *big.Int {
	noncesRaw, ok := t.pendingNonces.Load(wallet)
	if !ok {
		return nil
	}
	walletPendingNonces := noncesRaw.(map[uint64]*big.Int)
	result := walletPendingNonces[chainID]
	if result != nil {
		// when there is a pending nonce, we add 1 to get the next nonce
		result = big.NewInt(0).Add(result, big.NewInt(1))
	}
	return result
}

// GetPendingNonce returns the pending nonce with locking
func (t *Tracker) GetPendingNonce(wallet common.Address, chainID uint64) *big.Int {
	lock := t.getWalletLock(wallet)
	lock.RLock()
	defer lock.RUnlock()
	return t.GetPendingNonceUnlocked(wallet, chainID)
}

// AcquireResult contains the result of a nonce acquisition
type AcquireResult struct {
	Nonce          uint64
	DecisionReason string
}

// GapFinder is a callback that checks for nonce gaps in a given range [from, to).
// The claimed set contains gap nonces that were already claimed by previous callers
// but not yet recorded in the external store — the finder MUST skip these.
// It returns the gap nonce and true if a gap is found, or 0 and false otherwise.
// This is called under the wallet's write lock, so implementations should not
// attempt to acquire the same lock (e.g., by calling GetPendingNonce or AcquireNonce).
type GapFinder func(wallet common.Address, chainID uint64, from, to uint64, claimed map[uint64]bool) (gapNonce uint64, found bool)

// AcquireNonce atomically determines and reserves the next nonce for a transaction.
// It takes the remote state (mined and pending nonces from the network) and combines
// with local tracking to determine the next nonce.
//
// If gapFinder is non-nil and local pending > remote pending, the gap finder is
// called under the wallet lock to check for nonce gaps before advancing. This
// ensures that two concurrent callers cannot both claim the same gap nonce.
//
// Parameters:
//   - wallet: the wallet address
//   - chainID: the network chain ID
//   - networkName: the network name (for logging)
//   - minedNonce: the mined nonce from the network
//   - remotePendingNonce: the pending nonce from the network
//   - gapFinder: optional callback to detect nonce gaps (may be nil)
//
// Returns the acquired nonce and decision reason
func (t *Tracker) AcquireNonce(
	wallet common.Address,
	chainID uint64,
	networkName string,
	minedNonce uint64,
	remotePendingNonce uint64,
	gapFinder GapFinder,
) (*AcquireResult, error) {
	lock := t.getWalletLock(wallet)
	lock.Lock()
	defer lock.Unlock()

	// Check for nonce gaps under the lock before the normal nonce logic.
	// This prevents two concurrent callers from both finding and returning
	// the same gap nonce.
	if gapFinder != nil {
		localPendingNonceBig := t.GetPendingNonceUnlocked(wallet, chainID)
		if localPendingNonceBig != nil {
			localNonce := localPendingNonceBig.Uint64()
			if localNonce > remotePendingNonce {
				claimed := t.getClaimedGaps(wallet, chainID)
				// Clean up claimed entries below remotePendingNonce (already confirmed)
				for n := range claimed {
					if n < remotePendingNonce {
						delete(claimed, n)
					}
				}

				gapNonce, found := gapFinder(wallet, chainID, remotePendingNonce, localNonce, claimed)
				if found {
					t.claimGap(wallet, chainID, gapNonce)
					logger.WithFields(logger.Fields{
						"wallet":         wallet.Hex(),
						"network":        networkName,
						"chain_id":       chainID,
						"gap_nonce":      gapNonce,
						"remote_pending": remotePendingNonce,
						"local_pending":  localNonce,
					}).Debug("acquireNonce: filling nonce gap")
					return &AcquireResult{
						Nonce:          gapNonce,
						DecisionReason: "gap fill",
					}, nil
				}
			}
		}
	}

	localPendingNonceBig := t.GetPendingNonceUnlocked(wallet, chainID)

	var nextNonce uint64
	var decisionReason string

	if localPendingNonceBig == nil {
		// First transaction for this wallet/network in this session
		// Use the max of mined and remote pending nonce
		if remotePendingNonce > minedNonce {
			nextNonce = remotePendingNonce
			decisionReason = "first tx, using remote pending (higher than mined)"
		} else {
			nextNonce = minedNonce
			decisionReason = "first tx, using mined nonce"
		}
	} else {
		localPendingNonce := localPendingNonceBig.Uint64()

		hasPendingTxsOnNodes := minedNonce < remotePendingNonce
		if !hasPendingTxsOnNodes {
			if minedNonce > remotePendingNonce {
				logger.WithFields(logger.Fields{
					"wallet":         wallet.Hex(),
					"network":        networkName,
					"chain_id":       chainID,
					"mined_nonce":    minedNonce,
					"remote_pending": remotePendingNonce,
					"local_pending":  localPendingNonce,
				}).Debug("acquireNonce: abnormal state - mined > remote pending")
				return nil, ErrAbnormalNonceState
			}
			// minedNonce == remotePendingNonce (no pending txs on nodes)
			// Use max of local and mined nonce
			if localPendingNonce > minedNonce {
				nextNonce = localPendingNonce
				decisionReason = "no pending on nodes, using local (higher than mined)"
			} else {
				nextNonce = minedNonce
				decisionReason = "no pending on nodes, using mined (>= local)"
			}
		} else {
			// There are pending txs on nodes
			// Use max of local, remote pending nonce
			if localPendingNonce > remotePendingNonce {
				nextNonce = localPendingNonce
				decisionReason = "pending on nodes, using local (higher than remote)"
			} else {
				nextNonce = remotePendingNonce
				decisionReason = "pending on nodes, using remote (>= local)"
			}
		}
	}

	// Reserve this nonce by updating local pending nonce
	// This ensures the next call to AcquireNonce will get nextNonce+1
	t.SetPendingNonceUnlocked(wallet, chainID, networkName, nextNonce)

	var localNonceStr string
	if localPendingNonceBig != nil {
		localNonceStr = localPendingNonceBig.String()
	} else {
		localNonceStr = "nil"
	}

	logger.WithFields(logger.Fields{
		"wallet":         wallet.Hex(),
		"network":        networkName,
		"chain_id":       chainID,
		"acquired_nonce": nextNonce,
		"mined_nonce":    minedNonce,
		"remote_pending": remotePendingNonce,
		"local_pending":  localNonceStr,
		"decision":       decisionReason,
	}).Debug("acquireNonce: nonce acquired and reserved")

	return &AcquireResult{
		Nonce:          nextNonce,
		DecisionReason: decisionReason,
	}, nil
}

// ReleaseNonce releases a previously acquired nonce that was not used.
// This allows the nonce to be reused by subsequent transactions.
// Note: This only affects local tracking. If the transaction was already broadcast
// to some nodes, calling this may cause issues.
func (t *Tracker) ReleaseNonce(wallet common.Address, chainID uint64, networkName string, nonce uint64) {
	lock := t.getWalletLock(wallet)
	lock.Lock()
	defer lock.Unlock()

	noncesRaw, ok := t.pendingNonces.Load(wallet)
	if !ok {
		logger.WithFields(logger.Fields{
			"wallet":   wallet.Hex(),
			"network":  networkName,
			"chain_id": chainID,
			"nonce":    nonce,
		}).Debug("ReleaseNonce: no nonce map found for wallet, nothing to release")
		return
	}

	walletNonces := noncesRaw.(map[uint64]*big.Int)
	currentNonce := walletNonces[chainID]

	// Only release if this is the most recent nonce
	// (we can only release the "tip" of the nonce sequence)
	if currentNonce != nil && currentNonce.Uint64() == nonce {
		if nonce > 0 {
			walletNonces[chainID] = big.NewInt(int64(nonce - 1))
			logger.WithFields(logger.Fields{
				"wallet":         wallet.Hex(),
				"network":        networkName,
				"chain_id":       chainID,
				"released_nonce": nonce,
				"new_stored":     nonce - 1,
			}).Debug("ReleaseNonce: nonce released successfully")
		} else {
			delete(walletNonces, chainID)
			logger.WithFields(logger.Fields{
				"wallet":         wallet.Hex(),
				"network":        networkName,
				"chain_id":       chainID,
				"released_nonce": nonce,
			}).Debug("ReleaseNonce: nonce 0 released, removed network entry")
		}
	} else {
		var currentNonceStr string
		if currentNonce != nil {
			currentNonceStr = currentNonce.String()
		} else {
			currentNonceStr = "nil"
		}
		logger.WithFields(logger.Fields{
			"wallet":          wallet.Hex(),
			"network":         networkName,
			"chain_id":        chainID,
			"requested_nonce": nonce,
			"current_nonce":   currentNonceStr,
		}).Debug("ReleaseNonce: skipped - not the tip nonce")
	}
}
