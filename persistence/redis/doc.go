// Package redis provides Redis-based implementations of walletarmy persistence interfaces.
//
// This package enables crash-resilient transaction management by persisting transaction
// and nonce state to Redis. It provides three separate store implementations:
//   - TxStore: implements walletarmy.TxStore for transaction tracking
//   - NonceStore: implements walletarmy.NonceStore for nonce state management
//   - IdempotencyStore: implements idempotency.Store for preventing duplicate transactions
//
// # Basic Usage
//
//	import (
//	    "github.com/redis/go-redis/v9"
//	    "github.com/tranvictor/walletarmy"
//	    redisstore "github.com/tranvictor/walletarmy/persistence/redis"
//	)
//
//	// Create Redis client
//	client := redis.NewClient(&redis.Options{
//	    Addr: "localhost:6379",
//	})
//
//	// Create separate stores for transactions and nonces
//	txStore := redisstore.NewTxStore(client)
//	nonceStore := redisstore.NewNonceStore(client)
//
//	// Create WalletManager with persistence
//	wm := walletarmy.NewWalletManager(
//	    walletarmy.WithTxStore(txStore),
//	    walletarmy.WithNonceStore(nonceStore),
//	)
//
// # Multi-Tenant Usage
//
// Use key prefixes to isolate data for different applications or environments:
//
//	prodTxStore := redisstore.NewTxStore(client, redisstore.WithTxStoreKeyPrefix("prod"))
//	prodNonceStore := redisstore.NewNonceStore(client, redisstore.WithNonceStoreKeyPrefix("prod"))
//
//	testTxStore := redisstore.NewTxStore(client, redisstore.WithTxStoreKeyPrefix("test"))
//	testNonceStore := redisstore.NewNonceStore(client, redisstore.WithNonceStoreKeyPrefix("test"))
//
// # Separate Redis Instances
//
// You can use different Redis instances for TxStore and NonceStore:
//
//	txClient := redis.NewClient(&redis.Options{Addr: "redis-tx:6379"})
//	nonceClient := redis.NewClient(&redis.Options{Addr: "redis-nonce:6379"})
//
//	txStore := redisstore.NewTxStore(txClient)
//	nonceStore := redisstore.NewNonceStore(nonceClient)
//
// # Redis Key Structure
//
// TxStore uses the following key patterns:
//
//   - walletarmy:tx:{hash} - Transaction data (JSON)
//   - walletarmy:tx:pending - Set of all pending transaction hashes
//   - walletarmy:tx:wallet:{wallet}:{chainID}:pending - Set of pending tx hashes per wallet/chain
//   - walletarmy:tx:nonce:{wallet}:{chainID}:{nonce} - Set of tx hashes per nonce
//   - walletarmy:tx:created_at - Sorted set of tx hashes by creation time
//
// NonceStore uses the following key patterns:
//
//   - walletarmy:nonce:{wallet}:{chainID} - Nonce state (JSON)
//   - walletarmy:nonce:all - Set of all wallet:chainID pairs
//
// IdempotencyStore uses the following key patterns:
//
//   - walletarmy:idempotency:{key} - Record data (JSON, with TTL for automatic expiration)
//
// # Thread Safety
//
// All stores are thread-safe and can be used from multiple goroutines.
// Redis handles the underlying concurrency control.
//
// # Recovery
//
// On application restart, use the WalletManager.Recover method to:
//
//  1. Reconcile local nonce state with chain state
//  2. Check and update status of pending transactions
//  3. Optionally resume monitoring pending transactions
//
// # Cleanup
//
// Use TxStore.DeleteOlderThan to periodically clean up old completed transactions:
//
//	deleted, err := txStore.DeleteOlderThan(ctx, 24*time.Hour)
//
// Use NonceStore.Cleanup to remove orphaned index entries:
//
//	removed, err := nonceStore.Cleanup(ctx)
//
// # Idempotency Store
//
// The IdempotencyStore prevents duplicate transaction submissions by tracking
// idempotency keys. Typical usage:
//
//	store := redisstore.NewIdempotencyStore(client)
//
//	// With TTL for automatic expiration
//	store := redisstore.NewIdempotencyStore(client, redisstore.WithIdempotencyStoreTTL(24*time.Hour))
//
//	// Create a record (returns ErrDuplicateKey if key exists)
//	record, err := store.Create(idempotencyKey)
//	if err == idempotency.ErrDuplicateKey {
//	    // Key already exists - check record.Status for current state
//	}
//
// # Supported Redis Configurations
//
// All stores work with:
//   - Standalone Redis
//   - Redis Sentinel
//   - Redis Cluster
//
// Simply pass the appropriate redis.UniversalClient implementation to NewTxStore or NewNonceStore.
package redis
