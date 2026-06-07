package solana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/igwedaniel/bloop/internal/config"
	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

type Client struct {
	urls    []string
	wsURL   string
	http    *http.Client
	limiter *rate.Limiter
	logger  *logrus.Logger
	next    uint32
}

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func NewClient(cfg *config.SolanaConfig, logger *logrus.Logger) (*Client, error) {
	urls := cfg.RPCURLs
	if len(urls) == 0 && cfg.RPCURL != "" {
		urls = []string{cfg.RPCURL}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("no Solana RPC URLs configured")
	}

	rps := cfg.RequestsPerSecond
	if rps <= 0 {
		rps = 5
	}
	burst := cfg.RequestsBurst
	if burst <= 0 {
		burst = 10
	}

	return &Client{
		urls:    urls,
		wsURL:   cfg.WSURL,
		http:    &http.Client{Timeout: cfg.RPCTimeout},
		limiter: rate.NewLimiter(rate.Limit(rps), burst),
		logger:  logger,
	}, nil
}

func (c *Client) GetSlot(ctx context.Context) (uint64, error) {
	var slot uint64
	err := c.call(ctx, "getSlot", []interface{}{
		map[string]interface{}{"commitment": "confirmed"},
	}, &slot)
	return slot, err
}

func (c *Client) GetBlock(ctx context.Context, slot uint64) (*blockResponse, error) {
	var block *blockResponse
	err := c.call(ctx, "getBlock", []interface{}{
		slot,
		map[string]interface{}{
			"encoding":                       "jsonParsed",
			"transactionDetails":             "full",
			"rewards":                        false,
			"maxSupportedTransactionVersion": 0,
		},
	}, &block)
	if err != nil {
		return nil, err
	}
	return block, nil
}

func (c *Client) SubscribeToSlots(ctx context.Context, blockCh chan<- uint64) error {
	if c.wsURL == "" {
		return fmt.Errorf("ws_url not configured")
	}

	dialer := websocket.Dialer{}
	baseBackoff := 250 * time.Millisecond
	maxBackoff := 15 * time.Second
	attempt := 0

	for {
		if ctx.Err() != nil {
			return nil
		}

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

		conn, _, err := dialer.DialContext(ctx, c.wsURL, nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.logger.WithFields(logrus.Fields{
				"ws_url":  c.wsURL,
				"attempt": attempt + 1,
				"error":   err,
			}).Warn("Solana websocket dial failed")
			attempt++
			continue
		}

		msg := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "slotSubscribe",
		}
		if err := conn.WriteJSON(msg); err != nil {
			_ = conn.Close()
			attempt++
			continue
		}
		attempt = 0

		for {
			var notification struct {
				Method string `json:"method"`
				Params struct {
					Result struct {
						Slot uint64 `json:"slot"`
					} `json:"result"`
				} `json:"params"`
			}
			if err := conn.ReadJSON(&notification); err != nil {
				_ = conn.Close()
				if ctx.Err() != nil {
					return nil
				}
				c.logger.WithFields(logrus.Fields{
					"ws_url": c.wsURL,
					"error":  err,
				}).Warn("Solana websocket disconnected")
				attempt++
				break
			}
			if notification.Method != "slotNotification" || notification.Params.Result.Slot == 0 {
				continue
			}
			select {
			case blockCh <- notification.Params.Result.Slot:
			default:
			}

			select {
			case <-ctx.Done():
				_ = conn.Close()
				return nil
			default:
			}
		}
	}
}

func (c *Client) call(ctx context.Context, method string, params interface{}, dest interface{}) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}

	var lastErr error
	for i := 0; i < len(c.urls); i++ {
		url := c.urls[int(atomic.AddUint32(&c.next, 1)-1)%len(c.urls)]
		if err := c.callURL(ctx, url, method, params, dest); err != nil {
			lastErr = err
			c.logger.WithFields(logrus.Fields{
				"provider": url,
				"method":   method,
				"error":    err,
			}).Debug("Solana provider request failed")
			continue
		}
		return nil
	}
	return lastErr
}

func (c *Client) callURL(ctx context.Context, url, method string, params interface{}, dest interface{}) error {
	payload, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected HTTP status %d: %s", resp.StatusCode, string(body))
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return err
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if len(rpcResp.Result) == 0 || string(rpcResp.Result) == "null" {
		return nil
	}
	return json.Unmarshal(rpcResp.Result, dest)
}
