package solana

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/igwedaniel/bloop/internal/blockchain/base"
	"github.com/igwedaniel/bloop/internal/config"
	"github.com/igwedaniel/bloop/internal/storage"
	"github.com/igwedaniel/bloop/internal/types"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

const (
	solanaSystemProgramID    = "11111111111111111111111111111111"
	solanaTokenProgramID     = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	solanaToken2022ProgramID = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
)

type Processor struct {
	storage        storage.Storage
	config         *config.SolanaConfig
	logger         *logrus.Logger
	baseTracker    *base.BaseTracker
	client         *Client
	tokenByMint    map[string]config.TokenConfig
	network        types.BlockchainType
	nativeCurrency types.Currency
}

type blockResponse struct {
	BlockTime    *int64             `json:"blockTime"`
	Transactions []blockTransaction `json:"transactions"`
}

type blockTransaction struct {
	Transaction parsedTransaction `json:"transaction"`
	Meta        transactionMeta   `json:"meta"`
}

type parsedTransaction struct {
	Signatures []string `json:"signatures"`
	Message    struct {
		AccountKeys  []accountKey        `json:"accountKeys"`
		Instructions []parsedInstruction `json:"instructions"`
	} `json:"message"`
}

type accountKey struct {
	Pubkey string
}

func (a *accountKey) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		a.Pubkey = asString
		return nil
	}
	var asObject struct {
		Pubkey string `json:"pubkey"`
	}
	if err := json.Unmarshal(data, &asObject); err != nil {
		return err
	}
	a.Pubkey = asObject.Pubkey
	return nil
}

type transactionMeta struct {
	Err               interface{} `json:"err"`
	Fee               uint64      `json:"fee"`
	PreBalances       []uint64    `json:"preBalances"`
	PostBalances      []uint64    `json:"postBalances"`
	InnerInstructions []struct {
		Instructions []parsedInstruction `json:"instructions"`
	} `json:"innerInstructions"`
	PreTokenBalances  []tokenBalance `json:"preTokenBalances"`
	PostTokenBalances []tokenBalance `json:"postTokenBalances"`
}

type parsedInstruction struct {
	Program        string               `json:"program"`
	ProgramID      string               `json:"programId"`
	ProgramIDIndex uint64               `json:"programIdIndex"`
	Accounts       []instructionAccount `json:"accounts"`
	Data           string               `json:"data"`
	Parsed         json.RawMessage      `json:"parsed"`
}

type instructionAccount struct {
	Pubkey   string
	Index    int
	HasIndex bool
}

func (a *instructionAccount) UnmarshalJSON(data []byte) error {
	var idx int
	if err := json.Unmarshal(data, &idx); err == nil {
		a.Index = idx
		a.HasIndex = true
		return nil
	}
	var pubkey string
	if err := json.Unmarshal(data, &pubkey); err != nil {
		return err
	}
	a.Pubkey = pubkey
	return nil
}

type nativeTransfer struct {
	Source      string
	Destination string
	Lamports    uint64
}

type tokenTransfer struct {
	SourceTokenAccount      string
	DestinationTokenAccount string
	Mint                    string
	Amount                  *big.Int
}

type tokenBalance struct {
	AccountIndex  uint32 `json:"accountIndex"`
	Mint          string `json:"mint"`
	Owner         string `json:"owner"`
	UITokenAmount struct {
		Amount         string `json:"amount"`
		Decimals       int32  `json:"decimals"`
		UIAmountString string `json:"uiAmountString"`
	} `json:"uiTokenAmount"`
}

func NewProcessor(cfg *config.SolanaConfig, storage storage.Storage, logger *logrus.Logger) (*Processor, error) {
	tokenByMint := make(map[string]config.TokenConfig, len(cfg.Tokens))
	for _, token := range cfg.Tokens {
		tokenByMint[token.Contract] = token
	}

	if cfg.NativeCurrency == "" {
		cfg.NativeCurrency = types.SOL
	}

	return &Processor{
		storage:        storage,
		config:         cfg,
		logger:         logger,
		tokenByMint:    tokenByMint,
		network:        types.Solana,
		nativeCurrency: cfg.NativeCurrency,
	}, nil
}

