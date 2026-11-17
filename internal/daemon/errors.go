package daemon

import (
	"errors"
	"fmt"
	"net/http"
)

var (
	errInvalid  = errors.New("invalid request")
	errNotFound = errors.New("not found")
	errConflict = errors.New("conflict")
)

// requestError preserves the human-readable message while allowing handlers
// to classify errors without matching strings.
type requestError struct {
	kind    error
	message string
}

func (e *requestError) Error() string { return e.message }
func (e *requestError) Unwrap() error { return e.kind }
func invalidf(format string, args ...any) error {
	return &requestError{errInvalid, fmt.Sprintf(format, args...)}
}
func notFoundf(format string, args ...any) error {
	return &requestError{errNotFound, fmt.Sprintf(format, args...)}
}
func conflictf(format string, args ...any) error {
	return &requestError{errConflict, fmt.Sprintf(format, args...)}
}

func writeAPIError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, errInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, errNotFound):
		status = http.StatusNotFound
	case errors.Is(err, errConflict):
		status = http.StatusConflict
	}
	http.Error(w, err.Error(), status)
}
