package walletarmy

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/tranvictor/jarvis/networks"
)

func TestTxExecutionContext_BumpGasForSlowTx(t *testing.T) {
	ctx := &TxExecutionContext{
		Gas: GasBounds{
			ExtraGasPrice: 10.0,
			ExtraTipCap:   5.0,
			MaxGasPrice:   0, // No limit
			MaxTipCap:     0, // No limit
		},
	}

	// Test with nil transaction
	result := ctx.BumpGasForSlowTx(nil)
	if result != false {
		t.Error("BumpGasForSlowTx should return false for nil transaction")
	}
	if ctx.State.GasPrice != 0 || ctx.State.TipCap != 0 {
		t.Error("BumpGasForSlowTx should not modify values when tx is nil")
	}

	// Test with valid transaction
	gasPrice := big.NewInt(100000000000) // 100 Gwei
	tipCap := big.NewInt(50000000000)    // 50 Gwei
	tx := types.NewTx(&types.DynamicFeeTx{
		GasFeeCap: gasPrice,
		GasTipCap: tipCap,
		Nonce:     5,
	})

	result = ctx.BumpGasForSlowTx(tx)
	if result != true {
		t.Error("BumpGasForSlowTx should return true for successful adjustment")
	}

	expectedGasPrice := 100.0 * DefaultGasPriceBumpFactor // (100 * 1.2) = 120
	expectedTipCap := 50.0 * DefaultTipCapBumpFactor      // (50 * 1.1) = 55

	const epsilon = 0.0001
	if diff := ctx.State.GasPrice - expectedGasPrice; diff < -epsilon || diff > epsilon {
		t.Errorf("Expected retry gas price %f, got %f", expectedGasPrice, ctx.State.GasPrice)
	}

	if diff := ctx.State.TipCap - expectedTipCap; diff < -epsilon || diff > epsilon {
		t.Errorf("Expected retry tip cap %f, got %f", expectedTipCap, ctx.State.TipCap)
	}

	if ctx.State.Nonce.Cmp(big.NewInt(5)) != 0 {
		t.Errorf("Expected retry nonce 5, got %s", ctx.State.Nonce.String())
	}

	// Test with gas price limit that allows adjustment
	ctxWithLimit := &TxExecutionContext{
		Gas: GasBounds{
			ExtraGasPrice: 10.0,
			ExtraTipCap:   5.0,
			MaxGasPrice:   130.0, // Set a limit higher than adjusted price (120)
			MaxTipCap:     0,     // No tip cap limit
		},
	}

	result = ctxWithLimit.BumpGasForSlowTx(tx)
	if result != true {
		t.Error("Expected adjustment to succeed when below gas price limit")
	}

	// This should fail due to gas price limit reached
	ctxWithLowLimit := &TxExecutionContext{
		Gas: GasBounds{
			ExtraGasPrice: 10.0,
			ExtraTipCap:   5.0,
			MaxGasPrice:   115.0, // Lower than 120 (100 * 1.2)
			MaxTipCap:     0,     // No tip cap limit
		},
	}

	result = ctxWithLowLimit.BumpGasForSlowTx(tx)
	if result != false {
		t.Error("Expected adjustment to fail when gas price limit reached")
	}
}

// TestTxExecutionContext_AdjustGasPricesForSlowTx tests the deprecated alias
func TestTxExecutionContext_AdjustGasPricesForSlowTx(t *testing.T) {
	ctx := &TxExecutionContext{
		Gas: GasBounds{
			MaxGasPrice: 0,
			MaxTipCap:   0,
		},
	}

	gasPrice := big.NewInt(100000000000) // 100 Gwei
	tipCap := big.NewInt(50000000000)    // 50 Gwei
	tx := types.NewTx(&types.DynamicFeeTx{
		GasFeeCap: gasPrice,
		GasTipCap: tipCap,
		Nonce:     5,
	})

	// Deprecated alias should work the same
	result := ctx.AdjustGasPricesForSlowTx(tx)
	if result != true {
		t.Error("AdjustGasPricesForSlowTx (deprecated alias) should return true")
	}
}

func TestNewTxExecutionContext_Validation(t *testing.T) {
	// Use mainnet for testing
	network, err := networks.GetNetwork("mainnet")
	if err != nil {
		t.Fatalf("Failed to get mainnet network: %v", err)
	}
	from := common.HexToAddress("0x1234567890123456789012345678901234567890")

	// Test with negative retries - should be set to 0
	ctx, err := NewTxExecutionContext(
		TxParams{From: from, Network: network},
		RetryConfig{MaxAttempts: -1},
		GasBounds{},
		TxHooks{},
		0, 0,
	)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if ctx.Retry.MaxAttempts != 0 {
		t.Errorf("Expected retries to be 0 when negative value passed, got %d", ctx.Retry.MaxAttempts)
	}

	if ctx.Retry.SleepDuration != DefaultSleepDuration {
		t.Errorf("Expected default sleep duration %v, got %v", DefaultSleepDuration, ctx.Retry.SleepDuration)
	}

	if ctx.Retry.TxCheckInterval != DefaultTxCheckInterval {
		t.Errorf("Expected default tx check interval %v, got %v", DefaultTxCheckInterval, ctx.Retry.TxCheckInterval)
	}

	if ctx.Params.Value == nil {
		t.Error("Value should be initialized to zero when nil")
	}

	// Test error conditions
	t.Run("zero from address", func(t *testing.T) {
		_, err := NewTxExecutionContext(
			TxParams{From: common.Address{}, Network: network},
			RetryConfig{MaxAttempts: 1, SleepDuration: time.Second, TxCheckInterval: time.Second},
			GasBounds{},
			TxHooks{},
			0, 0,
		)
		if err != ErrFromAddressZero {
			t.Errorf("Expected ErrFromAddressZero, got %v", err)
		}
	})

	t.Run("nil network", func(t *testing.T) {
		_, err := NewTxExecutionContext(
			TxParams{From: from, Network: nil},
			RetryConfig{MaxAttempts: 1, SleepDuration: time.Second, TxCheckInterval: time.Second},
			GasBounds{},
			TxHooks{},
			0, 0,
		)
		if err != ErrNetworkNil {
			t.Errorf("Expected ErrNetworkNil, got %v", err)
		}
	})
}
