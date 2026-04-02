package tronpayment

import "errors"

var (
	ErrInvoiceNotFound    = errors.New("tronpayment: invoice not found")
	ErrInvoiceExpired     = errors.New("tronpayment: invoice has expired")
	ErrAllKeysRateLimited = errors.New("tronpayment: all API keys are rate limited")
	ErrNoAPIKeys          = errors.New("tronpayment: no API keys configured")
	ErrInvalidAddress     = errors.New("tronpayment: invalid TRON address")
	ErrPaymentNotFound    = errors.New("tronpayment: no matching payment found")
	ErrAmountConflict     = errors.New("tronpayment: no available amount offset, too many pending invoices for this amount")
	ErrInsufficientAmount = errors.New("tronpayment: received amount is less than expected")
	ErrTransactionFailed  = errors.New("tronpayment: transaction execution failed")
	ErrClientClosed       = errors.New("tronpayment: client has been closed")
)
