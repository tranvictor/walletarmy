// racing runs two independent WalletManager instances in parallel using the
// same ephemeral wallet on the same chain. It validates that the optimistic
// nonce retry logic correctly handles every contention scenario.
//
// Flow:
//  1. Generate a fresh private key and print the address.
//  2. Wait for the tester to fund the wallet (polls every 1s).
//  3. Split N total transactions across 2 independent WalletManager instances,
//     each sending funds back to a configurable return address.
//  4. After the race, sweep any remaining balance back.
//  5. Report results: nonce correctness, timing, and instance skew.
//
// Usage:
//
//	go run ./examples/racing [flags]
//
// Flags:
//
//	-txs        Total number of race transactions (split across 2 instances, default 10)
//	-chain      Chain name: "rise-testnet" (default)
//	-return-to  Address to send funds back to (required)
//	-timeout    Maximum time for the entire test (default 5m)
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/tranvictor/jarvis/networks"
	"github.com/tranvictor/jarvis/util"
	"github.com/tranvictor/jarvis/util/account"

	"github.com/tranvictor/walletarmy"
)

type chainConfig struct {
	Network networks.Network
	TxType  uint8
}

var chainConfigs = map[string]chainConfig{}

func init() {
	riseTestnet := networks.NewGenericOptimismNetwork(networks.GenericOptimismNetworkConfig{
		Name:               "rise-testnet",
		AlternativeNames:   []string{"rise"},
		ChainID:            11155931,
		NativeTokenSymbol:  "ETH",
		NativeTokenDecimal: 18,
		BlockTime:          1,
		NodeVariableName:   "RISE_TESTNET_NODE",
		DefaultNodes: map[string]string{
			"rise-public": "https://testnet.riselabs.xyz",
		},
		MultiCallContractAddress: common.HexToAddress("0xcA11bde05977b3631167028862bE2a173976CA11"),
		SyncTxSupported:          true,
	})

	chainConfigs["rise-testnet"] = chainConfig{
		Network: riseTestnet,
		TxType:  types.DynamicFeeTxType,
	}
}

