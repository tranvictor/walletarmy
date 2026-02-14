# WalletArmy LLM API Reference

> Module: `github.com/tranvictor/walletarmy` — Go 1.24+

Condensed API reference for LLM consumption. For narrative docs see `README.md`; for usage patterns see `SKILL.md`.

---

## Exported Types

| Type | File | Description |
|------|------|-------------|
| `WalletManager` | `manager.go` | Core manager — accounts, nonces, gas, circuit breakers, tx execution |
| `Manager` | `interface.go` | Interface for `WalletManager` (dependency injection / mocking) |
| `TxRequest` | `request.go` | Builder pattern for transaction configuration and execution |
| `ManagerDefaults` | `types.go` | Default config inherited by every `TxRequest` |
| `TxExecutionResult` | `types.go` | Outcome of a transaction execution step |
| `WalletManagerOption` | `options.go` | Functional option: `func(*WalletManager)` |
| `GasInfo` | `gas_info.go` | Cached gas price information |
| `TxInfo` | `deps.go` | Transaction status + receipt from reader |
| `TxInfoStatus` | `deps.go` | String enum for tx status |
| `BroadcastError` | `broadcast_error.go` | Typed broadcast error |
| `ErrorDecoder` | `error_decoder.go` | ABI revert error decoder |
| `PendingTx` | `persistence.go` | Tracked in-flight transaction |
| `PendingTxStatus` | `persistence.go` | String enum for pending tx status |
| `NonceState` | `persistence.go` | Persisted nonce state per wallet/network |
| `RecoveryResult` | `persistence.go` | Results of crash recovery |
| `RecoveryOptions` | `persistence.go` | Configuration for recovery |
| `ResumeTransactionOptions` | `persistence.go` | Configuration for resuming a pending tx |

## Hook Types

| Type | Signature | When it fires |
|------|-----------|---------------|
| `Hook` | `func(tx *types.Transaction, err error) error` | Before/after sign+broadcast |
| `TxMinedHook` | `func(tx *types.Transaction, receipt *types.Receipt) error` | When tx is mined (success or revert) |
| `GasEstimationFailedHook` | `func(tx *types.Transaction, abiError *abi.Error, revertParams any, revertMsgError, gasEstimationError error) (*big.Int, error)` | Gas estimation fails (requires ABIs) |
| `SimulationFailedHook` | `func(tx *types.Transaction, revertData []byte, abiError *abi.Error, revertParams any, err error) (shouldRetry bool, retErr error)` | eth_call simulation fails |

## Dependency Interfaces

| Interface | File | Description |
|-----------|------|-------------|
| `EthReader` | `deps.go` | Read blockchain state (nonces, gas, simulate) |
| `EthBroadcaster` | `deps.go` | Broadcast transactions |
| `TxMonitor` | `deps.go` | Monitor transaction status |
| `NonceStore` | `persistence.go` | Persist nonce state across restarts |
| `TxStore` | `persistence.go` | Track in-flight transactions across restarts |
| `idempotency.Store` | `idempotency/idempotency.go` | Prevent duplicate submissions |

## Networks

WalletArmy uses `networks.Network` from `github.com/tranvictor/jarvis/networks` directly
(not a local interface) because the default adapter layer passes the full object to jarvis
utilities (`util.EthReader`, `util.EthBroadcaster`, `txanalyzer`).

**Methods walletarmy calls on a Network:**
- `GetChainID() uint64` — primary key for all per-network maps
- `GetName() string` — logging, nonce tracker
- `IsSyncTxSupported() bool` — decides sync vs async broadcast

**Built-in networks:** `networks.EthereumMainnet`, `networks.BSCMainnet`, `networks.Polygon`,
`networks.Arbitrum`, `networks.Optimism`, `networks.Avalanche`, `networks.Fantom`, etc.

**Lookup:** `networks.GetNetwork(name)`, `networks.GetNetworkByID(chainID)`

