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
	}, nil
}

func (bp *BitcoinProcessor) SetBaseTracker(baseTracker *base.BaseTracker) {
	bp.baseTracker = baseTracker
}
func (bp *BitcoinProcessor) GetNetwork() types.BlockchainType { return types.Bitcoin }

func (bp *BitcoinProcessor) InitializeProviders(ctx context.Context) error {
	// Prefer REST API if configured; else use JSON-RPC
	rc, err := newRESTClient(bp.config)
	if err != nil {
		return err
	}
	bp.rpc = rc
	bp.wsURL = bp.config.WSURL
	bp.logger.Info("Bitcoin RPC initialized")
	return nil
}

func (bp *BitcoinProcessor) CleanupProviders() error { return nil }

func (bp *BitcoinProcessor) GetCurrentBlockHeight(ctx context.Context) (uint64, error) {
	return bp.rpc.GetBlockCount(ctx)
}

func (bp *BitcoinProcessor) SubscribeToNewBlocks(ctx context.Context, blockCh chan<- uint64) error {
	if bp.wsURL == "" {
		return fmt.Errorf("ws_url not configured")
	}

	u, err := url.Parse(bp.wsURL)
	if err != nil {
		return fmt.Errorf("invalid ws_url: %w", err)
	}

	dialer := websocket.Dialer{}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("ws dial failed: %w", err)
	}

	// request block notifications (per TS: {action:"want", data:["blocks"]})
	_ = conn.WriteJSON(map[string]interface{}{"action": "want", "data": []string{"blocks"}})

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	type wsBlockMsg struct {
		Block struct {
			Height uint64 `json:"height"`
		} `json:"block"`
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if websocket.IsCloseError(err) {
				return nil
			}
			return fmt.Errorf("ws read failed: %w", err)
		}
		var evt wsBlockMsg
		if err := json.Unmarshal(message, &evt); err == nil && evt.Block.Height > 0 {
			select {
			case blockCh <- evt.Block.Height:
			default:
			}
		}

		select {
		case <-pingTicker.C:
			if err := conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second)); err != nil {
				return fmt.Errorf("ws ping failed: %w", err)
			}
		default:
		}
	}
}

func (bp *BitcoinProcessor) ProcessBlock(ctx context.Context, blockNumber uint64) (bool, error) {
	hash, err := bp.rpc.GetBlockHash(ctx, blockNumber)
	if err != nil {
		return false, err
	}
	// verbosity 2 returns decoded txs
	block, err := bp.rpc.GetBlockVerbose(ctx, hash)
	if err != nil {
		return false, err
	}

	for _, tx := range block.Tx {
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
					Timestamp:     time.Unix(block.Time, 0),
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
