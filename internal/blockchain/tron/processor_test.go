package tron

import (
	"math/big"
	"testing"
)

func TestParseHexUint256AndFormatToken(t *testing.T) {
	amount, err := parseHexUint256("00000000000000000000000000000000000000000000000000000000000f4240")
	if err != nil {
		t.Fatalf("parseHexUint256 returned error: %v", err)
	}
	if amount.Cmp(big.NewInt(1_000_000)) != 0 {
		t.Fatalf("parseHexUint256 = %s, want 1000000", amount.String())
	}
	if got := formatToken(amount, 6); got != "1" {
		t.Fatalf("formatToken = %s, want 1", got)
	}
}
