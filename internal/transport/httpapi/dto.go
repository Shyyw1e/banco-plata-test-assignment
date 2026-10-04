package httpapi

import (
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
)

type createRequest struct {
	Pair string `json:"pair"`
}

type receiptResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type updateResponse struct {
	ID         string            `json:"id"`
	Pair       string            `json:"pair"`
	Status     string            `json:"status"`
	Price      string            `json:"price,omitempty"`
	UpdatedAt  string            `json:"updated_at,omitempty"`
	SourceDate string            `json:"source_date,omitempty"`
	Source     string            `json:"source,omitempty"`
	Error      *jobErrorResponse `json:"error,omitempty"`
}

type jobErrorResponse struct {
	Code string `json:"code"`
}

type errorResponse struct {
	Error apiErrorResponse `json:"error"`
}

type apiErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func toUpdateResponse(update *domain.QuoteUpdate) (*updateResponse, error) {
	if update == nil {
		return nil, domain.ErrInvalidUpdate
	}
	if err := update.Validate(); err != nil {
		return nil, err
	}

	resp := updateResponse{
		ID:     update.ID.String(),
		Pair:   update.Pair.String(),
		Status: string(update.Status),
	}
	switch update.Status {
	case domain.StatusSucceeded:
		if update.Result.Quote.Source != "frankfurter:ecb" {
			return nil, domain.ErrInvalidQuote
		}
		resp.Price = update.Result.Quote.Price.String()
		resp.UpdatedAt = update.Result.UpdatedAt.UTC().Format(time.RFC3339Nano)
		resp.SourceDate = update.Result.Quote.SourceDate.Format("2006-01-02")
		resp.Source = update.Result.Quote.Source
	case domain.StatusFailed:
		resp.Error = &jobErrorResponse{Code: string(*update.ErrorCode)}
	}

	return &resp, nil
}
