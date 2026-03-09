package walletarmy

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDataError implements rpc.DataError for testing
type mockDataError struct {
	data    interface{}
	message string
}

func (e *mockDataError) Error() string {
	return e.message
}

func (e *mockDataError) ErrorData() interface{} {
	return e.data
}

// createTestABI creates a simple ABI with a custom error for testing
func createTestABI(t *testing.T) abi.ABI {
	// Simple ABI JSON with a custom error: error InsufficientBalance(uint256 available, uint256 required)
	const abiJSON = `[{"type":"error","name":"InsufficientBalance","inputs":[{"name":"available","type":"uint256"},{"name":"required","type":"uint256"}]}]`

	parsedABI, err := abi.JSON(strings.NewReader(abiJSON))
	require.NoError(t, err)
	return parsedABI
}

// createMultiErrorABI creates an ABI with multiple errors
func createMultiErrorABI(t *testing.T) abi.ABI {
	const abiJSON = `[
		{"type":"error","name":"InsufficientBalance","inputs":[{"name":"available","type":"uint256"},{"name":"required","type":"uint256"}]},
		{"type":"error","name":"Unauthorized","inputs":[{"name":"caller","type":"address"}]},
		{"type":"error","name":"InvalidAmount","inputs":[]}
	]`

	parsedABI, err := abi.JSON(strings.NewReader(abiJSON))
	require.NoError(t, err)
	return parsedABI
}

func TestNewErrorDecoder_NoABIs_ReturnsError(t *testing.T) {
	decoder, err := NewErrorDecoder()

	assert.Nil(t, decoder)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least one ABI must be provided")
}

func TestNewErrorDecoder_SingleABI_Success(t *testing.T) {
	testABI := createTestABI(t)

	decoder, err := NewErrorDecoder(testABI)

	assert.NoError(t, err)
	assert.NotNil(t, decoder)
	assert.NotEmpty(t, decoder.errorBySelector)
}

func TestNewErrorDecoder_MultipleABIs_MergesErrors(t *testing.T) {
	abi1 := createTestABI(t)
	abi2 := createMultiErrorABI(t)

	decoder, err := NewErrorDecoder(abi1, abi2)

	assert.NoError(t, err)
	assert.NotNil(t, decoder)
	// Should have errors from both ABIs (though some may overlap)
	assert.GreaterOrEqual(t, len(decoder.errorBySelector), 1)
}

func TestNewErrorDecoder_EmptyABI_Success(t *testing.T) {
	// ABI with no errors
	emptyABI := abi.ABI{}

	decoder, err := NewErrorDecoder(emptyABI)

	assert.NoError(t, err)
	assert.NotNil(t, decoder)
	assert.Empty(t, decoder.errorBySelector)
}

func TestErrorDecoder_Decode_NotDataError_ReturnsWrapped(t *testing.T) {
	testABI := createTestABI(t)
	decoder, err := NewErrorDecoder(testABI)
	require.NoError(t, err)

	// Regular error, not an rpc.DataError
	regularErr := errors.New("some regular error")

	abiError, params, resultErr := decoder.Decode(regularErr)

	assert.Nil(t, abiError)
	assert.Nil(t, params)
	assert.Error(t, resultErr)
	assert.Contains(t, resultErr.Error(), "not a Solidity custom error")
	assert.ErrorIs(t, resultErr, regularErr)
}

func TestErrorDecoder_Decode_NoErrorData_ReturnsWrapped(t *testing.T) {
	testABI := createTestABI(t)
	decoder, err := NewErrorDecoder(testABI)
	require.NoError(t, err)

	// DataError with nil data
	dataErr := &mockDataError{data: nil, message: "execution reverted"}

	abiError, params, resultErr := decoder.Decode(dataErr)

	assert.Nil(t, abiError)
	assert.Nil(t, params)
	assert.Error(t, resultErr)
	assert.Contains(t, resultErr.Error(), "no error data")
}

func TestErrorDecoder_Decode_DataNotString_ReturnsWrapped(t *testing.T) {
	testABI := createTestABI(t)
	decoder, err := NewErrorDecoder(testABI)
	require.NoError(t, err)

	// DataError with non-string data
	dataErr := &mockDataError{data: 12345, message: "execution reverted"}

	abiError, params, resultErr := decoder.Decode(dataErr)

	assert.Nil(t, abiError)
	assert.Nil(t, params)
	assert.Error(t, resultErr)
	assert.Contains(t, resultErr.Error(), "error data is not string")
}

func TestErrorDecoder_Decode_InvalidHexData_ReturnsWrapped(t *testing.T) {
	testABI := createTestABI(t)
	decoder, err := NewErrorDecoder(testABI)
	require.NoError(t, err)

	// DataError with invalid hex string
	dataErr := &mockDataError{data: "not-valid-hex!", message: "execution reverted"}

	abiError, params, resultErr := decoder.Decode(dataErr)

	assert.Nil(t, abiError)
	assert.Nil(t, params)
	assert.Error(t, resultErr)
	assert.Contains(t, resultErr.Error(), "failed to decode error data")
}

func TestErrorDecoder_Decode_DataTooShort_ReturnsWrapped(t *testing.T) {
	testABI := createTestABI(t)
	decoder, err := NewErrorDecoder(testABI)
	require.NoError(t, err)

	// DataError with hex data that's less than 4 bytes (selector size)
	dataErr := &mockDataError{data: "0xabcd", message: "execution reverted"} // Only 2 bytes

	abiError, params, resultErr := decoder.Decode(dataErr)

	assert.Nil(t, abiError)
	assert.Nil(t, params)
	assert.Error(t, resultErr)
	assert.Contains(t, resultErr.Error(), "invalid error data length")
}