**Custom network constructors** (from `github.com/tranvictor/jarvis/networks`):
```go
// Standard EVM (Ethereum, BSC, Polygon, etc.)
networks.NewGenericNetwork(networks.GenericNetworkConfig{
    Name, ChainID, NativeTokenSymbol, NativeTokenDecimal, BlockTime,
    NodeVariableName, DefaultNodes, BlockExplorerAPIKeyVariableName,
    BlockExplorerAPIURL, MultiCallContractAddress,
})

// Optimism-based L2s (OP Stack, Base, custom OP chains)
networks.NewGenericOptimismNetwork(networks.GenericOptimismNetworkConfig{
    // Same fields as above, plus:
    SyncTxSupported bool  // true if the L2 supports eth_sendRawTransactionSync
})

// Arbitrum-based L2s
networks.NewGenericArbitrumNetwork(networks.GenericArbitrumNetworkConfig{...})
```

**Network Resolver** — needed for crash recovery and raw `BroadcastTx` calls (which only
have chain ID, not the network object). For custom networks, provide a resolver:
```go
walletarmy.WithNetworkResolver(func(chainID uint64) (networks.Network, error) {
    if chainID == 12345 { return myCustomNetwork, nil }
    return networks.GetNetworkByID(chainID) // fallback
})
```

## Factory Types

| Type | Signature |
|------|-----------|
| `NetworkReaderFactory` | `func(network networks.Network) (EthReader, error)` |
| `NetworkBroadcasterFactory` | `func(network networks.Network) (EthBroadcaster, error)` |
| `NetworkTxMonitorFactory` | `func(reader EthReader) TxMonitor` |
| `NetworkResolver` | `func(chainID uint64) (networks.Network, error)` |

---

## Constructor

```go
func NewWalletManager(opts ...WalletManagerOption) *WalletManager
```

## WalletManagerOption Functions

| Option | Parameters | Default | Description |
|--------|-----------|---------|-------------|
| `WithDefaultNetwork` | `networks.Network` | `EthereumMainnet` | Default network for requests |
| `WithDefaultNumRetries` | `int` | `9` | Retry attempts |
| `WithDefaultSleepDuration` | `time.Duration` | `5s` | Sleep between retries |
| `WithDefaultTxCheckInterval` | `time.Duration` | `5s` | Tx status polling interval |
| `WithDefaultSlowTxTimeout` | `time.Duration` | `5s` | Walletarmy-internal timeout: time before a broadcasted tx is considered "slow" (not from monitor) |
| `WithDefaultExtraGasLimit` | `uint64` | `0` | Added to gas estimate |
| `WithDefaultExtraGasPrice` | `float64` | `0` | Added to suggested gas price (gwei) |
| `WithDefaultExtraTipCap` | `float64` | `0` | Added to suggested tip cap (gwei) |
| `WithDefaultMaxGasPrice` | `float64` | `0` (auto: 5x suggested) | Gas price ceiling (gwei) |
| `WithDefaultMaxTipCap` | `float64` | `0` (auto: 5x suggested) | Tip cap ceiling (gwei) |
| `WithDefaultTxType` | `uint8` | `0` (legacy) | Tx type: 0=legacy, 2=EIP-1559 |
| `WithDefaultGasPriceIncreasePercent` | `float64` | `1.2` | Multiplier on slow/lost blocking tx (1.2 = +20%) |
| `WithDefaultTipCapIncreasePercent` | `float64` | `1.1` | Multiplier on slow/lost blocking tx (1.1 = +10%) |
| `WithDefaults` | `ManagerDefaults` | — | Set all defaults at once |
| `WithIdempotencyStore` | `idempotency.Store` | `nil` | Custom idempotency store |
| `WithDefaultIdempotencyStore` | `time.Duration` | `nil` | In-memory idempotency store with TTL |
| `WithNonceStore` | `NonceStore` | `nil` | Persist nonce state (crash recovery) |
| `WithTxStore` | `TxStore` | `nil` | Track in-flight txs (crash recovery) |
| `WithReaderFactory` | `NetworkReaderFactory` | jarvis default | Custom reader factory |
| `WithBroadcasterFactory` | `NetworkBroadcasterFactory` | jarvis default | Custom broadcaster factory |
| `WithTxMonitorFactory` | `NetworkTxMonitorFactory` | jarvis default | Custom tx monitor factory |
| `WithNetworkResolver` | `NetworkResolver` | `networks.GetNetworkByID` | Custom chain ID → network resolver |

---

## TxRequest Builder Methods

Created via `wm.R()`. All `Set*` methods return `*TxRequest` for chaining.

