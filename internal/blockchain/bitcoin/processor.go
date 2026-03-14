package bitcoin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"errors"
	"net"

	"github.com/gorilla/websocket"
	"github.com/igwedaniel/bloop/internal/blockchain/base"
	"github.com/igwedaniel/bloop/internal/config"
	"github.com/igwedaniel/bloop/internal/storage"
	"github.com/igwedaniel/bloop/internal/types"
	"github.com/sirupsen/logrus"
)

type BitcoinProcessor struct {
	storage     storage.Storage
	config      *config.BitcoinConfig
	logger      *logrus.Logger
	baseTracker *base.BaseTracker
	rpc         Client
	wsURL       string

	tipTTL time.Duration
}

func NewBitcoinProcessor(
	cfg *config.BitcoinConfig,
	storage storage.Storage,
	logger *logrus.Logger,
) (*BitcoinProcessor, error) {
	return &BitcoinProcessor{
		storage: storage,
		config:  cfg,
		logger:  logger,
		tipTTL:  2 * time.Second,
	}, nil
}

func (bp *BitcoinProcessor) SetBaseTracker(baseTracker *base.BaseTracker) {
	bp.baseTracker = baseTracker
}
func (bp *BitcoinProcessor) GetNetwork() types.BlockchainType { return types.Bitcoin }
func (bp *BitcoinProcessor) InFlightTxs() uint64 {
	if rc, ok := bp.rpc.(interface{ InFlightTxs() uint64 }); ok {
		return rc.InFlightTxs()
	}
	return 0
}
func (bp *BitcoinProcessor) ProviderStats() (map[string]uint64, map[string]uint64, string) {
	if rc, ok := bp.rpc.(interface {
		ProviderStats() (map[string]uint64, map[string]uint64, string)
	}); ok {
		return rc.ProviderStats()
	}
	return nil, nil, ""
}

func (bp *BitcoinProcessor) InitializeProviders(ctx context.Context) error {
	// Prefer REST API if configured; else use JSON-RPC
	rc, err := newBitcoinClient(bp.config, bp.logger)
	if err != nil {
		return err
	}
	bp.rpc = rc
	bp.wsURL = bp.config.WSURL
	bp.logger.Info("Bitcoin RPC initialized")
	return nil
}

func (bp *BitcoinProcessor) CleanupProviders() error { return nil }

// Caching is appropriate for Bitcoin because new blocks are produced at a relatively slow and predictable rate (approximately every 10 minutes).
// This allows us to safely cache the current block height for a short period without risking significant staleness or missing new blocks.
func (bp *BitcoinProcessor) GetCurrentBlockHeight(ctx context.Context) (uint64, error) {
	var cached struct {
		Height uint64 `json:"height"`
	}
	cacheKey := fmt.Sprintf("%s:tip", bp.GetNetwork())
	if err := bp.storage.GetCache(ctx, cacheKey, &cached); err == nil && cached.Height > 0 {
		return cached.Height, nil
	}

	// Miss: fetch and set short TTL
	h, err := bp.rpc.GetBlockCount(ctx)
	if err != nil {
		return 0, err
	}
	_ = bp.storage.SetCache(ctx, cacheKey, struct {
		Height uint64 `json:"height"`
	}{Height: h}, bp.tipTTL)
	return h, nil
}

func (bp *BitcoinProcessor) SubscribeToNewBlocks(ctx context.Context, blockCh chan<- uint64) error {
	if bp.wsURL == "" {
		return fmt.Errorf("ws_url not configured")
	}

	u, err := url.Parse(bp.wsURL)
	if err != nil {
		return fmt.Errorf("invalid ws_url: %w", err)
	}

	// Reconnect loop: first retry is immediate; subsequent retries back off exponentially up to a cap.
	// This function only returns when the context is canceled.
	dialer := websocket.Dialer{}
	baseBackoff := 250 * time.Millisecond
	maxBackoff := 15 * time.Second
	attempt := 0

	type wsBlockMsg struct {
		Block struct {
			Height uint64 `json:"height"`
		} `json:"block"`
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		// Backoff before dialing only after the first failed attempt; the first retry is immediate.
		if attempt > 0 {
			delay := baseBackoff << (attempt - 1)
			if delay > maxBackoff {
				delay = maxBackoff
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return nil
			case <-timer.C:
			}
		}

		conn, _, err := dialer.DialContext(ctx, u.String(), nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			bp.logger.Warnf("BTC websocket dial failed (attempt %d): %v", attempt+1, err)
			attempt++
			continue
		}

		// Reset attempts on successful connection
		attempt = 0

		// request block notifications (per TS: {action:"want", data:["blocks"]})
		msg := map[string]interface{}{
			"action": "want",
			"data":   []string{"blocks"},
		}

		bp.logger.WithField("msg", msg).Info("Sending message to websocket")
		_ = conn.WriteJSON(msg)

		pingTicker := time.NewTicker(30 * time.Second)
		// Ensure ticker stopped when we exit this connection scope
		connClosed := make(chan struct{})
		go func() {
			<-ctx.Done()
			_ = conn.Close()
			close(connClosed)
		}()

		// Read loop for a single connection
	readLoop:
		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				pingTicker.Stop()
				return nil
			default:
			}

			_, message, err := conn.ReadMessage()
			if err != nil {
				// Treat all read/close errors as triggers to reconnect unless context is canceled
				if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) || websocket.IsCloseError(err) {
					_ = conn.Close()
					pingTicker.Stop()
					if ctx.Err() != nil {
						return nil
					}
					bp.logger.Warnf("BTC websocket disconnected: %v (reconnecting)", err)
					break readLoop
				}
				_ = conn.Close()
				pingTicker.Stop()
				if ctx.Err() != nil {
					return nil
				}
				bp.logger.Warnf("BTC websocket read error: %v (reconnecting)", err)
				break readLoop
			}
			var evt wsBlockMsg
			if err := json.Unmarshal(message, &evt); err == nil && evt.Block.Height > 0 {
				select {
				case blockCh <- evt.Block.Height:
					bp.logger.WithField("block_height", evt.Block.Height).Info("New block height received")
				default:
				}
			}

			select {
			case <-pingTicker.C:
				if err := conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second)); err != nil {
					_ = conn.Close()
					pingTicker.Stop()
					bp.logger.Warnf("BTC websocket ping failed: %v (reconnecting)", err)
					break readLoop
				}
			default:
			}
		}

		// Increment attempt count so that if we immediately fail again, we will start backing off
		attempt++
	}
}

