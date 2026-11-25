package httpclient

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/storage"
)

// HTTPError is a non-2xx response from the object-store HTTP API. It carries
// the status code and the server's error message, and matches the repository
// sentinels with errors.Is as the inverse of httpapi.StatusTable: a sentinel
// matches when the table maps it to Status and either the message names it
// (the server's message is the wrapped error's text, so it contains the
// sentinel's own text) or the message names no sentinel of that status and the
// sentinel is the status's fallback.
type HTTPError struct {
	Status  int
	Message string
	rows    []storage.StatusRow
}

// Error formats the status code and server message.
func (e *HTTPError) Error() string {
	return fmt.Sprintf("http %d %s: %s", e.Status, http.StatusText(e.Status), e.Message)
}

// StatusCode returns the HTTP status code of the response.
func (e *HTTPError) StatusCode() int { return e.Status }

// Is reports whether target is a sentinel the server maps to e.Status and that
// e.Message names, or the status's fallback when the message names none.
func (e *HTTPError) Is(target error) bool {
	st, ok := statusFor(e.rows, target)
	if !ok || st != e.Status {
		return false
	}
	if strings.Contains(e.Message, target.Error()) {
		return true
	}
	for _, r := range e.rows {
		if r.Status == e.Status && strings.Contains(e.Message, r.Err.Error()) {
			return false
		}
	}
	return slices.Contains(fallback(e.Status), target)
}

// statusFor returns the status of the first row in rows whose sentinel target
// is or wraps, mirroring httpapi.StatusOf, and false when no row matches.
func statusFor(rows []storage.StatusRow, target error) (int, bool) {
	if target == nil {
		return 0, false
	}
	for _, r := range rows {
		if errors.Is(target, r.Err) {
			return r.Status, true
		}
	}
	return 0, false
}

// fallback returns the sentinels an HTTPError with the given status matches
// when its message names no sentinel of that status (for example a proxy
// error page). Each fallback maps to status in httpapi.StatusTable; 500 has no
// fallback because it also stands for every unmapped error.
func fallback(status int) []error {
	switch status {
	case http.StatusNotFound:
		return []error{storage.ErrNotFound, occ.ErrNotFound}
	case http.StatusPreconditionFailed:
		return []error{occ.ErrPreconditionFailed}
	case http.StatusConflict:
		return []error{storage.ErrExists}
	case http.StatusBadRequest:
		return []error{storage.ErrInvalid}
	case http.StatusRequestEntityTooLarge:
		return []error{env.ErrTooLarge}
	case http.StatusMethodNotAllowed:
		return []error{storage.ErrMethod}
	case http.StatusServiceUnavailable:
		return []error{storage.ErrUnavailable}
	case http.StatusGatewayTimeout:
		return []error{env.ErrTimeout}
	case http.StatusInsufficientStorage:
		return []error{occ.ErrQuotaExceeded}
	}
	return nil
}
