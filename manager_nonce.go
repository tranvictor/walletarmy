package walletarmy

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/tranvictor/jarvis/networks"
	"github.com/tranvictor/walletarmy/internal/nonce"
)

// setPendingNonce sets the pending nonce for a wallet on a network
func (wm *WalletManager) setPendingNonce(wallet common.Address, network networks.Network, nonce uint64) {
	wm.nonceTracker.SetPendingNonce(wallet, network.GetChainID(), network.GetName(), nonce)
}

// pendingNonce returns the pending nonce for a wallet on a network
func (wm *WalletManager) pendingNonce(wallet common.Address, network networks.Network) *big.Int {
	return wm.nonceTracker.GetPendingNonce(wallet, network.GetChainID())
}

// acquireNonce atomically determines and reserves the next nonce for a transaction.
// This prevents race conditions where multiple concurrent transactions could get the same nonce.
// The nonce is reserved immediately, so even if the transaction fails, subsequent calls will get
// a different nonce. Use ReleaseNonce if you need to release an unused nonce.
//
// Logic:
//  1. Get remote pending nonce and mined nonce from the network
//  2. Compare with local pending nonce to determine the correct next nonce
//  3. If local > remote and a TxStore is configured, scan for nonce gaps
//     (nonces acquired via BuildTx but never broadcast) and fill the first gap
//  4. Atomically reserve the nonce by incrementing local pending nonce
//  5. Persist the nonce acquisition (if persistence is enabled)
//  6. Return the reserved nonce
func (wm *WalletManager) acquireNonce(wallet common.Address, network networks.Network) (*big.Int, error) {
	// Get remote nonces first (before acquiring lock to avoid holding lock during network calls)
	r, err := wm.Reader(network)
	if err != nil {
		return nil, fmt.Errorf("couldn't get reader: %w", err)
	}

	minedNonce, err := r.GetMinedNonce(wallet.Hex())
	if err != nil {
		return nil, fmt.Errorf("couldn't get mined nonce in context manager: %s", err)
	}

	remotePendingNonce, err := r.GetPendingNonce(wallet.Hex())
	if err != nil {
		return nil, fmt.Errorf("couldn't get remote pending nonce in context manager: %s", err)
	}

	// Build a gap finder callback if we have a TxStore.
	// This will be called under the tracker's wallet lock to prevent two
	// concurrent callers from both claiming the same gap nonce.
	var gapFinder nonce.GapFinder
	if wm.txStore != nil {
		gapFinder = wm.findNonceGap
	}

	// Delegate to the nonce tracker (gap detection runs inside the lock)
	result, err := wm.nonceTracker.AcquireNonce(
		wallet,
		network.GetChainID(),
		network.GetName(),
		minedNonce,
		remotePendingNonce,
		gapFinder,
	)
	if err != nil {
		return nil, err
	}

	// Persist nonce acquisition for crash recovery
	wm.persistNonceAcquisition(wallet, network.GetChainID(), result.Nonce)

	return big.NewInt(int64(result.Nonce)), nil
}

// findNonceGap scans the TxStore for the first nonce in [from, to) that has no
// associated transaction and is not already claimed. Returns the gap nonce and
// true if found, or 0 and false if all nonces in the range are accounted for.
//
// The claimed set contains gap nonces already reserved by concurrent callers
// within the same lock scope — these must be skipped.
func (wm *WalletManager) findNonceGap(wallet common.Address, chainID uint64, from, to uint64, claimed map[uint64]bool) (uint64, bool) {
	ctx := context.Background()
	for n := from; n < to; n++ {
		if claimed[n] {
			continue
		}
		txs, err := wm.txStore.GetByNonce(ctx, wallet, chainID, n)
		if err != nil {
			// If we can't check, skip this nonce — don't fill what we can't verify
			continue
		}
		if len(txs) == 0 {
			return n, true
		}
	}
	return 0, false
}

// ReleaseNonce releases a previously acquired nonce that was not used.
// This allows the nonce to be reused by subsequent transactions.
// Note: This only affects local tracking. If the transaction was already broadcast
// to some nodes, calling this may cause issues.
func (wm *WalletManager) ReleaseNonce(wallet common.Address, network networks.Network, nonce uint64) {
	wm.nonceTracker.ReleaseNonce(wallet, network.GetChainID(), network.GetName(), nonce)
	// Persist nonce release for crash recovery
	wm.persistNonceRelease(wallet, network.GetChainID(), nonce)
}

// nonce is deprecated: use acquireNonce instead for race-safe nonce acquisition.
// This function is kept for backward compatibility but has race conditions
// when called concurrently for the same wallet/network.
func (wm *WalletManager) nonce(wallet common.Address, network networks.Network) (*big.Int, error) {
	return wm.acquireNonce(wallet, network)
}

// persistNonceAcquisition persists a nonce acquisition to the nonce store
func (wm *WalletManager) persistNonceAcquisition(wallet common.Address, chainID uint64, nonce uint64) {
	if wm.nonceStore == nil {
		return
	}
	ctx := context.Background()
	_ = wm.nonceStore.AddReservedNonce(ctx, wallet, chainID, nonce)
	_ = wm.nonceStore.SavePendingNonce(ctx, wallet, chainID, nonce)
}

// nonceStatus describes the state of a nonce relative to the chain's mined nonce.
type nonceStatus int

const (
	// nonceStatusUnknown means we couldn't determine the chain state (RPC error).
	nonceStatusUnknown nonceStatus = iota
	// nonceStatusBlocking means the nonce equals the mined nonce — it must be confirmed
	// before any higher-nonce transactions can proceed.
	nonceStatusBlocking
	// nonceStatusPending means the nonce is higher than the mined nonce — there are
	// earlier nonces that must be confirmed first.
	nonceStatusPending
	// nonceStatusConsumed means the nonce is lower than the mined nonce — another
	// transaction with this nonce has already been mined. This transaction can never
	// be included.
	nonceStatusConsumed
)

// getNonceStatus determines the state of a nonce relative to the chain.
func (wm *WalletManager) getNonceStatus(nonce uint64, wallet common.Address, network networks.Network) nonceStatus {
	if network == nil {
		return nonceStatusUnknown
	}
	r, err := wm.Reader(network)
	if err != nil {
		return nonceStatusUnknown
	}

	minedNonce, err := r.GetMinedNonce(wallet.Hex())
	if err != nil {
		return nonceStatusUnknown
	}

	if nonce < minedNonce {
		return nonceStatusConsumed
	}
	if nonce == minedNonce {
		return nonceStatusBlocking
	}
	return nonceStatusPending
}


// persistNonceRelease persists a nonce release to the nonce store
func (wm *WalletManager) persistNonceRelease(wallet common.Address, chainID uint64, nonce uint64) {
	if wm.nonceStore == nil {
		return
	}
	ctx := context.Background()
	_ = wm.nonceStore.RemoveReservedNonce(ctx, wallet, chainID, nonce)
}