func (bp *BitcoinProcessor) ProcessBlock(ctx context.Context, blockNumber uint64) (bool, error) {
	bp.logger.WithField("block_number", blockNumber).Info("Processing block")
	hash, err := bp.rpc.GetBlockHash(ctx, blockNumber)
	if err != nil {
		return false, err
	}
	bp.logger.WithFields(logrus.Fields{
		"block_number": blockNumber,
		"block_hash":   hash,
	}).Debug("Fetched BTC block hash")
	// verbosity 2 returns decoded txs
	block, err := bp.rpc.GetBlockVerbose(ctx, hash)
	if err != nil {
		return false, err
	}
	bp.logger.WithFields(logrus.Fields{
		"block_number": blockNumber,
		"tx_count":     len(block.Tx),
	}).Debug("Fetched BTC block transactions")

	for _, tx := range block.Tx {
		bp.logger.WithFields(logrus.Fields{
			"block_number": blockNumber,
			"txid":         tx.Txid,
		}).Debug("Processing BTC transaction")
		watchedInputWalletID := ""
		watchedInputAddress := ""
		multiWallet := false
		for _, vin := range tx.Vin {
			addr := vin.Prevout.Address
			if addr == "" {
				continue
			}
			walletID, isWatched, err := bp.storage.IsWatchedWallet(ctx, types.Bitcoin, addr)
			if err != nil {
				return false, err
			}
			if !isWatched {
				continue
			}
			if watchedInputWalletID == "" {
				watchedInputWalletID = walletID
				watchedInputAddress = addr
				continue
			}
			if walletID != watchedInputWalletID {
				multiWallet = true
				break
			}
		}

		if watchedInputWalletID != "" && !multiWallet {
			externalAmount := 0.0
			externalTo := ""
			for _, vout := range tx.Vout {
				if len(vout.ScriptPubKey.Addresses) == 0 {
					continue
				}
				addr := vout.ScriptPubKey.Addresses[0]
				_, isWatched, err := bp.storage.IsWatchedWallet(ctx, types.Bitcoin, addr)
				if err != nil {
					return false, err
				}
				if isWatched {
					continue
				}
				externalAmount += vout.Value
				if externalTo == "" {
					externalTo = addr
				}
			}
			if externalAmount > 0 {
				withdrawal := &types.WalletWithdrawal{
					TxHash:        tx.Txid,
					WalletID:      watchedInputWalletID,
					WalletAddress: watchedInputAddress,
					ToAddress:     externalTo,
					Amount:        fmt.Sprintf("%.8f", externalAmount),
					Currency:      types.BTC,
					Network:       types.Bitcoin,
					BlockNumber:   blockNumber,
					Confirmations: 1,
					Timestamp:     time.Unix(block.Time, 0).UTC(),
					NetworkFee:    "",
					Status:        types.StatusConfirmed,
				}
				if bp.baseTracker != nil {
					if err := bp.baseTracker.PublishWithdrawal(ctx, withdrawal); err != nil {
						bp.logger.Errorf("publish withdrawal: %v", err)
					}
				}
			}
		}

		for _, vout := range tx.Vout {
			if len(vout.ScriptPubKey.Addresses) == 0 {
				continue
			}
			for _, addr := range vout.ScriptPubKey.Addresses {
				walletID, isWatched, err := bp.storage.IsWatchedWallet(ctx, types.Bitcoin, addr)
				if err != nil {
					return false, err
				}
				if !isWatched {
					continue
				}

				dep := &types.WalletDeposit{
					TxHash:        tx.Txid,
					WalletID:      walletID,
					WalletAddress: addr,
					FromAddress:   "",
					Amount:        fmt.Sprintf("%.8f", vout.Value),
					Currency:      types.BTC,
					Network:       types.Bitcoin,
					BlockNumber:   blockNumber,
					Confirmations: 1,
					Timestamp:     time.Unix(block.Time, 0).UTC(),
					NetworkFee:    "",
					Status:        types.StatusConfirmed,
				}
				if bp.baseTracker != nil {
					if err := bp.baseTracker.PublishDeposit(ctx, dep); err != nil {
						bp.logger.Errorf("publish deposit: %v", err)
					}
				}
			}
		}
	}
	return true, nil
}
