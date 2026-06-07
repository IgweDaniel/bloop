package bitcoin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/igwedaniel/bloop/internal/config"
	"github.com/sirupsen/logrus"
)

// Public types used by the processor
type btcVout struct {
	Value        float64 `json:"value"`
	ScriptPubKey struct {
		Addresses []string `json:"addresses"`
	} `json:"scriptPubKey"`
}

type btcVin struct {
	Prevout struct {
		Value   int64  `json:"value"`
		Address string `json:"scriptpubkey_address"`
	} `json:"prevout"`
}

type btcTx struct {
	Txid string    `json:"txid"`
	Vin  []btcVin  `json:"vin"`
	Vout []btcVout `json:"vout"`
}

type btcBlock struct {
	Hash   string  `json:"hash"`
	Height uint64  `json:"height"`
	Time   int64   `json:"time"`
	Tx     []btcTx `json:"tx"`
}

type Client interface {
	// WARNING: Do NOT add a rate limiter to this method, as it can significantly slow down stats retrieval.
	// Instead, consider using caching strategies at a higher level to avoid excessive network calls.
	GetBlockCount(ctx context.Context) (uint64, error)
	GetBlockHash(ctx context.Context, height uint64) (string, error)
	GetBlockVerbose(ctx context.Context, hash string) (*btcBlock, error)
}

type baseClient struct {
	base    *url.URL
	http    *http.Client
	limiter *rate.Limiter
	logger  *logrus.Logger
}

type esploraClient struct {
	*baseClient
	fetchConcurrency int
	inFlightTxs      uint64
}

type blockchainInfoClient struct {
	*baseClient
}

type multiClient struct {
	clients []Client
	names   []string
	next    uint32
	last    uint32
	counts  []uint64
	errors  []uint64
	logger  *logrus.Logger
}

func newBaseClient(cfg *config.UTXOConfig, logger *logrus.Logger, baseURL string) (*baseClient, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("api_url not set")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	rps := cfg.RequestsPerSecond
	if rps <= 0 {
		rps = 3
	}
	burst := cfg.RequestsBurst
	if burst <= 0 {
		burst = 5
	}
	return &baseClient{
		base:    u,
		http:    &http.Client{Timeout: cfg.RPCTimeout},
		limiter: rate.NewLimiter(rate.Limit(rps), burst),
		logger:  logger,
	}, nil
}

func (c *baseClient) url(path string) string {
	u := *c.base
	basePath := strings.TrimRight(u.Path, "/")
	rel := strings.TrimLeft(path, "/")
	u.Path = basePath + "/" + rel
	return u.String()
}

func isHTML(resp *http.Response, body []byte) bool {
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/html") {
		return true
	}
	b := strings.TrimSpace(strings.ToLower(string(body)))
	return strings.HasPrefix(b, "<!doctype html") || strings.HasPrefix(b, "<html")
}

func (c *baseClient) waitRateLimit(ctx context.Context, op string, fields logrus.Fields) error {
	if c.limiter == nil {
		return nil
	}
	res := c.limiter.Reserve()
	if !res.OK() {
		return fmt.Errorf("rate limiter rejected request for %s", op)
	}
	delay := res.Delay()
	if delay <= 0 {
		return nil
	}
	if c.logger != nil {
		c.logger.WithFields(fields).WithField("delay", delay).Debugf("BTC rate limit wait for %s", op)
	}
	timer := time.NewTimer(delay)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
	}
	return nil
}

// doWithBackoff retries on 429 with exponential backoff and respects Retry-After if present
func (c *baseClient) doWithBackoff(ctx context.Context, req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error
	backoff := 300 * time.Millisecond
	for attempt := 0; attempt < 4; attempt++ {
		resp, err = c.http.Do(req.Clone(ctx))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		// Handle 429
		_ = resp.Body.Close()
		if c.logger != nil {
			c.logger.WithFields(logrus.Fields{
				"url":     req.URL.String(),
				"attempt": attempt + 1,
			}).Debug("BTC HTTP 429 received; backing off")
		}
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if d, parseErr := time.ParseDuration(ra + "s"); parseErr == nil {
				if c.logger != nil {
					c.logger.WithFields(logrus.Fields{
						"url":     req.URL.String(),
						"attempt": attempt + 1,
						"delay":   d,
					}).Debug("BTC respecting Retry-After")
				}
				timer := time.NewTimer(d)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
				}
				continue
			}
		}
		timer := time.NewTimer(backoff)
		if c.logger != nil {
			c.logger.WithFields(logrus.Fields{
				"url":     req.URL.String(),
				"attempt": attempt + 1,
				"delay":   backoff,
			}).Debug("BTC backoff before retry")
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
	}
	return resp, err
}

