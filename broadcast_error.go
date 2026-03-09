package walletarmy

import (
	"fmt"
	"strings"
)

type BroadcastError error

var (
	ErrInsufficientFund        = BroadcastError(fmt.Errorf("insufficient fund"))
	ErrNonceIsLow              = BroadcastError(fmt.Errorf("nonce is low"))
	ErrReplacementUnderpriced  = BroadcastError(fmt.Errorf("replacement transaction underpriced"))
	ErrGasLimitIsTooLow        = BroadcastError(fmt.Errorf("gas limit is too low"))
	ErrTxIsKnown               = BroadcastError(fmt.Errorf("tx is known"))
)

func NewBroadcastError(err error) BroadcastError {
	if err == nil {
		return nil
	}

	// Check error conditions in priority order.
	// IsReplacementUnderpriced must be checked before IsNonceIsLow because
	// "underpriced" is a more specific condition (nonce is still pending but
	// replacement gas is too low) vs "nonce too low" (nonce already mined).
	if IsInsufficientFund(err) {
		return ErrInsufficientFund
	}
	if IsReplacementUnderpriced(err) {
		return ErrReplacementUnderpriced
	}
	if IsNonceIsLow(err) {
		return ErrNonceIsLow
	}
	if IsGasLimitIsTooLow(err) {
		return ErrGasLimitIsTooLow
	}
	if IsTxIsKnown(err) {
		return ErrTxIsKnown
	}

	return BroadcastError(err)
}

func IsTxIsKnown(err error) bool {
	return strings.Contains(err.Error(), "already known") || strings.Contains(err.Error(), "known transaction")
}

func IsGasLimitIsTooLow(err error) bool {
	hasGasTooLow := strings.Contains(err.Error(), "gas limit") && strings.Contains(err.Error(), "low")
	hasIntrinsicGasTooLow := strings.Contains(err.Error(), "intrinsic gas") && strings.Contains(err.Error(), "low")
	hasGasLimitReach := strings.Contains(err.Error(), "gas limit") && strings.Contains(err.Error(), "reach")
	return hasGasTooLow || hasIntrinsicGasTooLow || hasGasLimitReach
}

func IsReplacementUnderpriced(err error) bool {
	return strings.Contains(err.Error(), "underprice")
}

func IsNonceIsLow(err error) bool {
	hasNonceAndLow := strings.Contains(err.Error(), "nonce") && strings.Contains(err.Error(), "low")
	hasNonceAlreadyExist := strings.Contains(err.Error(), "nonce") && strings.Contains(err.Error(), "already exist")
	return hasNonceAndLow || hasNonceAlreadyExist
}

func IsInsufficientFund(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	hasInsufficientFunds := strings.Contains(errMsg, "insufficient funds")
	hasInsufficientBalance := strings.Contains(errMsg, "insufficient balance")
	hasNotEnoughFunds := strings.Contains(errMsg, "not enough funds")
	hasBalanceTooLow := strings.Contains(errMsg, "balance too low")
	return hasInsufficientFunds || hasInsufficientBalance || hasNotEnoughFunds || hasBalanceTooLow
}
