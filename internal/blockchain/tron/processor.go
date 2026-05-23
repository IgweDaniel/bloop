package tron

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/igwedaniel/bloop/internal/blockchain/base"
	"github.com/igwedaniel/bloop/internal/config"
	"github.com/igwedaniel/bloop/internal/storage"
	"github.com/igwedaniel/bloop/internal/types"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"
)

var transferEventSignatureHex = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)")).Hex()

type Processor struct {
	client       *Client
	storage      storage.Storage
	config       *config.TronConfig
	logger       *logrus.Logger
	baseTracker  *base.BaseTracker
	usdtContract string
}

func NewProcessor(cfg *config.TronConfig, storage storage.Storage, logger *logrus.Logger) (*Processor, error) {
	usdtContract := ""
	if cfg.USDTContract != "" {
		hexAddr, err := addressToHex41(cfg.USDTContract)
		if err != nil {
			return nil, fmt.Errorf("invalid TRON USDT contract address: %w", err)
		}
		usdtContract = hexAddr
	}

	return &Processor{
		storage:      storage,
		config:       cfg,
		logger:       logger,
		usdtContract: usdtContract,
	}, nil
}

func (p *Processor) SetBaseTracker(baseTracker *base.BaseTracker) {
	p.baseTracker = baseTracker
}

func (p *Processor) GetNetwork() types.BlockchainType {
	return types.Tron
}

func (p *Processor) InitializeProviders(ctx context.Context) error {
	client, err := NewClient(p.config, p.logger)
	if err != nil {
		return err
	}

	if _, err := client.GetCurrentBlockHeight(ctx); err != nil {
		return fmt.Errorf("failed to verify TRON provider: %w", err)
	}

	p.client = client
	p.logger.Info("TRON providers initialized successfully")
	return nil
}

func (p *Processor) CleanupProviders() error {
	return nil
}

func (p *Processor) GetCurrentBlockHeight(ctx context.Context) (uint64, error) {
	return p.client.GetCurrentBlockHeight(ctx)
}

func (p *Processor) SubscribeToNewBlocks(ctx context.Context, blockCh chan<- uint64) error {
	return fmt.Errorf("TRON websocket subscription is not configured; polling will be used")
}

func (p *Processor) InFlightTxs() uint64 {
	if p.client == nil {
		return 0
	}
	return p.client.InFlightTxs()
}

func (p *Processor) ProviderStats() (map[string]uint64, map[string]uint64, string) {
	if p.client == nil {
		return nil, nil, ""
	}
	return p.client.ProviderStats()
}

func (p *Processor) ProcessBlock(ctx context.Context, blockNumber uint64) (bool, error) {
	block, err := p.client.GetBlockByNumber(ctx, blockNumber)
	if err != nil {
		return false, fmt.Errorf("failed to get TRON block %d: %w", blockNumber, err)
	}

	processedTxs, err := p.storage.GetProcessedTransactions(ctx, p.GetNetwork(), blockNumber)
	if err != nil {
		return false, fmt.Errorf("failed to get processed TRON transactions: %w", err)
	}
	processedTxMap := make(map[string]bool, len(processedTxs))
	for _, txID := range processedTxs {
		processedTxMap[txID] = true
	}

	timestamp := time.UnixMilli(block.BlockHeader.RawData.Timestamp)
	if block.BlockHeader.RawData.Timestamp == 0 {
		timestamp = time.Now().UTC()
	}

	txInfos, err := p.getTransactionInfoByBlock(ctx, blockNumber)
	if err != nil {
		return false, fmt.Errorf("failed to get TRON transaction info for block %d: %w", blockNumber, err)
	}

	for _, tx := range block.Transactions {
		if tx.TxID == "" || processedTxMap[tx.TxID] {
			continue
		}

		if p.isSuccessfulTransaction(tx) {
			if err := p.processTransaction(ctx, blockNumber, timestamp, tx, txInfos); err != nil {
				p.logger.WithFields(logrus.Fields{
					"block_number": blockNumber,
					"tx_id":        tx.TxID,
				}).Errorf("Failed to process TRON transaction: %v", err)
			}
		}

		if err := p.storage.AddProcessedTransaction(ctx, p.GetNetwork(), blockNumber, tx.TxID); err != nil {
			p.logger.Errorf("Failed to mark TRON transaction as processed: %v", err)
		}
	}

	if err := p.storage.ClearBlockProgress(ctx, p.GetNetwork(), blockNumber); err != nil {
		p.logger.Errorf("Failed to clear TRON block progress: %v", err)
	}

	if p.baseTracker != nil {
		p.baseTracker.IncrementTxCount(uint64(len(block.Transactions)))
	}

	return true, nil
}