func newEsploraClient(cfg *config.UTXOConfig, logger *logrus.Logger, baseURL string) (*esploraClient, error) {
	base, err := newBaseClient(cfg, logger, baseURL)
	if err != nil {
		return nil, err
	}
	fc := cfg.TxFetchConcurrency
	if fc <= 0 {
		fc = 20
	}
	return &esploraClient{
		baseClient:       base,
		fetchConcurrency: fc,
	}, nil
}

func newBlockchainInfoClient(cfg *config.UTXOConfig, logger *logrus.Logger, baseURL string) (*blockchainInfoClient, error) {
	base, err := newBaseClient(cfg, logger, baseURL)
	if err != nil {
		return nil, err
	}
	return &blockchainInfoClient{baseClient: base}, nil
}

func (c *esploraClient) GetBlockCount(ctx context.Context) (uint64, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/blocks/tip/height"), nil)
	resp, err := c.doWithBackoff(ctx, req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode >= 400 || isHTML(resp, b) {
		return 0, fmt.Errorf("bad response %d from tip height: %s", resp.StatusCode, string(b))
	}
	var height uint64
	if err := json.Unmarshal(b, &height); err == nil {
		return height, nil
	}
	s := strings.TrimSpace(string(b))
	num, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse height: %w (body=%q)", err, s)
	}
	return num, nil
}

func (c *esploraClient) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	if err := c.waitRateLimit(ctx, "get_block_hash", logrus.Fields{"height": height}); err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/block-height/%d", height)), nil)
	resp, err := c.doWithBackoff(ctx, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 || isHTML(resp, b) {
		return "", fmt.Errorf("bad response %d from block-height: %s", resp.StatusCode, string(b))
	}
	var hash string
	if err := json.Unmarshal(b, &hash); err == nil {
		return hash, nil
	}
	return strings.TrimSpace(string(b)), nil
}

