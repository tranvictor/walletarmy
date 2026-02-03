package redis

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/goccy/go-json"
	"github.com/redis/go-redis/v9"
	"github.com/tranvictor/walletarmy"
)

// Key prefixes for transaction storage
const (
	txKeyPrefix          = "walletarmy:tx:"          // tx data by hash
	txPendingSetKey      = "walletarmy:tx:pending"   // set of all pending tx hashes
	txWalletPendingKey   = "walletarmy:tx:wallet:"   // pending txs by wallet:chainID
	txNonceKey           = "walletarmy:tx:nonce:"    // txs by wallet:chainID:nonce
	txTimestampSortedSet = "walletarmy:tx:timestamp" // sorted set by timestamp (created_at initially, updated to updated_at when skipped during cleanup)
)

// statusPriority defines the priority order for transaction statuses.
// Higher values indicate more "final" states that should not be overwritten.
var statusPriority = map[walletarmy.PendingTxStatus]int{
	walletarmy.PendingTxStatusPending:     1,
	walletarmy.PendingTxStatusBroadcasted: 2,
	walletarmy.PendingTxStatusDropped:     3,
	walletarmy.PendingTxStatusReplaced:    4,
	walletarmy.PendingTxStatusReverted:    5,
	walletarmy.PendingTxStatusMined:       5,
}

// TxStore provides Redis-based persistence for transaction tracking.
// It implements the walletarmy.TxStore interface.
//
// Note: Transaction records do not automatically expire. Use DeleteOlderThan
// for periodic cleanup of old records.
type TxStore struct {
	client    redis.UniversalClient
	keyPrefix string
}

// TxStoreOption configures a TxStore.
type TxStoreOption func(*TxStore)

// WithTxStoreKeyPrefix sets a custom prefix for all Redis keys.
func WithTxStoreKeyPrefix(prefix string) TxStoreOption {
	return func(s *TxStore) {
		s.keyPrefix = prefix
	}
}

// NewTxStore creates a new Redis-based transaction store.
func NewTxStore(client redis.UniversalClient, opts ...TxStoreOption) *TxStore {
	s := &TxStore{
		client: client,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// key returns the full Redis key with optional prefix.
func (s *TxStore) key(parts ...string) string {
	key := strings.Join(parts, "")
	if s.keyPrefix != "" {
		return s.keyPrefix + ":" + key
	}
	return key
}

// pendingTxData is the JSON-serializable form of PendingTx
type pendingTxData struct {
	Hash        string            `json:"hash"`
	Wallet      string            `json:"wallet"`
	ChainID     uint64            `json:"chain_id"`
	Nonce       uint64            `json:"nonce"`
	Status      string            `json:"status"`
	TxRLP       []byte            `json:"tx_rlp,omitempty"`
	ReceiptJSON []byte            `json:"receipt_json,omitempty"`
	CreatedAt   int64             `json:"created_at"` // Nanoseconds
	UpdatedAt   int64             `json:"updated_at"` // Nanoseconds
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Save persists a pending transaction to Redis.
// Uses WATCH/MULTI/EXEC for optimistic locking to prevent race conditions
// with concurrent UpdateStatus calls.
func (s *TxStore) Save(ctx context.Context, tx *walletarmy.PendingTx) error {
	if tx == nil {
		return fmt.Errorf("transaction cannot be nil")
	}

	hashKey := s.key(txKeyPrefix, tx.Hash.Hex())
	walletKey := s.walletPendingKey(tx.Wallet, tx.ChainID)
	nonceKey := s.nonceIndexKey(tx.Wallet, tx.ChainID, tx.Nonce)
	hashHex := tx.Hash.Hex()

	const maxRetries = 10
	var lastErr error

	for i := 0; i < maxRetries; i++ {
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
			// Check if tx already exists and has a "final" status
			existingData, err := rtx.Get(ctx, hashKey).Bytes()
			if err != nil && err != redis.Nil {
				return fmt.Errorf("failed to get existing transaction: %w", err)
			}

			// If tx exists, check if we should allow overwrite
			if err != redis.Nil {
				existingTx, parseErr := s.deserializePendingTx(existingData)
				if parseErr == nil {
					// Don't overwrite if existing tx has a more "final" status
					// Status priority: mined/reverted > replaced > dropped > broadcasted > pending
					if isMoreFinalStatus(existingTx.Status, tx.Status) {
						// Existing status is more final, skip update
						return nil
					}
				}
			}

			data, err := s.serializePendingTx(tx)
			if err != nil {
				return fmt.Errorf("failed to serialize transaction: %w", err)
			}

			// Execute atomically
			_, err = rtx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				// Store tx data
				pipe.Set(ctx, hashKey, data, 0)

				// Add to pending set if status is pending/broadcasted
				if tx.Status == walletarmy.PendingTxStatusPending || tx.Status == walletarmy.PendingTxStatusBroadcasted {
					pipe.SAdd(ctx, s.key(txPendingSetKey), hashHex)
					pipe.SAdd(ctx, walletKey, hashHex)
				} else {
					// Remove from pending sets if status changed
					pipe.SRem(ctx, s.key(txPendingSetKey), hashHex)
					pipe.SRem(ctx, walletKey, hashHex)
				}

				// Add to nonce index
				pipe.SAdd(ctx, nonceKey, hashHex)

				// Add to sorted set for time-based cleanup
				pipe.ZAdd(ctx, s.key(txTimestampSortedSet), redis.Z{
					Score:  float64(tx.CreatedAt.Unix()),
					Member: hashHex,
				})

				return nil
			})
			return err
		}, hashKey)

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

	return fmt.Errorf("failed to save transaction after %d retries: %w", maxRetries, lastErr)
}

