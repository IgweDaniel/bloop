package tron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/igwedaniel/bloop/internal/config"
	"github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

type Client struct {
	baseURLs    []string
	apiKey      string
	useSolidity bool
	httpClient  *http.Client
	limiter     *rate.Limiter
	logger      *logrus.Logger

	mu           sync.Mutex
	currentIndex int
	requests     map[string]uint64
	errors       map[string]uint64
	lastProvider string
	inFlight     uint64

	retryAttempts int
	retryDelay    time.Duration
}

func NewClient(cfg *config.TronConfig, logger *logrus.Logger) (*Client, error) {
	urls := cfg.APIURLs
	if len(urls) == 0 && cfg.APIURL != "" {
		urls = []string{cfg.APIURL}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("no TRON API URLs configured")
	}

	timeout := cfg.RPCTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	rps := cfg.RequestsPerSecond
	if rps <= 0 {
		rps = 5
	}
	burst := cfg.RequestsBurst
	if burst <= 0 {
		burst = 10
	}

	retryAttempts := cfg.RetryAttempts
	if retryAttempts <= 0 {
		retryAttempts = 3
	}
	retryDelay := cfg.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 2 * time.Second
	}

	return &Client{
		baseURLs:      urls,
		apiKey:        cfg.APIKey,
		useSolidity:   cfg.UseSolidity,
		httpClient:    &http.Client{Timeout: timeout},
		limiter:       rate.NewLimiter(rate.Limit(rps), burst),
		logger:        logger,
		requests:      make(map[string]uint64),
		errors:        make(map[string]uint64),
		retryAttempts: retryAttempts,
		retryDelay:    retryDelay,
	}, nil
}

func (c *Client) GetCurrentBlockHeight(ctx context.Context) (uint64, error) {
	var block blockResponse
	if err := c.post(ctx, c.walletPath("getnowblock"), map[string]any{}, &block); err != nil {
		return 0, err
	}
	return block.BlockHeader.RawData.Number, nil
}

func (c *Client) GetBlockByNumber(ctx context.Context, number uint64) (*blockResponse, error) {
	var block blockResponse
	if err := c.post(ctx, c.walletPath("getblockbynum"), map[string]uint64{"num": number}, &block); err != nil {
		return nil, err
	}
	if number != 0 && block.BlockID == "" && block.BlockHeader.RawData.Number == 0 && len(block.Transactions) == 0 {
		return nil, fmt.Errorf("TRON block %d not found", number)
	}
	return &block, nil
}

func (c *Client) GetTransactionInfo(ctx context.Context, txID string) (*transactionInfoResponse, error) {
	var info transactionInfoResponse
	if err := c.post(ctx, c.walletPath("gettransactioninfobyid"), map[string]string{"value": txID}, &info); err != nil {
		return nil, err
	}
	if info.ID == "" {
		info.ID = txID
	}
	return &info, nil
}

func (c *Client) GetTransactionInfoByBlockNumber(ctx context.Context, number uint64) ([]transactionInfoResponse, error) {
	var infos []transactionInfoResponse
	if err := c.post(ctx, c.walletPath("gettransactioninfobyblocknum"), map[string]uint64{"num": number}, &infos); err != nil {
		return nil, err
	}
	return infos, nil
}

func (c *Client) InFlightTxs() uint64 {
	return atomic.LoadUint64(&c.inFlight)
}

func (c *Client) ProviderStats() (map[string]uint64, map[string]uint64, string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	requests := make(map[string]uint64, len(c.requests))
	for k, v := range c.requests {
		requests[k] = v
	}

	errors := make(map[string]uint64, len(c.errors))
	for k, v := range c.errors {
		errors[k] = v
	}

	return requests, errors, c.lastProvider
}

func (c *Client) walletPath(method string) string {
	if c.useSolidity {
		return "/walletsolidity/" + method
	}
	return "/wallet/" + method
}

func (c *Client) post(ctx context.Context, path string, payload any, dest any) error {
	var lastErr error
	for attempt := 0; attempt < c.retryAttempts; attempt++ {
		for i := 0; i < len(c.baseURLs); i++ {
			baseURL := c.nextProvider()
			if err := c.doPost(ctx, baseURL, path, payload, dest); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				lastErr = err
				c.recordError(baseURL)
				c.logger.WithFields(logrus.Fields{
					"provider": baseURL,
					"path":     path,
					"attempt":  attempt + 1,
				}).Warnf("TRON provider request failed: %v", err)
				continue
			}
			c.recordSuccess(baseURL)
			return nil
		}

		if attempt < c.retryAttempts-1 {
			timer := time.NewTimer(c.retryDelay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("TRON request failed")
	}
	return lastErr
}

func (c *Client) doPost(ctx context.Context, baseURL, path string, payload any, dest any) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := strings.TrimRight(baseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", c.apiKey)
	}

	atomic.AddUint64(&c.inFlight, 1)
	resp, err := c.httpClient.Do(req)
	atomic.AddUint64(&c.inFlight, ^uint64(0))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("unexpected HTTP status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("failed to decode TRON response: %w", err)
	}
	return nil
}

func (c *Client) nextProvider() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	url := c.baseURLs[c.currentIndex]
	c.currentIndex = (c.currentIndex + 1) % len(c.baseURLs)
	return url
}

func (c *Client) recordSuccess(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.requests[provider]++
	c.lastProvider = provider
}

func (c *Client) recordError(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.errors[provider]++
	c.lastProvider = provider
}
