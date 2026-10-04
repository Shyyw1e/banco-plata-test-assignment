package httpapi

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, err = w.Write(append(data, '\n'))
	return err
}
func (h *Handler) respond(w http.ResponseWriter, status int, body any) {
	if err := writeJSON(w, status, body); err != nil {
		h.log.Warn("HTTP response write failed", "status", status)
	}
}