func TestErrorDecoder_Decode_UnknownSelector_ReturnsUnknown(t *testing.T) {
	testABI := createTestABI(t)
	decoder, err := NewErrorDecoder(testABI)
	require.NoError(t, err)

	// DataError with valid hex but unknown selector
	// Using a random 4-byte selector that doesn't match any error
	dataErr := &mockDataError{data: "0xdeadbeef", message: "execution reverted"}

	abiError, params, resultErr := decoder.Decode(dataErr)

	assert.Nil(t, abiError)
	assert.Nil(t, params)
	assert.Error(t, resultErr)
	assert.Contains(t, resultErr.Error(), "unknown error: 0xdeadbeef")
}

func TestErrorDecoder_Decode_KnownSelector_Success(t *testing.T) {
	testABI := createTestABI(t)
	decoder, err := NewErrorDecoder(testABI)
	require.NoError(t, err)

	// Get the actual selector for InsufficientBalance error
	insufficientBalanceErr := testABI.Errors["InsufficientBalance"]
	selector := hex.EncodeToString(insufficientBalanceErr.ID[:4])

	// Create properly encoded error data
	// InsufficientBalance(uint256 available, uint256 required)
	// Pack: selector + available (100) + required (200)
	available := make([]byte, 32)
	available[31] = 100 // available = 100
	required := make([]byte, 32)
	required[31] = 200 // required = 200

	errorData := "0x" + selector + hex.EncodeToString(available) + hex.EncodeToString(required)
	dataErr := &mockDataError{data: errorData, message: "execution reverted"}

	abiError, params, resultErr := decoder.Decode(dataErr)

	assert.NotNil(t, abiError)
	assert.Equal(t, "InsufficientBalance", abiError.Name)
	assert.NotNil(t, params)
	assert.Error(t, resultErr) // Always returns an error that wraps the original
	assert.Contains(t, resultErr.Error(), "contract error: InsufficientBalance")
}

func TestErrorDecoder_Decode_KnownSelector_UnpackFailure(t *testing.T) {
	testABI := createTestABI(t)
	decoder, err := NewErrorDecoder(testABI)
	require.NoError(t, err)

	// Get the actual selector for InsufficientBalance error
	insufficientBalanceErr := testABI.Errors["InsufficientBalance"]
	selector := hex.EncodeToString(insufficientBalanceErr.ID[:4])

	// Create malformed error data - correct selector but wrong payload
	// InsufficientBalance expects 2 uint256 (64 bytes), but we only provide 16 bytes
	errorData := "0x" + selector + "0000000000000000" // Only 8 bytes of data instead of 64
	dataErr := &mockDataError{data: errorData, message: "execution reverted"}

	abiError, params, resultErr := decoder.Decode(dataErr)

	assert.NotNil(t, abiError) // Should still return the ABI error
	assert.Equal(t, "InsufficientBalance", abiError.Name)
	assert.Nil(t, params) // Params should be nil on unpack failure
	assert.Error(t, resultErr)
	assert.Contains(t, resultErr.Error(), "failed to unpack error selector")
}

func TestErrorDecoder_Decode_WithoutHexPrefix(t *testing.T) {
	testABI := createTestABI(t)
	decoder, err := NewErrorDecoder(testABI)
	require.NoError(t, err)

	// Get the actual selector for InsufficientBalance error
	insufficientBalanceErr := testABI.Errors["InsufficientBalance"]
	selector := hex.EncodeToString(insufficientBalanceErr.ID[:4])

	// Create error data WITHOUT 0x prefix
	available := make([]byte, 32)
	available[31] = 50
	required := make([]byte, 32)
	required[31] = 100

	// Note: no "0x" prefix
	errorData := selector + hex.EncodeToString(available) + hex.EncodeToString(required)
	dataErr := &mockDataError{data: errorData, message: "execution reverted"}

	abiError, params, resultErr := decoder.Decode(dataErr)

	assert.NotNil(t, abiError)
	assert.Equal(t, "InsufficientBalance", abiError.Name)
	assert.NotNil(t, params)
	assert.Error(t, resultErr)
	assert.Contains(t, resultErr.Error(), "contract error: InsufficientBalance")
}

func TestErrorDecoder_Decode_MultipleErrors_SelectsCorrect(t *testing.T) {
	multiABI := createMultiErrorABI(t)
	decoder, err := NewErrorDecoder(multiABI)
	require.NoError(t, err)

	// Test with InvalidAmount error (no inputs)
	invalidAmountErr := multiABI.Errors["InvalidAmount"]
	selector := hex.EncodeToString(invalidAmountErr.ID[:4])

	errorData := "0x" + selector // No additional data needed for this error
	dataErr := &mockDataError{data: errorData, message: "execution reverted"}

	abiError, params, resultErr := decoder.Decode(dataErr)

	assert.NotNil(t, abiError)
	assert.Equal(t, "InvalidAmount", abiError.Name)
	assert.NotNil(t, params) // Should be empty but not nil
	assert.Error(t, resultErr)
	assert.Contains(t, resultErr.Error(), "contract error: InvalidAmount")
}
