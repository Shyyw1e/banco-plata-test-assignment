package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
	"io"
	"mime"
	"net/http"
	"strings"
)

const maxRequestBodyBytes int64 = 4096

func (h *Handler) CreateUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Context().Err() != nil {
		return
	}
	types := r.Header.Values("Content-Type")
	if len(types) != 1 {
		h.writeError(w, 415, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	media, params, err := mime.ParseMediaType(types[0])
	valid := err == nil && media == "application/json"
	for k, v := range params {
		if k != "charset" || !strings.EqualFold(v, "utf-8") {
			valid = false
		}
	}
	if !valid {
		h.writeError(w, 415, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			h.writeError(w, 413, "request_too_large", "Request body exceeds 4096 bytes")
		} else {
			h.writeError(w, 400, "invalid_json", "Invalid JSON body")
		}
		return
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var request *createRequest
	if err = dec.Decode(&request); err != nil || request == nil {
		h.writeError(w, 400, "invalid_json", "Invalid JSON body")
		return
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		h.writeError(w, 400, "invalid_json", "Invalid JSON body")
		return
	}
	// encoding/json matches struct field names case-insensitively, while the
	// API schema allows only the exact property name "pair".
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		h.writeError(w, 400, "invalid_json", "Invalid JSON body")
		return
	}
	for field := range fields {
		if field != "pair" {
			h.writeError(w, 400, "invalid_json", "Invalid JSON body")
			return
		}
	}
	pair, err := domain.ParsePair(request.Pair)
	if err != nil {
		h.handleServiceError(w, r, err, "")
		return
	}
	var key *string
	values := r.Header.Values("Idempotency-Key")
	if len(values) > 1 {
		h.handleServiceError(w, r, usecase.ErrInvalidIdempotencyKey, "")
		return
	}
	if len(values) == 1 {
		value := values[0]
		valid := len(value) >= 1 && len(value) <= 128
		for i := 0; i < len(value); i++ {
			if value[i] < '!' || value[i] > '~' {
				valid = false
			}
		}
		if !valid {
			h.handleServiceError(w, r, usecase.ErrInvalidIdempotencyKey, "")
			return
		}
		key = &value
	}
	update, err := h.service.CreateUpdate(r.Context(), *pair, key)
	if err != nil {
		h.handleServiceError(w, r, err, "")
		return
	}
	if update == nil {
		h.internalError(w)
		return
	}
	if err = update.Validate(); err != nil {
		h.internalError(w)
		return
	}
	status := http.StatusAccepted
	if update.Status == domain.StatusSucceeded || update.Status == domain.StatusFailed {
		status = http.StatusOK
	}
	w.Header().Set("Location", "/v1/quote-updates/"+update.ID.String())
	h.respond(w, status, receiptResponse{ID: update.ID.String(), Status: string(update.Status)})
}
