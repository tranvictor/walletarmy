# WalletArmy — Cursor Agent Skill

## When to Use

Use walletarmy when the user wants to:
- Manage Ethereum wallets and send transactions from Go code
- Sign transactions with private keys, keystore files, or hardware wallets (Ledger, Trezor)
- Handle nonce management, gas estimation, retries, and monitoring automatically
- Build resilient transaction pipelines with hooks, idempotency, and circuit breakers
- Support multiple EVM networks (Ethereum, Matic/Polygon, Arbitrum, Optimism, custom chains)
- Recover in-flight transactions after a crash or restart

## Installation

```bash
go get github.com/tranvictor/walletarmy
```

## Import Paths

```go
import (
    "github.com/tranvictor/walletarmy"                // main package
    "github.com/tranvictor/walletarmy/idempotency"    // idempotency store
    "github.com/tranvictor/jarvis/networks"            // network constants + custom network constructors
    "github.com/tranvictor/jarvis/util/account"        // account management (private key, keystore, Ledger, Trezor)
    "github.com/ethereum/go-ethereum/common"           // common.Address
    "github.com/ethereum/go-ethereum/core/types"       // types.Transaction, types.Receipt
    "github.com/ethereum/go-ethereum/accounts/abi"     // abi.ABI for hooks
)
```

## Quick Start — Minimal ETH Transfer

```go
package main

import (
    "fmt"
    "math/big"

    "github.com/ethereum/go-ethereum/common"
    "github.com/tranvictor/jarvis/networks"
    "github.com/tranvictor/jarvis/util/account"
    "github.com/tranvictor/walletarmy"
)

func main() {
    // 1. Create manager
    wm := walletarmy.NewWalletManager(
        walletarmy.WithDefaultNetwork(networks.EthereumMainnet),
    )

    // 2. Register a wallet (from private key)
    acc, _ := account.NewPrivateKeyAccount("your_hex_private_key_without_0x")
    wm.SetAccount(acc)

    // 3. Send ETH using the builder pattern
    tx, receipt, err := wm.R().
        SetFrom(acc.Address()).
        SetTo(common.HexToAddress("0xRecipient")).
        SetValue(big.NewInt(1e18)). // 1 ETH in wei
        Execute()

    if err != nil {
        fmt.Printf("Transaction failed: %v\n", err)
        return
    }
    fmt.Printf("Mined in block %d, tx hash: %s\n", receipt.BlockNumber, tx.Hash().Hex())
}
```

## Builder Pattern (TxRequest)

All transactions use the builder pattern via `wm.R()`:

```go
tx, receipt, err := wm.R().
    SetFrom(wallet).                          // required: sender address
    SetTo(recipient).                         // required: recipient address
    SetValue(amount).                         // optional: ETH value (default: 0)
    SetData(calldata).                        // optional: contract call data
    SetNetwork(networks.EthereumMainnet).     // optional if default set
    SetGasLimit(21000).                       // optional: override gas estimation
    SetMaxGasPrice(100.0).                    // optional: gas price cap in gwei
    SetNumRetries(5).                         // optional: retry count
    SetIdempotencyKey("unique-key").          // optional: prevent duplicates
    SetBeforeSignAndBroadcastHook(myHook).    // optional: pre-broadcast hook
    SetAfterSignAndBroadcastHook(myHook).     // optional: post-broadcast hook
    SetGasEstimationFailedHook(gasHook).      // optional: handle gas estimation failure
    SetSimulationFailedHook(simHook).         // optional: handle simulation failure
    SetTxMinedHook(minedHook).                // optional: handle tx mined event
    SetAbis(contractABI).                     // optional: for error decoding in hooks
    Execute()                                 // or ExecuteContext(ctx) for cancellation
```

### Builder Methods

