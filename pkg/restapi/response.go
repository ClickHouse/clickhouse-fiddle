package restapi

import (
	"encoding/json"
	"net/http"

	zlog "github.com/rs/zerolog/log"
)

type Response struct {
	Result any            `json:"result,omitempty"`
	Error  *ErrorResponse `json:"error,omitempty"`
}

type ErrorResponse struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

func writeError(w http.ResponseWriter, msg string, code int) {
	if code < 600 { // nolint
		w.WriteHeader(code)
	} else {
		w.WriteHeader(http.StatusInternalServerError)
	}

	writeResponse(w, &Response{
		Error: &ErrorResponse{
			Message: msg,
			Code:    code,
		},
	})
}

func writeResult(w http.ResponseWriter, result any) {
	writeResponse(w, &Response{
		Result: result,
	})
}

func writeResponse(w http.ResponseWriter, resp *Response) {
	err := json.NewEncoder(w).Encode(resp)
	if err != nil {
		zlog.Error().Err(err).Interface("response", resp).Msg("response encoding failed")
	}
}