// isMoreFinalStatus returns true if existingStatus is more "final" than newStatus.
// Status priority (most final first): mined, reverted > replaced > dropped > broadcasted > pending
func isMoreFinalStatus(existingStatus, newStatus walletarmy.PendingTxStatus) bool {
	return statusPriority[existingStatus] > statusPriority[newStatus]
}

// Get retrieves a pending transaction by hash.
func (s *TxStore) Get(ctx context.Context, hash common.Hash) (*walletarmy.PendingTx, error) {
	hashKey := s.key(txKeyPrefix, hash.Hex())

	data, err := s.client.Get(ctx, hashKey).Bytes()
	if err == redis.Nil {
		return nil, nil // Not found is not an error
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction: %w", err)
	}

	return s.deserializePendingTx(data)
}

// GetByNonce retrieves pending transactions by wallet, chainID, and nonce.
func (s *TxStore) GetByNonce(ctx context.Context, wallet common.Address, chainID uint64, nonce uint64) ([]*walletarmy.PendingTx, error) {
	nonceKey := s.nonceIndexKey(wallet, chainID, nonce)

	hashes, err := s.client.SMembers(ctx, nonceKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get tx hashes by nonce: %w", err)
	}

	return s.getTransactionsByHashes(ctx, hashes)
}

// ListPending returns all transactions with pending/broadcasted status for a wallet on a network.
func (s *TxStore) ListPending(ctx context.Context, wallet common.Address, chainID uint64) ([]*walletarmy.PendingTx, error) {
	walletKey := s.walletPendingKey(wallet, chainID)

	hashes, err := s.client.SMembers(ctx, walletKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get pending tx hashes: %w", err)
	}

	return s.getTransactionsByHashes(ctx, hashes)
}

// ListAllPending returns all pending/broadcasted transactions across all wallets/networks.
func (s *TxStore) ListAllPending(ctx context.Context) ([]*walletarmy.PendingTx, error) {
	hashes, err := s.client.SMembers(ctx, s.key(txPendingSetKey)).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get all pending tx hashes: %w", err)
	}

	return s.getTransactionsByHashes(ctx, hashes)
}

// UpdateStatus updates the status of a transaction and optionally sets the receipt.
// Uses WATCH/MULTI/EXEC for optimistic locking with exponential backoff.
func (s *TxStore) UpdateStatus(ctx context.Context, hash common.Hash, status walletarmy.PendingTxStatus, receipt *types.Receipt) error {
	hashKey := s.key(txKeyPrefix, hash.Hex())
	hashHex := hash.Hex()

	const maxRetries = 10
	var lastErr error

	for i := 0; i < maxRetries; i++ {
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
			// Get current value within the watch
			data, err := rtx.Get(ctx, hashKey).Bytes()
			if err == redis.Nil {
				return nil // Transaction not found, nothing to update
			}
			if err != nil {
				return fmt.Errorf("failed to get transaction: %w", err)
			}

			tx, err := s.deserializePendingTx(data)
			if err != nil {
				return fmt.Errorf("failed to deserialize transaction: %w", err)
			}

			// Don't downgrade to a less final status
			if isMoreFinalStatus(tx.Status, status) {
				return nil
			}

			// Update fields
			tx.Status = status
			tx.Receipt = receipt
			tx.UpdatedAt = time.Now()

			// Serialize updated transaction
			newData, err := s.serializePendingTx(tx)
			if err != nil {
				return fmt.Errorf("failed to serialize transaction: %w", err)
			}

			// Execute transaction atomically
			_, err = rtx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				// Store updated tx data
				pipe.Set(ctx, hashKey, newData, 0)

				// Update pending sets based on new status
				walletKey := s.walletPendingKey(tx.Wallet, tx.ChainID)
				if status == walletarmy.PendingTxStatusPending || status == walletarmy.PendingTxStatusBroadcasted {
					pipe.SAdd(ctx, s.key(txPendingSetKey), hashHex)
					pipe.SAdd(ctx, walletKey, hashHex)
				} else {
					pipe.SRem(ctx, s.key(txPendingSetKey), hashHex)
					pipe.SRem(ctx, walletKey, hashHex)
				}

				return nil
			})
			return err
		}, hashKey)

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

	return fmt.Errorf("failed to update transaction status after %d retries: %w", maxRetries, lastErr)
}

