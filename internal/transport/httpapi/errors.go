package httpapi

import (
	"context"
	"errors"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
	"net/http"
)

func (h *Handler) writeError(w http.ResponseWriter, status int, code, message string) {
	h.respond(w, status, errorResponse{Error: apiErrorResponse{Code: code, Message: message}})
}
func (h *Handler) internalError(w http.ResponseWriter) {
	h.log.Error("HTTP request failed", "code", "internal_error")
	h.writeError(w, 500, "internal_error", "Internal server error")
}
func (h *Handler) handleServiceError(w http.ResponseWriter, r *http.Request, err error, notFoundCode string) {
	if r.Context().Err() != nil {
		return
	}
	status, code, message := 500, "internal_error", "Internal server error"
	switch {
	case errors.Is(err, domain.ErrInvalidPair), errors.Is(err, domain.ErrSameCurrency):
		status, code, message = 400, "invalid_pair", "Invalid currency pair"
	case errors.Is(err, domain.ErrUnsupportedPair):
		status, code, message = 422, "unsupported_pair", "Currency pair is not supported"
	case errors.Is(err, domain.ErrInvalidID):
		status, code, message = 400, "invalid_id", "Invalid update ID"
	case errors.Is(err, usecase.ErrInvalidIdempotencyKey):
		status, code, message = 400, "invalid_idempotency_key", "Invalid idempotency key"
	case errors.Is(err, repository.ErrNotFound):
		if notFoundCode == "update_not_found" {
			status, code, message = 404, notFoundCode, "Update not found"
		} else if notFoundCode == "quote_not_found" {
			status, code, message = 404, notFoundCode, "Quote not found"
		}
	case errors.Is(err, repository.ErrIdempotencyConflict):
		status, code, message = 409, "idempotency_conflict", "Idempotency key belongs to a different pair"
	case errors.Is(err, repository.ErrQueueFull):
		status, code, message = 503, "queue_full", "Update queue is full"
	case errors.Is(err, repository.ErrUnavailable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status, code, message = 503, "database_unavailable", "Database is unavailable"
	}
	if status == 503 {
		w.Header().Set("Retry-After", "1")
	}
	if status >= 500 {
		h.log.Error("HTTP request failed", "code", code, "method", r.Method)
	}
	h.writeError(w, status, code, message)
}