func (p *Processor) SetBaseTracker(baseTracker *base.BaseTracker) {
	p.baseTracker = baseTracker
}

func (p *Processor) GetNetwork() types.BlockchainType {
	return p.network
}

func (p *Processor) InitializeProviders(ctx context.Context) error {
	client, err := NewClient(p.config, p.logger)
	if err != nil {
		return err
	}
	p.client = client
	if _, err := p.client.GetSlot(ctx); err != nil {
		return fmt.Errorf("failed to get Solana slot: %w", err)
	}
	p.logger.Info("SOLANA RPC initialized")
	return nil
}

func (p *Processor) CleanupProviders() error {
	return nil
}

func (p *Processor) GetCurrentBlockHeight(ctx context.Context) (uint64, error) {
	return p.client.GetSlot(ctx)
}

func (p *Processor) SubscribeToNewBlocks(ctx context.Context, blockCh chan<- uint64) error {
	if p.client == nil {
		return fmt.Errorf("Solana client not initialized")
	}
	return p.client.SubscribeToSlots(ctx, blockCh)
}

func (p *Processor) ProcessBlock(ctx context.Context, blockNumber uint64) (bool, error) {
	block, err := p.client.GetBlock(ctx, blockNumber)
	if err != nil {
		if isSkippedSlotError(err) {
			p.logger.WithFields(logrus.Fields{
				"network":      p.network,
				"block_number": blockNumber,
				"error":        err,
			}).Debug("Skipping empty Solana slot")
			return true, nil
		}
		return false, err
	}
	if block == nil {
		return true, nil
	}

	for _, tx := range block.Transactions {
		if tx.Meta.Err != nil {
			continue
		}
		if err := p.processTransaction(ctx, blockNumber, block.blockTime(), tx); err != nil {
			return false, err
		}
	}

	if p.baseTracker != nil {
		p.baseTracker.IncrementTxCount(uint64(len(block.Transactions)))
	}
	return true, nil
}

func (b *blockResponse) blockTime() time.Time {
	if b.BlockTime == nil {
		return time.Now().UTC()
	}
	return time.Unix(*b.BlockTime, 0).UTC()
}

func (p *Processor) processTransaction(ctx context.Context, blockNumber uint64, blockTime time.Time, tx blockTransaction) error {
	signature := ""
	if len(tx.Transaction.Signatures) > 0 {
		signature = tx.Transaction.Signatures[0]
	}
	if signature == "" {
		return nil
	}

	if err := p.processNativeDeltas(ctx, blockNumber, blockTime, signature, tx); err != nil {
		return err
	}
	return p.processTokenTransfers(ctx, blockNumber, blockTime, signature, tx)
}

