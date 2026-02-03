package redis

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/redis/go-redis/v9"

	"github.com/tranvictor/walletarmy"
)

func Example_basicUsage() {
	// Create Redis client
	client := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password
		DB:       0,
	})
	defer func() { _ = client.Close() }()

	// Create separate stores for transactions and nonces
	txStore := NewTxStore(client)
	nonceStore := NewNonceStore(client)

	// Create WalletManager with persistence
	wm := walletarmy.NewWalletManager(
		walletarmy.WithTxStore(txStore),
		walletarmy.WithNonceStore(nonceStore),
	)

	// Use wm for transaction management...
	_ = wm
	fmt.Println("WalletManager created with Redis persistence")
	// Output: WalletManager created with Redis persistence
}

func Example_multiTenant() {
	// Create Redis client
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer func() { _ = client.Close() }()

	// Create separate stores for different applications/tenants
	appATxStore := NewTxStore(client, WithTxStoreKeyPrefix("app-a"))
	appANonceStore := NewNonceStore(client, WithNonceStoreKeyPrefix("app-a"))

	appBTxStore := NewTxStore(client, WithTxStoreKeyPrefix("app-b"))
	appBNonceStore := NewNonceStore(client, WithNonceStoreKeyPrefix("app-b"))

	// Each app has isolated storage
	_ = appATxStore
	_ = appANonceStore
	_ = appBTxStore
	_ = appBNonceStore
	fmt.Println("Multi-tenant stores created")
	// Output: Multi-tenant stores created
}

func Example_recovery() {
	// Create Redis client
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer func() { _ = client.Close() }()

	// Create separate stores
	txStore := NewTxStore(client)
	nonceStore := NewNonceStore(client)

	// Create WalletManager with persistence
	wm := walletarmy.NewWalletManager(
		walletarmy.WithTxStore(txStore),
		walletarmy.WithNonceStore(nonceStore),
	)

	ctx := context.Background()

	// On application startup, perform recovery with options
	result, err := wm.RecoverWithOptions(ctx, walletarmy.RecoveryOptions{
		ResumeMonitoring:      true,
		MaxConcurrentMonitors: 5,
		OnTxMined: func(ptx *walletarmy.PendingTx, receipt *types.Receipt) {
			log.Printf("Recovered tx %s was mined", ptx.Hash.Hex())
		},
		OnTxDropped: func(ptx *walletarmy.PendingTx) {
			log.Printf("Recovered tx %s was dropped", ptx.Hash.Hex())
		},
	})
	if err != nil {
		log.Printf("Recovery failed: %v", err)
		return
	}

	fmt.Printf("Recovery: %d txs recovered, %d mined, %d dropped\n",
		result.RecoveredTxs, result.MinedTxs, result.DroppedTxs)
}

func Example_cleanup() {
	// Create Redis client
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer func() { _ = client.Close() }()

	txStore := NewTxStore(client)
	ctx := context.Background()

	// Clean up transactions older than 24 hours
	deleted, err := txStore.DeleteOlderThan(ctx, 24*time.Hour)
	if err != nil {
		log.Printf("Cleanup failed: %v", err)
		return
	}

	fmt.Printf("Cleaned up %d old transactions\n", deleted)
}

func Example_separateRedisInstances() {
	// You can use different Redis instances for TxStore and NonceStore
	txClient := redis.NewClient(&redis.Options{
		Addr: "redis-tx:6379",
	})
	defer func() { _ = txClient.Close() }()

	nonceClient := redis.NewClient(&redis.Options{
		Addr: "redis-nonce:6379",
	})
	defer func() { _ = nonceClient.Close() }()

	txStore := NewTxStore(txClient)
	nonceStore := NewNonceStore(nonceClient)

	wm := walletarmy.NewWalletManager(
		walletarmy.WithTxStore(txStore),
		walletarmy.WithNonceStore(nonceStore),
	)

	_ = wm
	fmt.Println("Separate Redis instances configured")
	// Output: Separate Redis instances configured
}
