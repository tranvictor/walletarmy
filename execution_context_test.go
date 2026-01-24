package walletarmy

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/tranvictor/jarvis/networks"
)

func TestTxExecutionContext_AdjustGasPricesForSlowTx(t *testing.T) {
	ctx := &TxExecutionContext{
		ExtraGasPrice:   10.0,
		ExtraTipCapGwei: 5.0,
		MaxGasPrice:     0, // No limit
		MaxTipCap:       0, // No limit
	}

	// Test with nil transaction
	result := ctx.AdjustGasPricesForSlowTx(nil)
	if result != false {
		t.Error("AdjustGasPricesForSlowTx should return false for nil transaction")
	}
	if ctx.RetryGasPrice != 0 || ctx.RetryTipCap != 0 {
		t.Error("AdjustGasPricesForSlowTx should not modify values when tx is nil")
	}

	// Test with valid transaction
	gasPrice := big.NewInt(100000000000) // 100 Gwei
	tipCap := big.NewInt(50000000000)    // 50 Gwei
	tx := types.NewTx(&types.DynamicFeeTx{
		GasFeeCap: gasPrice,
		GasTipCap: tipCap,
		Nonce:     5,
	})

	result = ctx.AdjustGasPricesForSlowTx(tx)
	if result != true {
		t.Error("AdjustGasPricesForSlowTx should return true for successful adjustment")
	}

	expectedGasPrice := 100.0 * DefaultGasPriceIncreasePercent // (100 * 1.2) = 120
	expectedTipCap := 50.0 * DefaultTipCapIncreasePercent      // (50 * 1.1) = 55

	const epsilon = 0.0001
	if diff := ctx.RetryGasPrice - expectedGasPrice; diff < -epsilon || diff > epsilon {
		t.Errorf("Expected retry gas price %f, got %f", expectedGasPrice, ctx.RetryGasPrice)
	}

	if diff := ctx.RetryTipCap - expectedTipCap; diff < -epsilon || diff > epsilon {
		t.Errorf("Expected retry tip cap %f, got %f", expectedTipCap, ctx.RetryTipCap)
	}

	if ctx.RetryNonce.Cmp(big.NewInt(5)) != 0 {
		t.Errorf("Expected retry nonce 5, got %s", ctx.RetryNonce.String())
	}

	// Test with gas price limit that allows adjustment
	ctxWithLimit := &TxExecutionContext{
		ExtraGasPrice:   10.0,
		ExtraTipCapGwei: 5.0,
		MaxGasPrice:     130.0, // Set a limit higher than adjusted price (120)
		MaxTipCap:       0,     // No tip cap limit
	}

	result = ctxWithLimit.AdjustGasPricesForSlowTx(tx)
	if result != true {
		t.Error("Expected adjustment to succeed when below gas price limit")
	}

	// This should fail due to gas price limit reached
	ctxWithLowLimit := &TxExecutionContext{
		ExtraGasPrice:   10.0,
		ExtraTipCapGwei: 5.0,
		MaxGasPrice:     115.0, // Lower than 120 (100 * 1.2)
		MaxTipCap:       0,     // No tip cap limit
	}

	result = ctxWithLowLimit.AdjustGasPricesForSlowTx(tx)
	if result != false {
		t.Error("Expected adjustment to fail when gas price limit reached")
	}
}

func TestNewTxExecutionContext_Validation(t *testing.T) {
	// Use mainnet for testing
	network, err := networks.GetNetwork("mainnet")
	if err != nil {
		t.Fatalf("Failed to get mainnet network: %v", err)
	}
	from := common.HexToAddress("0x1234567890123456789012345678901234567890")
	to := common.HexToAddress("0x0987654321098765432109876543210987654321")

	// Test with negative retries - should be set to 0
	ctx, err := NewTxExecutionContext(
		-1, 0, 0,
		0, from, to, nil,
		0, 0, 0, 0, 0, 0,
		0, 0, // maxGasPrice, maxTipCap
		nil, network,
		nil, nil, nil, nil,
		nil, nil, // simulationFailedHook, txMinedHook
	)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if ctx.NumRetries != 0 {
		t.Errorf("Expected retries to be 0 when negative value passed, got %d", ctx.NumRetries)
	}

	if ctx.SleepDuration != DefaultSleepDuration {
		t.Errorf("Expected default sleep duration %v, got %v", DefaultSleepDuration, ctx.SleepDuration)
	}

	if ctx.TxCheckInterval != DefaultTxCheckInterval {
		t.Errorf("Expected default tx check interval %v, got %v", DefaultTxCheckInterval, ctx.TxCheckInterval)
	}

	if ctx.Value == nil {
		t.Error("Value should be initialized to zero when nil")
	}

	// Test error conditions
	t.Run("zero from address", func(t *testing.T) {
		_, err := NewTxExecutionContext(
			1, time.Second, time.Second,
			0, common.Address{}, to, nil,
			0, 0, 0, 0, 0, 0,
			0, 0, // maxGasPrice, maxTipCap
			nil, network,
			nil, nil, nil, nil,
			nil, nil, // simulationFailedHook, txMinedHook
		)
		if err != ErrFromAddressZero {
			t.Errorf("Expected ErrFromAddressZero, got %v", err)
		}
	})

	t.Run("nil network", func(t *testing.T) {
		_, err := NewTxExecutionContext(
			1, time.Second, time.Second,
			0, from, to, nil,
			0, 0, 0, 0, 0, 0,
			0, 0, // maxGasPrice, maxTipCap
			nil, nil,
			nil, nil, nil, nil,
			nil, nil, // simulationFailedHook, txMinedHook
		)
		if err != ErrNetworkNil {
			t.Errorf("Expected ErrNetworkNil, got %v", err)
		}
	})
}
