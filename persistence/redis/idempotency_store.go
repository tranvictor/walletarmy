package redis

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/goccy/go-json"
	"github.com/redis/go-redis/v9"

	"github.com/tranvictor/walletarmy/idempotency"
)

// Key prefix for idempotency storage
const (
	idempotencyRecordKeyPrefix = "walletarmy:idempotency:" // record data by key
)

// IdempotencyStore provides Redis-based persistence for idempotency keys.
// It implements the idempotency.Store interface.
//
// Records expire automatically based on the TTL configured via WithIdempotencyStoreTTL.
type IdempotencyStore struct {
	client    redis.UniversalClient
	keyPrefix string
	ttl       time.Duration // TTL for records (required for automatic expiration)
}

// IdempotencyStoreOption configures an IdempotencyStore.
type IdempotencyStoreOption func(*IdempotencyStore)

// WithIdempotencyStoreKeyPrefix sets a custom prefix for all Redis keys.
// Useful for multi-tenant deployments sharing the same Redis instance.
func WithIdempotencyStoreKeyPrefix(prefix string) IdempotencyStoreOption {
	return func(s *IdempotencyStore) {
		s.keyPrefix = prefix
	}
}

// WithIdempotencyStoreTTL sets a TTL for records. Redis will automatically expire records
// after the specified duration.
func WithIdempotencyStoreTTL(ttl time.Duration) IdempotencyStoreOption {
	return func(s *IdempotencyStore) {
		s.ttl = ttl
	}
}

// NewIdempotencyStore creates a new Redis-based idempotency store.
func NewIdempotencyStore(client redis.UniversalClient, opts ...IdempotencyStoreOption) *IdempotencyStore {
	s := &IdempotencyStore{
		client: client,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// key returns the full Redis key with optional prefix.
func (s *IdempotencyStore) key(parts ...string) string {
	var key string
	for _, p := range parts {
		key += p
	}
	if s.keyPrefix != "" {
		return s.keyPrefix + ":" + key
	}
	return key
}

// idempotencyRecordData is the JSON-serializable form of idempotency.Record
type idempotencyRecordData struct {
	Key         string `json:"key"`
	Status      int    `json:"status"`
	TxHash      string `json:"tx_hash,omitempty"`
	TxRLP       []byte `json:"tx_rlp,omitempty"`
	ReceiptJSON []byte `json:"receipt_json,omitempty"`
	Error       string `json:"error,omitempty"`
	CreatedAt   int64  `json:"created_at"` // Nanoseconds
	UpdatedAt   int64  `json:"updated_at"` // Nanoseconds
}

// Get retrieves an existing record by key.
func (s *IdempotencyStore) Get(key string) (*idempotency.Record, error) {
	ctx := context.Background()
	recordKey := s.recordKey(key)

	data, err := s.client.Get(ctx, recordKey).Bytes()
	if err == redis.Nil {
		return nil, idempotency.ErrKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get record: %w", err)
	}

	return s.deserializeRecord(data)
}

// Create creates a new record, returning error if key already exists.
// Uses Redis SetNX for atomic creation. If the key exists, returns the existing
// record along with ErrDuplicateKey.
func (s *IdempotencyStore) Create(key string) (*idempotency.Record, error) {
	ctx := context.Background()
	recordKey := s.recordKey(key)

	now := time.Now()
	record := &idempotency.Record{
		Key:       key,
		Status:    idempotency.StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}

	data, err := s.serializeRecord(record)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize record: %w", err)
	}

	// Try atomic creation with SETNX
	var setCmd *redis.BoolCmd
	if s.ttl > 0 {
		setCmd = s.client.SetNX(ctx, recordKey, data, s.ttl)
	} else {
		setCmd = s.client.SetNX(ctx, recordKey, data, 0)
	}

	created, err := setCmd.Result()
	if err != nil {
		return nil, fmt.Errorf("failed to create record: %w", err)
	}

	if created {
		return record, nil
	}

	// Key already exists - get the existing record
	existingData, err := s.client.Get(ctx, recordKey).Bytes()
	if err == redis.Nil {
		// Race condition: key was deleted between SetNX and Get
		// Try again with a simple recursive call (should succeed now)
		return s.Create(key)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get existing record: %w", err)
	}

	existingRecord, err := s.deserializeRecord(existingData)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize existing record: %w", err)
	}

	return existingRecord, idempotency.ErrDuplicateKey
}

