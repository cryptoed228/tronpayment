package tronpayment

import "time"

// Network represents the TRON network.
type Network string

const (
	Mainnet Network = "mainnet"
	Shasta  Network = "shasta"
	Nile    Network = "nile"
)

// InvoiceStatus represents the lifecycle state of an invoice.
type InvoiceStatus string

const (
	StatusPending   InvoiceStatus = "pending"
	StatusDetected  InvoiceStatus = "detected"
	StatusConfirmed InvoiceStatus = "confirmed"
	StatusExpired   InvoiceStatus = "expired"
)

// Base URLs for TronGrid API.
const (
	MainnetBaseURL = "https://api.trongrid.io"
	ShastaBaseURL  = "https://api.shasta.trongrid.io"
	NileBaseURL    = "https://nile.trongrid.io"
)

// USDT contract constants.
const (
	MainnetUSDTContract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	USDTDecimals        = 6
	SunPerUSDT          = 1_000_000
)

// Internal constants.
const (
	MaxAmountOffset    = 999
	APIKeyHeader       = "TRON-PRO-API-KEY"
	KeyBackoffDuration = 60 * time.Second
	MaxRetries         = 3

	DefaultPollInterval   = 15 * time.Second
	DefaultInvoiceTTL     = 30 * time.Minute
	DefaultRequestTimeout = 10 * time.Second
	DefaultConfirmations  = 19
)

// Invoice represents a payment request tracked in memory.
type Invoice struct {
	ID          string
	Address     string
	AmountSun   int64
	ActualSun   int64
	Status      InvoiceStatus
	TxID        string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	ConfirmedAt time.Time
	Metadata    map[string]string
}

// TRC20Transaction represents a parsed TRC20 transfer.
type TRC20Transaction struct {
	TxID           string
	From           string
	To             string
	ValueSun       int64
	BlockTimestamp  int64
	Confirmed      bool
	TokenSymbol    string
	TokenAddress   string
	TokenDecimals  int
}

// PaymentResult is returned by CheckPayment.
type PaymentResult struct {
	Invoice     *Invoice
	Transaction *TRC20Transaction
	Confirmed   bool
}

// EventCallbacks holds optional callback functions for payment events.
type EventCallbacks struct {
	OnPaymentDetected  func(invoice *Invoice, tx *TRC20Transaction)
	OnPaymentConfirmed func(invoice *Invoice, tx *TRC20Transaction)
	OnInvoiceExpired   func(invoice *Invoice)
}

// InvoiceOption configures optional invoice parameters.
type InvoiceOption func(*invoiceParams)

type invoiceParams struct {
	id       string
	ttl      time.Duration
	metadata map[string]string
}

// WithInvoiceID sets a custom invoice ID instead of auto-generated UUID.
func WithInvoiceID(id string) InvoiceOption {
	return func(p *invoiceParams) {
		p.id = id
	}
}

// WithTTL overrides the default invoice expiration duration.
func WithTTL(d time.Duration) InvoiceOption {
	return func(p *invoiceParams) {
		p.ttl = d
	}
}

// WithMetadata attaches arbitrary key-value data to the invoice.
func WithMetadata(meta map[string]string) InvoiceOption {
	return func(p *invoiceParams) {
		p.metadata = meta
	}
}

// TransferOption configures GetTRC20Transfers query parameters.
type TransferOption func(*transferParams)

type transferParams struct {
	contractAddress string
	onlyTo          bool
	onlyFrom        bool
	onlyConfirmed   bool
	limit           int
	minTimestamp     int64
	maxTimestamp     int64
	orderBy         string
}

// WithContractAddress filters transfers by TRC20 contract address.
func WithContractAddress(addr string) TransferOption {
	return func(p *transferParams) {
		p.contractAddress = addr
	}
}

// WithOnlyTo returns only incoming transfers.
func WithOnlyTo(v bool) TransferOption {
	return func(p *transferParams) {
		p.onlyTo = v
	}
}

// WithOnlyFrom returns only outgoing transfers.
func WithOnlyFrom(v bool) TransferOption {
	return func(p *transferParams) {
		p.onlyFrom = v
	}
}

// WithOnlyConfirmed returns only confirmed transfers.
func WithOnlyConfirmed(v bool) TransferOption {
	return func(p *transferParams) {
		p.onlyConfirmed = v
	}
}

// WithLimit sets the maximum number of transfers to return (max 200).
func WithLimit(n int) TransferOption {
	return func(p *transferParams) {
		p.limit = n
	}
}

// WithMinTimestamp filters transfers after the given unix timestamp (ms).
func WithMinTimestamp(ts int64) TransferOption {
	return func(p *transferParams) {
		p.minTimestamp = ts
	}
}

// WithMaxTimestamp filters transfers before the given unix timestamp (ms).
func WithMaxTimestamp(ts int64) TransferOption {
	return func(p *transferParams) {
		p.maxTimestamp = ts
	}
}

// WithOrderBy sets the sort order (e.g. "block_timestamp,asc").
func WithOrderBy(order string) TransferOption {
	return func(p *transferParams) {
		p.orderBy = order
	}
}

// --- TronGrid API response types ---

type trc20Response struct {
	Data    []trc20TxRaw `json:"data"`
	Success bool         `json:"success"`
	Meta    struct {
		At          int64  `json:"at"`
		PageSize    int    `json:"page_size"`
		Fingerprint string `json:"fingerprint"`
	} `json:"meta"`
}

type trc20TxRaw struct {
	TransactionID  string `json:"transaction_id"`
	From           string `json:"from"`
	To             string `json:"to"`
	Value          string `json:"value"`
	Type           string `json:"type"`
	BlockTimestamp int64  `json:"block_timestamp"`
	TokenInfo      struct {
		Symbol   string `json:"symbol"`
		Address  string `json:"address"`
		Decimals int    `json:"decimals"`
		Name     string `json:"name"`
	} `json:"token_info"`
}

// TransactionInfo maps the response from /wallet/gettransactioninfobyid.
type TransactionInfo struct {
	ID             string   `json:"id"`
	Fee            int64    `json:"fee"`
	BlockNumber    int64    `json:"blockNumber"`
	BlockTimestamp int64    `json:"blockTimeStamp"`
	ContractResult []string `json:"contractResult"`
	Receipt        struct {
		NetFee int64 `json:"net_fee"`
	} `json:"receipt"`
}

type triggerConstantResponse struct {
	Result struct {
		Result  bool   `json:"result"`
		Message string `json:"message"`
	} `json:"result"`
	ConstantResult []string `json:"constant_result"`
}

type blockResponse struct {
	BlockHeader struct {
		RawData struct {
			Number int64 `json:"number"`
		} `json:"raw_data"`
	} `json:"block_header"`
}
