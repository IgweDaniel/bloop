package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

const tronAddressPrefix = byte(0x41)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func normalizeTronAddress(address string) (string, error) {
	hex41, err := addressToHex41(address)
	if err != nil {
		return "", err
	}
	return hexToBase58Address(hex41)
}

func NormalizeAddress(address string) (string, error) {
	return normalizeTronAddress(address)
}

func addressToHex41(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", fmt.Errorf("empty TRON address")
	}

	hexAddr := strings.TrimPrefix(strings.ToLower(address), "0x")
	if isHexString(hexAddr) {
		switch len(hexAddr) {
		case 40:
			return "41" + hexAddr, nil
		case 42:
			if !strings.HasPrefix(hexAddr, "41") {
				return "", fmt.Errorf("TRON hex address must start with 41")
			}
			return hexAddr, nil
		}
	}

	payload, err := base58CheckDecode(address)
	if err != nil {
		return "", err
	}
	if len(payload) != 21 || payload[0] != tronAddressPrefix {
		return "", fmt.Errorf("invalid TRON address payload")
	}
	return hex.EncodeToString(payload), nil
}

func hexToBase58Address(hexAddr string) (string, error) {
	hexAddr = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(hexAddr)), "0x")
	if len(hexAddr) == 40 {
		hexAddr = "41" + hexAddr
	}
	if len(hexAddr) != 42 {
		return "", fmt.Errorf("invalid TRON hex address length")
	}

	payload, err := hex.DecodeString(hexAddr)
	if err != nil {
		return "", fmt.Errorf("invalid TRON hex address: %w", err)
	}
	if len(payload) != 21 || payload[0] != tronAddressPrefix {
		return "", fmt.Errorf("TRON hex address must start with 41")
	}

	return base58CheckEncode(payload), nil
}

func topicToBase58Address(topic string) (string, error) {
	topic = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(topic)), "0x")
	if len(topic) < 40 || !isHexString(topic) {
		return "", fmt.Errorf("invalid TRC20 address topic")
	}
	return hexToBase58Address("41" + topic[len(topic)-40:])
}

func base58CheckEncode(payload []byte) string {
	checksum := checksum(payload)
	return base58Encode(append(append([]byte{}, payload...), checksum...))
}

func base58CheckDecode(address string) ([]byte, error) {
	decoded, err := base58Decode(address)
	if err != nil {
		return nil, err
	}
	if len(decoded) < 5 {
		return nil, fmt.Errorf("invalid base58check payload")
	}

	payload := decoded[:len(decoded)-4]
	gotChecksum := decoded[len(decoded)-4:]
	expectedChecksum := checksum(payload)
	for i := range gotChecksum {
		if gotChecksum[i] != expectedChecksum[i] {
			return nil, fmt.Errorf("invalid base58check checksum")
		}
	}
	return payload, nil
}

func checksum(payload []byte) []byte {
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	return second[:4]
}

func base58Encode(input []byte) string {
	x := new(big.Int).SetBytes(input)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)

	var encoded []byte
	for x.Cmp(zero) > 0 {
		x.DivMod(x, base, mod)
		encoded = append(encoded, base58Alphabet[mod.Int64()])
	}

	for _, b := range input {
		if b != 0 {
			break
		}
		encoded = append(encoded, base58Alphabet[0])
	}

	for i, j := 0, len(encoded)-1; i < j; i, j = i+1, j-1 {
		encoded[i], encoded[j] = encoded[j], encoded[i]
	}
	return string(encoded)
}

func base58Decode(input string) ([]byte, error) {
	result := big.NewInt(0)
	base := big.NewInt(58)

	for _, r := range input {
		index := strings.IndexRune(base58Alphabet, r)
		if index < 0 {
			return nil, fmt.Errorf("invalid base58 character %q", r)
		}
		result.Mul(result, base)
		result.Add(result, big.NewInt(int64(index)))
	}

	decoded := result.Bytes()
	leadingZeros := 0
	for leadingZeros < len(input) && input[leadingZeros] == base58Alphabet[0] {
		leadingZeros++
	}
	if leadingZeros > 0 {
		decoded = append(make([]byte, leadingZeros), decoded...)
	}
	return decoded, nil
}

func isHexString(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}
