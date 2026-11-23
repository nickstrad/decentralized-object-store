package node

import "github.com/nickstrad/object-store/internal/storage"

// StatusRows returns the HTTP status mappings for this package's sentinel
// errors, intended to be passed as extra rows to httpapi.NewHandler or
// httpapi.NewServer.
func StatusRows() []storage.StatusRow {
	return []storage.StatusRow{
		{Err: ErrNotReady, Status: 503},
		{Err: ErrStopped, Status: 503},
		{Err: ErrUnsettled, Status: 503},
		{Err: ErrNoMigration, Status: 400},
		{Err: ErrUnknownSaga, Status: 404},
		{Err: ErrFenced, Status: 409},
		{Err: ErrStale, Status: 409},
		{Err: ErrMalformed, Status: 400},
		{Err: ErrFrame, Status: 400},
		{Err: ErrConfig, Status: 400},
	}
}
