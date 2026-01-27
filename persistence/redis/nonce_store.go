package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/redis/go-redis/v9"
	"github.com/tranvictor/walletarmy"
)

// Key prefixes for nonce storage
const (
	nonceKeyPrefix         = "walletarmy:nonce:"          // nonce state by wallet:chainID
	nonceAllSetKey         = "walletarmy:nonce:all"       // set of all wallet:chainID pairs
	nonceReservedSetPrefix = "walletarmy:nonce:reserved:" // reserved nonces set by wallet:chainID
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
	UpdatedAt         int64   `json:"updated_at"`
}

// Get retrieves the nonce state for a wallet on a network.
func (s *NonceStore) Get(ctx context.Context, wallet common.Address, chainID uint64) (*walletarmy.NonceState, error) {
	stateKey := s.nonceStateKey(wallet, chainID)
	reservedKey := s.reservedNoncesKey(wallet, chainID)

	// Get both the state and reserved nonces in parallel
	pipe := s.client.Pipeline()
	stateCmd := pipe.Get(ctx, stateKey)
	reservedCmd := pipe.SMembers(ctx, reservedKey)

	_, err := pipe.Exec(ctx)
	// Ignore pipeline error - check individual commands

	// Check state
	data, err := stateCmd.Bytes()
	if err == redis.Nil {
		// No state exists, but check if there are reserved nonces
		reservedStrs, err := reservedCmd.Result()
		if err != nil && err != redis.Nil {
			return nil, fmt.Errorf("failed to get reserved nonces: %w", err)
		}
		if len(reservedStrs) == 0 {
			return nil, nil // Nothing exists
		}
		// Reserved nonces exist but no main state - reconstruct
		reservedNonces, err := s.parseReservedNonces(reservedStrs)
		if err != nil {
			return nil, err
		}
		return &walletarmy.NonceState{
			Wallet:         wallet,
			ChainID:        chainID,
			ReservedNonces: reservedNonces,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce state: %w", err)
	}

	state, err := s.deserializeNonceState(data)
	if err != nil {
		return nil, err
	}

	// Get reserved nonces
	reservedStrs, err := reservedCmd.Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("failed to get reserved nonces: %w", err)
	}

	if len(reservedStrs) > 0 {
		state.ReservedNonces, err = s.parseReservedNonces(reservedStrs)
		if err != nil {
			return nil, err
		}
	}

	return state, nil
}

// Save persists the nonce state.
// Note: This does NOT save ReservedNonces. Use AddReservedNonce/RemoveReservedNonce for that.
func (s *NonceStore) Save(ctx context.Context, state *walletarmy.NonceState) error {
	if state == nil {
		return fmt.Errorf("nonce state cannot be nil")
	}

	data, err := s.serializeNonceState(state)
	if err != nil {
		return fmt.Errorf("failed to serialize nonce state: %w", err)
	}

	stateKey := s.nonceStateKey(state.Wallet, state.ChainID)
	indexKey := fmt.Sprintf("%s:%d", state.Wallet.Hex(), state.ChainID)

	pipe := s.client.TxPipeline()
	pipe.Set(ctx, stateKey, data, 0)
	pipe.SAdd(ctx, s.key(nonceAllSetKey), indexKey)

	// If ReservedNonces is provided in the state, sync the set
	// This handles the case where someone calls Save with a full state
	if state.ReservedNonces != nil {
		reservedKey := s.reservedNoncesKey(state.Wallet, state.ChainID)
		// Delete existing and add new
		pipe.Del(ctx, reservedKey)
		if len(state.ReservedNonces) > 0 {
			members := make([]interface{}, len(state.ReservedNonces))
			for i, n := range state.ReservedNonces {
				members[i] = strconv.FormatUint(n, 10)
			}
			pipe.SAdd(ctx, reservedKey, members...)
		}
	}

	_, err = pipe.Exec(ctx)
	return err
}

// SavePendingNonce is a convenience method to update just the pending nonce.
// Uses WATCH/MULTI/EXEC for optimistic locking.
func (s *NonceStore) SavePendingNonce(ctx context.Context, wallet common.Address, chainID uint64, nonce uint64) error {
	stateKey := s.nonceStateKey(wallet, chainID)
	indexKey := fmt.Sprintf("%s:%d", wallet.Hex(), chainID)

	const maxRetries = 3
	var lastErr error

	for i := 0; i < maxRetries; i++ {
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
				pipe.SAdd(ctx, s.key(nonceAllSetKey), indexKey)
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
	pipe.SAdd(ctx, s.key(nonceAllSetKey), indexKey)

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
// Uses MGET for efficient batch retrieval.
func (s *NonceStore) ListAll(ctx context.Context) ([]*walletarmy.NonceState, error) {
	// Get all wallet:chainID pairs
	pairs, err := s.client.SMembers(ctx, s.key(nonceAllSetKey)).Result()
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
	stateKeys := make([]string, 0, len(pairs))
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
		stateKeys = append(stateKeys, s.nonceStateKey(wallet, chainID))
	}

	if len(stateKeys) == 0 {
		if len(parseErrors) > 0 {
			return nil, fmt.Errorf("all pairs invalid: %s", strings.Join(parseErrors, "; "))
		}
		return nil, nil
	}

	// Batch get all state data
	results, err := s.client.MGet(ctx, stateKeys...).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to batch get nonce states: %w", err)
	}

	// Build reserved nonce keys for batch retrieval
	reservedKeys := make([]string, len(validPairs))
	for i, p := range validPairs {
		reservedKeys[i] = s.reservedNoncesKey(p.wallet, p.chainID)
	}

	// Get all reserved nonces using pipeline
	pipe := s.client.Pipeline()
	reservedCmds := make([]*redis.StringSliceCmd, len(reservedKeys))
	for i, key := range reservedKeys {
		reservedCmds[i] = pipe.SMembers(ctx, key)
	}
	_, _ = pipe.Exec(ctx) // Ignore pipeline error, check individual commands

	// Process results
	states := make([]*walletarmy.NonceState, 0, len(results))
	var deserializeErrors []string

	for i, result := range results {
		p := validPairs[i]

		var state *walletarmy.NonceState

		if result == nil {
			// No main state, but might have reserved nonces
			state = &walletarmy.NonceState{
				Wallet:  p.wallet,
				ChainID: p.chainID,
			}
		} else {
			data, ok := result.(string)
			if !ok {
				deserializeErrors = append(deserializeErrors,
					fmt.Sprintf("%s:%d: unexpected type %T", p.wallet.Hex(), p.chainID, result))
				continue
			}

			var err error
			state, err = s.deserializeNonceState([]byte(data))
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
		UpdatedAt:         state.UpdatedAt.Unix(),
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
		UpdatedAt:         time.Unix(d.UpdatedAt, 0),
	}, nil
}

// Verify NonceStore implements walletarmy.NonceStore
var _ walletarmy.NonceStore = (*NonceStore)(nil)