| Method | Parameter Type | Description |
|--------|---------------|-------------|
| `SetFrom` | `common.Address` | Sender wallet (**required**) |
| `SetTo` | `common.Address` | Recipient address |
| `SetValue` | `*big.Int` | ETH value in wei (default: 0) |
| `SetData` | `[]byte` | Calldata |
| `SetNetwork` | `networks.Network` | Target network |
| `SetTxType` | `uint8` | 0=legacy, 2=EIP-1559 |
| `SetGasLimit` | `uint64` | Fixed gas limit (0 = estimate) |
| `SetExtraGasLimit` | `uint64` | Buffer added to estimate |
| `SetGasPrice` | `float64` | Fixed gas price (gwei) |
| `SetExtraGasPrice` | `float64` | Buffer added to suggested (gwei) |
| `SetTipCapGwei` | `float64` | EIP-1559 tip cap (gwei) |
| `SetExtraTipCapGwei` | `float64` | Buffer added to suggested tip (gwei) |
| `SetMaxGasPrice` | `float64` | Gas price ceiling (gwei) |
| `SetMaxTipCap` | `float64` | Tip cap ceiling (gwei) |
| `SetNumRetries` | `int` | Max retry attempts |
| `SetSleepDuration` | `time.Duration` | Sleep between retries |
| `SetTxCheckInterval` | `time.Duration` | Polling interval |
| `SetIdempotencyKey` | `string` | Dedup key (requires store) |
| `SetAbis` | `...abi.ABI` | ABIs for error decoding in hooks |
| `SetBeforeSignAndBroadcastHook` | `Hook` | Called before broadcast |
| `SetAfterSignAndBroadcastHook` | `Hook` | Called after broadcast |
| `SetGasEstimationFailedHook` | `GasEstimationFailedHook` | Called on gas estimation failure |
| `SetSimulationFailedHook` | `SimulationFailedHook` | Called on simulation failure |
| `SetTxMinedHook` | `TxMinedHook` | Called when tx is mined |
| `Execute` | — | Execute with `context.Background()` |
| `ExecuteContext` | `context.Context` | Execute with cancellation support |

**Returns:** `(*types.Transaction, *types.Receipt, error)`

---

## WalletManager Methods

### Account Management

Accounts come from the jarvis account package (`github.com/tranvictor/jarvis/util/account`).
WalletArmy uses `*account.Account` directly (not a local interface) because jarvis accounts
transparently support multiple signing backends: private keys, keystore files, Ledger, and Trezor
hardware wallets. All account types expose the same `SignTx(tx, chainID)` method.

**Account constructors** (from `github.com/tranvictor/jarvis/util/account`):
```go
account.NewPrivateKeyAccount(hexKeyWithout0x string) (*Account, error)
account.NewKeystoreAccount(file string, password string) (*Account, error)
account.NewLedgerAccount(derivationPath string, address string) (*Account, error)
account.NewTrezorAccount(derivationPath string, address string) (*Account, error)
```

**WalletManager methods:**
```go
func (wm *WalletManager) SetAccount(acc *account.Account)
func (wm *WalletManager) UnlockAccount(addr common.Address) (*account.Account, error)
func (wm *WalletManager) Account(wallet common.Address) *account.Account
```

`UnlockAccount` looks up the address in the jarvis wallet store and auto-detects the signing
backend (keystore, Ledger, Trezor). The unlocked account is automatically registered via `SetAccount`.

### Configuration

```go
func (wm *WalletManager) Defaults() ManagerDefaults
func (wm *WalletManager) SetDefaults(defaults ManagerDefaults)
func (wm *WalletManager) IdempotencyStore() idempotency.Store
func (wm *WalletManager) NonceStore() NonceStore
func (wm *WalletManager) TxStore() TxStore
```

### Network Infrastructure

```go
func (wm *WalletManager) Reader(network networks.Network) (EthReader, error)
func (wm *WalletManager) Broadcaster(network networks.Network) (EthBroadcaster, error)
func (wm *WalletManager) Analyzer(network networks.Network) (*txanalyzer.TxAnalyzer, error)
func (wm *WalletManager) GasSetting(network networks.Network) (*GasInfo, error)
```

### Nonce Management

