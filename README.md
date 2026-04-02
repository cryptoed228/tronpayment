# tronpayment

A Go package for accepting USDT TRC20 payments via the TronGrid API. Zero external dependencies — built entirely on the Go standard library.

## Features

- **Invoice creation** with unique micro-amount offsets for payment matching
- **Background monitoring** with configurable polling interval
- **Event callbacks** for payment detection, confirmation, and expiration
- **Multiple API key rotation** with automatic backoff on rate limits (HTTP 429)
- **No database dependency** — invoices stored in memory; persistence is the consumer's responsibility
- **Address validation** using Base58Check (stdlib only)

## Install

```bash
go get github.com/crypoed228/tronpayment
```

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/crypoed228/tronpayment"
)

func main() {
	cfg := tronpayment.DefaultConfig(
		"TYourReceivingAddressHere",  // your TRON address
		"api-key-1", "api-key-2",     // one or more TronGrid API keys
	)

	client, err := tronpayment.New(cfg, &tronpayment.EventCallbacks{
		OnPaymentDetected: func(inv *tronpayment.Invoice, tx *tronpayment.TRC20Transaction) {
			fmt.Printf("Payment detected: invoice=%s tx=%s\n", inv.ID, tx.TxID)
		},
		OnPaymentConfirmed: func(inv *tronpayment.Invoice, tx *tronpayment.TRC20Transaction) {
			fmt.Printf("Payment confirmed: invoice=%s amount=%d sun\n", inv.ID, inv.ActualSun)
		},
		OnInvoiceExpired: func(inv *tronpayment.Invoice) {
			fmt.Printf("Invoice expired: %s\n", inv.ID)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	// Create an invoice for 15.00 USDT.
	inv, err := client.CreateInvoice(context.Background(), 15.00)
	if err != nil {
		log.Fatal(err)
	}

	// The exact amount the payer must send (includes micro-offset).
	fmt.Printf("Invoice %s: send exactly %.6f USDT to %s\n",
		inv.ID,
		float64(inv.AmountSun)/float64(tronpayment.SunPerUSDT),
		inv.Address,
	)

	// Start background monitor — polls TronGrid every PollInterval.
	if err := client.StartMonitor(context.Background()); err != nil {
		log.Fatal(err)
	}

	// Callbacks fire automatically when payment is detected/confirmed.
	select {}
}
```

## Usage

### Configuration

```go
// Default config for mainnet with sensible defaults.
cfg := tronpayment.DefaultConfig("TYourAddress", "key1", "key2", "key3")

// Or customize:
cfg := tronpayment.Config{
	Network:        tronpayment.Mainnet,
	APIKeys:        []string{"key1", "key2"},
	ReceiveAddress: "TYourAddress",
	USDTContract:   tronpayment.MainnetUSDTContract,
	RequestTimeout: 10 * time.Second,
	InvoiceTTL:     30 * time.Minute,
	PollInterval:   15 * time.Second,
	Confirmations:  19, // ~1 minute on TRON
}
```

| Field | Default | Description |
|---|---|---|
| `Network` | `Mainnet` | `Mainnet`, `Shasta`, or `Nile` |
| `APIKeys` | — | One or more `TRON-PRO-API-KEY` values from [trongrid.io](https://www.trongrid.io) |
| `ReceiveAddress` | — | Your TRON address that receives all payments |
| `USDTContract` | `TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t` | USDT contract address |
| `RequestTimeout` | `10s` | HTTP request timeout |
| `InvoiceTTL` | `30m` | Default invoice expiration |
| `PollInterval` | `15s` | Background monitor polling interval |
| `Confirmations` | `19` | Required block confirmations (~1 min) |

### API Key Rotation

Supply multiple API keys to avoid rate limiting. The client uses round-robin rotation and automatically skips keys that received HTTP 429 (60-second backoff per key).

```go
cfg := tronpayment.DefaultConfig("TYourAddress",
	"key-1",
	"key-2",
	"key-3",
)
```

### Creating Invoices

```go
// Basic — auto-generated UUID, default TTL.
inv, err := client.CreateInvoice(ctx, 10.50)

// With options.
inv, err := client.CreateInvoice(ctx, 25.00,
	tronpayment.WithInvoiceID("order-123"),
	tronpayment.WithTTL(1*time.Hour),
	tronpayment.WithMetadata(map[string]string{
		"order_id": "123",
		"user_id":  "456",
	}),
)
```

The returned `Invoice.AmountSun` contains the exact amount (with micro-offset) that the payer must send. Up to 1000 concurrent invoices can share the same base amount.

### Checking Payments Manually

```go
result, err := client.CheckPayment(ctx, "order-123")
if err != nil {
	log.Fatal(err)
}

switch {
case result.Confirmed:
	fmt.Println("Paid and confirmed!")
case result.Transaction != nil:
	fmt.Println("Payment detected, waiting for confirmations...")
default:
	fmt.Println("No payment yet.")
}
```

### Background Monitor

```go
client.StartMonitor(ctx)
// Callbacks fire automatically.
// Call client.Close() to stop.
```

### Querying TRC20 Transfers

```go
txs, err := client.GetTRC20Transfers(ctx, "TSomeAddress",
	tronpayment.WithOnlyTo(true),
	tronpayment.WithLimit(50),
	tronpayment.WithMinTimestamp(time.Now().Add(-24*time.Hour).UnixMilli()),
)
```

### Other Methods

```go
// Verify a specific transaction on chain.
info, err := client.VerifyTransaction(ctx, "txid_hex_here")

// Get USDT balance for any address.
balance, err := client.GetUSDTBalance(ctx, "TSomeAddress")

// Retrieve a stored invoice.
inv, err := client.GetInvoice(ctx, "order-123")

// Validate a TRON address.
ok := tronpayment.ValidateAddress("TYourAddress")
```

## How Payment Matching Works

1. Consumer calls `CreateInvoice(ctx, 10.50)`.
2. Package converts to sun (`10_500_000`) and finds the smallest unused offset `[0..999]` among pending invoices.
3. Final amount: `10_500_003` sun = `10.500003 USDT`.
4. Payer sends **exactly** `10.500003 USDT` to the receiving address.
5. Monitor (or manual `CheckPayment`) fetches TRC20 transfers and matches by exact sun amount.
6. Once the transaction reaches the configured confirmation depth, the invoice moves to `confirmed`.

### Invoice Lifecycle

```
pending → detected → confirmed
   ↓
expired
```

| Status | Meaning |
|---|---|
| `pending` | Awaiting payment |
| `detected` | Transaction seen but not yet confirmed |
| `confirmed` | Transaction confirmed with sufficient block depth |
| `expired` | TTL elapsed with no payment |

## Errors

```go
tronpayment.ErrInvoiceNotFound    // invoice ID not in memory
tronpayment.ErrInvoiceExpired     // invoice TTL elapsed
tronpayment.ErrAllKeysRateLimited // all API keys in backoff
tronpayment.ErrNoAPIKeys          // no API keys provided
tronpayment.ErrInvalidAddress     // bad TRON address
tronpayment.ErrPaymentNotFound    // no matching transfer found
tronpayment.ErrAmountConflict     // no available offset for this amount
tronpayment.ErrInsufficientAmount // received less than expected
tronpayment.ErrTransactionFailed  // on-chain execution failed
tronpayment.ErrClientClosed       // client already closed
```

## Testing

Use the Shasta testnet for development:

```go
cfg := tronpayment.Config{
	Network:        tronpayment.Shasta,
	APIKeys:        []string{"your-shasta-key"},
	ReceiveAddress: "TYourShastaAddress",
}
```

Get test TRX from the [Shasta faucet](https://shasta.tronscan.org/#/tools/trc20-token-faucet).

## License

MIT
