package solana

import (
	"encoding/binary"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/igwedaniel/bloop/internal/config"
)

func TestNativeTransfersExtractsParsedSystemTransfers(t *testing.T) {
	tx := blockTransaction{}
	tx.Transaction.Message.Instructions = []parsedInstruction{
		{
			Program: "system",
			Parsed: rawJSON(t, map[string]interface{}{
				"type": "transfer",
				"info": map[string]interface{}{
					"source":      "source-wallet",
					"destination": "destination-wallet",
					"lamports":    123,
				},
			}),
		},
		{Program: "spl-token"},
	}

	transfers := nativeTransfers(tx)
	if len(transfers) != 1 {
		t.Fatalf("expected one transfer, got %d", len(transfers))
	}
	if transfers[0].Source != "source-wallet" || transfers[0].Destination != "destination-wallet" || transfers[0].Lamports != 123 {
		t.Fatalf("unexpected transfer: %+v", transfers[0])
	}
}

func TestTokenTransfersExtractsParsedSPLTransferChecked(t *testing.T) {
	tx := blockTransaction{}
	tx.Transaction.Message.Instructions = []parsedInstruction{
		{
			Program: "spl-token",
			Parsed: rawJSON(t, map[string]interface{}{
				"type": "transferChecked",
				"info": map[string]interface{}{
					"source":      "source-token-account",
					"destination": "destination-token-account",
					"mint":        "token-mint",
					"tokenAmount": map[string]interface{}{
						"amount": "1000000",
					},
				},
			}),
		},
	}

	transfers := tokenTransfers(tx)
	if len(transfers) != 1 {
		t.Fatalf("expected one transfer, got %d", len(transfers))
	}
	if transfers[0].SourceTokenAccount != "source-token-account" ||
		transfers[0].DestinationTokenAccount != "destination-token-account" ||
		transfers[0].Mint != "token-mint" ||
		transfers[0].Amount.Cmp(big.NewInt(1000000)) != 0 {
		t.Fatalf("unexpected transfer: %+v", transfers[0])
	}
}

func TestTokenTransfersExtractsRawTransferChecked(t *testing.T) {
	tx := blockTransaction{}
	tx.Transaction.Message.AccountKeys = []accountKey{
		{Pubkey: "source-token-account"},
		{Pubkey: "token-mint"},
		{Pubkey: "destination-token-account"},
		{Pubkey: solanaTokenProgramID},
	}
	data := make([]byte, 10)
	data[0] = 12
	binary.LittleEndian.PutUint64(data[1:9], 1000000)
	data[9] = 6
	tx.Transaction.Message.Instructions = []parsedInstruction{
		{
			ProgramIDIndex: 3,
			Accounts: []instructionAccount{
				{Index: 0, HasIndex: true},
				{Index: 1, HasIndex: true},
				{Index: 2, HasIndex: true},
			},
			Data: encodeBase58ForTest(data),
		},
	}

	transfers := tokenTransfers(tx)
	if len(transfers) != 1 {
		t.Fatalf("expected one transfer, got %d", len(transfers))
	}
	if transfers[0].SourceTokenAccount != "source-token-account" ||
		transfers[0].DestinationTokenAccount != "destination-token-account" ||
		transfers[0].Mint != "token-mint" ||
		transfers[0].Amount.Cmp(big.NewInt(1000000)) != 0 {
		t.Fatalf("unexpected transfer: %+v", transfers[0])
	}
}

func TestTokenDeltasAggregatesConfiguredMintByOwner(t *testing.T) {
	const mint = "token-mint"
	tx := blockTransaction{
		Meta: transactionMeta{
			PreTokenBalances: []tokenBalance{
				tokenBalanceFor("alice", mint, "100"),
				tokenBalanceFor("bob", mint, "0"),
			},
			PostTokenBalances: []tokenBalance{
				tokenBalanceFor("alice", mint, "40"),
				tokenBalanceFor("bob", mint, "60"),
			},
		},
	}

	deltas := tokenDeltas(tx, map[string]config.TokenConfig{
		mint: {Currency: "USDC", Contract: mint, Decimals: 6},
	})

	alice := deltas["alice:"+mint]
	bob := deltas["bob:"+mint]
	if alice.Amount.Cmp(big.NewInt(-60)) != 0 {
		t.Fatalf("expected alice delta -60, got %s", alice.Amount)
	}
	if bob.Amount.Cmp(big.NewInt(60)) != 0 {
		t.Fatalf("expected bob delta 60, got %s", bob.Amount)
	}
}

func rawJSON(t *testing.T, value interface{}) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func tokenBalanceFor(owner, mint, amount string) tokenBalance {
	balance := tokenBalance{
		Mint:  mint,
		Owner: owner,
	}
	balance.UITokenAmount.Amount = amount
	balance.UITokenAmount.Decimals = 6
	return balance
}

func encodeBase58ForTest(data []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	value := new(big.Int).SetBytes(data)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)

	var encoded []byte
	for value.Cmp(zero) > 0 {
		value.DivMod(value, base, mod)
		encoded = append(encoded, alphabet[mod.Int64()])
	}
	for _, b := range data {
		if b != 0 {
			break
		}
		encoded = append(encoded, alphabet[0])
	}
	for i, j := 0, len(encoded)-1; i < j; i, j = i+1, j-1 {
		encoded[i], encoded[j] = encoded[j], encoded[i]
	}
	return string(encoded)
}
