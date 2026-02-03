package redis

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/goccy/go-json"
	"github.com/redis/go-redis/v9"

	"github.com/tranvictor/walletarmy"
)

// Key prefixes for nonce storage
const (
	nonceKeyPrefix         = "walletarmy:nonce:"                // nonce state by wallet:chainID
	nonceWalletByChainKey  = "walletarmy:nonce:wallet_by_chain" // index of wallet:chainID pairs for discovery
	nonceReservedSetPrefix = "walletarmy:nonce:reserved:"       // reserved nonces set by wallet:chainID
)

// NonceStore provides Redis-based persistence for nonce state tracking.
// It implements the walletarmy.NonceStore interface.
//
// Reserved nonces are stored in separate Redis Sets for atomic SADD/SREM operations.
// The main nonce state (LocalPendingNonce, UpdatedAt) is stored as JSON.
type NonceStore struct {
	client    redis.UniversalClient
	keyPrefix string
}

// NonceStoreOption configures a NonceStore.
type NonceStoreOption func(*NonceStore)

// WithNonceStoreKeyPrefix sets a custom prefix for all Redis keys.
func WithNonceStoreKeyPrefix(prefix string) NonceStoreOption {
	return func(s *NonceStore) {
		s.keyPrefix = prefix
	}
}

// NewNonceStore creates a new Redis-based nonce store.
func NewNonceStore(client redis.UniversalClient, opts ...NonceStoreOption) *NonceStore {
	s := &NonceStore{
		client: client,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// key returns the full Redis key with optional prefix.
func (s *NonceStore) key(parts ...string) string {
	key := strings.Join(parts, "")
	if s.keyPrefix != "" {
		return s.keyPrefix + ":" + key
	}
	return key
}

// nonceStateData is the JSON-serializable form of NonceState (without ReservedNonces).
// ReservedNonces are stored in a separate Redis Set for atomic operations.
type nonceStateData struct {
	Wallet            string  `json:"wallet"`
	ChainID           uint64  `json:"chain_id"`
	LocalPendingNonce *uint64 `json:"local_pending_nonce,omitempty"`
	UpdatedAt         int64   `json:"updated_at"` // Nanoseconds
}

// Get retrieves the nonce state for a wallet on a network.
// Uses WATCH/MULTI/EXEC to ensure consistent read of state and reserved nonces.
// If the watched keys change during the read, the operation retries with exponential
// backoff and jitter.
func (s *NonceStore) Get(ctx context.Context, wallet common.Address, chainID uint64) (*walletarmy.NonceState, error) {
	stateKey := s.nonceStateKey(wallet, chainID)
	reservedKey := s.reservedNoncesKey(wallet, chainID)

	const maxRetries = 10
	var lastErr error

	for i := range maxRetries {
		// Exponential backoff with jitter on retries
		if i > 0 {
			backoff := time.Duration(1<<uint(i-1)) * time.Millisecond
			jitter := time.Duration(rand.Int63n(int64(backoff/2 + 1)))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff + jitter):
			}
		}

		var result *walletarmy.NonceState

		err := s.client.Watch(ctx, func(rtx *redis.Tx) error {
			// Execute reads within MULTI/EXEC to detect concurrent modifications.
			// If stateKey or reservedKey change between WATCH and EXEC, TxFailedErr is returned.
			cmds, err := rtx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Get(ctx, stateKey)
				pipe.SMembers(ctx, reservedKey)
				return nil
			})
			// TxPipelined returns redis.Nil if the first command returns Nil,
			// but we need to check individual command results
			if err != nil && err != redis.Nil {
				return err
			}

			// Extract results from commands
			stateCmd := cmds[0].(*redis.StringCmd)
			reservedCmd := cmds[1].(*redis.StringSliceCmd)

			// Check state
			data, err := stateCmd.Bytes()
			if err == redis.Nil {
				// No state exists, but check if there are reserved nonces
				reservedStrs, err := reservedCmd.Result()
				if err != nil && err != redis.Nil {
					return fmt.Errorf("failed to get reserved nonces: %w", err)
				}
				if len(reservedStrs) == 0 {
					result = nil // Nothing exists
					return nil
				}
				// Reserved nonces exist but no main state - reconstruct
				reservedNonces, err := s.parseReservedNonces(reservedStrs)
				if err != nil {
					return err
				}
				result = &walletarmy.NonceState{
					Wallet:         wallet,
					ChainID:        chainID,
					ReservedNonces: reservedNonces,
				}
				return nil
			}
			if err != nil {
				return fmt.Errorf("failed to get nonce state: %w", err)
			}

			state, err := s.deserializeNonceState(data)
			if err != nil {
				return err
			}

			// Get reserved nonces
			reservedStrs, err := reservedCmd.Result()
			if err != nil && err != redis.Nil {
				return fmt.Errorf("failed to get reserved nonces: %w", err)
			}

			if len(reservedStrs) > 0 {
				state.ReservedNonces, err = s.parseReservedNonces(reservedStrs)
				if err != nil {
					return err
				}
			}

			result = state
			return nil
		}, stateKey, reservedKey)

		if err == nil {
			return result, nil
		}
		if err == redis.TxFailedErr {
			// Data changed during read, retry
			lastErr = err
			continue
		}
		return nil, err
	}

	return nil, fmt.Errorf("failed to get nonce state after %d retries: %w", maxRetries, lastErr)
}