func (p *Processor) processTransaction(ctx context.Context, blockNumber uint64, timestamp time.Time, tx transactionResponse, txInfos map[string]transactionInfoResponse) error {
	processedSmartContractLogs := false
	for _, contract := range tx.RawData.Contract {
		switch contract.Type {
		case "TransferContract":
			if err := p.processTRXTransfer(ctx, blockNumber, timestamp, tx.TxID, contract, txInfos); err != nil {
				p.logger.Errorf("Failed to process TRX transfer %s: %v", tx.TxID, err)
			}
		case "TriggerSmartContract":
			if processedSmartContractLogs {
				continue
			}
			if !p.isConfiguredUSDTTrigger(contract) {
				continue
			}
			processedSmartContractLogs = true
			info, ok := txInfos[tx.TxID]
			if !ok {
				p.logger.Debugf("Skipping TRC20 logs for %s; transaction info not found in block response", tx.TxID)
				continue
			}
			if err := p.processTRC20TransferLogs(ctx, blockNumber, timestamp, tx.TxID, info); err != nil {
				p.logger.Errorf("Failed to process TRC20 transfer logs %s: %v", tx.TxID, err)
			}
		}
	}
	return nil
}

func (p *Processor) processTRXTransfer(ctx context.Context, blockNumber uint64, timestamp time.Time, txID string, contract contractResponse, txInfos map[string]transactionInfoResponse) error {
	var transfer transferContract
	if err := json.Unmarshal(contract.Parameter.Value, &transfer); err != nil {
		return fmt.Errorf("failed to decode TRX transfer contract: %w", err)
	}
	if transfer.Amount <= 0 {
		return nil
	}

	from, err := hexToBase58Address(transfer.OwnerAddress)
	if err != nil {
		return fmt.Errorf("invalid TRX sender address: %w", err)
	}
	to, err := hexToBase58Address(transfer.ToAddress)
	if err != nil {
		return fmt.Errorf("invalid TRX recipient address: %w", err)
	}

	fromWalletID, isFromWatched, err := p.storage.IsWatchedWallet(ctx, p.GetNetwork(), from)
	if err != nil {
		return fmt.Errorf("failed to check watched TRON sender: %w", err)
	}
	toWalletID, isToWatched, err := p.storage.IsWatchedWallet(ctx, p.GetNetwork(), to)
	if err != nil {
		return fmt.Errorf("failed to check watched TRON recipient: %w", err)
	}

	amount := formatSun(big.NewInt(transfer.Amount))
	if isFromWatched && !isToWatched {
		networkFee := p.networkFeeFromBlockInfo(txID, txInfos)

		withdrawal := &types.WalletWithdrawal{
			TxHash:        txID,
			WalletID:      fromWalletID,
			WalletAddress: from,
			ToAddress:     to,
			Amount:        amount,
			Currency:      types.TRX,
			Network:       p.GetNetwork(),
			BlockNumber:   blockNumber,
			Confirmations: uint64(p.config.Confirmations),
			Timestamp:     timestamp,
			NetworkFee:    networkFee,
			Status:        types.StatusConfirmed,
		}
		if p.baseTracker != nil {
			if err := p.baseTracker.PublishWithdrawal(ctx, withdrawal); err != nil {
				p.logger.Errorf("Failed to publish TRX withdrawal: %v", err)
			}
		}
	}

	if !isToWatched {
		return nil
	}

	deposit := &types.WalletDeposit{
		TxHash:        txID,
		WalletID:      toWalletID,
		WalletAddress: to,
		FromAddress:   from,
		Amount:        amount,
		Currency:      types.TRX,
		Network:       p.GetNetwork(),
		BlockNumber:   blockNumber,
		Confirmations: uint64(p.config.Confirmations),
		Timestamp:     timestamp,
		NetworkFee:    "0",
		Status:        types.StatusConfirmed,
	}

	if p.baseTracker != nil {
		return p.baseTracker.PublishDeposit(ctx, deposit)
	}
	return nil
}

