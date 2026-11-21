package env

import (
	"errors"
	"fmt"
	"strconv"
)

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

type Code uint8

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

const MaxErrorMessage = 200

type CodeEntry struct {
	Code Code
	Err  error
}

type CodeTable struct {
	entries []CodeEntry
	byCode  [256]error
}

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

func mustCodeTable() *CodeTable {
	t, err := NewCodeTable()
	if err != nil {
		panic(err)
	}
	return t
}

func DefaultCodes() *CodeTable { return defaultCodes }

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

func (t *CodeTable) Entries() []CodeEntry {
	out := make([]CodeEntry, len(t.entries))
	copy(out, t.entries)
	return out
}

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

func (t *CodeTable) Decode(code Code, msg string) error {
	if code == CodeOK {
		return nil
	}
	return &RemoteError{Code: code, Msg: msg, sentinel: t.byCode[code]}
}

type RemoteError struct {
	Code     Code
	Msg      string
	sentinel error
}

func (e *RemoteError) Error() string {
	return "remote error (code " + strconv.Itoa(int(e.Code)) + "): " + e.Msg
}

func (e *RemoteError) Unwrap() []error {
	if e.sentinel == nil || e.sentinel == ErrRemote {
		return []error{ErrRemote}
	}
	return []error{e.sentinel, ErrRemote}
}
