package env_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
)

// TestCodeTableRoundTripsEveryRow proves that every registered sentinel
// (built-in and application) survives Encode and Decode with its message and
// matches both the sentinel and ErrRemote.
func TestCodeTableRoundTripsEveryRow(t *testing.T) {
	errApp := errors.New("app condition")
	table, err := env.NewCodeTable(env.CodeEntry{Code: env.CodeAppMin, Err: errApp})
	if err != nil {
		t.Fatalf("NewCodeTable: %v", err)
	}
	rows := table.Entries()
	if len(rows) != 9 {
		t.Fatalf("got %d rows, want 9", len(rows))
	}
	for _, row := range rows {
		t.Run(row.Err.Error(), func(t *testing.T) {
			wrapped := fmt.Errorf("op on object: %w", row.Err)
			code, msg := table.Encode(wrapped)
			if code != row.Code {
				t.Fatalf("Encode code = %d, want %d", code, row.Code)
			}
			back := table.Decode(code, msg)
			if !errors.Is(back, row.Err) {
				t.Fatalf("Decode(Encode(%v)) = %v, not Is %v", wrapped, back, row.Err)
			}
			if !errors.Is(back, env.ErrRemote) {
				t.Fatalf("decoded error %v is not Is ErrRemote", back)
			}
			var re *env.RemoteError
			if !errors.As(back, &re) || re.Msg != wrapped.Error() {
				t.Fatalf("decoded error lost its message: %#v", back)
			}
		})
	}
}

// TestDefaultCodesCoverEverySentinel proves that DefaultCodes maps all eight
// built-in env sentinels cleanly across Encode and Decode.
func TestDefaultCodesCoverEverySentinel(t *testing.T) {
	sentinels := []error{env.ErrTimeout, env.ErrUnreachable, env.ErrClosed, env.ErrTooLarge,
		env.ErrRemote, env.ErrNotFound, env.ErrIO, env.ErrInvalidArgument}
	table := env.DefaultCodes()
	for _, s := range sentinels {
		code, msg := table.Encode(s)
		if !errors.Is(table.Decode(code, msg), s) {
			t.Errorf("sentinel %v does not round-trip (code %d)", s, code)
		}
	}
}

// TestCodeTableEdges proves boundary behaviour for nil errors, unknown codes
// and errors, message truncation, relayed RemoteErrors, and Entries copying.
func TestCodeTableEdges(t *testing.T) {
	table := env.DefaultCodes()
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"nil error encodes as OK and OK decodes as nil", func(t *testing.T) {
			code, msg := table.Encode(nil)
			if code != env.CodeOK || msg != "" {
				t.Fatalf("Encode(nil) = %d %q", code, msg)
			}
			if err := table.Decode(env.CodeOK, "ignored"); err != nil {
				t.Fatalf("Decode(OK) = %v", err)
			}
		}},
		{"an unknown error encodes as CodeRemote", func(t *testing.T) {
			code, _ := table.Encode(errors.New("mystery"))
			if code != env.CodeRemote {
				t.Fatalf("code = %d, want CodeRemote", code)
			}
		}},
		{"an unknown code decodes to ErrRemote only", func(t *testing.T) {
			err := table.Decode(200, "x")
			if !errors.Is(err, env.ErrRemote) || errors.Is(err, env.ErrNotFound) {
				t.Fatalf("Decode(200) = %v", err)
			}
			if !strings.Contains(err.Error(), "200") {
				t.Fatalf("message %q does not name the code", err.Error())
			}
		}},
		{"a long message is truncated to MaxErrorMessage", func(t *testing.T) {
			_, msg := table.Encode(errors.New(strings.Repeat("x", 1000)))
			if len(msg) != env.MaxErrorMessage {
				t.Fatalf("len = %d", len(msg))
			}
		}},
		{"a remote error relayed by a server keeps its sentinel", func(t *testing.T) {
			inner := table.Decode(env.CodeNotFound, "missing")
			code, _ := table.Encode(fmt.Errorf("relay: %w", inner))
			if code != env.CodeNotFound {
				t.Fatalf("relayed code = %d", code)
			}
		}},
		{"Entries returns a copy", func(t *testing.T) {
			rows := table.Entries()
			rows[0].Err = nil
			if table.Entries()[0].Err == nil {
				t.Fatal("Entries exposed internal state")
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, tc.run)
	}
}

// TestNewCodeTableRejectsBadRows proves that reserved codes, nil errors, and
// duplicate application codes all fail with ErrInvalidArgument.
func TestNewCodeTableRejectsBadRows(t *testing.T) {
	e := errors.New("e")
	tests := []struct {
		name string
		rows []env.CodeEntry
	}{
		{"a code inside the reserved env range", []env.CodeEntry{{Code: 10, Err: e}}},
		{"a nil error", []env.CodeEntry{{Code: 70, Err: nil}}},
		{"a duplicate code", []env.CodeEntry{{Code: 70, Err: e}, {Code: 70, Err: errors.New("f")}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := env.NewCodeTable(tc.rows...); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

// TestTimeArithmetic proves Time.Add, Time.Sub, and Time.String formatting.
func TestTimeArithmetic(t *testing.T) {
	var t0 env.Time = 1000
	if got := t0.Add(5 * time.Microsecond); got != 6000 {
		t.Fatalf("Add = %d", got)
	}
	if got := env.Time(6000).Sub(t0); got != 5*time.Microsecond {
		t.Fatalf("Sub = %v", got)
	}
	if t0.String() != "1000ns" {
		t.Fatalf("String = %q", t0.String())
	}
}

type fakeNet struct{ env.Transport }
type fakeDisk struct{ env.Disk }
type fakeClock struct{ env.Clock }

// TestEnvDerivationReplacesOneField proves that WithNet, WithDisk, and
// WithClock return modified copies without mutating the base Env.
func TestEnvDerivationReplacesOneField(t *testing.T) {
	base := env.Env{ID: "a", Region: "r"}
	n, d, c := fakeNet{}, fakeDisk{}, fakeClock{}
	got := base.WithNet(n).WithDisk(d).WithClock(c)
	if got.ID != "a" || got.Region != "r" || got.Net != n || got.Disk != d || got.Clock != c {
		t.Fatalf("derived env = %#v", got)
	}
	if base.Net != nil {
		t.Fatal("derivation mutated the base env")
	}
}