| Method | Type | Description |
|--------|------|-------------|
| `SetFrom(addr)` | `common.Address` | Sender wallet (required) |
| `SetTo(addr)` | `common.Address` | Recipient address |
| `SetValue(v)` | `*big.Int` | ETH value in wei |
| `SetData(d)` | `[]byte` | Calldata for contract calls |
| `SetNetwork(n)` | `networks.Network` | Target network |
| `SetTxType(t)` | `uint8` | Transaction type (0=legacy, 2=EIP-1559) |
| `SetGasLimit(g)` | `uint64` | Fixed gas limit (skips estimation) |
| `SetExtraGasLimit(g)` | `uint64` | Extra gas added to estimate |
| `SetGasPrice(p)` | `float64` | Fixed gas price in gwei |
| `SetExtraGasPrice(p)` | `float64` | Extra gas price in gwei |
| `SetTipCapGwei(t)` | `float64` | EIP-1559 tip cap in gwei |
| `SetExtraTipCapGwei(t)` | `float64` | Extra tip cap in gwei |
| `SetMaxGasPrice(p)` | `float64` | Gas price protection limit in gwei |
| `SetMaxTipCap(t)` | `float64` | Tip cap protection limit in gwei |
| `SetNumRetries(n)` | `int` | Max retry attempts |
| `SetSleepDuration(d)` | `time.Duration` | Sleep between retries |
| `SetTxCheckInterval(d)` | `time.Duration` | Polling interval for tx status |
| `SetIdempotencyKey(k)` | `string` | Unique key for dedup |
| `SetAbis(a...)` | `abi.ABI` | ABIs for error decoding |
| `Execute()` | — | Execute (background context) |
| `ExecuteContext(ctx)` | `context.Context` | Execute with cancellation |

## Common Recipes

### Contract Call with Error Decoding

```go
contractABI, _ := abi.JSON(strings.NewReader(myContractABIJSON))
calldata, _ := contractABI.Pack("transfer", recipient, amount)

tx, receipt, err := wm.R().
    SetFrom(wallet).
    SetTo(contractAddr).
    SetData(calldata).
    SetAbis(contractABI).
    Execute()

// Decoded revert info is in the err chain — no hooks needed
var decoded *walletarmy.DecodedError
if errors.As(err, &decoded) && decoded.AbiError != nil {
    fmt.Printf("Revert: %s, params: %v\n", decoded.AbiError.Name, decoded.RevertParams)
}
```

### Idempotent Transaction (Prevent Duplicates)

```go
wm := walletarmy.NewWalletManager(
    walletarmy.WithDefaultIdempotencyStore(1 * time.Hour),
)

// Same key → same result, second call returns cached result
tx, receipt, err := wm.R().
    SetFrom(wallet).
    SetTo(recipient).
    SetValue(amount).
    SetIdempotencyKey("payment-order-12345").
    Execute()
```

### Account Creation (Multiple Backends)

Accounts come from `github.com/tranvictor/jarvis/util/account`. All account types expose the
same `SignTx(tx, chainID)` method — WalletArmy doesn't need to know the signing backend.

```go
import "github.com/tranvictor/jarvis/util/account"

// Private key (hex without 0x prefix)
acc, _ := account.NewPrivateKeyAccount("abc123...")

// Encrypted keystore file
acc, _ := account.NewKeystoreAccount("/path/to/keystore.json", "passphrase")

// Hardware wallets (derivation path + expected address)
acc, _ := account.NewLedgerAccount("m/44'/60'/0'/0/0", "0xYourLedgerAddr")
acc, _ := account.NewTrezorAccount("m/44'/60'/0'/0/0", "0xYourTrezorAddr")

// Jarvis wallet store (auto-detects backend)
acc, _ := wm.UnlockAccount(common.HexToAddress("0x..."))

wm.SetAccount(acc) // register any account type
```

### Multi-Network Setup

```go
wm := walletarmy.NewWalletManager(
    walletarmy.WithDefaultNetwork(networks.EthereumMainnet),
    walletarmy.WithDefaultMaxGasPrice(100.0),
    walletarmy.WithDefaultNumRetries(5),
)

// Use default network (Ethereum)
wm.R().SetFrom(wallet).SetTo(to).SetValue(amount).Execute()

// Override for Matic/Polygon
wm.R().SetFrom(wallet).SetTo(to).SetValue(amount).
    SetNetwork(networks.Matic).Execute()

// Override for Arbitrum
wm.R().SetFrom(wallet).SetTo(to).SetValue(amount).
    SetNetwork(networks.ArbitrumMainnet).Execute()
```