func main() {
	totalTxs := flag.Int("txs", 10, "total race transactions (split across 2 instances)")
	chainName := flag.String("chain", "rise-testnet", "chain to test on")
	returnTo := flag.String("return-to", "", "address to return funds to (required)")
	pk := flag.String("pk", "", "private key hex (optional; generates ephemeral key if omitted)")
	timeout := flag.Duration("timeout", 5*time.Minute, "max duration for the entire test")
	flag.Parse()

	if *returnTo == "" {
		fmt.Fprintln(os.Stderr, "-return-to flag is required")
		flag.Usage()
		os.Exit(1)
	}
	returnAddr := common.HexToAddress(*returnTo)

	cfg, ok := chainConfigs[*chainName]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown chain %q, available:", *chainName)
		for k := range chainConfigs {
			fmt.Fprintf(os.Stderr, " %s", k)
		}
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}

	var pkHex string
	if *pk != "" {
		pkHex = *pk
	} else {
		privateKey, err := crypto.GenerateKey()
		if err != nil {
			fmt.Fprintf(os.Stderr, "generate key: %v\n", err)
			os.Exit(1)
		}
		pkHex = hex.EncodeToString(crypto.FromECDSA(privateKey))
	}
	acc, err := account.NewPrivateKeyAccount(pkHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad private key: %v\n", err)
		os.Exit(1)
	}
	wallet := acc.Address()

	txsA := *totalTxs / 2
	txsB := *totalTxs - txsA

	fmt.Printf("Chain:      %s (chainID %d)\n", cfg.Network.GetName(), cfg.Network.GetChainID())
	fmt.Printf("Return to:  %s\n", returnAddr.Hex())
	fmt.Printf("Txs:        %d total (%d for A, %d for B)\n", *totalTxs, txsA, txsB)
	fmt.Printf("Private key: %s\n", pkHex)
	fmt.Printf("\n=== Fund this address to start the test ===\n")
	fmt.Printf("    %s\n", wallet.Hex())
	fmt.Printf("=== Waiting for balance... ===\n\n")

	reader, err := util.EthReader(cfg.Network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reader: %v\n", err)
		os.Exit(1)
	}

	// Poll until funded
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var balance *big.Int
	for {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "timed out waiting for funding")
			os.Exit(1)
		}
		balance, err = reader.GetBalance(wallet.Hex())
		if err != nil {
			fmt.Fprintf(os.Stderr, "get balance: %v (retrying)\n", err)
		} else if balance.Sign() > 0 {
			fmt.Printf("Funded! Balance: %s wei\n", balance.String())
			break
		}
		time.Sleep(1 * time.Second)
	}

	startNonce, err := reader.GetMinedNonce(wallet.Hex())
	if err != nil {
		fmt.Fprintf(os.Stderr, "get mined nonce: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Starting mined nonce: %d\n", startNonce)

	// Estimate per-tx value: reserve gas budget, distribute the rest.
	// 30k gas per tx at a generous gas price should cover simple transfers.
	gasPerTx := big.NewInt(30000)
	gasPriceBuffer := big.NewInt(10_000_000_000) // 10 gwei buffer per tx
	// Reserve enough for race txs + 1 sweep tx
	gasReserve := new(big.Int).Mul(gasPerTx, gasPriceBuffer)
	gasReserve.Mul(gasReserve, big.NewInt(int64(*totalTxs+1)))
	// Be conservative: also reserve 10% of balance for gas fluctuations
	tenPercent := new(big.Int).Div(balance, big.NewInt(10))
	totalReserve := new(big.Int).Add(gasReserve, tenPercent)

	distributable := new(big.Int).Sub(balance, totalReserve)
	if distributable.Sign() <= 0 {
		fmt.Fprintln(os.Stderr, "balance too low to cover gas for all txs")
		os.Exit(1)
	}
	amountPerTx := new(big.Int).Div(distributable, big.NewInt(int64(*totalTxs)))
	fmt.Printf("Amount per tx: %s wei\n", amountPerTx.String())

	// Build two independent WalletManager instances
	newWM := func() *walletarmy.WalletManager {
		wm := walletarmy.NewWalletManager(
			walletarmy.WithDefaultNetwork(cfg.Network),
			walletarmy.WithDefaultNumRetries(20),
			walletarmy.WithDefaultSleepDuration(2*time.Second),
			walletarmy.WithDefaultTxCheckInterval(1*time.Second),
			walletarmy.WithDefaultSlowTxTimeout(10*time.Second),
			walletarmy.WithNetworkResolver(func(chainID uint64) (networks.Network, error) {
				if chainID == cfg.Network.GetChainID() {
					return cfg.Network, nil
				}
				return networks.GetNetworkByID(chainID)
			}),
		)
		wm.SetAccount(acc)
		return wm
	}

	wmA := newWM()
	wmB := newWM()

	type instanceResult struct {
		Label    string
		Duration time.Duration
		Errors   []error
	}

	var mu sync.Mutex
	results := make([]instanceResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	runInstance := func(idx int, label string, wm *walletarmy.WalletManager, numTxs int) {
		defer wg.Done()
		start := time.Now()
		var errs []error

		for i := 0; i < numTxs; i++ {
			if ctx.Err() != nil {
				errs = append(errs, fmt.Errorf("tx %d: context cancelled: %w", i, ctx.Err()))
				break
			}

			tx, receipt, txErr := wm.R().
				SetFrom(wallet).
				SetTo(returnAddr).
				SetValue(amountPerTx).
				SetTxType(cfg.TxType).
				SetSkipSimulation(true).
				SetGasLimit(30000).
				ExecuteContext(ctx)

			if txErr != nil {
				errs = append(errs, fmt.Errorf("[%s] tx %d failed: %w", label, i, txErr))
				fmt.Printf("[%s] tx %d FAILED: %v\n", label, i, txErr)
				continue
			}

			status := "success"
			if receipt != nil && receipt.Status == 0 {
				status = "reverted"
			}
			fmt.Printf("[%s] tx %d mined (%s) hash=%s nonce=%d\n",
				label, i, status, tx.Hash().Hex(), tx.Nonce())
		}

		mu.Lock()
		results[idx] = instanceResult{
			Label:    label,
			Duration: time.Since(start),
			Errors:   errs,
		}
		mu.Unlock()
	}

	fmt.Println("\n=== Starting race ===")
	totalStart := time.Now()

	go runInstance(0, "A", wmA, txsA)
	go runInstance(1, "B", wmB, txsB)
	wg.Wait()

	totalDuration := time.Since(totalStart)

	// Sweep remaining balance back
	fmt.Println("\n=== Sweeping remaining balance ===")
	remaining, err := reader.GetBalance(wallet.Hex())
	if err != nil {
		fmt.Fprintf(os.Stderr, "get remaining balance: %v\n", err)
	} else if remaining.Sign() > 0 {
		sweepGasCost := new(big.Int).Mul(big.NewInt(21000), gasPriceBuffer)
		sweepAmount := new(big.Int).Sub(remaining, sweepGasCost)
		if sweepAmount.Sign() > 0 {
			sweepWM := newWM()
			tx, _, sweepErr := sweepWM.R().
				SetFrom(wallet).
				SetTo(returnAddr).
				SetValue(sweepAmount).
				SetTxType(cfg.TxType).
				SetSkipSimulation(true).
				SetGasLimit(21000).
				ExecuteContext(ctx)
			if sweepErr != nil {
				fmt.Printf("Sweep failed: %v\n", sweepErr)
			} else {
				fmt.Printf("Swept %s wei back to %s (tx %s)\n",
					sweepAmount.String(), returnAddr.Hex(), tx.Hash().Hex())
			}
		} else {
			fmt.Println("Remaining balance too low to sweep (dust)")
		}
	}

	// ── Results ──────────────────────────────────────────────────
	fmt.Println("\n=== Results ===")
	fmt.Printf("Total wall-clock time: %.2fs\n\n", totalDuration.Seconds())

	allPassed := true

	for _, r := range results {
		fmt.Printf("Instance %s: %.2fs, %d errors\n", r.Label, r.Duration.Seconds(), len(r.Errors))
		for _, e := range r.Errors {
			fmt.Printf("  - %v\n", e)
		}
		if len(r.Errors) > 0 {
			allPassed = false
		}
	}

	// Check 1: mined nonce advanced correctly
	endNonce, err := reader.GetMinedNonce(wallet.Hex())
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nFailed to read end nonce: %v\n", err)
		os.Exit(1)
	}
	// +1 for the sweep tx
	expectedNonce := startNonce + uint64(*totalTxs) + 1
	fmt.Printf("\nNonce check: start=%d end=%d expected=%d\n", startNonce, endNonce, expectedNonce)
	if endNonce < expectedNonce {
		fmt.Printf("FAIL: mined nonce %d < expected %d — some txs were not mined\n", endNonce, expectedNonce)
		allPassed = false
	} else {
		fmt.Println("PASS: all transactions mined")
	}

	// Check 2: total time
	fmt.Printf("\nTiming check: total=%.2fs\n", totalDuration.Seconds())
	if totalDuration > *timeout {
		fmt.Println("FAIL: exceeded timeout")
		allPassed = false
	} else {
		fmt.Println("PASS: within timeout")
	}

	// Check 3: instance skew — neither should take more than 3× the other
	durationA := results[0].Duration
	durationB := results[1].Duration
	skewRatio := float64(0)
	if durationA > durationB && durationB > 0 {
		skewRatio = float64(durationA) / float64(durationB)
	} else if durationA > 0 {
		skewRatio = float64(durationB) / float64(durationA)
	}
	fmt.Printf("\nSkew check: A=%.2fs B=%.2fs ratio=%.2f\n",
		durationA.Seconds(), durationB.Seconds(), skewRatio)
	if skewRatio > 3.0 {
		fmt.Printf("FAIL: skew ratio %.2f > 3.0 — one instance was starved\n", skewRatio)
		allPassed = false
	} else {
		fmt.Println("PASS: both instances finished in similar time")
	}

	// Final verdict
	fmt.Println()
	if allPassed {
		fmt.Println("ALL CHECKS PASSED")
	} else {
		fmt.Println("SOME CHECKS FAILED")
		os.Exit(1)
	}
}