func (p *Processor) processNativeDeltas(ctx context.Context, blockNumber uint64, blockTime time.Time, signature string, tx blockTransaction) error {
	for _, transfer := range nativeTransfers(tx) {
		if transfer.Lamports == 0 {
			continue
		}

		sourceWalletID, sourceWatched, err := p.storage.IsWatchedWallet(ctx, p.network, transfer.Source)
		if err != nil {
			return err
		}
		destinationWalletID, destinationWatched, err := p.storage.IsWatchedWallet(ctx, p.network, transfer.Destination)
		if err != nil {
			return err
		}

		amount := new(big.Int).SetUint64(transfer.Lamports)
		if destinationWatched && !sourceWatched {
			deposit := &types.WalletDeposit{
				TxHash:        signature,
				WalletID:      destinationWalletID,
				WalletAddress: transfer.Destination,
				FromAddress:   transfer.Source,
				Amount:        formatLamports(amount),
				Currency:      p.nativeCurrency,
				Network:       p.network,
				BlockNumber:   blockNumber,
				Confirmations: uint64(p.config.Confirmations),
				Timestamp:     blockTime,
				NetworkFee:    formatLamports(new(big.Int).SetUint64(tx.Meta.Fee)),
				Status:        types.StatusConfirmed,
			}
			if p.baseTracker != nil {
				if err := p.baseTracker.PublishDeposit(ctx, deposit); err != nil {
					return err
				}
			}
			continue
		}

		if sourceWatched && !destinationWatched {
			withdrawal := &types.WalletWithdrawal{
				TxHash:        signature,
				WalletID:      sourceWalletID,
				WalletAddress: transfer.Source,
				ToAddress:     transfer.Destination,
				Amount:        formatLamports(amount),
				Currency:      p.nativeCurrency,
				Network:       p.network,
				BlockNumber:   blockNumber,
				Confirmations: uint64(p.config.Confirmations),
				Timestamp:     blockTime,
				NetworkFee:    formatLamports(new(big.Int).SetUint64(tx.Meta.Fee)),
				Status:        types.StatusConfirmed,
			}
			if p.baseTracker != nil {
				if err := p.baseTracker.PublishWithdrawal(ctx, withdrawal); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p *Processor) processTokenTransfers(ctx context.Context, blockNumber uint64, blockTime time.Time, signature string, tx blockTransaction) error {
	transfers := tokenTransfers(tx)
	if len(transfers) > 0 {
		return p.processTokenInstructionTransfers(ctx, blockNumber, blockTime, signature, tx, transfers)
	}
	return p.processTokenDeltas(ctx, blockNumber, blockTime, signature, tx)
}

func (p *Processor) processTokenInstructionTransfers(ctx context.Context, blockNumber uint64, blockTime time.Time, signature string, tx blockTransaction, transfers []tokenTransfer) error {
	ownerByAccount, mintByAccount := tokenAccountMetadata(tx)

	for _, transfer := range transfers {
		if transfer.Amount == nil || transfer.Amount.Sign() == 0 {
			continue
		}
		mint := transfer.Mint
		if mint == "" {
			mint = mintByAccount[transfer.SourceTokenAccount]
			if mint == "" {
				mint = mintByAccount[transfer.DestinationTokenAccount]
			}
		}

		token, ok := p.tokenByMint[mint]
		if !ok {
			continue
		}

		fromOwner := ownerByAccount[transfer.SourceTokenAccount]
		toOwner := ownerByAccount[transfer.DestinationTokenAccount]
		if fromOwner == "" || toOwner == "" {
			continue
		}

		fromWalletID, fromWatched, err := p.storage.IsWatchedWallet(ctx, p.network, fromOwner)
		if err != nil {
			return err
		}
		toWalletID, toWatched, err := p.storage.IsWatchedWallet(ctx, p.network, toOwner)
		if err != nil {
			return err
		}

		if toWatched && !fromWatched {
			deposit := &types.WalletDeposit{
				TxHash:        signature,
				WalletID:      toWalletID,
				WalletAddress: toOwner,
				FromAddress:   fromOwner,
				Amount:        formatToken(transfer.Amount, token.Decimals),
				Currency:      types.Currency(token.Currency),
				Network:       p.network,
				BlockNumber:   blockNumber,
				Confirmations: uint64(p.config.Confirmations),
				Timestamp:     blockTime,
				NetworkFee:    formatLamports(new(big.Int).SetUint64(tx.Meta.Fee)),
				Status:        types.StatusConfirmed,
			}
			if p.baseTracker != nil {
				if err := p.baseTracker.PublishDeposit(ctx, deposit); err != nil {
					return err
				}
			}
			continue
		}

		if fromWatched && !toWatched {
			withdrawal := &types.WalletWithdrawal{
				TxHash:        signature,
				WalletID:      fromWalletID,
				WalletAddress: fromOwner,
				ToAddress:     toOwner,
				Amount:        formatToken(transfer.Amount, token.Decimals),
				Currency:      types.Currency(token.Currency),
				Network:       p.network,
				BlockNumber:   blockNumber,
				Confirmations: uint64(p.config.Confirmations),
				Timestamp:     blockTime,
				NetworkFee:    formatLamports(new(big.Int).SetUint64(tx.Meta.Fee)),
				Status:        types.StatusConfirmed,
			}
			if p.baseTracker != nil {
				if err := p.baseTracker.PublishWithdrawal(ctx, withdrawal); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p *Processor) processTokenDeltas(ctx context.Context, blockNumber uint64, blockTime time.Time, signature string, tx blockTransaction) error {
	deltas := tokenDeltas(tx, p.tokenByMint)
	for key, delta := range deltas {
		if delta.Amount.Sign() == 0 {
			continue
		}

		walletID, watched, err := p.storage.IsWatchedWallet(ctx, p.network, delta.Owner)
		if err != nil {
			return err
		}
		if !watched {
			continue
		}

		token := p.tokenByMint[delta.Mint]
		counterparty := oppositeTokenOwner(deltas, key, delta)
		amount := new(big.Int).Abs(delta.Amount)
		if delta.Amount.Sign() > 0 {
			deposit := &types.WalletDeposit{
				TxHash:        signature,
				WalletID:      walletID,
				WalletAddress: delta.Owner,
				FromAddress:   counterparty,
				Amount:        formatToken(amount, token.Decimals),
				Currency:      types.Currency(token.Currency),
				Network:       p.network,
				BlockNumber:   blockNumber,
				Confirmations: uint64(p.config.Confirmations),
				Timestamp:     blockTime,
				NetworkFee:    formatLamports(new(big.Int).SetUint64(tx.Meta.Fee)),
				Status:        types.StatusConfirmed,
			}
			if p.baseTracker != nil {
				if err := p.baseTracker.PublishDeposit(ctx, deposit); err != nil {
					return err
				}
			}
			continue
		}

		withdrawal := &types.WalletWithdrawal{
			TxHash:        signature,
			WalletID:      walletID,
			WalletAddress: delta.Owner,
			ToAddress:     counterparty,
			Amount:        formatToken(amount, token.Decimals),
			Currency:      types.Currency(token.Currency),
			Network:       p.network,
			BlockNumber:   blockNumber,
			Confirmations: uint64(p.config.Confirmations),
			Timestamp:     blockTime,
			NetworkFee:    formatLamports(new(big.Int).SetUint64(tx.Meta.Fee)),
			Status:        types.StatusConfirmed,
		}
		if p.baseTracker != nil {
			if err := p.baseTracker.PublishWithdrawal(ctx, withdrawal); err != nil {
				return err
			}
		}
	}
	return nil
}

func nativeTransfers(tx blockTransaction) []nativeTransfer {
	instructions := append([]parsedInstruction{}, tx.Transaction.Message.Instructions...)
	for _, inner := range tx.Meta.InnerInstructions {
		instructions = append(instructions, inner.Instructions...)
	}

	transfers := make([]nativeTransfer, 0)
	for _, instruction := range instructions {
		if transfer, ok := parseSystemTransfer(instruction, tx.Transaction.Message.AccountKeys); ok {
			transfers = append(transfers, transfer)
		}
	}
	return transfers
}

func tokenTransfers(tx blockTransaction) []tokenTransfer {
	instructions := append([]parsedInstruction{}, tx.Transaction.Message.Instructions...)
	for _, inner := range tx.Meta.InnerInstructions {
		instructions = append(instructions, inner.Instructions...)
	}

	transfers := make([]tokenTransfer, 0)
	for _, instruction := range instructions {
		if transfer, ok := parseTokenTransfer(instruction, tx.Transaction.Message.AccountKeys); ok {
			transfers = append(transfers, transfer)
		}
	}
	return transfers
}

func parseSystemTransfer(instruction parsedInstruction, accountKeys []accountKey) (nativeTransfer, bool) {
	if instruction.Program == "system" || instruction.ProgramID == solanaSystemProgramID {
		var parsed struct {
			Type string `json:"type"`
			Info struct {
				Source      string      `json:"source"`
				Destination string      `json:"destination"`
				Lamports    interface{} `json:"lamports"`
			} `json:"info"`
		}
		if len(instruction.Parsed) > 0 && json.Unmarshal(instruction.Parsed, &parsed) == nil && parsed.Type == "transfer" {
			lamports, ok := uint64FromAny(parsed.Info.Lamports)
			if ok && parsed.Info.Source != "" && parsed.Info.Destination != "" && lamports > 0 {
				return nativeTransfer{
					Source:      parsed.Info.Source,
					Destination: parsed.Info.Destination,
					Lamports:    lamports,
				}, true
			}
		}
	}

	programID := instructionProgramID(instruction, accountKeys)
	if programID != solanaSystemProgramID {
		return nativeTransfer{}, false
	}

	indices := instructionAccountIndices(instruction.Accounts, accountKeys)
	if len(indices) < 2 {
		return nativeTransfer{}, false
	}
	data, err := decodeBase58(instruction.Data)
	if err != nil || len(data) != 12 || binary.LittleEndian.Uint32(data[0:4]) != 2 {
		return nativeTransfer{}, false
	}

	source, ok := accountKeyAt(accountKeys, indices[0])
	if !ok {
		return nativeTransfer{}, false
	}
	destination, ok := accountKeyAt(accountKeys, indices[1])
	if !ok {
		return nativeTransfer{}, false
	}
	lamports := binary.LittleEndian.Uint64(data[4:12])
	if lamports == 0 {
		return nativeTransfer{}, false
	}
	return nativeTransfer{Source: source, Destination: destination, Lamports: lamports}, true
}

func parseTokenTransfer(instruction parsedInstruction, accountKeys []accountKey) (tokenTransfer, bool) {
	if isTokenProgram(instruction.Program, instruction.ProgramID) {
		var parsed struct {
			Type string `json:"type"`
			Info struct {
				Source      string      `json:"source"`
				Destination string      `json:"destination"`
				Mint        string      `json:"mint"`
				Amount      interface{} `json:"amount"`
				TokenAmount struct {
					Amount interface{} `json:"amount"`
				} `json:"tokenAmount"`
			} `json:"info"`
		}
		if len(instruction.Parsed) > 0 && json.Unmarshal(instruction.Parsed, &parsed) == nil {
			amountValue := parsed.Info.Amount
			if parsed.Type == "transferChecked" {
				amountValue = parsed.Info.TokenAmount.Amount
			}
			amount, ok := bigIntFromAny(amountValue)
			if ok && amount.Sign() > 0 && parsed.Info.Source != "" && parsed.Info.Destination != "" {
				return tokenTransfer{
					SourceTokenAccount:      parsed.Info.Source,
					DestinationTokenAccount: parsed.Info.Destination,
					Mint:                    parsed.Info.Mint,
					Amount:                  amount,
				}, parsed.Type == "transfer" || parsed.Type == "transferChecked"
			}
		}
	}

	programID := instructionProgramID(instruction, accountKeys)
	if programID != solanaTokenProgramID && programID != solanaToken2022ProgramID {
		return tokenTransfer{}, false
	}

	indices := instructionAccountIndices(instruction.Accounts, accountKeys)
	data, err := decodeBase58(instruction.Data)
	if err != nil || len(data) < 9 {
		return tokenTransfer{}, false
	}

	switch data[0] {
	case 3:
		if len(indices) < 2 {
			return tokenTransfer{}, false
		}
		source, ok := accountKeyAt(accountKeys, indices[0])
		if !ok {
			return tokenTransfer{}, false
		}
		destination, ok := accountKeyAt(accountKeys, indices[1])
		if !ok {
			return tokenTransfer{}, false
		}
		amount := new(big.Int).SetUint64(binary.LittleEndian.Uint64(data[1:9]))
		if amount.Sign() == 0 {
			return tokenTransfer{}, false
		}
		return tokenTransfer{SourceTokenAccount: source, DestinationTokenAccount: destination, Amount: amount}, true
	case 12:
		if len(indices) < 3 {
			return tokenTransfer{}, false
		}
		source, ok := accountKeyAt(accountKeys, indices[0])
		if !ok {
			return tokenTransfer{}, false
		}
		mint, ok := accountKeyAt(accountKeys, indices[1])
		if !ok {
			return tokenTransfer{}, false
		}
		destination, ok := accountKeyAt(accountKeys, indices[2])
		if !ok {
			return tokenTransfer{}, false
		}
		amount := new(big.Int).SetUint64(binary.LittleEndian.Uint64(data[1:9]))
		if amount.Sign() == 0 {
			return tokenTransfer{}, false
		}
		return tokenTransfer{SourceTokenAccount: source, DestinationTokenAccount: destination, Mint: mint, Amount: amount}, true
	default:
		return tokenTransfer{}, false
	}
}

type tokenDelta struct {
	Owner  string
	Mint   string
	Amount *big.Int
}

func tokenDeltas(tx blockTransaction, tokenByMint map[string]config.TokenConfig) map[string]tokenDelta {
	pre := aggregateTokenBalances(tx.Meta.PreTokenBalances, tokenByMint)
	post := aggregateTokenBalances(tx.Meta.PostTokenBalances, tokenByMint)

	for key, preBalance := range pre {
		if _, ok := post[key]; !ok {
			post[key] = tokenDelta{
				Owner:  preBalance.Owner,
				Mint:   preBalance.Mint,
				Amount: big.NewInt(0),
			}
		}
	}

	for key, postBalance := range post {
		preAmount := big.NewInt(0)
		if preBalance, ok := pre[key]; ok {
			preAmount = preBalance.Amount
		}
		postBalance.Amount = new(big.Int).Sub(postBalance.Amount, preAmount)
		post[key] = postBalance
	}
	return post
}

func aggregateTokenBalances(balances []tokenBalance, tokenByMint map[string]config.TokenConfig) map[string]tokenDelta {
	result := make(map[string]tokenDelta)
	for _, balance := range balances {
		if _, ok := tokenByMint[balance.Mint]; !ok {
			continue
		}
		if balance.Owner == "" {
			continue
		}
		amount, ok := new(big.Int).SetString(balance.UITokenAmount.Amount, 10)
		if !ok {
			continue
		}
		key := balance.Owner + ":" + balance.Mint
		current := result[key]
		if current.Amount == nil {
			current = tokenDelta{
				Owner:  balance.Owner,
				Mint:   balance.Mint,
				Amount: big.NewInt(0),
			}
		}
		current.Amount = new(big.Int).Add(current.Amount, amount)
		result[key] = current
	}
	return result
}

func tokenAccountMetadata(tx blockTransaction) (map[string]string, map[string]string) {
	ownerByAccount := make(map[string]string)
	mintByAccount := make(map[string]string)
	for _, balance := range tx.Meta.PreTokenBalances {
		addTokenAccountMetadata(balance, tx.Transaction.Message.AccountKeys, ownerByAccount, mintByAccount)
	}
	for _, balance := range tx.Meta.PostTokenBalances {
		addTokenAccountMetadata(balance, tx.Transaction.Message.AccountKeys, ownerByAccount, mintByAccount)
	}
	return ownerByAccount, mintByAccount
}

func addTokenAccountMetadata(balance tokenBalance, accountKeys []accountKey, ownerByAccount map[string]string, mintByAccount map[string]string) {
	account, ok := accountKeyAt(accountKeys, int(balance.AccountIndex))
	if !ok {
		return
	}
	if balance.Owner != "" {
		ownerByAccount[account] = balance.Owner
	}
	if balance.Mint != "" {
		mintByAccount[account] = balance.Mint
	}
}

func oppositeTokenOwner(deltas map[string]tokenDelta, currentKey string, current tokenDelta) string {
	for key, candidate := range deltas {
		if key == currentKey || candidate.Mint != current.Mint || candidate.Amount.Sign() == 0 {
			continue
		}
		if candidate.Amount.Sign() != current.Amount.Sign() {
			return candidate.Owner
		}
	}
	return ""
}

func instructionProgramID(instruction parsedInstruction, accountKeys []accountKey) string {
	if instruction.ProgramID != "" {
		return instruction.ProgramID
	}
	programID, ok := accountKeyAt(accountKeys, int(instruction.ProgramIDIndex))
	if !ok {
		return ""
	}
	return programID
}

func instructionAccountIndices(accounts []instructionAccount, accountKeys []accountKey) []int {
	pubkeyIndex := make(map[string]int, len(accountKeys))
	for i, account := range accountKeys {
		pubkeyIndex[account.Pubkey] = i
	}

	indices := make([]int, 0, len(accounts))
	for _, account := range accounts {
		if account.HasIndex {
			indices = append(indices, account.Index)
			continue
		}
		index, ok := pubkeyIndex[account.Pubkey]
		if !ok {
			return nil
		}
		indices = append(indices, index)
	}
	return indices
}

func accountKeyAt(accountKeys []accountKey, index int) (string, bool) {
	if index < 0 || index >= len(accountKeys) || accountKeys[index].Pubkey == "" {
		return "", false
	}
	return accountKeys[index].Pubkey, true
}

func isTokenProgram(program, programID string) bool {
	return program == "spl-token" || programID == solanaTokenProgramID || programID == solanaToken2022ProgramID
}

func uint64FromAny(value interface{}) (uint64, bool) {
	switch v := value.(type) {
	case float64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case uint64:
		return v, true
	case string:
		parsed, err := strconv.ParseUint(v, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func bigIntFromAny(value interface{}) (*big.Int, bool) {
	switch v := value.(type) {
	case float64:
		if v < 0 {
			return nil, false
		}
		return new(big.Int).SetUint64(uint64(v)), true
	case int:
		if v < 0 {
			return nil, false
		}
		return big.NewInt(int64(v)), true
	case uint64:
		return new(big.Int).SetUint64(v), true
	case string:
		parsed, ok := new(big.Int).SetString(v, 10)
		return parsed, ok
	default:
		return nil, false
	}
}

func decodeBase58(value string) ([]byte, error) {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	if value == "" {
		return nil, nil
	}

	result := big.NewInt(0)
	base := big.NewInt(58)
	for _, r := range value {
		index := strings.IndexRune(alphabet, r)
		if index < 0 {
			return nil, fmt.Errorf("invalid base58 character %q", r)
		}
		result.Mul(result, base)
		result.Add(result, big.NewInt(int64(index)))
	}

	decoded := result.Bytes()
	for _, r := range value {
		if r != '1' {
			break
		}
		decoded = append([]byte{0}, decoded...)
	}
	return decoded, nil
}

func formatLamports(lamports *big.Int) string {
	if lamports == nil {
		return "0"
	}
	return decimal.NewFromBigInt(lamports, -9).StringFixed(9)
}

func formatToken(amount *big.Int, decimals int32) string {
	if amount == nil {
		return "0"
	}
	if decimals < 0 {
		decimals = 0
	}
	return decimal.NewFromBigInt(amount, -decimals).StringFixed(decimals)
}

func isSkippedSlotError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "was skipped") ||
		strings.Contains(msg, "block not available") ||
		strings.Contains(msg, "not available")
}