// Delete removes a transaction record.
// Uses WATCH/MULTI/EXEC for atomic read-then-delete to prevent race conditions.
func (s *TxStore) Delete(ctx context.Context, hash common.Hash) error {
	hashKey := s.key(txKeyPrefix, hash.Hex())
	hashHex := hash.Hex()

	const maxRetries = 10
	var lastErr error

	for i := 0; i < maxRetries; i++ {
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
			// Get tx within the watch to know which indexes to clean up
			data, err := rtx.Get(ctx, hashKey).Bytes()
			if err == redis.Nil {
				return nil // Already deleted, nothing to do
			}
			if err != nil {
				return fmt.Errorf("failed to get transaction: %w", err)
			}

			tx, err := s.deserializePendingTx(data)
			if err != nil {
				return fmt.Errorf("failed to deserialize transaction: %w", err)
			}

			walletKey := s.walletPendingKey(tx.Wallet, tx.ChainID)
			nonceKey := s.nonceIndexKey(tx.Wallet, tx.ChainID, tx.Nonce)

			// Execute delete atomically
			_, err = rtx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Del(ctx, hashKey)
				pipe.SRem(ctx, s.key(txPendingSetKey), hashHex)
				pipe.SRem(ctx, walletKey, hashHex)
				pipe.SRem(ctx, nonceKey, hashHex)
				pipe.ZRem(ctx, s.key(txTimestampSortedSet), hashHex)
				return nil
			})
			return err
		}, hashKey)

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

	return fmt.Errorf("failed to delete transaction after %d retries: %w", maxRetries, lastErr)
}

// DeleteOlderThan removes transactions older than the given duration.
// Uses batched operations for better performance with configurable batch size.
// Transactions that have been updated recently (within a grace period) are skipped
// to avoid race conditions with concurrent status updates.
// Also cleans up empty nonce index sets after removal.
func (s *TxStore) DeleteOlderThan(ctx context.Context, age time.Duration) (int, error) {
	return s.DeleteOlderThanWithOptions(ctx, age, 1000, 5*time.Minute)
}