### Custom Network

For private chains or unsupported L2s, create a custom network using jarvis constructors:

```go
import "github.com/tranvictor/jarvis/networks"

// Standard EVM chain (Etherscan-compatible block explorer)
customNet := networks.NewGenericEtherscanNetwork(networks.GenericEtherscanNetworkConfig{
    Name: "my-chain", ChainID: 12345,
    NativeTokenSymbol: "ETH", NativeTokenDecimal: 18, BlockTime: 2,
    DefaultNodes: map[string]string{"default": "https://rpc.my-chain.io"},
})

// Optimism-based L2 (supports sync tx)
customL2 := networks.NewGenericOptimismNetwork(networks.GenericOptimismNetworkConfig{
    Name: "my-op-l2", ChainID: 67890,
    NativeTokenSymbol: "ETH", NativeTokenDecimal: 18, BlockTime: 1,
    DefaultNodes: map[string]string{"default": "https://rpc.my-op-l2.io"},
    SyncTxSupported: true,
})

// Register a resolver so WalletArmy can look up your custom network by chain ID
// (needed for crash recovery and raw BroadcastTx calls)
wm := walletarmy.NewWalletManager(
    walletarmy.WithNetworkResolver(func(chainID uint64) (networks.Network, error) {
        if chainID == 12345 { return customNet, nil }
        return networks.GetNetworkByID(chainID) // fallback to built-in
    }),
)

wm.R().SetFrom(wallet).SetTo(to).SetValue(amount).SetNetwork(customNet).Execute()
```

### Hooks — Before/After Broadcast

```go
tx, receipt, err := wm.R().
    SetFrom(wallet).
    SetTo(to).
    SetValue(amount).
    SetBeforeSignAndBroadcastHook(func(tx *types.Transaction, err error) error {
        fmt.Printf("About to broadcast tx with nonce %d\n", tx.Nonce())
        return nil // return error to abort
    }).
    SetAfterSignAndBroadcastHook(func(tx *types.Transaction, err error) error {
        fmt.Printf("Broadcast tx %s\n", tx.Hash().Hex())
        return nil
    }).
    SetTxMinedHook(func(tx *types.Transaction, receipt *types.Receipt) error {
        fmt.Printf("Mined! status=%d gas=%d\n", receipt.Status, receipt.GasUsed)
        return nil
    }).
    Execute()
```

### Crash Recovery with Persistence

```go
wm := walletarmy.NewWalletManager(
    walletarmy.WithNonceStore(myNonceStoreImpl),
    walletarmy.WithTxStore(myTxStoreImpl),
)

// On startup: recover pending transactions
result, err := wm.Recover(ctx)
fmt.Printf("Recovered %d txs, %d mined, %d dropped\n",
    result.RecoveredTxs, result.MinedTxs, result.DroppedTxs)
```

### Circuit Breaker (Automatic Network Health)

```go
// Circuit breakers are automatic per-network. Check stats:
stats := wm.GetCircuitBreakerStats(networks.EthereumMainnet)
fmt.Printf("State: %s, Failures: %d\n", stats.State, stats.ConsecutiveFailures)

// Manual reset if needed:
wm.ResetCircuitBreaker(networks.EthereumMainnet)
```

## Constructor Options (`WithXxx`)

