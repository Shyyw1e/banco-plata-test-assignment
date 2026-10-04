package httpapi

import (
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"net/http"
	"net/url"
)

func (h *Handler) GetByID(w http.ResponseWriter, r *http.Request) {
	if r.Context().Err() != nil {
		return
	}
	id, err := domain.ParseUpdateID(r.PathValue("id"))
	if err != nil {
		h.handleServiceError(w, r, err, "")
		return
	}
	update, err := h.service.GetByID(r.Context(), id)
	if err != nil {
		h.handleServiceError(w, r, err, "update_not_found")
		return
	}
	response, err := toUpdateResponse(update)
	if err != nil {
		h.internalError(w)
		return
	}
	h.respond(w, 200, response)
}
func (h *Handler) GetLatest(w http.ResponseWriter, r *http.Request) {
	if r.Context().Err() != nil {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["pair"]) != 1 {
		h.writeError(w, 400, "invalid_query", "Invalid query parameters")
		return
	}
	pair, err := domain.ParsePair(query["pair"][0])
	if err != nil {
		h.handleServiceError(w, r, err, "")
		return
	}
	update, err := h.service.GetLatest(r.Context(), *pair)
	if err != nil {
		h.handleServiceError(w, r, err, "quote_not_found")
		return
	}
	if update == nil || update.Status != domain.StatusSucceeded {
		h.internalError(w)
		return
	}
	response, err := toUpdateResponse(update)
	if err != nil {
		h.internalError(w)
		return
	}
	h.respond(w, 200, response)
}
