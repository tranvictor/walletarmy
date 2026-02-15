# AGENTS.md — Contributor Conventions for AI Agents

This document tells AI coding agents how to contribute to the walletarmy codebase.

## Package Layout

```
walletarmy/                  # Root package — all public API lives here
├── manager.go               # WalletManager struct, constructor, account management
├── manager_nonce.go         # Nonce acquisition and release
├── manager_network.go       # Network infrastructure (reader, broadcaster, analyzer)
├── manager_tx.go            # Transaction building, signing, broadcasting, EnsureTx
├── request.go               # TxRequest builder pattern
├── execution_context.go     # TxExecutionContext (internal tx state machine)
├── interface.go             # Manager interface (the public contract)
├── hook.go                  # Hook type definitions
├── errors.go                # Sentinel errors (transaction execution)
├── broadcast_error.go       # Broadcast error classification
├── error_decoder.go         # ABI error decoding
├── types.go                 # Core types and constants
├── options.go               # WithXxx functional options
├── deps.go                  # Minimal interfaces for external dependencies
├── gas_info.go              # Gas information types
├── persistence.go           # Persistence interfaces (NonceStore, TxStore)
├── recovery.go              # Crash recovery implementation
├── adapters.go              # Adapters wrapping jarvis types
├── testing_mocks_test.go    # Mock implementations (test file, avoids import cycles)
├── idempotency/             # Public subpackage: idempotency store
│   └── idempotency.go       # Store interface + InMemoryStore
├── internal/
│   ├── nonce/               # Thread-safe nonce tracking (not exported)
│   │   └── tracker.go
│   └── circuitbreaker/      # Circuit breaker (not exported)
│       └── circuitbreaker.go
├── testutil/                # Test utilities (exported for downstream tests)
│   ├── fixtures.go          # Test addresses, keys, values
│   ├── builders.go          # Transaction/receipt builders
│   ├── mocks.go             # Mock helpers
│   └── networks.go          # Mock network implementations
└── examples/
    └── fund_distribution/   # Example: parallel multi-wallet transfers
```

## Where New Code Goes

| What you're adding | Where it goes |
|---|---|
| New builder method on `TxRequest` | `request.go` |
| New `WithXxx` option | `options.go` |
| New hook type | `hook.go` |
| New sentinel error | `errors.go` (execution) or `broadcast_error.go` (broadcast) |
| New persistence interface | `persistence.go` |
| New dependency interface | `deps.go` |
| New field on `ManagerDefaults` | `types.go` |
| New method on `Manager` interface | `interface.go` (interface) + `manager*.go` (implementation) |
| New internal implementation | `internal/<package>/` |
| New public subpackage | Top-level directory (e.g., `idempotency/`) |
| New test utilities | `testutil/` |
| New mock for tests | `testing_mocks_test.go` (to avoid import cycles) |

## Coding Conventions

### Naming

- **Exported types**: PascalCase (`TxRequest`, `WalletManager`, `ManagerDefaults`)
- **Options**: `WithXxx` pattern (`WithDefaultNetwork`, `WithNonceStore`)
- **Builder methods**: `SetXxx` pattern (`SetFrom`, `SetNetwork`, `SetMaxGasPrice`)
- **Errors**: `ErrXxx` pattern (`ErrFromAddressZero`, `ErrCircuitBreakerOpen`)
- **Hook types**: descriptive name ending in `Hook` (`TxMinedHook`, `SimulationFailedHook`)
- **Interfaces**: short, behavior-focused names (`EthReader`, `NonceStore`, `Manager`)
- **Test functions**: `Test<Type>_<Method>_<Scenario>` (e.g., `TestWalletManager_SetAccount`, `TestTxRequest_Execute_ReturnsError_WhenFromIsZero`)

### Error Handling

- **Sentinel errors** in `errors.go` — use `fmt.Errorf("descriptive message")`
- **Wrap errors** with `fmt.Errorf("context: %w", err)` to preserve the chain
- **Broadcast errors** are a separate type (`BroadcastError`) with classifier functions (`IsNonceIsLow`, `IsInsufficientFund`, etc.)
- Never return bare `nil` error from a function that clearly failed — always wrap

### Interface-First Design

- Define the public contract in `interface.go` (`Manager` interface)
- Add `var _ Manager = (*WalletManager)(nil)` compile-time checks
- New public methods should be added to both the `Manager` interface and the `WalletManager` struct
- Dependency interfaces live in `deps.go` — keep them minimal

### Functional Options

- All constructor configuration uses the `WithXxx` functional option pattern
- Each option is a `func(*WalletManager)` (type `WalletManagerOption`)
- Document what the option does and mention defaults in the godoc comment
- Place new options in `options.go`

### Builder Pattern

- `TxRequest` uses method chaining — every `Set` method returns `*TxRequest`
- New fields need: struct field + `Set` method + initialization in `R()` (if default applies)
- Execution goes through `Execute()` → `ExecuteContext()` → `executeInternal()`
- `SetSkipSimulation(true)` bypasses the `eth_call` simulation before broadcast; caller should also set `SetGasLimit()` when using this

### Thread Safety

- `WalletManager` is safe for concurrent use
- Use `sync.Map` for per-key concurrent access (wallets, networks)
- Use `sync.RWMutex` via `defaultsMu` for the defaults struct
- `TxRequest` is NOT shared — each `R()` creates a new instance

## Testing Conventions

### Framework

- Use `github.com/stretchr/testify` — `assert` for soft checks, `require` for fatal checks
- Import both: `"github.com/stretchr/testify/assert"` and `"github.com/stretchr/testify/require"`

### Test Structure

```go
func TestWalletManager_MethodName_Scenario(t *testing.T) {
    // Arrange
    wm := NewWalletManager(WithReaderFactory(mockFactory))

    // Act
    result, err := wm.SomeMethod(args)

    // Assert
    require.NoError(t, err)
    assert.Equal(t, expected, result)
}
```

### Mocks

- **Mock implementations** go in `testing_mocks_test.go` (same package, test file)
- This avoids import cycles between `walletarmy` and `testutil`
- Use `testutil/` for helpers that don't depend on walletarmy types (fixtures, builders, mock networks)
- Inject mocks via `WithReaderFactory`, `WithBroadcasterFactory`, `WithTxMonitorFactory`

### Concurrency Tests

- Use `sync.WaitGroup` for parallel goroutine tests
- Test with meaningful iteration counts (e.g., 100 iterations × 10 goroutines)
- Always check for race conditions: `go test -race ./...`

### Test Fixtures

- Common addresses, keys, and values are in `testutil/fixtures.go`
- Transaction builders are in `testutil/builders.go`
- Mock networks are in `testutil/networks.go`

## Running Tests

```bash
# All tests
go test ./...

# With race detector
go test -race ./...

# Specific package
go test -v ./idempotency/

# Specific test
go test -run TestWalletManager_SetAccount -v
```

## Dependencies

- `github.com/tranvictor/jarvis` — Ethereum toolkit (networks, accounts, tx analysis)
- `github.com/ethereum/go-ethereum` — Go Ethereum types and crypto
- `github.com/stretchr/testify` — Testing assertions
- `github.com/KyberNetwork/logger` — Structured logging

Do not add new dependencies without a strong reason. Prefer the standard library.

## Commit Style

- Keep commits focused on a single concern
- Use descriptive commit messages: `add WithDefaultSlowTxTimeout option` not `update options`
- Test changes should accompany code changes in the same commit