// DeleteOlderThanWithOptions removes transactions older than the given duration.
// Parameters:
//   - age: minimum age of transactions to delete (based on CreatedAt)
//   - batchSize: maximum number of transactions to process per batch (0 = unlimited)
//   - gracePeriod: skip transactions updated within this duration to avoid race conditions
func (s *TxStore) DeleteOlderThanWithOptions(ctx context.Context, age time.Duration, batchSize int64, gracePeriod time.Duration) (int, error) {
	cutoff := time.Now().Add(-age).Unix()
	graceTime := time.Now().Add(-gracePeriod)
	totalDeleted := 0

	for {
		// Get hashes of old transactions with batch limit
		rangeBy := &redis.ZRangeBy{
			Min: "-inf",
			Max: strconv.FormatInt(cutoff, 10),
		}
		if batchSize > 0 {
			rangeBy.Count = batchSize
		}

		hashes, err := s.client.ZRangeByScore(ctx, s.key(txTimestampSortedSet), rangeBy).Result()
		if err != nil {
			return totalDeleted, fmt.Errorf("failed to get old transactions: %w", err)
		}

		if len(hashes) == 0 {
			break
		}

		// Batch get all transactions to know which indexes to clean up
		keys := make([]string, len(hashes))
		for i, h := range hashes {
			keys[i] = s.key(txKeyPrefix, h)
		}

		results, err := s.client.MGet(ctx, keys...).Result()
		if err != nil {
			return totalDeleted, fmt.Errorf("failed to batch get transactions: %w", err)
		}

		// Build batch delete operations
		pipe := s.client.TxPipeline()
		deleted := 0
		skipped := 0
		var parseErrors []string
		nonceKeysToCheck := make(map[string]struct{})

		for i, result := range results {
			hashHex := hashes[i]

			if result == nil {
				// Already deleted, just clean up indexes
				pipe.ZRem(ctx, s.key(txTimestampSortedSet), hashHex)
				pipe.SRem(ctx, s.key(txPendingSetKey), hashHex)
				deleted++
				continue
			}

			data, ok := result.(string)
			if !ok {
				parseErrors = append(parseErrors, fmt.Sprintf("hash %s: unexpected type %T", hashHex, result))
				continue
			}

			tx, err := s.deserializePendingTx([]byte(data))
			if err != nil {
				parseErrors = append(parseErrors, fmt.Sprintf("hash %s: %v", hashHex, err))
				// Still try to delete the corrupted data
				pipe.Del(ctx, s.key(txKeyPrefix, hashHex))
				pipe.ZRem(ctx, s.key(txTimestampSortedSet), hashHex)
				pipe.SRem(ctx, s.key(txPendingSetKey), hashHex)
				deleted++
				continue
			}

			// Skip if the transaction was updated recently (within grace period)
			// This prevents race conditions with concurrent UpdateStatus calls
			if tx.UpdatedAt.After(graceTime) {
				skipped++
				// Remove from sorted set so we don't keep checking it
				pipe.ZRem(ctx, s.key(txTimestampSortedSet), hashHex)
				// Re-add with updated timestamp to maintain sorted set integrity
				pipe.ZAdd(ctx, s.key(txTimestampSortedSet), redis.Z{
					Score:  float64(tx.UpdatedAt.Unix()),
					Member: hashHex,
				})
				continue
			}

			// Queue all delete operations
			walletKey := s.walletPendingKey(tx.Wallet, tx.ChainID)
			nonceKey := s.nonceIndexKey(tx.Wallet, tx.ChainID, tx.Nonce)

			pipe.Del(ctx, s.key(txKeyPrefix, hashHex))
			pipe.SRem(ctx, s.key(txPendingSetKey), hashHex)
			pipe.SRem(ctx, walletKey, hashHex)
			pipe.SRem(ctx, nonceKey, hashHex)
			pipe.ZRem(ctx, s.key(txTimestampSortedSet), hashHex)
			deleted++

			// Track nonce keys for cleanup
			nonceKeysToCheck[nonceKey] = struct{}{}
		}

		// Execute batch delete
		_, err = pipe.Exec(ctx)
		if err != nil {
			return totalDeleted, fmt.Errorf("failed to execute batch delete: %w", err)
		}

		totalDeleted += deleted

		// Clean up empty nonce index sets
		if len(nonceKeysToCheck) > 0 {
			if err := s.cleanupEmptyNonceIndexes(ctx, nonceKeysToCheck); err != nil {
				// Log but don't fail - empty sets are just wasted memory, not data corruption
				parseErrors = append(parseErrors, fmt.Sprintf("nonce index cleanup: %v", err))
			}
		}

		// Return partial results with error if there were parse failures
		if len(parseErrors) > 0 {
			return totalDeleted, fmt.Errorf("encountered %d errors during delete: %s",
				len(parseErrors), strings.Join(parseErrors, "; "))
		}

		// If we processed fewer than batch size, we're done
		if batchSize == 0 || int64(len(hashes)) < batchSize {
			break
		}

		// If we skipped all items in this batch, break to avoid infinite loop
		if skipped == len(hashes) {
			break
		}
	}

	return totalDeleted, nil
}

// cleanupEmptyNonceIndexes removes nonce index sets that are now empty.
func (s *TxStore) cleanupEmptyNonceIndexes(ctx context.Context, nonceKeys map[string]struct{}) error {
	if len(nonceKeys) == 0 {
		return nil
	}

	// Check cardinality of each set
	pipe := s.client.Pipeline()
	cardCmds := make(map[string]*redis.IntCmd, len(nonceKeys))
	for key := range nonceKeys {
		cardCmds[key] = pipe.SCard(ctx, key)
	}
	_, _ = pipe.Exec(ctx)

	// Delete empty sets
	var emptyKeys []string
	for key, cmd := range cardCmds {
		count, err := cmd.Result()
		if err != nil {
			continue // Skip on error
		}
		if count == 0 {
			emptyKeys = append(emptyKeys, key)
		}
	}

	if len(emptyKeys) > 0 {
		return s.client.Del(ctx, emptyKeys...).Err()
	}

	return nil
}