```go
func (wm *WalletManager) ReleaseNonce(wallet common.Address, network networks.Network, nonce uint64)
```

### Circuit Breaker

```go
func (wm *WalletManager) GetCircuitBreakerStats(network networks.Network) circuitbreaker.Stats
func (wm *WalletManager) ResetCircuitBreaker(network networks.Network)
func (wm *WalletManager) RecordNetworkSuccess(network networks.Network)
func (wm *WalletManager) RecordNetworkFailure(network networks.Network)
```

### Transaction Execution

```go
func (wm *WalletManager) R() *TxRequest  // Builder pattern entry point (preferred)

func (wm *WalletManager) BuildTx(txType uint8, from, to common.Address, nonce *big.Int,
    value *big.Int, gasLimit uint64, extraGasLimit uint64, gasPrice float64,
    extraGasPrice float64, tipCapGwei float64, extraTipCapGwei float64,
    data []byte, network networks.Network) (*types.Transaction, error)

func (wm *WalletManager) SignTx(wallet common.Address, tx *types.Transaction,
    network networks.Network) (common.Address, *types.Transaction, error)

func (wm *WalletManager) BroadcastTx(tx *types.Transaction) (string, bool, BroadcastError)
func (wm *WalletManager) BroadcastTxSync(tx *types.Transaction) (*types.Receipt, error)

func (wm *WalletManager) MonitorTx(tx *types.Transaction, network networks.Network,
    txCheckInterval time.Duration) <-chan TxInfo
func (wm *WalletManager) MonitorTxContext(ctx context.Context, tx *types.Transaction,
    network networks.Network, txCheckInterval time.Duration) <-chan TxInfo

func (wm *WalletManager) EnsureTx(...) (*types.Transaction, *types.Receipt, error)
func (wm *WalletManager) EnsureTxWithHooks(...) (*types.Transaction, *types.Receipt, error)
func (wm *WalletManager) EnsureTxWithHooksContext(...) (*types.Transaction, *types.Receipt, error)
```

### Recovery

```go
func (wm *WalletManager) Recover(ctx context.Context) (*RecoveryResult, error)
func (wm *WalletManager) RecoverWithOptions(ctx context.Context, opts RecoveryOptions) (*RecoveryResult, error)
func (wm *WalletManager) ResumePendingTransaction(ctx context.Context, pendingTx *PendingTx,
    opts ResumeTransactionOptions) (*types.Transaction, *types.Receipt, error)
```

---

## Sentinel Errors

### Transaction Execution (`errors.go`)

| Variable | Message |
|----------|---------|
| `ErrEstimateGasFailed` | "estimate gas failed" |
| `ErrAcquireNonceFailed` | "acquire nonce failed" |
| `ErrGetGasSettingFailed` | "get gas setting failed" |
| `ErrEnsureTxOutOfRetries` | "ensure tx out of retries" |
| `ErrGasPriceLimitReached` | "gas price protection limit reached" |
| `ErrFromAddressZero` | "from address cannot be zero" |
| `ErrNetworkNil` | "network cannot be nil" |
| `ErrSimulatedTxReverted` | "tx will be reverted" |
| `ErrSimulatedTxFailed` | "couldn't simulate tx at pending state" |
| `ErrCircuitBreakerOpen` | "circuit breaker is open: network temporarily unavailable" |
| `ErrSyncBroadcastTimeout` | "sync broadcast timed out, falling back to async monitoring" |

### Broadcast Errors (`broadcast_error.go`)

| Variable | Classifier Function |
|----------|-------------------|
| `ErrInsufficientFund` | `IsInsufficientFund(err)` |
| `ErrNonceIsLow` | `IsNonceIsLow(err)` |
| `ErrGasLimitIsTooLow` | `IsGasLimitIsTooLow(err)` |
| `ErrTxIsKnown` | `IsTxIsKnown(err)` |

### Idempotency Errors (`idempotency/`)

| Variable | Description |
|----------|-------------|
| `idempotency.ErrDuplicateKey` | Key already exists |
| `idempotency.ErrKeyNotFound` | Key not found |

---

## Constants

