package tronpayment

import (
	"crypto/sha256"
	"math/big"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// ValidateAddress checks if a string is a valid TRON Base58Check address.
// A valid TRON address starts with 'T', is 34 characters long, and passes checksum verification.
func ValidateAddress(address string) bool {
	if len(address) != 34 || address[0] != 'T' {
		return false
	}

	decoded, err := base58Decode(address)
	if err != nil || len(decoded) != 25 {
		return false
	}

	// First byte must be 0x41 (TRON prefix).
	if decoded[0] != 0x41 {
		return false
	}

	// Verify checksum: last 4 bytes == first 4 bytes of double SHA256 of payload.
	payload := decoded[:21]
	checksum := decoded[21:]

	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])

	return h2[0] == checksum[0] && h2[1] == checksum[1] && h2[2] == checksum[2] && h2[3] == checksum[3]
}

func base58Decode(input string) ([]byte, error) {
	result := big.NewInt(0)
	base := big.NewInt(58)

	for _, c := range input {
		idx := int64(-1)
		for i, a := range base58Alphabet {
			if a == c {
				idx = int64(i)
				break
			}
		}
		if idx < 0 {
			return nil, ErrInvalidAddress
		}
		result.Mul(result, base)
		result.Add(result, big.NewInt(idx))
	}

	tmpBytes := result.Bytes()

	// Count leading '1's — they map to leading 0x00 bytes.
	var leadingZeros int
	for _, c := range input {
		if c == '1' {
			leadingZeros++
		} else {
			break
		}
	}

	out := make([]byte, leadingZeros+len(tmpBytes))
	copy(out[leadingZeros:], tmpBytes)
	return out, nil
}

func base58Encode(input []byte) string {
	x := new(big.Int).SetBytes(input)
	base := big.NewInt(58)
	mod := new(big.Int)
	zero := big.NewInt(0)

	var encoded []byte
	for x.Cmp(zero) > 0 {
		x.DivMod(x, base, mod)
		encoded = append([]byte{base58Alphabet[mod.Int64()]}, encoded...)
	}

	// Leading zero bytes map to '1'.
	for _, b := range input {
		if b == 0 {
			encoded = append([]byte{'1'}, encoded...)
		} else {
			break
		}
	}

	return string(encoded)
}