// Helper methods

func (s *TxStore) walletPendingKey(wallet common.Address, chainID uint64) string {
	return s.key(txWalletPendingKey, wallet.Hex(), ":", strconv.FormatUint(chainID, 10), ":pending")
}

func (s *TxStore) nonceIndexKey(wallet common.Address, chainID uint64, nonce uint64) string {
	return s.key(txNonceKey, wallet.Hex(), ":", strconv.FormatUint(chainID, 10), ":", strconv.FormatUint(nonce, 10))
}

func (s *TxStore) getTransactionsByHashes(ctx context.Context, hashes []string) ([]*walletarmy.PendingTx, error) {
	if len(hashes) == 0 {
		return nil, nil
	}

	// Build keys
	keys := make([]string, len(hashes))
	for i, h := range hashes {
		keys[i] = s.key(txKeyPrefix, h)
	}

	// Batch get
	results, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get transactions: %w", err)
	}

	txs := make([]*walletarmy.PendingTx, 0, len(results))
	var deserializeErrors []string

	for i, result := range results {
		if result == nil {
			// Transaction was deleted or expired, this is expected
			continue
		}

		data, ok := result.(string)
		if !ok {
			// Unexpected type from Redis
			deserializeErrors = append(deserializeErrors, fmt.Sprintf("hash %s: unexpected type %T", hashes[i], result))
			continue
		}

		tx, err := s.deserializePendingTx([]byte(data))
		if err != nil {
			// Data corruption - track the error
			deserializeErrors = append(deserializeErrors, fmt.Sprintf("hash %s: %v", hashes[i], err))
			continue
		}
		txs = append(txs, tx)
	}

	// Return partial results with error if there were deserialization failures
	if len(deserializeErrors) > 0 {
		return txs, fmt.Errorf("failed to deserialize %d transactions: %s", len(deserializeErrors), strings.Join(deserializeErrors, "; "))
	}

	return txs, nil
}

func (s *TxStore) serializePendingTx(tx *walletarmy.PendingTx) ([]byte, error) {
	data := pendingTxData{
		Hash:      tx.Hash.Hex(),
		Wallet:    tx.Wallet.Hex(),
		ChainID:   tx.ChainID,
		Nonce:     tx.Nonce,
		Status:    string(tx.Status),
		CreatedAt: tx.CreatedAt.UnixNano(),
		UpdatedAt: tx.UpdatedAt.UnixNano(),
		Metadata:  tx.Metadata,
	}

	// Serialize transaction using RLP
	if tx.Transaction != nil {
		txRLP, err := tx.Transaction.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal transaction: %w", err)
		}
		data.TxRLP = txRLP
	}

	// Serialize receipt as JSON (RLP for receipts is complex)
	if tx.Receipt != nil {
		receiptJSON, err := tx.Receipt.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal receipt: %w", err)
		}
		data.ReceiptJSON = receiptJSON
	}

	return json.Marshal(data)
}

func (s *TxStore) deserializePendingTx(data []byte) (*walletarmy.PendingTx, error) {
	var d pendingTxData
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pending tx: %w", err)
	}

	tx := &walletarmy.PendingTx{
		Hash:      common.HexToHash(d.Hash),
		Wallet:    common.HexToAddress(d.Wallet),
		ChainID:   d.ChainID,
		Nonce:     d.Nonce,
		Status:    walletarmy.PendingTxStatus(d.Status),
		CreatedAt: time.Unix(0, d.CreatedAt),
		UpdatedAt: time.Unix(0, d.UpdatedAt),
		Metadata:  d.Metadata,
	}

	// Deserialize transaction
	if len(d.TxRLP) > 0 {
		ethTx := new(types.Transaction)
		if err := ethTx.UnmarshalBinary(d.TxRLP); err != nil {
			return nil, fmt.Errorf("failed to unmarshal transaction: %w", err)
		}
		tx.Transaction = ethTx
	}

	// Deserialize receipt
	if len(d.ReceiptJSON) > 0 {
		receipt := new(types.Receipt)
		if err := receipt.UnmarshalJSON(d.ReceiptJSON); err != nil {
			return nil, fmt.Errorf("failed to unmarshal receipt: %w", err)
		}
		tx.Receipt = receipt
	}

	return tx, nil
}

// Verify TxStore implements walletarmy.TxStore
var _ walletarmy.TxStore = (*TxStore)(nil)
