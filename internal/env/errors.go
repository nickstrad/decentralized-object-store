package env

import (
	"errors"
	"fmt"
	"strconv"
)

// Sentinel errors returned by Transport, Disk, and simulator operations:
//   - ErrTimeout: a request expired before any response arrived; the server may still have acted.
//   - ErrUnreachable: the target node is unknown or refused the connection while down.
//   - ErrClosed: the node incarnation's Transport or Disk handle has been closed.
//   - ErrTooLarge: a datagram, request, or reply exceeded the configured size limit.
//   - ErrRemote: an error returned across the wire from a peer; every *RemoteError matches it.
//   - ErrNotFound: a named blob does not exist in the disk's written layer.
//   - ErrIO: an injected or underlying disk I/O failure.
//   - ErrInvalidArgument: a caller supplied an out-of-range or empty parameter.
var (
	ErrTimeout         = errors.New("timeout")
	ErrUnreachable     = errors.New("unreachable")
	ErrClosed          = errors.New("closed")
	ErrTooLarge        = errors.New("too large")
	ErrRemote          = errors.New("remote error")
	ErrNotFound        = errors.New("not found")
	ErrIO              = errors.New("i/o error")
	ErrInvalidArgument = errors.New("invalid argument")
)

// Code is a single-byte wire status code sent in place of Go error values.
type Code uint8

// Wire status codes for built-in env sentinels. CodeOK marks a nil error;
// codes below CodeAppMin are reserved for this package, and application
// protocols register their own sentinels at CodeAppMin and above.
const (
	CodeOK              Code = 0
	CodeRemote          Code = 1
	CodeTimeout         Code = 2
	CodeUnreachable     Code = 3
	CodeClosed          Code = 4
	CodeTooLarge        Code = 5
	CodeNotFound        Code = 6
	CodeIO              Code = 7
	CodeInvalidArgument Code = 8
	CodeAppMin          Code = 64
)

// MaxErrorMessage caps the byte length of the error message carried on the wire.
const MaxErrorMessage = 200

// CodeEntry pairs a wire Code with the sentinel error it represents.
type CodeEntry struct {
	Code Code
	Err  error
}

// CodeTable maps Go errors to wire status codes on send and decodes them back
// to *RemoteError values on receipt.
type CodeTable struct {
	entries []CodeEntry
	byCode  [256]error
}

// baseEntries order matters: ErrRemote is last so a relayed *RemoteError
// matches its specific sentinel before falling back to CodeRemote.
var baseEntries = []CodeEntry{
	{CodeTimeout, ErrTimeout},
	{CodeUnreachable, ErrUnreachable},
	{CodeClosed, ErrClosed},
	{CodeTooLarge, ErrTooLarge},
	{CodeNotFound, ErrNotFound},
	{CodeIO, ErrIO},
	{CodeInvalidArgument, ErrInvalidArgument},
	{CodeRemote, ErrRemote},
}

var defaultCodes = mustCodeTable()

// mustCodeTable builds the package default table from the static baseEntries;
// failure indicates an internal bug in this package.
func mustCodeTable() *CodeTable {
	t, err := NewCodeTable()
	if err != nil {
		panic(err)
	}
	return t
}

// DefaultCodes returns a read-only CodeTable containing the built-in env sentinels.
func DefaultCodes() *CodeTable { return defaultCodes }

// NewCodeTable builds a CodeTable containing the given application entries plus
// all built-in env entries. Application entries are checked before built-in
// entries during Encode. It returns ErrInvalidArgument if any entry has a code
// below CodeAppMin, a nil error, or a duplicate code.
func NewCodeTable(app ...CodeEntry) (*CodeTable, error) {
	t := &CodeTable{}
	for _, e := range app {
		if e.Code < CodeAppMin {
			return nil, fmt.Errorf("env: code table: code %d below CodeAppMin: %w", e.Code, ErrInvalidArgument)
		}
		if e.Err == nil {
			return nil, fmt.Errorf("env: code table: code %d has nil error: %w", e.Code, ErrInvalidArgument)
		}
		if t.byCode[e.Code] != nil {
			return nil, fmt.Errorf("env: code table: code %d registered twice: %w", e.Code, ErrInvalidArgument)
		}
		t.byCode[e.Code] = e.Err
		t.entries = append(t.entries, e)
	}
	for _, e := range baseEntries {
		t.byCode[e.Code] = e.Err
		t.entries = append(t.entries, e)
	}
	return t, nil
}

// Entries returns a defensive copy of all registered entries in match order.
func (t *CodeTable) Entries() []CodeEntry {
	out := make([]CodeEntry, len(t.entries))
	copy(out, t.entries)
	return out
}

// Encode maps err to its wire status code and truncated message string.
// A nil error encodes as (CodeOK, ""); an unrecognized error encodes as CodeRemote.
func (t *CodeTable) Encode(err error) (Code, string) {
	if err == nil {
		return CodeOK, ""
	}
	msg := err.Error()
	if len(msg) > MaxErrorMessage {
		msg = msg[:MaxErrorMessage]
	}
	for _, e := range t.entries {
		if errors.Is(err, e.Err) {
			return e.Code, msg
		}
	}
	return CodeRemote, msg
}

// Decode reconstructs an error from a wire code and message.
// CodeOK decodes as nil; every non-zero code returns a *RemoteError matching
// ErrRemote and, when the code is registered, its mapped sentinel.
func (t *CodeTable) Decode(code Code, msg string) error {
	if code == CodeOK {
		return nil
	}
	return &RemoteError{Code: code, Msg: msg, sentinel: t.byCode[code]}
}

// RemoteError represents an error returned by a remote peer across a Transport.
// It unwraps to ErrRemote and, when its Code is known to the CodeTable, to the
// corresponding sentinel error.
type RemoteError struct {
	Code     Code
	Msg      string
	sentinel error
}

// Error formats the wire status code and remote message.
func (e *RemoteError) Error() string {
	return "remote error (code " + strconv.Itoa(int(e.Code)) + "): " + e.Msg
}

// Unwrap exposes the mapped sentinel (if distinct from ErrRemote) and ErrRemote
// so errors.Is matches both.
func (e *RemoteError) Unwrap() []error {
	if e.sentinel == nil || e.sentinel == ErrRemote {
		return []error{ErrRemote}
	}
	return []error{e.sentinel, ErrRemote}
}
