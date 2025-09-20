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
	GetBlockCount(ctx context.Context) (uint64, error)
	GetBlockHash(ctx context.Context, height uint64) (string, error)
	GetBlockVerbose(ctx context.Context, hash string) (*btcBlock, error)
}

type restClient struct {
	base *url.URL
	http *http.Client
}

func newRESTClient(cfg *config.BitcoinConfig) (*restClient, error) {
	if cfg.APIURL == "" {
		return nil, fmt.Errorf("api_url not set")
	}
	u, err := url.Parse(cfg.APIURL)
	if err != nil {
		return nil, err
	}
	return &restClient{base: u, http: &http.Client{Timeout: cfg.RPCTimeout}}, nil
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
	resp, err := c.http.Do(req)
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/block-height/%d", height)), nil)
	resp, err := c.http.Do(req)
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/block/%s/txids", hash)), nil)
	resp, err := c.http.Do(req)
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
	block.Tx = make([]btcTx, 0, len(txids))
	for _, txid := range txids {
		reqTx, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/tx/%s", txid)), nil)
		respTx, err := c.http.Do(reqTx)
		if err != nil {
			return nil, err
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
			return nil, err
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
		block.Tx = append(block.Tx, tx)
	}
	return block, nil
}