// Save persists the nonce state.
// If state.ReservedNonces is non-nil, it will SYNC the reserved nonces set
// by computing the diff and applying incremental changes (SREM/SADD).
// Uses WATCH/MULTI/EXEC for consistency.
// For incremental updates, prefer AddReservedNonce/RemoveReservedNonce instead.
func (s *NonceStore) Save(ctx context.Context, state *walletarmy.NonceState) error {
	if state == nil {
		return fmt.Errorf("nonce state cannot be nil")
	}

	stateKey := s.nonceStateKey(state.Wallet, state.ChainID)
	reservedKey := s.reservedNoncesKey(state.Wallet, state.ChainID)
	indexKey := fmt.Sprintf("%s:%d", state.Wallet.Hex(), state.ChainID)

	// If no reserved nonces to sync, use simple pipeline
	if state.ReservedNonces == nil {
		data, err := s.serializeNonceState(state)
		if err != nil {
			return fmt.Errorf("failed to serialize nonce state: %w", err)
		}

		pipe := s.client.TxPipeline()
		pipe.Set(ctx, stateKey, data, 0)
		pipe.SAdd(ctx, s.key(nonceWalletByChainKey), indexKey)
		_, err = pipe.Exec(ctx)
		return err
	}

	// Need to sync reserved nonces - use WATCH for consistency
	const maxRetries = 10
	var lastErr error

	for i := range maxRetries {
		// Exponential backoff with jitter on retries
		if i > 0 {
			backoff := time.Duration(1<<uint(i-1)) * time.Millisecond
			jitter := time.Duration(rand.Int63n(int64(backoff/2 + 1)))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff + jitter):
			}
		}

		err := s.client.Watch(ctx, func(rtx *redis.Tx) error {
			// Get current reserved nonces
			currentReserved, err := rtx.SMembers(ctx, reservedKey).Result()
			if err != nil && err != redis.Nil {
				return fmt.Errorf("failed to get current reserved nonces: %w", err)
			}

			// Build sets for comparison
			currentSet := make(map[string]struct{}, len(currentReserved))
			for _, n := range currentReserved {
				currentSet[n] = struct{}{}
			}

			newSet := make(map[string]struct{}, len(state.ReservedNonces))
			for _, n := range state.ReservedNonces {
				newSet[strconv.FormatUint(n, 10)] = struct{}{}
			}

			// Compute diff
			var toRemove, toAdd []interface{}
			for n := range currentSet {
				if _, exists := newSet[n]; !exists {
					toRemove = append(toRemove, n)
				}
			}
			for n := range newSet {
				if _, exists := currentSet[n]; !exists {
					toAdd = append(toAdd, n)
				}
			}

			data, err := s.serializeNonceState(state)
			if err != nil {
				return fmt.Errorf("failed to serialize nonce state: %w", err)
			}

			// Execute atomically
			_, err = rtx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, stateKey, data, 0)
				pipe.SAdd(ctx, s.key(nonceWalletByChainKey), indexKey)

				if len(toRemove) > 0 {
					pipe.SRem(ctx, reservedKey, toRemove...)
				}
				if len(toAdd) > 0 {
					pipe.SAdd(ctx, reservedKey, toAdd...)
				}
				return nil
			})
			return err
		}, stateKey, reservedKey)

		if err == nil {
			return nil
		}
		if err == redis.TxFailedErr {
			lastErr = err
			continue
		}
		return err
	}

	return fmt.Errorf("failed to save nonce state after %d retries: %w", maxRetries, lastErr)
}

