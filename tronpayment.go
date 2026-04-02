package tronpayment

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client is the main entry point for the TRON USDT payment package.
type Client struct {
	config     Config
	callbacks  *EventCallbacks
	rotator    *keyRotator
	httpClient *http.Client
	baseURL    string

	mu       sync.RWMutex
	invoices map[string]*Invoice
	closed   bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a new Client. Callbacks are optional (may be nil).
func New(cfg Config, callbacks *EventCallbacks) (*Client, error) {
	if len(cfg.APIKeys) == 0 {
		return nil, ErrNoAPIKeys
	}
	if !ValidateAddress(cfg.ReceiveAddress) {
		return nil, ErrInvalidAddress
	}
	if cfg.USDTContract == "" {
		cfg.USDTContract = MainnetUSDTContract
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = DefaultRequestTimeout
	}
	if cfg.InvoiceTTL == 0 {
		cfg.InvoiceTTL = DefaultInvoiceTTL
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.Confirmations == 0 {
		cfg.Confirmations = DefaultConfirmations
	}

	return &Client{
		config:     cfg,
		callbacks:  callbacks,
		rotator:    newKeyRotator(cfg.APIKeys),
		httpClient: &http.Client{Timeout: cfg.RequestTimeout},
		baseURL:    baseURLForNetwork(cfg.Network),
		invoices:   make(map[string]*Invoice),
	}, nil
}

// CreateInvoice creates a payment invoice for the given USDT amount.
// The amount is in human-readable USDT (e.g. 10.50). A unique micro-offset
// is appended to distinguish this invoice from other pending invoices with
// the same base amount.
func (c *Client) CreateInvoice(ctx context.Context, amountUSDT float64, opts ...InvoiceOption) (*Invoice, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, ErrClientClosed
	}

	params := invoiceParams{
		ttl: c.config.InvoiceTTL,
	}
	for _, opt := range opts {
		opt(&params)
	}

	if params.id == "" {
		params.id = generateUUID()
	}

	if _, exists := c.invoices[params.id]; exists {
		return nil, fmt.Errorf("%w: %s", ErrAmountConflict, params.id)
	}

	baseSun := int64(amountUSDT * SunPerUSDT)

	// Find unused offset among pending invoices.
	used := make(map[int64]bool)
	for _, inv := range c.invoices {
		if inv.Status == StatusPending || inv.Status == StatusDetected {
			diff := inv.AmountSun - baseSun
			if diff >= 0 && diff <= MaxAmountOffset {
				used[diff] = true
			}
		}
	}

	offset := int64(-1)
	for i := int64(0); i <= MaxAmountOffset; i++ {
		if !used[i] {
			offset = i
			break
		}
	}
	if offset < 0 {
		return nil, ErrAmountConflict
	}

	now := time.Now()
	inv := &Invoice{
		ID:        params.id,
		Address:   c.config.ReceiveAddress,
		AmountSun: baseSun + offset,
		Status:    StatusPending,
		CreatedAt: now,
		ExpiresAt: now.Add(params.ttl),
		Metadata:  params.metadata,
	}

	c.invoices[inv.ID] = inv
	return inv, nil
}

// GetInvoice retrieves an invoice by ID.
func (c *Client) GetInvoice(_ context.Context, id string) (*Invoice, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	inv, ok := c.invoices[id]
	if !ok {
		return nil, ErrInvoiceNotFound
	}
	return inv, nil
}

// CheckPayment queries TronGrid for the invoice's expected payment,
// matches by exact sun amount, and updates the invoice status.
func (c *Client) CheckPayment(ctx context.Context, invoiceID string) (*PaymentResult, error) {
	c.mu.RLock()
	inv, ok := c.invoices[invoiceID]
	if !ok {
		c.mu.RUnlock()
		return nil, ErrInvoiceNotFound
	}
	c.mu.RUnlock()

	if inv.Status == StatusConfirmed {
		return &PaymentResult{Invoice: inv, Confirmed: true}, nil
	}

	now := time.Now()
	if inv.Status == StatusPending && now.After(inv.ExpiresAt) {
		c.mu.Lock()
		inv.Status = StatusExpired
		c.mu.Unlock()
		if c.callbacks != nil && c.callbacks.OnInvoiceExpired != nil {
			c.callbacks.OnInvoiceExpired(inv)
		}
		return nil, ErrInvoiceExpired
	}

	transfers, err := c.fetchTRC20Transfers(ctx, inv.Address, inv.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("fetch transfers: %w", err)
	}

	tx := c.matchPayment(transfers, inv)
	if tx == nil {
		return &PaymentResult{Invoice: inv}, nil
	}

	c.mu.Lock()
	inv.TxID = tx.TxID
	inv.ActualSun = tx.ValueSun

	if tx.Confirmed {
		info, err := c.getTransactionInfo(ctx, tx.TxID)
		if err == nil && info.BlockNumber > 0 {
			currentBlock, err := c.getCurrentBlock(ctx)
			if err == nil && (currentBlock-info.BlockNumber) >= int64(c.config.Confirmations) {
				inv.Status = StatusConfirmed
				inv.ConfirmedAt = time.Now()
				c.mu.Unlock()
				if c.callbacks != nil && c.callbacks.OnPaymentConfirmed != nil {
					c.callbacks.OnPaymentConfirmed(inv, tx)
				}
				return &PaymentResult{Invoice: inv, Transaction: tx, Confirmed: true}, nil
			}
		}
	}

	if inv.Status == StatusPending {
		inv.Status = StatusDetected
		c.mu.Unlock()
		if c.callbacks != nil && c.callbacks.OnPaymentDetected != nil {
			c.callbacks.OnPaymentDetected(inv, tx)
		}
	} else {
		c.mu.Unlock()
	}

	return &PaymentResult{Invoice: inv, Transaction: tx}, nil
}

// VerifyTransaction fetches full transaction info from the chain.
func (c *Client) VerifyTransaction(ctx context.Context, txID string) (*TransactionInfo, error) {
	return c.getTransactionInfo(ctx, txID)
}

// GetUSDTBalance returns the USDT balance in sun for the given address.
func (c *Client) GetUSDTBalance(ctx context.Context, address string) (int64, error) {
	if !ValidateAddress(address) {
		return 0, ErrInvalidAddress
	}

	// balanceOf(address) selector: 0x70a08231
	// ABI-encode the address (pad hex address to 32 bytes).
	hexAddr, err := addressToHex(address)
	if err != nil {
		return 0, fmt.Errorf("address to hex: %w", err)
	}
	parameter := fmt.Sprintf("%064s", hexAddr[2:]) // strip 0x41 prefix, pad to 64

	body := fmt.Sprintf(`{
		"owner_address": "%s",
		"contract_address": "%s",
		"function_selector": "balanceOf(address)",
		"parameter": "%s",
		"visible": true
	}`, address, c.config.USDTContract, parameter)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/wallet/triggerconstantcontract", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.doRequest(ctx, req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var result triggerConstantResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}

	if !result.Result.Result || len(result.ConstantResult) == 0 {
		return 0, fmt.Errorf("contract call failed: %s", result.Result.Message)
	}

	balance, err := strconv.ParseInt(strings.TrimLeft(result.ConstantResult[0], "0"), 16, 64)
	if err != nil {
		if result.ConstantResult[0] == strings.Repeat("0", len(result.ConstantResult[0])) {
			return 0, nil
		}
		return 0, fmt.Errorf("parse balance: %w", err)
	}

	return balance, nil
}

// GetTRC20Transfers returns TRC20 transfers for the given address.
func (c *Client) GetTRC20Transfers(ctx context.Context, address string, opts ...TransferOption) ([]TRC20Transaction, error) {
	params := transferParams{
		contractAddress: c.config.USDTContract,
		limit:           200,
		orderBy:         "block_timestamp,desc",
	}
	for _, opt := range opts {
		opt(&params)
	}
	return c.fetchTRC20TransfersWithParams(ctx, address, params)
}

// StartMonitor begins a background goroutine that periodically polls for
// incoming payments on all pending invoices.
func (c *Client) StartMonitor(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClientClosed
	}

	mctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.mu.Unlock()

	c.wg.Add(1)
	go c.monitorLoop(mctx)
	return nil
}

