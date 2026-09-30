package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type Currency string
type UpdateID uuid.UUID // uuid - значение
type UpdateStatus string
type FailureCode string

const (
	EUR Currency = "EUR"
	USD Currency = "USD"
	MXN Currency = "MXN"

	StatusQueued     UpdateStatus = "queued"
	StatusProcessing UpdateStatus = "processing"
	StatusSucceeded  UpdateStatus = "succeeded"
	StatusFailed     UpdateStatus = "failed"

	CodeUnavailable       FailureCode = "provider_unavailable"
	CodeRejected          FailureCode = "provider_rejected"
	CodeInvalidResponse   FailureCode = "provider_invalid_response"
	CodeAttemptsExhausted FailureCode = "attempts_exhausted"
)

var (
	ErrSameCurrency    = errors.New("pair must contain different currencies")
	ErrInvalidPair     = errors.New("invalid currency pair")
	ErrUnsupportedPair = errors.New("unsupported currency pair")
	ErrInvalidID       = errors.New("invalid update ID")
	ErrInvalidPrice    = errors.New("invalid price")
	ErrInvalidQuote    = errors.New("invalid quote")
	ErrInvalidResult   = errors.New("invalid update result")
	ErrInvalidUpdate   = errors.New("invalid quote update")
	ErrInvalidAttempt  = errors.New("invalid attempt")
)

/*
Если валютная пара USD/EUR, это показывает что USD - базовая валюта (Base), EUR - котируемая (Quote)
Т.е. сколько будет стоить 1 USD в EUR.
*/
type Pair struct {
	Base  Currency
	Quote Currency
}

type Price struct {
	value decimal.Decimal
}

// Котировка, полученная от провайдера (в данном случае frankfurter.dev)
type Quote struct {
	Pair       Pair      // валютная пара
	Price      Price     // значение курса
	Source     string    // источник (frankfurter)
	SourceDate time.Time // дата курса у источника
}

// Результат обновления
type UpdateResult struct {
	Quote     Quote
	UpdatedAt time.Time
}

// Задание на обновление
type QuoteUpdate struct {
	ID        UpdateID      // идентификатор задания
	Pair      Pair          // запрошенная валютная пара
	Status    UpdateStatus  // состояние задания
	Result    *UpdateResult // результат, только у succeeded
	ErrorCode *FailureCode  // код ошибки, только у failed

	CreatedAt time.Time // время создания задания
}

type Attempt struct {
	ID         UpdateID
	Pair       Pair
	Number     int64
	LeaseUntil time.Time
}
