package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/redis/go-redis/v9"
	"github.com/tranvictor/walletarmy"
)

// Key prefixes for transaction storage
const (
	txKeyPrefix          = "walletarmy:tx:"           // tx data by hash
	txPendingSetKey      = "walletarmy:tx:pending"    // set of all pending tx hashes
	txWalletPendingKey   = "walletarmy:tx:wallet:"    // pending txs by wallet:chainID
	txNonceKey           = "walletarmy:tx:nonce:"     // txs by wallet:chainID:nonce
	txCreatedAtSortedSet = "walletarmy:tx:created_at" // sorted set by creation time
)

// TxStore provides Redis-based persistence for transaction tracking.
// It implements the walletarmy.TxStore interface.
type TxStore struct {
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration // Optional TTL for transaction records
}

// TxStoreOption configures a TxStore.
type TxStoreOption func(*TxStore)

// WithTxStoreKeyPrefix sets a custom prefix for all Redis keys.
func WithTxStoreKeyPrefix(prefix string) TxStoreOption {
	return func(s *TxStore) {
		s.keyPrefix = prefix
	}
}

// WithTxStoreTTL sets a TTL for transaction records.
// Records will automatically expire after this duration.
// If not set, records never expire automatically.
func WithTxStoreTTL(ttl time.Duration) TxStoreOption {
	return func(s *TxStore) {
		s.ttl = ttl
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
	CreatedAt   int64             `json:"created_at"`
	UpdatedAt   int64             `json:"updated_at"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Save persists a pending transaction to Redis.
func (s *TxStore) Save(ctx context.Context, tx *walletarmy.PendingTx) error {
	if tx == nil {
		return fmt.Errorf("transaction cannot be nil")
	}

	data, err := s.serializePendingTx(tx)
	if err != nil {
		return fmt.Errorf("failed to serialize transaction: %w", err)
	}

	hashKey := s.key(txKeyPrefix, tx.Hash.Hex())
	walletKey := s.walletPendingKey(tx.Wallet, tx.ChainID)
	nonceKey := s.nonceIndexKey(tx.Wallet, tx.ChainID, tx.Nonce)
	hashHex := tx.Hash.Hex()

	// Use transaction for atomicity
	pipe := s.client.TxPipeline()

	// Store tx data (with optional TTL)
	pipe.Set(ctx, hashKey, data, s.ttl)

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
	pipe.ZAdd(ctx, s.key(txCreatedAtSortedSet), redis.Z{
		Score:  float64(tx.CreatedAt.Unix()),
		Member: hashHex,
	})

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to save transaction: %w", err)
	}

	return nil
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
func (s *TxStore) UpdateStatus(ctx context.Context, hash common.Hash, status walletarmy.PendingTxStatus, receipt *types.Receipt) error {
	hashKey := s.key(txKeyPrefix, hash.Hex())
	hashHex := hash.Hex()

	const maxRetries = 3
	var lastErr error

	for i := 0; i < maxRetries; i++ {
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
				pipe.Set(ctx, hashKey, newData, s.ttl)

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
func (s *TxStore) Delete(ctx context.Context, hash common.Hash) error {
	// Get tx first to know which indexes to clean up
	tx, err := s.Get(ctx, hash)
	if err != nil {
		return err
	}
	if tx == nil {
		return nil // Already deleted
	}

	hashKey := s.key(txKeyPrefix, hash.Hex())
	walletKey := s.walletPendingKey(tx.Wallet, tx.ChainID)
	nonceKey := s.nonceIndexKey(tx.Wallet, tx.ChainID, tx.Nonce)
	hashHex := hash.Hex()

	pipe := s.client.TxPipeline()
	pipe.Del(ctx, hashKey)
	pipe.SRem(ctx, s.key(txPendingSetKey), hashHex)
	pipe.SRem(ctx, walletKey, hashHex)
	pipe.SRem(ctx, nonceKey, hashHex)
	pipe.ZRem(ctx, s.key(txCreatedAtSortedSet), hashHex)

	_, err = pipe.Exec(ctx)
	return err
}

// DeleteOlderThan removes transactions older than the given duration.
func (s *TxStore) DeleteOlderThan(ctx context.Context, age time.Duration) (int, error) {
	cutoff := time.Now().Add(-age).Unix()

	// Get hashes of old transactions
	hashes, err := s.client.ZRangeByScore(ctx, s.key(txCreatedAtSortedSet), &redis.ZRangeBy{
		Min: "-inf",
		Max: strconv.FormatInt(cutoff, 10),
	}).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to get old transactions: %w", err)
	}

	if len(hashes) == 0 {
		return 0, nil
	}

	// Delete each transaction, tracking failures
	deleted := 0
	var deleteErrors []string

	for _, hashHex := range hashes {
		hash := common.HexToHash(hashHex)
		if err := s.Delete(ctx, hash); err != nil {
			deleteErrors = append(deleteErrors, fmt.Sprintf("hash %s: %v", hashHex, err))
			continue
		}
		deleted++
	}

	// Return partial results with error if there were deletion failures
	if len(deleteErrors) > 0 {
		return deleted, fmt.Errorf("failed to delete %d of %d transactions: %s",
			len(deleteErrors), len(hashes), strings.Join(deleteErrors, "; "))
	}

	return deleted, nil
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
		CreatedAt: tx.CreatedAt.Unix(),
		UpdatedAt: tx.UpdatedAt.Unix(),
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
		CreatedAt: time.Unix(d.CreatedAt, 0),
		UpdatedAt: time.Unix(d.UpdatedAt, 0),
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
