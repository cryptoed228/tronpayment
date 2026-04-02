package tronpayment

import (
	"sync"
	"time"
)

// Config holds the client configuration.
type Config struct {
	Network        Network
	APIKeys        []string
	ReceiveAddress string
	USDTContract   string
	RequestTimeout time.Duration
	InvoiceTTL     time.Duration
	PollInterval   time.Duration
	Confirmations  int
}

// DefaultConfig returns a Config with sensible defaults for mainnet
func DefaultConfig(receiveAddress string, apiKeys ...string) Config {
	return Config{
		Network:        Mainnet,
		APIKeys:        apiKeys,
		ReceiveAddress: receiveAddress,
		USDTContract:   MainnetUSDTContract,
		RequestTimeout: DefaultRequestTimeout,
		InvoiceTTL:     DefaultInvoiceTTL,
		PollInterval:   DefaultPollInterval,
		Confirmations:  DefaultConfirmations,
	}
}

func baseURLForNetwork(n Network) string {
	switch n {
	case Shasta:
		return ShastaBaseURL
	case Nile:
		return NileBaseURL
	default:
		return MainnetBaseURL
	}
}

// keyRotator manages round-robin API key rotation with backoff on rate limits.
type keyRotator struct {
	mu      sync.Mutex
	keys    []string
	current int
	backoff map[int]time.Time // index -> backoff-until timestamp
}

func newKeyRotator(keys []string) *keyRotator {
	return &keyRotator{
		keys:    keys,
		backoff: make(map[int]time.Time),
	}
}

// next returns the next available API key and its index.
// Returns ErrAllKeysRateLimited if every key is in backoff.
func (kr *keyRotator) next() (key string, index int, err error) {
	kr.mu.Lock()
	defer kr.mu.Unlock()

	now := time.Now()
	n := len(kr.keys)

	for i := 0; i < n; i++ {
		idx := (kr.current + i) % n
		if until, ok := kr.backoff[idx]; ok && now.Before(until) {
			continue
		}
		delete(kr.backoff, idx)
		kr.current = (idx + 1) % n
		return kr.keys[idx], idx, nil
	}

	return "", 0, ErrAllKeysRateLimited
}

// markRateLimited puts the key at the given index into backoff.
func (kr *keyRotator) markRateLimited(index int, d time.Duration) {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	kr.backoff[index] = time.Now().Add(d)
}
