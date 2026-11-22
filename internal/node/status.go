package node

import "github.com/nickstrad/decentralized-object-store/internal/storage"

func StatusRows() []storage.StatusRow {
	return []storage.StatusRow{
		{Err: ErrNotReady, Status: 503},
		{Err: ErrStopped, Status: 503},
		{Err: ErrUnsettled, Status: 503},
		{Err: ErrNoMigration, Status: 400},
		{Err: ErrUnknownSaga, Status: 404},
		{Err: ErrFenced, Status: 409},
		{Err: ErrStale, Status: 409},
		{Err: ErrStaleRequest, Status: 409},
		{Err: ErrMalformed, Status: 400},
		{Err: ErrFrame, Status: 400},
		{Err: ErrConfig, Status: 400},
	}
}