// Close gracefully shuts down the monitor and marks the client as closed.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return ErrClientClosed
	}
	c.closed = true

	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	return nil
}

// --- Internal methods ---

func (c *Client) monitorLoop(ctx context.Context) {
	defer c.wg.Done()

	ticker := time.NewTicker(c.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.pollPendingInvoices(ctx)
		}
	}
}

func (c *Client) pollPendingInvoices(ctx context.Context) {
	c.mu.RLock()
	var pending []*Invoice
	for _, inv := range c.invoices {
		if inv.Status == StatusPending || inv.Status == StatusDetected {
			pending = append(pending, inv)
		}
	}
	c.mu.RUnlock()

	if len(pending) == 0 {
		return
	}

	// Expire old invoices.
	now := time.Now()
	for _, inv := range pending {
		if inv.Status == StatusPending && now.After(inv.ExpiresAt) {
			c.mu.Lock()
			inv.Status = StatusExpired
			c.mu.Unlock()
			if c.callbacks != nil && c.callbacks.OnInvoiceExpired != nil {
				c.callbacks.OnInvoiceExpired(inv)
			}
			continue
		}
	}

	// Find the earliest created time among remaining pending invoices.
	var earliest time.Time
	for _, inv := range pending {
		if inv.Status == StatusPending || inv.Status == StatusDetected {
			if earliest.IsZero() || inv.CreatedAt.Before(earliest) {
				earliest = inv.CreatedAt
			}
		}
	}
	if earliest.IsZero() {
		return
	}

	transfers, err := c.fetchTRC20Transfers(ctx, c.config.ReceiveAddress, earliest)
	if err != nil {
		return
	}

	for _, inv := range pending {
		if inv.Status != StatusPending && inv.Status != StatusDetected {
			continue
		}
		tx := c.matchPayment(transfers, inv)
		if tx == nil {
			continue
		}

		c.mu.Lock()
		inv.TxID = tx.TxID
		inv.ActualSun = tx.ValueSun

		if tx.Confirmed {
			info, err := c.getTransactionInfo(ctx, tx.TxID)
			if err == nil && info.BlockNumber > 0 {
				currentBlock, err := c.getCurrentBlock(ctx)
				if err == nil && (currentBlock-info.BlockNumber) >= int64(c.config.Confirmations) {
					inv.Status = StatusConfirmed
					inv.ConfirmedAt = time.Now()
					c.mu.Unlock()
					if c.callbacks != nil && c.callbacks.OnPaymentConfirmed != nil {
						c.callbacks.OnPaymentConfirmed(inv, tx)
					}
					continue
				}
			}
		}

		if inv.Status == StatusPending {
			inv.Status = StatusDetected
			c.mu.Unlock()
			if c.callbacks != nil && c.callbacks.OnPaymentDetected != nil {
				c.callbacks.OnPaymentDetected(inv, tx)
			}
		} else {
			c.mu.Unlock()
		}
	}
}

