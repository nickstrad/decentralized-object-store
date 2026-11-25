// Package objctl is the business logic of the objctl command-line client:
// typed Command values, their execution against an httpapi.Service, human and
// JSON rendering of the results, and the mapping of outcomes to exit codes.
//
// It holds no command-line framework and no HTTP code. The command tree that
// turns argv into a Command lives in cmd/objctl, and the HTTP transport is
// internal/storage/httpclient; both meet here only through Command and the
// httpapi.Service interface, so every verb runs in tests against an in-memory
// service with injected standard streams and files.
package objctl

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
)

// Verb names one objctl operation; multi-word verbs are space separated.
type Verb string

// The objctl verbs.
const (
	VerbHealth        Verb = "health"
	VerbMembers       Verb = "members"
	VerbMakeBucket    Verb = "mb"
	VerbList          Verb = "ls"
	VerbPut           Verb = "put"
	VerbGet           Verb = "get"
	VerbHead          Verb = "head"
	VerbRemove        Verb = "rm"
	VerbTagSet        Verb = "tag set"
	VerbTagList       Verb = "tag ls"
	VerbACLSet        Verb = "acl set"
	VerbACLGet        Verb = "acl get"
	VerbQuotaSet      Verb = "quota set"
	VerbQuotaGet      Verb = "quota get"
	VerbMigrateStart  Verb = "migrate start"
	VerbMigrateStatus Verb = "migrate status"
)

// Defaults for the global options.
const (
	DefaultAddr    = "127.0.0.1:8080"
	DefaultTimeout = 30 * time.Second
	AddrEnv        = "OBJCTL_ADDR"
)

// Exit codes returned by Run.
const (
	ExitOK           = 0
	ExitFailure      = 1
	ExitUsage        = 2
	ExitNotFound     = 3
	ExitPrecondition = 4
)

// ErrUsage marks an invalid invocation: a malformed argument or flag, or an
// input the client refuses before contacting the server. Run maps it to
// ExitUsage. Errors in this package carry no "objctl: " prefix; the code that
// prints them adds it.
var ErrUsage = errors.New("invalid usage")

// Globals are the options every verb accepts.
type Globals struct {
	Addr    string
	Timeout time.Duration
	JSON    bool
	Verbose bool
}

// Command is one fully parsed objctl invocation. Only the fields its Verb
// uses are set; the others stay zero.
type Command struct {
	Verb Verb
	Globals

	Bucket string
	Key    string

	Region       string
	QuotaBytes   uint64
	QuotaObjects uint64

	Prefix string
	Limit  int

	File        string
	Output      string
	ContentType string
	Cond        storage.Condition

	Name      string
	Value     string
	Principal string
	Perm      string

	To   string
	ID   string
	Wait bool
}

// Usagef returns a usage error with the given message, wrapping ErrUsage.
func Usagef(format string, a ...any) error {
	return fmt.Errorf(format+": %w", append(a, ErrUsage)...)
}

// Validate checks the rules that span flags and arguments and normalises "-"
// (stdin or stdout) in File and Output to "". The command tree calls it once
// per invocation, and Run calls it again for Commands built elsewhere.
func (c *Command) Validate() error {
	if c.File == "-" {
		c.File = ""
	}
	if c.Output == "-" {
		c.Output = ""
	}
	switch {
	case c.Timeout <= 0:
		return Usagef("--timeout %v must be positive", c.Timeout)
	case c.Verb == VerbList && (c.Limit < 0 || c.Limit > httpapi.MaxListLimit):
		return Usagef("--limit %d outside [0, %d]", c.Limit, httpapi.MaxListLimit)
	case c.Verb == VerbMigrateStatus && c.ID == "":
		return Usagef("empty migration id")
	case c.Verb == VerbGet && c.JSON && c.Output == "":
		return Usagef("get --json needs --output: the object bytes and the JSON would share stdout")
	}
	return nil
}

// SplitObject splits a "bucket/key" argument at its first slash. Bucket names
// cannot contain a slash, so every byte after it, slashes included, is the key.
func SplitObject(arg string) (bucket, key string, err error) {
	bucket, key, ok := strings.Cut(arg, "/")
	if !ok || bucket == "" || key == "" {
		return "", "", Usagef("object %q: want bucket/key", arg)
	}
	if err := storage.ValidBucket(bucket); err != nil {
		return "", "", Usagef("object %q: %v", arg, err)
	}
	return bucket, key, nil
}

// CheckBucket validates a bucket-name argument.
func CheckBucket(arg string) error {
	if err := storage.ValidBucket(arg); err != nil {
		return Usagef("bucket %q: %v", arg, err)
	}
	return nil
}

// ParseCondition builds a storage.Condition from repeated --if-match values
// and an --if-none-match value. An --if-match value is "*" or an entity tag,
// with or without its surrounding double quotes; "*" cannot be combined with
// tags. --if-none-match accepts only "*", and cannot be combined with
// --if-match. noneSet reports whether --if-none-match was given.
func ParseCondition(ifMatch []string, ifNoneMatch string, noneSet bool) (storage.Condition, error) {
	var c storage.Condition
	if noneSet {
		if ifNoneMatch != "*" {
			return c, Usagef("--if-none-match %q: only * is supported", ifNoneMatch)
		}
		if len(ifMatch) > 0 {
			return c, Usagef("--if-match and --if-none-match together")
		}
		c.IfNoneMatch = true
		return c, nil
	}
	for _, v := range ifMatch {
		if v == "*" {
			if len(ifMatch) > 1 {
				return c, Usagef("--if-match * combined with entity tags")
			}
			c.IfMatch = "*"
			return c, nil
		}
	}
	if len(ifMatch) > 0 {
		c.IfMatchList = true
		for _, v := range ifMatch {
			if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
				v = v[1 : len(v)-1]
			}
			c.IfMatchAny = append(c.IfMatchAny, v)
		}
	}
	return c, nil
}
