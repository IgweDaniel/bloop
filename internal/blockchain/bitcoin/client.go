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
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/igwedaniel/bloop/internal/config"
)

// Public types used by the processor
type btcVout struct {
	Value        float64 `json:"value"`
	ScriptPubKey struct {
		Addresses []string `json:"addresses"`
	} `json:"scriptPubKey"`
}

type btcTx struct {
	Txid string    `json:"txid"`
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

type restClient struct {
	base             *url.URL
	http             *http.Client
	fetchConcurrency int
	limiter          *rate.Limiter
}

func newRESTClient(cfg *config.BitcoinConfig) (*restClient, error) {
	if cfg.APIURL == "" {
		return nil, fmt.Errorf("api_url not set")
	}
	u, err := url.Parse(cfg.APIURL)
	if err != nil {
		return nil, err
	}
	fc := cfg.TxFetchConcurrency
	if fc <= 0 {
		fc = 20
	}
	rps := cfg.RequestsPerSecond
	if rps <= 0 {
		rps = 3
	}
	burst := cfg.RequestsBurst
	if burst <= 0 {
		burst = 5
	}
	return &restClient{
		base:             u,
		http:             &http.Client{Timeout: cfg.RPCTimeout},
		fetchConcurrency: fc,
		limiter:          rate.NewLimiter(rate.Limit(rps), burst),
	}, nil
}

func (c *restClient) url(path string) string {
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

func (c *restClient) GetBlockCount(ctx context.Context) (uint64, error) {
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
	// try JSON then plain text
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

func (c *restClient) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	if err := c.limiter.Wait(ctx); err != nil {
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

func (c *restClient) GetBlockVerbose(ctx context.Context, hash string) (*btcBlock, error) {
	// txids
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/block/%s/txids", hash)), nil)
	resp, err := c.doWithBackoff(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var txids []string
	if err := json.NewDecoder(resp.Body).Decode(&txids); err != nil {
		return nil, err
	}

	// header
	reqH, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/block/%s", hash)), nil)
	respH, err := c.http.Do(reqH)
	if err != nil {
		return nil, err
	}
	defer respH.Body.Close()
	var hdr struct {
		Height uint64 `json:"height"`
		Time   int64  `json:"timestamp"`
	}
	if err := json.NewDecoder(respH.Body).Decode(&hdr); err != nil {
		return nil, err
	}

	block := &btcBlock{Hash: hash, Height: hdr.Height, Time: hdr.Time}
	results := make([]btcTx, len(txids))
	type txResult struct {
		idx int
		tx  btcTx
		err error
	}
	sem := make(chan struct{}, c.fetchConcurrency)
	wg := sync.WaitGroup{}
	resCh := make(chan txResult, len(txids))
	for i, txid := range txids {
		wg.Add(1)
		go func(i int, txid string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := c.limiter.Wait(ctx); err != nil {
				resCh <- txResult{idx: i, err: err}
				return
			}
			reqTx, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/tx/%s", txid)), nil)
			respTx, err := c.doWithBackoff(ctx, reqTx)
			if err != nil {
				resCh <- txResult{idx: i, err: err}
				return
			}
			var txBody struct {
				Txid string `json:"txid"`
				Vout []struct {
					Value   int64  `json:"value"`
					Address string `json:"scriptpubkey_address"`
				} `json:"vout"`
			}
			if err := json.NewDecoder(respTx.Body).Decode(&txBody); err != nil {
				respTx.Body.Close()
				resCh <- txResult{idx: i, err: err}
				return
			}
			respTx.Body.Close()
			tx := btcTx{Txid: txBody.Txid}
			for _, v := range txBody.Vout {
				valueBtc := float64(v.Value) / 1e8
				addrs := []string{}
				if v.Address != "" {
					addrs = []string{v.Address}
				}
				tx.Vout = append(tx.Vout, btcVout{Value: valueBtc, ScriptPubKey: struct {
					Addresses []string `json:"addresses"`
				}{Addresses: addrs}})
			}
			resCh <- txResult{idx: i, tx: tx}
		}(i, txid)
	}
	wg.Wait()
	close(resCh)
	for r := range resCh {
		if r.err != nil {
			return nil, r.err
		}
		results[r.idx] = r.tx
	}
	block.Tx = results
	return block, nil
}

// doWithBackoff retries on 429 with exponential backoff and respects Retry-After if present
func (c *restClient) doWithBackoff(ctx context.Context, req *http.Request) (*http.Response, error) {
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
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if d, parseErr := time.ParseDuration(ra + "s"); parseErr == nil {
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