func (c *Client) fetchTRC20Transfers(ctx context.Context, address string, since time.Time) ([]TRC20Transaction, error) {
	params := transferParams{
		contractAddress: c.config.USDTContract,
		onlyTo:          true,
		onlyConfirmed:   false,
		limit:           200,
		minTimestamp:     since.UnixMilli(),
		orderBy:         "block_timestamp,desc",
	}
	return c.fetchTRC20TransfersWithParams(ctx, address, params)
}

func (c *Client) fetchTRC20TransfersWithParams(ctx context.Context, address string, params transferParams) ([]TRC20Transaction, error) {
	u, err := url.Parse(fmt.Sprintf("%s/v1/accounts/%s/transactions/trc20", c.baseURL, address))
	if err != nil {
		return nil, err
	}

	q := u.Query()
	if params.contractAddress != "" {
		q.Set("contract_address", params.contractAddress)
	}
	if params.onlyTo {
		q.Set("only_to", "true")
	}
	if params.onlyFrom {
		q.Set("only_from", "true")
	}
	if params.onlyConfirmed {
		q.Set("only_confirmed", "true")
	}
	if params.limit > 0 {
		q.Set("limit", strconv.Itoa(params.limit))
	}
	if params.minTimestamp > 0 {
		q.Set("min_timestamp", strconv.FormatInt(params.minTimestamp, 10))
	}
	if params.maxTimestamp > 0 {
		q.Set("max_timestamp", strconv.FormatInt(params.maxTimestamp, 10))
	}
	if params.orderBy != "" {
		q.Set("order_by", params.orderBy)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.doRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var raw trc20Response
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode trc20 response: %w", err)
	}

	if !raw.Success {
		return nil, fmt.Errorf("trongrid returned success=false")
	}

	txs := make([]TRC20Transaction, 0, len(raw.Data))
	for _, r := range raw.Data {
		valueSun, _ := strconv.ParseInt(r.Value, 10, 64)
		txs = append(txs, TRC20Transaction{
			TxID:           r.TransactionID,
			From:           r.From,
			To:             r.To,
			ValueSun:       valueSun,
			BlockTimestamp:  r.BlockTimestamp,
			Confirmed:      r.Type == "Transfer",
			TokenSymbol:    r.TokenInfo.Symbol,
			TokenAddress:   r.TokenInfo.Address,
			TokenDecimals:  r.TokenInfo.Decimals,
		})
	}

	return txs, nil
}