// SavePendingNonce is a convenience method to update just the pending nonce.
// Uses WATCH/MULTI/EXEC for optimistic locking with exponential backoff.
func (s *NonceStore) SavePendingNonce(ctx context.Context, wallet common.Address, chainID uint64, nonce uint64) error {
	stateKey := s.nonceStateKey(wallet, chainID)
	indexKey := fmt.Sprintf("%s:%d", wallet.Hex(), chainID)

	const maxRetries = 10
	var lastErr error

	for i := range maxRetries {
		// Exponential backoff with jitter on retries
		if i > 0 {
			backoff := time.Duration(1<<uint(i-1)) * time.Millisecond
			jitter := time.Duration(rand.Int63n(int64(backoff/2 + 1)))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff + jitter):
			}
		}
		err := s.client.Watch(ctx, func(rtx *redis.Tx) error {
			// Get current state within the watch
			data, err := rtx.Get(ctx, stateKey).Bytes()

			var state *walletarmy.NonceState
			if err == redis.Nil {
				// Create new state
				state = &walletarmy.NonceState{
					Wallet:  wallet,
					ChainID: chainID,
				}
			} else if err != nil {
				return fmt.Errorf("failed to get nonce state: %w", err)
			} else {
				state, err = s.deserializeNonceState(data)
				if err != nil {
					return err
				}
			}

			// Update pending nonce
			state.LocalPendingNonce = &nonce
			state.UpdatedAt = time.Now()

			// Serialize updated state
			newData, err := s.serializeNonceState(state)
			if err != nil {
				return fmt.Errorf("failed to serialize nonce state: %w", err)
			}

			// Execute transaction atomically
			_, err = rtx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, stateKey, newData, 0)
				pipe.SAdd(ctx, s.key(nonceWalletByChainKey), indexKey)
				return nil
			})
			return err
		}, stateKey)

		if err == nil {
			return nil
		}
		if err == redis.TxFailedErr {
			// Optimistic lock failed, retry
			lastErr = err
			continue
		}
		return err
	}

	return fmt.Errorf("failed to save pending nonce after %d retries: %w", maxRetries, lastErr)
}

// AddReservedNonce adds a nonce to the reserved set.
// This is an atomic operation using Redis SADD.
func (s *NonceStore) AddReservedNonce(ctx context.Context, wallet common.Address, chainID uint64, nonce uint64) error {
	reservedKey := s.reservedNoncesKey(wallet, chainID)
	indexKey := fmt.Sprintf("%s:%d", wallet.Hex(), chainID)

	// Use pipeline for atomicity
	pipe := s.client.TxPipeline()
	pipe.SAdd(ctx, reservedKey, strconv.FormatUint(nonce, 10))
	pipe.SAdd(ctx, s.key(nonceWalletByChainKey), indexKey)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to add reserved nonce: %w", err)
	}

	return nil
}

// RemoveReservedNonce removes a nonce from the reserved set.
// This is an atomic operation using Redis SREM.
func (s *NonceStore) RemoveReservedNonce(ctx context.Context, wallet common.Address, chainID uint64, nonce uint64) error {
	reservedKey := s.reservedNoncesKey(wallet, chainID)

	err := s.client.SRem(ctx, reservedKey, strconv.FormatUint(nonce, 10)).Err()
	if err != nil {
		return fmt.Errorf("failed to remove reserved nonce: %w", err)
	}

	return nil
}

