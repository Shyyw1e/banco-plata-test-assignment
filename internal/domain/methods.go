package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// NewPrice constructs coefficient * 10^exponent and rounds half-even to 10 places.
func NewPrice(value int64, exp int32) (Price, error) {
	return checkedPrice(decimal.New(value, exp))
}

// ParsePrice preserves decimal precision without passing through float64.
func ParsePrice(raw string) (Price, error) {
	// Bound parsing work independently of the provider's HTTP response limit.
	if len(raw) == 0 || len(raw) > 128 {
		return Price{}, ErrInvalidPrice
	}
	value, err := decimal.NewFromString(raw)
	if err != nil {
		return Price{}, fmt.Errorf("%w: %w", ErrInvalidPrice, err)
	}
	return checkedPrice(value)
}

func checkedPrice(value decimal.Decimal) (Price, error) {
	if !value.IsPositive() {
		return Price{}, ErrInvalidPrice
	}
	// Check magnitude before rescaling, including extreme int32 exponents.
	order := int64(value.NumDigits()) + int64(value.Exponent())
	if order > 10 || order < -10 {
		return Price{}, ErrInvalidPrice
	}
	value = value.RoundBank(10)
	if !value.IsPositive() || value.GreaterThanOrEqual(decimal.New(1, 10)) {
		return Price{}, ErrInvalidPrice
	}
	return Price{value: value}, nil
}

func (p Price) Validate() error {
	checked, err := checkedPrice(p.value)
	if err != nil || !checked.value.Equal(p.value) {
		return ErrInvalidPrice
	}
	return nil
}

func (p Price) Decimal() decimal.Decimal { return p.value }
func (p Price) String() string           { return p.value.StringFixed(10) }

func NewPair(base, quote Currency) (*Pair, error) {
	for _, currency := range []Currency{base, quote} {
		if len(currency) != 3 {
			return nil, ErrInvalidPair
		}
		for _, c := range currency {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
				return nil, ErrInvalidPair
			}
		}
	}
	pair := Pair{Base: Currency(strings.ToUpper(string(base))), Quote: Currency(strings.ToUpper(string(quote)))}
	if err := pair.Validate(); err != nil {
		return nil, err
	}
	return &pair, nil
}

func ParsePair(raw string) (*Pair, error) {
	if len(raw) != 7 || raw[3] != '/' {
		return nil, ErrInvalidPair
	}
	return NewPair(Currency(raw[:3]), Currency(raw[4:]))
}

func (p Pair) Validate() error {
	for _, currency := range []Currency{p.Base, p.Quote} {
		if len(currency) != 3 {
			return ErrInvalidPair
		}
		for _, c := range currency {
			if c < 'A' || c > 'Z' {
				return ErrInvalidPair
			}
		}
	}
	if p.Base == p.Quote {
		return ErrSameCurrency
	}
	supported := func(c Currency) bool { return c == EUR || c == USD || c == MXN }
	if !supported(p.Base) || !supported(p.Quote) {
		return ErrUnsupportedPair
	}
	return nil
}

func (p Pair) String() string { return string(p.Base) + "/" + string(p.Quote) }

func NewUpdateID() (UpdateID, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return UpdateID{}, fmt.Errorf("generate update ID: %w", err)
	}
	return UpdateID(id), nil
}

func ParseUpdateID(raw string) (UpdateID, error) {
	if len(raw) != 36 || raw[8] != '-' || raw[13] != '-' || raw[18] != '-' || raw[23] != '-' {
		return UpdateID{}, ErrInvalidID
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return UpdateID{}, ErrInvalidID
	}
	return UpdateID(id), nil
}

func (id UpdateID) String() string { return uuid.UUID(id).String() }

func NewQuote(pair Pair, price Price, source string, sourceDate time.Time) (*Quote, error) {
	if sourceDate.IsZero() {
		return nil, ErrInvalidQuote
	}
	year, month, day := sourceDate.Date()
	quote := &Quote{
		Pair: pair, Price: price, Source: source,
		SourceDate: time.Date(year, month, day, 0, 0, 0, 0, time.UTC),
	}
	if err := quote.Validate(); err != nil {
		return nil, err
	}
	return quote, nil
}

func (q Quote) Validate() error {
	if err := q.Pair.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidQuote, err)
	}
	if err := q.Price.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidQuote, err)
	}
	if strings.TrimSpace(q.Source) == "" || q.Source != strings.TrimSpace(q.Source) || q.SourceDate.IsZero() {
		return ErrInvalidQuote
	}
	year, month, day := q.SourceDate.Date()
	if year < 1 || year > 9999 || !q.SourceDate.Equal(time.Date(year, month, day, 0, 0, 0, 0, time.UTC)) {
		return ErrInvalidQuote
	}
	return nil
}

func NewUpdateResult(quote Quote, updatedAt time.Time) (*UpdateResult, error) {
	result := &UpdateResult{Quote: quote, UpdatedAt: updatedAt.UTC()}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r UpdateResult) Validate() error {
	if err := r.Quote.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidResult, err)
	}
	if !validTime(r.UpdatedAt) {
		return ErrInvalidResult
	}
	return nil
}

func NewQuoteUpdate(id UpdateID, pair Pair, createdAt time.Time) (*QuoteUpdate, error) {
	update := &QuoteUpdate{ID: id, Pair: pair, Status: StatusQueued, CreatedAt: createdAt.UTC()}
	if err := update.Validate(); err != nil {
		return nil, err
	}
	return update, nil
}

func (u QuoteUpdate) Validate() error {
	if u.ID == UpdateID(uuid.Nil) || !validTime(u.CreatedAt) {
		return ErrInvalidUpdate
	}
	if err := u.Pair.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidUpdate, err)
	}
	switch u.Status {
	case StatusQueued, StatusProcessing:
		if u.Result != nil || u.ErrorCode != nil {
			return ErrInvalidUpdate
		}
	case StatusSucceeded:
		if u.Result == nil || u.ErrorCode != nil || u.Result.Quote.Pair != u.Pair {
			return ErrInvalidUpdate
		}
		if err := u.Result.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidUpdate, err)
		}
	case StatusFailed:
		if u.Result != nil || u.ErrorCode == nil || !validFailureCode(*u.ErrorCode) {
			return ErrInvalidUpdate
		}
	default:
		return ErrInvalidUpdate
	}
	return nil
}

func validFailureCode(code FailureCode) bool {
	switch code {
	case CodeUnavailable, CodeRejected, CodeInvalidResponse, CodeAttemptsExhausted:
		return true
	default:
		return false
	}
}

func NewAttempt(id UpdateID, pair Pair, number int64, leaseUntil time.Time) (*Attempt, error) {
	attempt := &Attempt{ID: id, Pair: pair, Number: number, LeaseUntil: leaseUntil.UTC()}
	if err := attempt.Validate(); err != nil {
		return nil, err
	}
	return attempt, nil
}

func (a Attempt) Validate() error {
	if a.ID == UpdateID(uuid.Nil) || a.Number <= 0 || !validTime(a.LeaseUntil) {
		return ErrInvalidAttempt
	}
	if err := a.Pair.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAttempt, err)
	}
	return nil
}

func validTime(value time.Time) bool {
	return !value.IsZero() && value.Year() >= 1 && value.Year() <= 9999
}