// Update updates an existing record.
// Uses WATCH/MULTI/EXEC for optimistic locking to prevent race conditions.
func (s *IdempotencyStore) Update(record *idempotency.Record) error {
	if record == nil {
		return fmt.Errorf("record cannot be nil")
	}

	ctx := context.Background()
	recordKey := s.recordKey(record.Key)

	const maxRetries = 10
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		// Exponential backoff with jitter on retries
		if i > 0 {
			backoff := time.Duration(1<<uint(i-1)) * time.Millisecond
			jitter := time.Duration(rand.Int63n(int64(backoff/2 + 1)))
			time.Sleep(backoff + jitter)
		}

		err := s.client.Watch(ctx, func(rtx *redis.Tx) error {
			// Check if record exists
			exists, err := rtx.Exists(ctx, recordKey).Result()
			if err != nil {
				return fmt.Errorf("failed to check record existence: %w", err)
			}
			if exists == 0 {
				return idempotency.ErrKeyNotFound
			}

			// Update timestamp
			record.UpdatedAt = time.Now()

			data, err := s.serializeRecord(record)
			if err != nil {
				return fmt.Errorf("failed to serialize record: %w", err)
			}

			// Execute atomically
			_, err = rtx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				if s.ttl > 0 {
					pipe.Set(ctx, recordKey, data, s.ttl)
				} else {
					pipe.Set(ctx, recordKey, data, 0)
				}
				return nil
			})
			return err
		}, recordKey)

		if err == nil {
			return nil
		}
		if err == idempotency.ErrKeyNotFound {
			return err
		}
		if err == redis.TxFailedErr {
			// Optimistic lock failed, retry
			lastErr = err
			continue
		}
		return err
	}

	return fmt.Errorf("failed to update record after %d retries: %w", maxRetries, lastErr)
}

// Delete removes a record by key.
func (s *IdempotencyStore) Delete(key string) error {
	ctx := context.Background()
	recordKey := s.recordKey(key)

	err := s.client.Del(ctx, recordKey).Err()
	if err != nil {
		return fmt.Errorf("failed to delete record: %w", err)
	}

	return nil
}

// Helper methods
func (s *IdempotencyStore) recordKey(key string) string {
	return s.key(idempotencyRecordKeyPrefix, key)
}

func (s *IdempotencyStore) serializeRecord(record *idempotency.Record) ([]byte, error) {
	data := idempotencyRecordData{
		Key:       record.Key,
		Status:    int(record.Status),
		TxHash:    record.TxHash.Hex(),
		CreatedAt: record.CreatedAt.UnixNano(),
		UpdatedAt: record.UpdatedAt.UnixNano(),
	}

	// Serialize transaction using RLP if present
	if record.Transaction != nil {
		txRLP, err := record.Transaction.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal transaction: %w", err)
		}
		data.TxRLP = txRLP
	}

	// Serialize receipt as JSON if present
	if record.Receipt != nil {
		receiptJSON, err := record.Receipt.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal receipt: %w", err)
		}
		data.ReceiptJSON = receiptJSON
	}

	// Serialize error if present
	if record.Error != nil {
		data.Error = record.Error.Error()
	}

	return json.Marshal(data)
}

func (s *IdempotencyStore) deserializeRecord(data []byte) (*idempotency.Record, error) {
	var d idempotencyRecordData
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("failed to unmarshal record: %w", err)
	}

	record := &idempotency.Record{
		Key:       d.Key,
		Status:    idempotency.Status(d.Status),
		TxHash:    common.HexToHash(d.TxHash),
		CreatedAt: time.Unix(0, d.CreatedAt),
		UpdatedAt: time.Unix(0, d.UpdatedAt),
	}

	// Deserialize transaction if present
	if len(d.TxRLP) > 0 {
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(d.TxRLP); err != nil {
			return nil, fmt.Errorf("failed to unmarshal transaction: %w", err)
		}
		record.Transaction = tx
	}

	// Deserialize receipt if present
	if len(d.ReceiptJSON) > 0 {
		receipt := new(types.Receipt)
		if err := receipt.UnmarshalJSON(d.ReceiptJSON); err != nil {
			return nil, fmt.Errorf("failed to unmarshal receipt: %w", err)
		}
		record.Receipt = receipt
	}

	// Deserialize error if present
	if d.Error != "" {
		record.Error = fmt.Errorf("%s", d.Error)
	}

	return record, nil
}

// Verify IdempotencyStore implements idempotency.Store
var _ idempotency.Store = (*IdempotencyStore)(nil)