func (p *Processor) processTRC20TransferLogs(ctx context.Context, blockNumber uint64, timestamp time.Time, txID string, info transactionInfoResponse) error {
	if p.usdtContract == "" {
		return nil
	}

	if info.Receipt.Result != "" && !strings.EqualFold(info.Receipt.Result, "SUCCESS") {
		return nil
	}

	for _, log := range info.Log {
		contractHex, err := addressToHex41(log.Address)
		if err != nil {
			p.logger.Debugf("Skipping TRON log with invalid contract address %q: %v", log.Address, err)
			continue
		}
		if contractHex != p.usdtContract {
			continue
		}
		if len(log.Topics) < 3 || !strings.EqualFold(normalizeTopic(log.Topics[0]), normalizeTopic(transferEventSignatureHex)) {
			continue
		}

		from, err := topicToBase58Address(log.Topics[1])
		if err != nil {
			p.logger.Errorf("Failed to parse TRC20 sender: %v", err)
			continue
		}
		to, err := topicToBase58Address(log.Topics[2])
		if err != nil {
			p.logger.Errorf("Failed to parse TRC20 recipient: %v", err)
			continue
		}
		amount, err := parseHexUint256(log.Data)
		if err != nil {
			p.logger.Errorf("Failed to parse TRC20 amount: %v", err)
			continue
		}
		if amount.Sign() <= 0 {
			continue
		}

		fromWalletID, isFromWatched, err := p.storage.IsWatchedWallet(ctx, p.GetNetwork(), from)
		if err != nil {
			p.logger.Errorf("Failed to check watched TRON token sender: %v", err)
			continue
		}
		toWalletID, isToWatched, err := p.storage.IsWatchedWallet(ctx, p.GetNetwork(), to)
		if err != nil {
			p.logger.Errorf("Failed to check watched TRON token recipient: %v", err)
			continue
		}

		networkFee := formatSun(big.NewInt(info.Fee))
		amountText := formatToken(amount, p.config.USDTDecimals)

		if isFromWatched && !isToWatched {
			withdrawal := &types.WalletWithdrawal{
				TxHash:        txID,
				WalletID:      fromWalletID,
				WalletAddress: from,
				ToAddress:     to,
				Amount:        amountText,
				Currency:      types.USDT,
				Network:       p.GetNetwork(),
				BlockNumber:   blockNumber,
				Confirmations: uint64(p.config.Confirmations),
				Timestamp:     timestamp,
				NetworkFee:    networkFee,
				Status:        types.StatusConfirmed,
			}
			if p.baseTracker != nil {
				if err := p.baseTracker.PublishWithdrawal(ctx, withdrawal); err != nil {
					p.logger.Errorf("Failed to publish TRC20 withdrawal: %v", err)
				}
			}
		}

		if !isToWatched {
			continue
		}

		deposit := &types.WalletDeposit{
			TxHash:        txID,
			WalletID:      toWalletID,
			WalletAddress: to,
			FromAddress:   from,
			Amount:        amountText,
			Currency:      types.USDT,
			Network:       p.GetNetwork(),
			BlockNumber:   blockNumber,
			Confirmations: uint64(p.config.Confirmations),
			Timestamp:     timestamp,
			NetworkFee:    networkFee,
			Status:        types.StatusConfirmed,
		}
		if p.baseTracker != nil {
			if err := p.baseTracker.PublishDeposit(ctx, deposit); err != nil {
				p.logger.Errorf("Failed to publish TRC20 deposit: %v", err)
			}
		}
	}

	return nil
}

func (p *Processor) getTransactionInfoByBlock(ctx context.Context, blockNumber uint64) (map[string]transactionInfoResponse, error) {
	infos, err := p.client.GetTransactionInfoByBlockNumber(ctx, blockNumber)
	if err != nil {
		return nil, err
	}

	byTxID := make(map[string]transactionInfoResponse, len(infos))
	for _, info := range infos {
		txID := info.txID()
		if txID == "" {
			continue
		}
		byTxID[txID] = info
	}
	return byTxID, nil
}

func (p *Processor) isSuccessfulTransaction(tx transactionResponse) bool {
	if len(tx.Ret) == 0 {
		return true
	}
	return strings.EqualFold(tx.Ret[0].ContractRet, "SUCCESS")
}

func (p *Processor) isConfiguredUSDTTrigger(contract contractResponse) bool {
	if p.usdtContract == "" {
		return false
	}

	var trigger triggerSmartContract
	if err := json.Unmarshal(contract.Parameter.Value, &trigger); err != nil {
		p.logger.Debugf("Failed to decode TRON smart contract trigger: %v", err)
		return false
	}

	contractHex, err := addressToHex41(trigger.ContractAddress)
	if err != nil {
		p.logger.Debugf("Skipping TRON smart contract trigger with invalid contract address %q: %v", trigger.ContractAddress, err)
		return false
	}
	return contractHex == p.usdtContract
}

func (p *Processor) networkFeeFromBlockInfo(txID string, txInfos map[string]transactionInfoResponse) string {
	info, ok := txInfos[txID]
	if !ok {
		return "0"
	}
	return formatSun(big.NewInt(info.Fee))
}

func parseHexUint256(value string) (*big.Int, error) {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "0x")
	if value == "" {
		return big.NewInt(0), nil
	}
	data, err := hex.DecodeString(value)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(data), nil
}

func normalizeTopic(value string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "0x")
}

func formatSun(amount *big.Int) string {
	return decimal.NewFromBigInt(amount, -6).String()
}

func formatToken(amount *big.Int, decimals int32) string {
	return decimal.NewFromBigInt(amount, -decimals).String()
}
