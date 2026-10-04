package postgres

import (
	"database/sql"
	"fmt"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"time"
)

const updateColumns = `id::text, pair, status, created_at, completed_at,
 price::text, source_date, source, last_error_code`

func scanUpdate(row *sql.Row) (*domain.QuoteUpdate, error) {
	var rawID, rawPair, status string
	var created time.Time
	var completed, date sql.NullTime
	var price, source, code sql.NullString
	if err := row.Scan(&rawID, &rawPair, &status, &created, &completed, &price, &date, &source, &code); err != nil {
		return nil, fmt.Errorf("scan update: %w", err)
	}
	id, err := domain.ParseUpdateID(rawID)
	if err != nil {
		return nil, fmt.Errorf("stored ID: %w", err)
	}
	pair, err := domain.ParsePair(rawPair)
	if err != nil {
		return nil, fmt.Errorf("stored pair: %w", err)
	}
	u := &domain.QuoteUpdate{ID: id, Pair: *pair, Status: domain.UpdateStatus(status), CreatedAt: created.UTC()}
	invalid := func() (*domain.QuoteUpdate, error) {
		return nil, fmt.Errorf("inconsistent stored fields: %w", domain.ErrInvalidUpdate)
	}
	switch u.Status {
	case domain.StatusSucceeded:
		if !completed.Valid || !price.Valid || !date.Valid || !source.Valid || code.Valid {
			return invalid()
		}
		p, err := domain.ParsePrice(price.String)
		if err != nil {
			return nil, fmt.Errorf("stored price: %w", err)
		}
		q, err := domain.NewQuote(*pair, p, source.String, date.Time)
		if err != nil {
			return nil, err
		}
		result, err := domain.NewUpdateResult(*q, completed.Time)
		if err != nil {
			return nil, err
		}
		u.Result = result
	case domain.StatusFailed:
		if !completed.Valid || !code.Valid || price.Valid || date.Valid || source.Valid {
			return invalid()
		}
		c := domain.FailureCode(code.String)
		u.ErrorCode = &c
	case domain.StatusQueued, domain.StatusProcessing:
		if completed.Valid || price.Valid || date.Valid || source.Valid {
			return invalid()
		}
	default:
		return invalid()
	}
	if err := u.Validate(); err != nil {
		return nil, fmt.Errorf("stored update: %w", err)
	}
	return u, nil
}