```go
wm := walletarmy.NewWalletManager(
    // Defaults inherited by every TxRequest
    walletarmy.WithDefaultNetwork(networks.EthereumMainnet),
    walletarmy.WithDefaultNumRetries(9),
    walletarmy.WithDefaultSleepDuration(5 * time.Second),
    walletarmy.WithDefaultTxCheckInterval(5 * time.Second),
    walletarmy.WithDefaultSlowTxTimeout(5 * time.Second), // starts after first monitor check, not broadcast
    walletarmy.WithDefaultExtraGasLimit(0),
    walletarmy.WithDefaultExtraGasPrice(0),
    walletarmy.WithDefaultExtraTipCap(0),
    walletarmy.WithDefaultMaxGasPrice(0),        // 0 = auto (5x suggested)
    walletarmy.WithDefaultMaxTipCap(0),           // 0 = auto (5x suggested)
    walletarmy.WithDefaultTxType(0),              // 0 = legacy, 2 = EIP-1559
    walletarmy.WithDefaultGasPriceIncreasePercent(1.2), // 20% bump on slow/lost blocking tx
    walletarmy.WithDefaultTipCapIncreasePercent(1.1),   // 10% bump on slow/lost blocking tx

    // Or set all defaults at once
    walletarmy.WithDefaults(walletarmy.ManagerDefaults{...}),

    // Idempotency
    walletarmy.WithDefaultIdempotencyStore(1 * time.Hour),
    walletarmy.WithIdempotencyStore(customStore),

    // Persistence (for crash recovery)
    walletarmy.WithNonceStore(myNonceStore),
    walletarmy.WithTxStore(myTxStore),

    // Custom network resolver (for private chains)
    walletarmy.WithNetworkResolver(func(chainID uint64) (networks.Network, error) {
        // ...
    }),

    // Testing: inject mock factories
    walletarmy.WithReaderFactory(mockReaderFactory),
    walletarmy.WithBroadcasterFactory(mockBroadcasterFactory),
    walletarmy.WithTxMonitorFactory(mockMonitorFactory),
)
```

## Gotchas

1. **Nonce management is automatic** — do NOT manually set nonces. WalletArmy tracks nonces per wallet per network and handles races.
2. **`SetFrom` is required** — `Execute()` returns `ErrFromAddressZero` without it.
3. **Network defaults to Ethereum mainnet** — if not set via `WithDefaultNetwork` or `SetNetwork`.
4. **Gas limits are estimated** — only set `SetGasLimit` if you need a fixed value. Use `SetExtraGasLimit` to add a buffer.
5. **Gas price protection** — `SetMaxGasPrice` / `SetMaxTipCap` prevent runaway costs. When 0, defaults to 5x the suggested price.
6. **Circuit breaker** — if an RPC node fails repeatedly, the circuit opens and returns `ErrCircuitBreakerOpen`. It auto-recovers.
7. **Idempotency requires a store** — `SetIdempotencyKey` is a no-op without `WithDefaultIdempotencyStore` or `WithIdempotencyStore`.
8. **Slow tx handling** — "slow" is a walletarmy-internal concept (not from the node/monitor): the slow timer starts after the monitor confirms the tx is still pending (not from broadcast time), then fires after `SlowTxTimeout`. Gas is only bumped if the tx's nonce is the blocking nonce (next nonce the chain expects). Non-blocking slow txs just keep waiting.
9. **Thread safety** — `WalletManager` is safe for concurrent use from multiple goroutines. Each `R()` call creates an independent request.
10. **`GasEstimationFailedHook` requires ABIs** — the hook's `abiError`/`revertParams` args are nil unless you set `SetAbis(...)` on the request. However, the `DecodedError` in the returned `err` is always populated when ABIs are provided, regardless of whether the hook is set.
11. **Mined reverts return `ErrTxReverted`** — When a transaction is mined but reverted on-chain, `Execute` returns `ErrTxReverted` alongside the `tx` and `receipt`. Check `receipt.Status` or use `errors.Is(err, walletarmy.ErrTxReverted)`.
12. **Hooks are for control flow** — Use hooks to decide retry/abort behavior. Use `errors.As(err, &DecodedError{})` on the returned error to inspect decoded revert reasons.

## Key Error Sentinels