// ListAll returns all stored nonce states.
// Uses a single pipeline for efficient batch retrieval (2 round trips total).
//
// Note: This method does NOT provide atomic/transactional consistency across all
// returned states. Each individual state is read independently, so if data changes
// during the batch read, the returned states may represent different points in time.
// This is acceptable for recovery/initialization purposes but callers should be
// aware of this limitation.
func (s *NonceStore) ListAll(ctx context.Context) ([]*walletarmy.NonceState, error) {
	// Get all wallet:chainID pairs
	pairs, err := s.client.SMembers(ctx, s.key(nonceWalletByChainKey)).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list nonce states: %w", err)
	}

	if len(pairs) == 0 {
		return nil, nil
	}

	// Parse pairs and build keys for batch retrieval
	type pairInfo struct {
		wallet  common.Address
		chainID uint64
	}
	validPairs := make([]pairInfo, 0, len(pairs))
	var parseErrors []string

	for _, pair := range pairs {
		parts := strings.Split(pair, ":")
		if len(parts) != 2 {
			parseErrors = append(parseErrors, fmt.Sprintf("invalid pair format: %s", pair))
			continue
		}

		wallet := common.HexToAddress(parts[0])
		chainID, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("invalid chainID in %s: %v", pair, err))
			continue
		}

		validPairs = append(validPairs, pairInfo{wallet: wallet, chainID: chainID})
	}

	if len(validPairs) == 0 {
		if len(parseErrors) > 0 {
			return nil, fmt.Errorf("all pairs invalid: %s", strings.Join(parseErrors, "; "))
		}
		return nil, nil
	}

	// Single pipeline to get both state data and reserved nonces
	pipe := s.client.Pipeline()
	stateCmds := make([]*redis.StringCmd, len(validPairs))
	reservedCmds := make([]*redis.StringSliceCmd, len(validPairs))

	for i, p := range validPairs {
		stateCmds[i] = pipe.Get(ctx, s.nonceStateKey(p.wallet, p.chainID))
		reservedCmds[i] = pipe.SMembers(ctx, s.reservedNoncesKey(p.wallet, p.chainID))
	}

	_, _ = pipe.Exec(ctx) // Ignore pipeline error, check individual commands

	// Process results
	states := make([]*walletarmy.NonceState, 0, len(validPairs))
	var deserializeErrors []string

	for i, p := range validPairs {
		var state *walletarmy.NonceState

		// Get state data
		data, err := stateCmds[i].Bytes()
		if err == redis.Nil {
			// No main state, but might have reserved nonces
			state = &walletarmy.NonceState{
				Wallet:  p.wallet,
				ChainID: p.chainID,
			}
		} else if err != nil {
			deserializeErrors = append(deserializeErrors,
				fmt.Sprintf("%s:%d state: %v", p.wallet.Hex(), p.chainID, err))
			continue
		} else {
			state, err = s.deserializeNonceState(data)
			if err != nil {
				deserializeErrors = append(deserializeErrors,
					fmt.Sprintf("%s:%d: %v", p.wallet.Hex(), p.chainID, err))
				continue
			}
		}

		// Get reserved nonces
		reservedStrs, err := reservedCmds[i].Result()
		if err != nil && err != redis.Nil {
			deserializeErrors = append(deserializeErrors,
				fmt.Sprintf("%s:%d reserved nonces: %v", p.wallet.Hex(), p.chainID, err))
		} else if len(reservedStrs) > 0 {
			reserved, err := s.parseReservedNonces(reservedStrs)
			if err != nil {
				deserializeErrors = append(deserializeErrors,
					fmt.Sprintf("%s:%d reserved nonces parse: %v", p.wallet.Hex(), p.chainID, err))
			} else {
				state.ReservedNonces = reserved
			}
		}

		// Only add state if it has meaningful data
		if state.LocalPendingNonce != nil || len(state.ReservedNonces) > 0 {
			states = append(states, state)
		}
	}

	// Return partial results with error if there were failures
	if len(deserializeErrors) > 0 || len(parseErrors) > 0 {
		allErrors := append(parseErrors, deserializeErrors...)
		return states, fmt.Errorf("encountered %d errors during ListAll: %s",
			len(allErrors), strings.Join(allErrors, "; "))
	}

	return states, nil
}

