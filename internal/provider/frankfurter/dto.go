package frankfurter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"time"
)

type rateResponse struct {
	Date  string          `json:"date"`
	Base  string          `json:"base"`
	Quote string          `json:"quote"`
	Rate  json.RawMessage `json:"rate"`
}

func toQuote(r rateResponse, requested domain.Pair) (*domain.Quote, error) {
	pair, err := domain.NewPair(domain.Currency(r.Base), domain.Currency(r.Quote))
	if err != nil {
		return nil, err
	}
	if *pair != requested {
		return nil, fmt.Errorf("response pair mismatch: %w", domain.ErrInvalidQuote)
	}
	date, err := time.Parse("2006-01-02", r.Date)
	if err != nil {
		return nil, fmt.Errorf("response date: %w", err)
	}
	raw := bytes.TrimSpace(r.Rate)
	// Require a JSON number, not a quoted decimal or null.
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return nil, domain.ErrInvalidPrice
	}
	price, err := domain.ParsePrice(string(raw))
	if err != nil {
		return nil, err
	}
	return domain.NewQuote(requested, price, "frankfurter:ecb", date)
}