```go
const (
    DefaultNumRetries              = 9
    DefaultSleepDuration           = 5 * time.Second
    DefaultTxCheckInterval         = 5 * time.Second
    DefaultSlowTxTimeout           = 5 * time.Second
    DefaultGasPriceIncreasePercent = 1.2  // 20% bump
    DefaultTipCapIncreasePercent   = 1.1  // 10% bump
    MaxCapMultiplier               = 5.0  // For auto max gas/tip
)
```

## TxInfoStatus Values

Statuses have two origins:

**From the external TxMonitor** (node/mempool state):
```go
TxStatusMined     = "mined"     // mapped from monitor "done"
TxStatusReverted  = "reverted"  // mapped from monitor "reverted"
TxStatusLost      = "lost"      // mapped from monitor "lost"
TxStatusPending   = "pending"
TxStatusDone      = "done"      // raw monitor status (mapped to TxStatusMined internally)
```

**Generated internally by walletarmy** (NOT from the monitor):
```go
TxStatusSlow      = "slow"      // fired by MonitorTxContext when tx is not mined within SlowTxTimeout
TxStatusCancelled = "cancelled" // fired by MonitorTxContext when caller's context is cancelled
```

> **Important**: `TxStatusSlow` is a walletarmy-level timeout signal, not a status reported by
> any node or TxMonitor implementation. Custom TxMonitor implementations should NOT return "slow" —
> it will be treated as an unrecognized status and the slow timeout will fire independently.

## PendingTxStatus Values

```go
PendingTxStatusPending     = "pending"
PendingTxStatusBroadcasted = "broadcasted"
PendingTxStatusMined       = "mined"
PendingTxStatusReverted    = "reverted"
PendingTxStatusDropped     = "dropped"
PendingTxStatusReplaced    = "replaced"
```

## Idempotency Status Values

```go
idempotency.StatusPending   = 0  // Being processed
idempotency.StatusSubmitted = 1  // Submitted to network
idempotency.StatusConfirmed = 2  // Mined
idempotency.StatusFailed    = 3  // Failed permanently
```

---

## Subpackage: `idempotency`

```go
import "github.com/tranvictor/walletarmy/idempotency"

// Store interface
type Store interface {
    Get(key string) (*Record, error)
    Create(key string) (*Record, error)
    Update(record *Record) error
    Delete(key string) error
}

// Built-in implementation
func NewInMemoryStore(ttl time.Duration) *InMemoryStore
func (s *InMemoryStore) Stop()   // Stop cleanup goroutine
func (s *InMemoryStore) Size() int

// Record holds idempotency state
type Record struct {
    Key         string
    Status      Status
    TxHash      common.Hash
    Transaction *types.Transaction
    Receipt     *types.Receipt
    Error       error
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

---

## Persistence Interfaces (for crash recovery)

### NonceStore

```go
type NonceStore interface {
    Get(ctx, wallet, chainID) (*NonceState, error)
    Save(ctx, *NonceState) error
    SavePendingNonce(ctx, wallet, chainID, nonce) error
    AddReservedNonce(ctx, wallet, chainID, nonce) error
    RemoveReservedNonce(ctx, wallet, chainID, nonce) error
    ListAll(ctx) ([]*NonceState, error)
}
```

### TxStore

```go
type TxStore interface {
    Save(ctx, *PendingTx) error
    Get(ctx, hash) (*PendingTx, error)
    GetByNonce(ctx, wallet, chainID, nonce) ([]*PendingTx, error)
    ListPending(ctx, wallet, chainID) ([]*PendingTx, error)
    ListAllPending(ctx) ([]*PendingTx, error)
    UpdateStatus(ctx, hash, status, receipt) error
    Delete(ctx, hash) error
    DeleteOlderThan(ctx, age) (int, error)
}
```

---

## ErrorDecoder

```go
func NewErrorDecoder(abis ...abi.ABI) (*ErrorDecoder, error)
func (d *ErrorDecoder) Decode(err error) (abiError *abi.Error, errorParams any, resultErr error)
```

## GasInfo

```go
type GasInfo struct {
    GasPrice         float64    // Legacy gas price (gwei)
    BaseGasPrice     *big.Int   // Base fee (wei)
    MaxPriorityPrice float64    // EIP-1559 priority fee (gwei)
    FeePerGas        float64    // EIP-1559 max fee per gas (gwei)
    Timestamp        time.Time  // When fetched (TTL: 60s)
}
```
