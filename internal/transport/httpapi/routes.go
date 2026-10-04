package httpapi

import "net/http"

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/quote-updates", h.CreateUpdate)
	mux.HandleFunc("GET /v1/quote-updates/{id}", h.GetByID)
	mux.HandleFunc("GET /v1/quotes/latest", h.GetLatest)
	// Method-free fallbacks keep routing errors in the same JSON format.
	method := func(allow string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", allow)
			h.writeError(w, 405, "method_not_allowed", "Method not allowed")
		}
	}
	mux.HandleFunc("/v1/quote-updates", method("POST"))
	mux.HandleFunc("/v1/quote-updates/{id}", method("GET, HEAD"))
	mux.HandleFunc("/v1/quotes/latest", method("GET, HEAD"))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		h.writeError(w, 404, "route_not_found", "Route not found")
	})
	return mux
}
