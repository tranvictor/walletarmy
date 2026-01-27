package redis

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

var (
	sharedRedisContainer *tcredis.RedisContainer
	sharedRedisConnStr   string
	containerErr         error
)

// TestMain sets up a shared Redis container for all tests in this package.
func TestMain(m *testing.M) {
	ctx := context.Background()

	// Start Redis container once for all tests
	redisContainer, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		// Container failed to start - tests will skip
		containerErr = err
		os.Exit(m.Run())
	}

	sharedRedisContainer = redisContainer

	// Get connection string
	connStr, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		containerErr = err
		os.Exit(m.Run())
	}
	sharedRedisConnStr = connStr

	// Run tests
	code := m.Run()

	// Cleanup
	if redisContainer != nil {
		_ = redisContainer.Terminate(ctx)
	}

	os.Exit(code)
}

// testRedisClient returns a Redis client connected to the shared testcontainer Redis instance.
// Each test gets a fresh database by using FLUSHDB.
func testRedisClient(t *testing.T) redis.UniversalClient {
	if containerErr != nil {
		t.Skipf("Redis container not available: %v", containerErr)
	}

	// Parse connection string and create client
	opts, err := redis.ParseURL(sharedRedisConnStr)
	if err != nil {
		t.Fatalf("Failed to parse Redis URL: %v", err)
	}

	client := redis.NewClient(opts)

	// Clean database for this test
	ctx := context.Background()
	if err := client.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("Failed to flush Redis DB: %v", err)
	}

	return client
}