```go
// Check errors with errors.Is():
walletarmy.ErrTxReverted            // tx mined but reverted (tx + receipt still returned)
walletarmy.ErrFromAddressZero       // from address not set
walletarmy.ErrNetworkNil            // network not set
walletarmy.ErrEstimateGasFailed     // gas estimation failed
walletarmy.ErrAcquireNonceFailed    // couldn't get nonce
walletarmy.ErrGetGasSettingFailed   // couldn't get gas price
walletarmy.ErrEnsureTxOutOfRetries  // exhausted all retries
walletarmy.ErrGasPriceLimitReached  // hit max gas price
walletarmy.ErrSimulatedTxReverted   // eth_call shows revert
walletarmy.ErrSimulatedTxFailed     // simulation failed
walletarmy.ErrCircuitBreakerOpen    // RPC circuit breaker open
walletarmy.ErrSyncBroadcastTimeout  // sync broadcast timed out
idempotency.ErrDuplicateKey         // duplicate idempotency key
```

## Decoded Contract Errors

When ABIs are provided via `SetAbis()`, revert reasons are automatically decoded and
wrapped in the returned `err` as `*walletarmy.DecodedError`. Extract with `errors.As`:

```go
tx, receipt, err := wm.R().
    SetFrom(wallet).SetTo(contract).SetData(data).
    SetAbis(contractABI).
    Execute()

var decoded *walletarmy.DecodedError
if errors.As(err, &decoded) && decoded.AbiError != nil {
    fmt.Printf("Revert: %s(%v)\n", decoded.AbiError.Name, decoded.RevertParams)
}
```

`DecodedError` appears in the error chain for simulation reverts (`ErrSimulatedTxReverted`)
and gas estimation failures (`ErrEstimateGasFailed`). Hooks are for control flow only —
error details are always available through the returned `err`.

## Hook Signatures

```go
// Before/After broadcast (return error to abort)
type Hook func(tx *types.Transaction, err error) error

// When tx is mined (success or revert)
type TxMinedHook func(tx *types.Transaction, receipt *types.Receipt) error

// When gas estimation fails — return gasLimit to override, or error to stop
type GasEstimationFailedHook func(
    tx *types.Transaction,
    abiError *abi.Error,
    revertParams any,
    revertMsgError, gasEstimationError error,
) (gasLimit *big.Int, err error)

// When eth_call simulation fails — return shouldRetry=true to keep trying
type SimulationFailedHook func(
    tx *types.Transaction,
    revertData []byte,
    abiError *abi.Error,
    revertParams any,
    err error,
) (shouldRetry bool, retErr error)
```

## Persistence Interfaces

To enable crash recovery, implement `NonceStore` and `TxStore`:

```go
type NonceStore interface {
    Get(ctx context.Context, wallet common.Address, chainID uint64) (*NonceState, error)
    Save(ctx context.Context, state *NonceState) error
    SavePendingNonce(ctx context.Context, wallet common.Address, chainID uint64, nonce uint64) error
    AddReservedNonce(ctx context.Context, wallet common.Address, chainID uint64, nonce uint64) error
    RemoveReservedNonce(ctx context.Context, wallet common.Address, chainID uint64, nonce uint64) error
    ListAll(ctx context.Context) ([]*NonceState, error)
}

type TxStore interface {
    Save(ctx context.Context, tx *PendingTx) error
    Get(ctx context.Context, hash common.Hash) (*PendingTx, error)
    GetByNonce(ctx context.Context, wallet common.Address, chainID uint64, nonce uint64) ([]*PendingTx, error)
    ListPending(ctx context.Context, wallet common.Address, chainID uint64) ([]*PendingTx, error)
    ListAllPending(ctx context.Context) ([]*PendingTx, error)
    UpdateStatus(ctx context.Context, hash common.Hash, status PendingTxStatus, receipt *types.Receipt) error
    Delete(ctx context.Context, hash common.Hash) error
    DeleteOlderThan(ctx context.Context, age time.Duration) (int, error)
}
```

## See Also

- Full API reference: `docs/llm-reference.md`
- Examples: `examples/` directory
- README: `README.md` (comprehensive human-oriented docs)