// Cleanup removes orphaned index entries where no actual state exists.
// This should be called periodically to clean up stale entries from nonceWalletByChainKey.
// Uses batched operations for better performance.
// Returns the number of orphaned entries removed.
func (s *NonceStore) Cleanup(ctx context.Context) (int, error) {
	// Get all wallet:chainID pairs from the index
	pairs, err := s.client.SMembers(ctx, s.key(nonceWalletByChainKey)).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to list nonce index: %w", err)
	}

	if len(pairs) == 0 {
		return 0, nil
	}

	// First pass: parse pairs and build keys for batch existence check
	type pairInfo struct {
		pair     string
		wallet   common.Address
		chainID  uint64
		stateKey string
		reserved string
	}

	validPairs := make([]pairInfo, 0, len(pairs))
	invalidPairs := make([]string, 0)

	for _, pair := range pairs {
		parts := strings.Split(pair, ":")
		if len(parts) != 2 {
			invalidPairs = append(invalidPairs, pair)
			continue
		}

		wallet := common.HexToAddress(parts[0])
		chainID, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			invalidPairs = append(invalidPairs, pair)
			continue
		}

		validPairs = append(validPairs, pairInfo{
			pair:     pair,
			wallet:   wallet,
			chainID:  chainID,
			stateKey: s.nonceStateKey(wallet, chainID),
			reserved: s.reservedNoncesKey(wallet, chainID),
		})
	}

	// Batch check existence of all state keys and reserved sets
	pipe := s.client.Pipeline()
	existsCmds := make([]*redis.IntCmd, len(validPairs))
	scardCmds := make([]*redis.IntCmd, len(validPairs))

	for i, p := range validPairs {
		existsCmds[i] = pipe.Exists(ctx, p.stateKey)
		scardCmds[i] = pipe.SCard(ctx, p.reserved)
	}

	_, _ = pipe.Exec(ctx)

	// Find orphans
	orphanPairs := make([]interface{}, 0)
	for i, p := range validPairs {
		stateExists, _ := existsCmds[i].Result()
		reservedCount, _ := scardCmds[i].Result()

		if stateExists == 0 && reservedCount == 0 {
			orphanPairs = append(orphanPairs, p.pair)
		}
	}

	// Add invalid pairs to removal list
	for _, p := range invalidPairs {
		orphanPairs = append(orphanPairs, p)
	}

	if len(orphanPairs) == 0 {
		return 0, nil
	}

	// Batch remove all orphans
	removed, err := s.client.SRem(ctx, s.key(nonceWalletByChainKey), orphanPairs...).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to remove orphan entries: %w", err)
	}

	return int(removed), nil
}

// Helper methods

func (s *NonceStore) nonceStateKey(wallet common.Address, chainID uint64) string {
	return s.key(nonceKeyPrefix, wallet.Hex(), ":", strconv.FormatUint(chainID, 10))
}

func (s *NonceStore) reservedNoncesKey(wallet common.Address, chainID uint64) string {
	return s.key(nonceReservedSetPrefix, wallet.Hex(), ":", strconv.FormatUint(chainID, 10))
}

func (s *NonceStore) parseReservedNonces(strs []string) ([]uint64, error) {
	nonces := make([]uint64, 0, len(strs))
	for _, str := range strs {
		n, err := strconv.ParseUint(str, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse reserved nonce %q: %w", str, err)
		}
		nonces = append(nonces, n)
	}
	return nonces, nil
}

func (s *NonceStore) serializeNonceState(state *walletarmy.NonceState) ([]byte, error) {
	data := nonceStateData{
		Wallet:            state.Wallet.Hex(),
		ChainID:           state.ChainID,
		LocalPendingNonce: state.LocalPendingNonce,
		UpdatedAt:         state.UpdatedAt.UnixNano(),
	}
	return json.Marshal(data)
}

func (s *NonceStore) deserializeNonceState(data []byte) (*walletarmy.NonceState, error) {
	var d nonceStateData
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("failed to unmarshal nonce state: %w", err)
	}

	return &walletarmy.NonceState{
		Wallet:            common.HexToAddress(d.Wallet),
		ChainID:           d.ChainID,
		LocalPendingNonce: d.LocalPendingNonce,
		UpdatedAt:         time.Unix(0, d.UpdatedAt),
	}, nil
}

// Verify NonceStore implements walletarmy.NonceStore
var _ walletarmy.NonceStore = (*NonceStore)(nil)