func (c *esploraClient) GetBlockVerbose(ctx context.Context, hash string) (*btcBlock, error) {
	if err := c.waitRateLimit(ctx, "get_block_header", logrus.Fields{"hash": hash}); err != nil {
		return nil, err
	}
	reqH, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/block/%s", hash)), nil)
	respH, err := c.doWithBackoff(ctx, reqH)
	if err != nil {
		return nil, err
	}
	defer respH.Body.Close()
	var hdr struct {
		Height  uint64 `json:"height"`
		Time    int64  `json:"timestamp"`
		TxCount int    `json:"tx_count"`
	}
	if err := json.NewDecoder(respH.Body).Decode(&hdr); err != nil {
		return nil, err
	}

	block := &btcBlock{Hash: hash, Height: hdr.Height, Time: hdr.Time}
	type esploraTx struct {
		Txid string `json:"txid"`
		Vin  []struct {
			Prevout *struct {
				Value               int64  `json:"value"`
				ScriptPubKeyAddress string `json:"scriptpubkey_address"`
			} `json:"prevout"`
		} `json:"vin"`
		Vout []struct {
			Value               int64  `json:"value"`
			ScriptPubKeyAddress string `json:"scriptpubkey_address"`
		} `json:"vout"`
	}

	start := 0
	for {
		if err := c.waitRateLimit(ctx, "get_block_txs", logrus.Fields{"hash": hash, "start": start}); err != nil {
			return nil, err
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/block/%s/txs/%d", hash, start)), nil)
		resp, err := c.doWithBackoff(ctx, req)
		if err != nil {
			return nil, err
		}
		var page []esploraTx
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		if len(page) == 0 {
			break
		}
		for _, etx := range page {
			tx := btcTx{Txid: etx.Txid}
			for _, v := range etx.Vin {
				if v.Prevout == nil {
					continue
				}
				tx.Vin = append(tx.Vin, btcVin{
					Prevout: struct {
						Value   int64  `json:"value"`
						Address string `json:"scriptpubkey_address"`
					}{
						Value:   v.Prevout.Value,
						Address: v.Prevout.ScriptPubKeyAddress,
					},
				})
			}
			for _, v := range etx.Vout {
				valueBtc := float64(v.Value) / 1e8
				addrs := []string{}
				if v.ScriptPubKeyAddress != "" {
					addrs = []string{v.ScriptPubKeyAddress}
				}
				tx.Vout = append(tx.Vout, btcVout{Value: valueBtc, ScriptPubKey: struct {
					Addresses []string `json:"addresses"`
				}{Addresses: addrs}})
			}
			block.Tx = append(block.Tx, tx)
		}
		start += len(page)
		if hdr.TxCount > 0 && start >= hdr.TxCount {
			break
		}
	}
	return block, nil
}

func (c *esploraClient) InFlightTxs() uint64 {
	return atomic.LoadUint64(&c.inFlightTxs)
}

func (c *blockchainInfoClient) GetBlockCount(ctx context.Context) (uint64, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/latestblock"), nil)
	resp, err := c.doWithBackoff(ctx, req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var latest struct {
		Height uint64 `json:"height"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&latest); err != nil {
		return 0, err
	}
	return latest.Height, nil
}

func (c *blockchainInfoClient) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	if err := c.waitRateLimit(ctx, "get_block_hash", logrus.Fields{"height": height}); err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/block-height/%d?format=json", height)), nil)
	resp, err := c.doWithBackoff(ctx, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var payload struct {
		Blocks []struct {
			Hash      string `json:"hash"`
			MainChain bool   `json:"main_chain"`
		} `json:"blocks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	for _, b := range payload.Blocks {
		if b.MainChain && b.Hash != "" {
			return b.Hash, nil
		}
	}
	if len(payload.Blocks) > 0 {
		return payload.Blocks[0].Hash, nil
	}
	return "", fmt.Errorf("no blocks found for height %d", height)
}

func (c *blockchainInfoClient) GetBlockVerbose(ctx context.Context, hash string) (*btcBlock, error) {
	if err := c.waitRateLimit(ctx, "get_block", logrus.Fields{"hash": hash}); err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/rawblock/%s", hash)), nil)
	resp, err := c.doWithBackoff(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw struct {
		Hash   string `json:"hash"`
		Height uint64 `json:"height"`
		Time   int64  `json:"time"`
		Tx     []struct {
			Hash   string `json:"hash"`
			Inputs []struct {
				PrevOut *struct {
					Value   int64  `json:"value"`
					Addr    string `json:"addr"`
					Address string `json:"address"`
				} `json:"prev_out"`
			} `json:"inputs"`
			Out []struct {
				Value   int64  `json:"value"`
				Addr    string `json:"addr"`
				Address string `json:"address"`
			} `json:"out"`
		} `json:"tx"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	block := &btcBlock{Hash: raw.Hash, Height: raw.Height, Time: raw.Time}
	for _, t := range raw.Tx {
		tx := btcTx{Txid: t.Hash}
		for _, vin := range t.Inputs {
			if vin.PrevOut == nil {
				continue
			}
			addr := vin.PrevOut.Addr
			if addr == "" {
				addr = vin.PrevOut.Address
			}
			tx.Vin = append(tx.Vin, btcVin{
				Prevout: struct {
					Value   int64  `json:"value"`
					Address string `json:"scriptpubkey_address"`
				}{
					Value:   vin.PrevOut.Value,
					Address: addr,
				},
			})
		}
		for _, vout := range t.Out {
			addr := vout.Addr
			if addr == "" {
				addr = vout.Address
			}
			valueBtc := float64(vout.Value) / 1e8
			addrs := []string{}
			if addr != "" {
				addrs = []string{addr}
			}
			tx.Vout = append(tx.Vout, btcVout{Value: valueBtc, ScriptPubKey: struct {
				Addresses []string `json:"addresses"`
			}{Addresses: addrs}})
		}
		block.Tx = append(block.Tx, tx)
	}
	return block, nil
}

func newMultiClient(clients []Client, names []string, logger *logrus.Logger) *multiClient {
	return &multiClient{
		clients: clients,
		names:   names,
		counts:  make([]uint64, len(clients)),
		errors:  make([]uint64, len(clients)),
		logger:  logger,
	}
}

func (m *multiClient) nextIndex() int {
	return int(atomic.AddUint32(&m.next, 1) % uint32(len(m.clients)))
}

func (m *multiClient) logFail(op string, idx int, err error) {
	atomic.AddUint64(&m.errors[idx], 1)
	if m.logger == nil {
		return
	}
	name := "unknown"
	if idx < len(m.names) {
		name = m.names[idx]
	}
	m.logger.WithFields(logrus.Fields{
		"op":       op,
		"provider": name,
		"error":    err.Error(),
	}).Debug("BTC provider failed, trying next")
}

func (m *multiClient) markSuccess(idx int) {
	atomic.AddUint64(&m.counts[idx], 1)
	atomic.StoreUint32(&m.last, uint32(idx))
}

func (m *multiClient) GetBlockCount(ctx context.Context) (uint64, error) {
	if len(m.clients) == 0 {
		return 0, fmt.Errorf("no bitcoin API clients configured")
	}
	start := m.nextIndex()
	var lastErr error
	for i := 0; i < len(m.clients); i++ {
		idx := (start + i) % len(m.clients)
		height, err := m.clients[idx].GetBlockCount(ctx)
		if err == nil {
			m.markSuccess(idx)
			return height, nil
		}
		lastErr = err
		m.logFail("GetBlockCount", idx, err)
	}
	return 0, lastErr
}

func (m *multiClient) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	if len(m.clients) == 0 {
		return "", fmt.Errorf("no bitcoin API clients configured")
	}
	start := m.nextIndex()
	var lastErr error
	for i := 0; i < len(m.clients); i++ {
		idx := (start + i) % len(m.clients)
		hash, err := m.clients[idx].GetBlockHash(ctx, height)
		if err == nil {
			m.markSuccess(idx)
			return hash, nil
		}
		lastErr = err
		m.logFail("GetBlockHash", idx, err)
	}
	return "", lastErr
}

func (m *multiClient) GetBlockVerbose(ctx context.Context, hash string) (*btcBlock, error) {
	if len(m.clients) == 0 {
		return nil, fmt.Errorf("no bitcoin API clients configured")
	}
	start := m.nextIndex()
	var lastErr error
	for i := 0; i < len(m.clients); i++ {
		idx := (start + i) % len(m.clients)
		block, err := m.clients[idx].GetBlockVerbose(ctx, hash)
		if err == nil {
			m.markSuccess(idx)
			return block, nil
		}
		lastErr = err
		m.logFail("GetBlockVerbose", idx, err)
	}
	return nil, lastErr
}

func (m *multiClient) InFlightTxs() uint64 {
	var total uint64
	for _, c := range m.clients {
		if c2, ok := c.(interface{ InFlightTxs() uint64 }); ok {
			total += c2.InFlightTxs()
		}
	}
	return total
}

func (m *multiClient) ProviderStats() (map[string]uint64, map[string]uint64, string) {
	providers := make(map[string]uint64, len(m.clients))
	errors := make(map[string]uint64, len(m.clients))
	for i := range m.clients {
		name := "unknown"
		if i < len(m.names) {
			name = m.names[i]
		}
		providers[name] = atomic.LoadUint64(&m.counts[i])
		errors[name] = atomic.LoadUint64(&m.errors[i])
	}
	lastIdx := int(atomic.LoadUint32(&m.last))
	last := ""
	if lastIdx >= 0 && lastIdx < len(m.names) {
		last = m.names[lastIdx]
	}
	return providers, errors, last
}

func newBitcoinClient(cfg *config.UTXOConfig, logger *logrus.Logger) (Client, error) {
	urls := make([]string, 0, len(cfg.APIURLs)+1)
	urls = append(urls, cfg.APIURLs...)
	if len(urls) == 0 && cfg.APIURL != "" {
		urls = append(urls, cfg.APIURL)
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("no bitcoin api urls configured")
	}

	var clients []Client
	var names []string
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		host := strings.ToLower(u.Host)
		switch {
		case strings.Contains(host, "blockchain.info") || strings.Contains(host, "blockchain.com"):
			if strings.Contains(host, "blockchain.com") && !strings.Contains(host, "blockchain.info") {
				if logger != nil {
					logger.WithField("api_url", raw).Warn("blockchain.com host detected; using blockchain.info-compatible endpoints")
				}
			}
			client, err := newBlockchainInfoClient(cfg, logger, raw)
			if err != nil {
				return nil, err
			}
			clients = append(clients, client)
			names = append(names, "blockchain")
		default:
			client, err := newEsploraClient(cfg, logger, raw)
			if err != nil {
				return nil, err
			}
			clients = append(clients, client)
			names = append(names, "esplora")
		}
	}
	if len(clients) == 1 {
		return clients[0], nil
	}
	return newMultiClient(clients, names, logger), nil
}