func (c *Client) getTransactionInfo(ctx context.Context, txID string) (*TransactionInfo, error) {
	body := fmt.Sprintf(`{"value":"%s"}`, txID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/wallet/gettransactioninfobyid", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.doRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var info TransactionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode transaction info: %w", err)
	}

	return &info, nil
}

func (c *Client) getCurrentBlock(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/wallet/getnowblock", nil)
	if err != nil {
		return 0, err
	}

	resp, err := c.doRequest(ctx, req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var block blockResponse
	if err := json.NewDecoder(resp.Body).Decode(&block); err != nil {
		return 0, fmt.Errorf("decode block response: %w", err)
	}

	return block.BlockHeader.RawData.Number, nil
}

func (c *Client) matchPayment(transfers []TRC20Transaction, inv *Invoice) *TRC20Transaction {
	for i := range transfers {
		tx := &transfers[i]
		if tx.ValueSun == inv.AmountSun && strings.EqualFold(tx.To, inv.Address) {
			// Ensure this tx is not already matched to another invoice.
			alreadyUsed := false
			c.mu.RLock()
			for _, other := range c.invoices {
				if other.ID != inv.ID && other.TxID == tx.TxID {
					alreadyUsed = true
					break
				}
			}
			c.mu.RUnlock()
			if !alreadyUsed {
				return tx
			}
		}
	}
	return nil
}

// doRequest performs an HTTP request with API key rotation and retry on 429.
func (c *Client) doRequest(_ context.Context, req *http.Request) (*http.Response, error) {
	for range MaxRetries {
		key, idx, err := c.rotator.next()
		if err != nil {
			return nil, err
		}

		req.Header.Set(APIKeyHeader, key)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			c.rotator.markRateLimited(idx, KeyBackoffDuration)
			continue
		}

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("trongrid API error %d: %s", resp.StatusCode, string(body))
		}

		return resp, nil
	}

	return nil, ErrAllKeysRateLimited
}

// addressToHex converts a Base58Check TRON address to hex (0x41...).
func addressToHex(address string) (string, error) {
	decoded, err := base58Decode(address)
	if err != nil {
		return "", err
	}
	// Remove the 4-byte checksum.
	payload := decoded[:len(decoded)-4]
	return fmt.Sprintf("%x", payload), nil
}

// generateUUID creates a UUID v4 using crypto/rand.
func generateUUID() string {
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}
