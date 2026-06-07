package tron

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/igwedaniel/bloop/internal/config"
	"github.com/sirupsen/logrus"
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

func TestProcessorMatchesConfiguredTRC20Tokens(t *testing.T) {
	usdtContract := "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"
	usdcContract := "TLa2f6VPqDgRE67v1736s7bJ8Ray5wYjU7"
	processor, err := NewProcessor(&config.TronConfig{
		Tokens: []config.TokenConfig{
			{Currency: "USDT", Contract: usdtContract, Decimals: 6},
			{Currency: "USDC", Contract: usdcContract, Decimals: 6},
		},
	}, nil, logrus.New())
	if err != nil {
		t.Fatalf("NewProcessor returned error: %v", err)
	}

	if len(processor.tokenContracts) != 2 {
		t.Fatalf("tokenContracts length = %d, want 2", len(processor.tokenContracts))
	}
	assertConfiguredTokenTrigger(t, processor, usdtContract, true)
	assertConfiguredTokenTrigger(t, processor, usdcContract, true)
	assertConfiguredTokenTrigger(t, processor, "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", false)
}

func assertConfiguredTokenTrigger(t *testing.T, processor *Processor, contract string, want bool) {
	t.Helper()

	value, err := json.Marshal(triggerSmartContract{ContractAddress: contract})
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	trigger := contractResponse{Type: "TriggerSmartContract"}
	trigger.Parameter.Value = value

	if got := processor.isConfiguredTokenTrigger(trigger); got != want {
		t.Fatalf("isConfiguredTokenTrigger(%s) = %t, want %t", contract, got, want)
	}
}
