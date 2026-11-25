package objctltest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/storage"
)

// Exit codes the oracle predicts (the objctl contract, restated so a change
// to it shows up as a test failure).
const (
	exitOK           = 0
	exitFailure      = 1
	exitUsage        = 2
	exitNotFound     = 3
	exitPrecondition = 4
)

// ModelOptions configures RunModel.
type ModelOptions struct {
	// Globals precede every invocation's verb, e.g. --addr and --verbose.
	Globals []string
	// Dir is the working directory of the runner: put --file sources are
	// written there and get --output files read back from there.
	Dir string
	// Steps is the number of invocations to generate.
	Steps int
}

// modelKeys are the object keys the generator draws from: slashes, dot segments,
// percent signs, query and fragment characters, white space, control bytes,
// quotes, a leading dash and non-ASCII text.
var modelKeys = []string{
	"a", "a/b", "a/b/", "dir//x", ".", "..", "../up", "./x", "%2F", "100%", "sp ace", "q?x=1#f", "ü/日本",
	"-dash", "tab\tkey", "new\nline", `"q"`, "*", "a/b/c/d/e",
}

// listPrefixes are the --prefix values the generator draws from.
var listPrefixes = []string{"", "a", "a/", "a/b", "..", "%", "ü", "zzz"}

var (
	modelBuckets  = []string{"m1", "m2", "ghost"}
	contentTypes  = []string{"", "text/plain", `application/x-weird; v="1"`, "x/ü", "application/octet-stream"}
	tagNames      = []string{"color", "Größe", "a b", "", "~sys", "k=v"}
	tagValues     = []string{"red", "", "multi\nline", "-starts-with-dash", "ü", strings.Repeat("v", 300)}
	principals    = []string{"alice", "bob@example", "", "c d"}
	perms         = []string{"r", "rw", "wa", "awr", "", "rr", "x", " r"}
	quotaBytesSet = []uint64{0, 0, 64, 300, 5000}
	quotaObjsSet  = []uint64{0, 0, 1, 2, 4}
)

type rec struct {
	record
	seen []string
}

func (r *rec) set(etag string) {
	r.live, r.etag = true, etag
	r.seen = append(r.seen, etag)
}

type mObject struct {
	rec
	data []byte
	ct   string
}

type mTag struct {
	rec
	value string
}

type mACL struct {
	rec
	perm string
}

type mBucket struct {
	quota   occ.Usage
	used    occ.Usage
	objects map[string]*mObject
	tags    map[string]map[string]*mTag
	acls    map[string]*mACL
}

type expectation struct {
	code  int
	what  string
	check func(stdout []byte) error
	apply func()
}

// model is the generator and the oracle: buckets is the expected state of
// the store, learned only from objctl output.
type model struct {
	r       *rand.Rand
	o       ModelOptions
	buckets map[string]*mBucket
	files   int
}

func (m *model) pick(xs []string) string { return xs[m.r.IntN(len(xs))] }

func (m *model) bucketName() string {
	if m.r.IntN(12) == 0 {
		return "ghost"
	}
	return modelBuckets[m.r.IntN(2)]
}

func (m *model) value() []byte {
	n := m.r.IntN(5)
	switch {
	case n == 0:
		return []byte{}
	case n == 1 && m.r.IntN(4) == 0:
		n = 64<<10 + m.r.IntN(8<<10)
	default:
		n = 1 + m.r.IntN(120)
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(m.r.UintN(256))
	}
	return b
}

// Decode parses stdout as exactly one JSON document of type T with no
// unknown fields, the --json contract.
func Decode[T any](stdout []byte) (T, error) {
	var v T
	dec := json.NewDecoder(bytes.NewReader(stdout))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("stdout is not one JSON %T: %v", v, err)
	}
	if dec.More() {
		return v, fmt.Errorf("stdout holds more than one JSON document")
	}
	return v, nil
}

// equals returns a check that stdout decodes to want.
func equals[T comparable](what string, want T) func([]byte) error {
	return func(out []byte) error {
		got, err := Decode[T](out)
		if err == nil && got != want {
			err = fmt.Errorf("%s %+v, want %+v", what, got, want)
		}
		return err
	}
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// conditionArgs draws a precondition for a record and returns its flags and
// the storage.Condition the server will evaluate (bad reports a condition the
// client must refuse).
func (m *model) conditionArgs(r *rec) (args []string, c storage.Condition, bad bool) {
	stale := "0123456789abcdef"
	if r != nil && len(r.seen) > 0 {
		stale = r.seen[m.r.IntN(len(r.seen))]
		if r.live && stale == r.etag && len(r.seen) > 1 {
			stale = r.seen[0]
		}
	}
	current := "fedcba9876543210"
	if r != nil && r.etag != "" {
		current = r.etag
	}
	switch m.r.IntN(9) {
	case 0:
		return []string{"--if-none-match", "*"}, storage.Condition{IfNoneMatch: true}, false
	case 1:
		return []string{"--if-match", current}, storage.Condition{IfMatchList: true, IfMatchAny: []string{current}}, false
	case 2:
		return []string{"--if-match", `"` + stale + `"`}, storage.Condition{IfMatchList: true, IfMatchAny: []string{stale}}, false
	case 3:
		return []string{"--if-match", "*"}, storage.Condition{IfMatch: "*"}, false
	case 4:
		return []string{"--if-match", stale, "--if-match", current}, storage.Condition{IfMatchList: true, IfMatchAny: []string{stale, current}}, false
	case 5:
		if m.r.IntN(2) == 0 {
			return []string{"--if-match", `"half`}, storage.Condition{}, true
		}
		return []string{"--if-none-match", "etag"}, storage.Condition{}, true
	}
	return nil, storage.Condition{}, false
}

func toRecord(r *rec) record {
	if r == nil {
		return record{}
	}
	return r.record
}

func condCode(r *rec, c storage.Condition) int {
	if err := holds(toRecord(r), c); err != nil {
		return exitPrecondition
	}
	return exitOK
}

func (m *model) bucket(name string) *mBucket { return m.buckets[name] }

// next draws one invocation and the oracle's expectation for it.
func (m *model) next() (args []string, stdin []byte, files map[string][]byte, e expectation) {
	b := m.bucketName()
	key := m.pick(modelKeys)
	mb := m.bucket(b)
	if mb != nil && m.r.IntN(10) < 6 {
		var live []string
		for k, ob := range mb.objects {
			if ob.live {
				live = append(live, k)
			}
		}
		if len(live) > 0 {
			sort.Strings(live)
			key = live[m.r.IntN(len(live))]
		}
	}
	obj := b + "/" + key
	var o *mObject
	if mb != nil {
		o = mb.objects[key]
	}
	var orec *rec
	if o != nil {
		orec = &o.rec
	}
	op := m.r.IntN(100)
	if mb == nil && b != "ghost" && m.r.IntN(10) < 7 {
		op = 0
	}
	switch {
	case op < 5:
		if b == "ghost" {
			b = "m1"
		}
		qb, qo := quotaBytesSet[m.r.IntN(len(quotaBytesSet))], quotaObjsSet[m.r.IntN(len(quotaObjsSet))]
		args = []string{"mb", "--json"}
		if qb > 0 {
			args = append(args, "--quota-bytes", strconv.FormatUint(qb, 10))
		}
		if qo > 0 {
			args = append(args, "--quota-objects", strconv.FormatUint(qo, 10))
		}
		args = append(args, "--", b)
		if m.bucket(b) != nil {
			return args, nil, nil, expectation{code: exitFailure, what: "exit 1: bucket exists"}
		}
		return args, nil, nil, expectation{code: exitOK, what: "exit 0, bucket created",
			check: equals("bucket spec", storage.BucketSpec{Name: b, QuotaBytes: qb, QuotaObjects: qo}),
			apply: func() {
				m.buckets[b] = &mBucket{quota: occ.Usage{Bytes: qb, Objects: qo}, objects: map[string]*mObject{}, tags: map[string]map[string]*mTag{}, acls: map[string]*mACL{}}
			}}
	case op < 30:
		data := m.value()
		ct := m.pick(contentTypes)
		if m.r.IntN(20) == 0 {
			ct = " padded"
		}
		cargs, cond, bad := m.conditionArgs(orec)
		args = append([]string{"put", "--json"}, cargs...)
		if ct != "" {
			args = append(args, "--content-type", ct)
		}
		if m.r.IntN(2) == 0 {
			m.files++
			name := fmt.Sprintf("in-%d.bin", m.files)
			files = map[string][]byte{name: data}
			args = append(args, "--file", name)
		} else {
			stdin = data
		}
		args = append(args, "--", obj)
		switch {
		case bad || ct == " padded":
			return args, stdin, files, expectation{code: exitUsage, what: "exit 2: client refuses the input"}
		case mb == nil:
			return args, stdin, files, expectation{code: exitNotFound, what: "exit 3: no such bucket"}
		case condCode(orec, cond) != exitOK:
			return args, stdin, files, expectation{code: exitPrecondition, what: fmt.Sprintf("exit 4: precondition fails against %s", recText(orec))}
		}
		dBytes, dObjs := int64(len(data)), int64(1)
		if o != nil && o.live {
			dBytes, dObjs = int64(len(data))-int64(len(o.data)), 0
		}
		if quotaCheck(mb.quota, mb.used, dBytes, dObjs) != nil {
			return args, stdin, files, expectation{code: exitFailure, what: fmt.Sprintf("exit 1: quota %v exceeded from usage %v", mb.quota, mb.used)}
		}
		var learned string
		return args, stdin, files, expectation{code: exitOK, what: fmt.Sprintf("exit 0, %d bytes stored over %s", len(data), recText(orec)),
			check: func(out []byte) error {
				oi, err := Decode[storage.ObjectInfo](out)
				if err != nil {
					return err
				}
				want := storage.ObjectInfo{Bucket: b, Key: key, Size: int64(len(data)), ETag: oi.ETag, Version: oi.Version, ContentSHA: sha(data), ContentType: ct}
				if oi != want || oi.ETag == "" || oi.Version == "" {
					return fmt.Errorf("put info %+v, want %+v", oi, want)
				}
				if orec != nil && slices.Contains(orec.seen, oi.ETag) {
					return fmt.Errorf("put reused etag %s", oi.ETag)
				}
				learned = oi.ETag
				return nil
			},
			apply: func() {
				if o == nil {
					o = &mObject{}
					mb.objects[key] = o
				}
				o.set(learned)
				o.data, o.ct = data, ct
				mb.used = addUsage(mb.used, dBytes, dObjs)
			}}
	case op < 45:
		toFile := m.r.IntN(3) == 0
		args = []string{"get"}
		var name string
		if toFile {
			m.files++
			name = fmt.Sprintf("out-%d.bin", m.files)
			args = append(args, "--json", "--output", name)
		} else if m.r.IntN(15) == 0 {
			args = append(args, "--json", "--", obj)
			return args, nil, nil, expectation{code: exitUsage, what: "exit 2: --json without --output"}
		}
		args = append(args, "--", obj)
		if code, what := missing(mb, o); code != 0 {
			return args, nil, nil, expectation{code: code, what: what}
		}
		data, ct, etag := o.data, o.ct, o.etag
		if ct == "" {
			ct = "application/octet-stream"
		}
		e := expectation{code: exitOK, what: fmt.Sprintf("exit 0, the %d bytes of %s", len(data), recText(orec))}
		if !toFile {
			e.check = func(out []byte) error {
				if !bytes.Equal(out, data) {
					return fmt.Errorf("get returned %s, want %s", showBytes(out, 40), showBytes(data, 40))
				}
				return nil
			}
			return args, nil, nil, e
		}
		e.check = func(out []byte) error {
			oi, err := Decode[storage.ObjectInfo](out)
			if err != nil {
				return err
			}
			want := storage.ObjectInfo{Bucket: b, Key: key, Size: int64(len(data)), ETag: etag, Version: oi.Version, ContentSHA: sha(data), ContentType: ct}
			if oi != want {
				return fmt.Errorf("get info %+v, want %+v", oi, want)
			}
			got, err := os.ReadFile(filepath.Join(m.o.Dir, name))
			if err != nil || !bytes.Equal(got, data) {
				return fmt.Errorf("output file holds %s (%v), want %s", showBytes(got, 40), err, showBytes(data, 40))
			}
			return nil
		}
		return args, nil, nil, e
	case op < 55:
		args = []string{"head", "--json", "--", obj}
		if code, what := missing(mb, o); code != 0 {
			return args, nil, nil, expectation{code: code, what: what}
		}
		data, ct, etag := o.data, o.ct, o.etag
		return args, nil, nil, expectation{code: exitOK, what: "exit 0, metadata of " + recText(orec), check: func(out []byte) error {
			oi, err := Decode[storage.ObjectInfo](out)
			if err != nil {
				return err
			}
			want := storage.ObjectInfo{Bucket: b, Key: key, Size: int64(len(data)), ETag: etag, Version: oi.Version, ContentSHA: sha(data), ContentType: ct}
			if oi != want || oi.Version == "" {
				return fmt.Errorf("head %+v, want %+v", oi, want)
			}
			return nil
		}}
	case op < 63:
		prefix := m.pick(listPrefixes)
		limit := 0
		if m.r.IntN(3) == 0 {
			limit = 1 + m.r.IntN(3)
		}
		args = []string{"ls", "--json"}
		if prefix != "" {
			args = append(args, "--prefix", prefix)
		}
		if limit > 0 {
			args = append(args, "--limit", strconv.Itoa(limit))
		}
		args = append(args, "--", b)
		if mb == nil {
			return args, nil, nil, expectation{code: exitNotFound, what: "exit 3: no such bucket"}
		}
		want := map[string]*mObject{}
		for k, ob := range mb.objects {
			if ob.live && strings.HasPrefix(k, prefix) {
				want[k] = ob
			}
		}
		n := len(want)
		if limit > 0 {
			n = min(n, limit)
		}
		return args, nil, nil, expectation{code: exitOK, what: fmt.Sprintf("exit 0, %d of %d matching objects", n, len(want)), check: func(out []byte) error {
			ois, err := Decode[[]storage.ObjectInfo](out)
			if err != nil {
				return err
			}
			if len(ois) != n {
				return fmt.Errorf("listed %d objects, want %d", len(ois), n)
			}
			seen := map[string]bool{}
			for _, oi := range ois {
				ob := want[oi.Key]
				if ob == nil || seen[oi.Key] {
					return fmt.Errorf("listed unexpected or repeated key %q", oi.Key)
				}
				seen[oi.Key] = true
				if oi.Bucket != b || oi.Size != int64(len(ob.data)) || oi.ETag != ob.etag || oi.ContentType != ob.ct || oi.ContentSHA != sha(ob.data) {
					return fmt.Errorf("listed %+v, want etag %s size %d", oi, ob.etag, len(ob.data))
				}
			}
			return nil
		}}
	case op < 73:
		cargs, cond, bad := m.conditionArgs(orec)
		args = append(append([]string{"rm", "--json"}, cargs...), "--", obj)
		switch {
		case bad:
			return args, nil, nil, expectation{code: exitUsage, what: "exit 2: client refuses the condition"}
		case mb == nil:
			return args, nil, nil, expectation{code: exitNotFound, what: "exit 3: no such bucket"}
		case condCode(orec, cond) != exitOK:
			return args, nil, nil, expectation{code: exitPrecondition, what: "exit 4: precondition fails against " + recText(orec)}
		case o == nil || !o.live:
			return args, nil, nil, expectation{code: exitNotFound, what: "exit 3: nothing live to delete"}
		}
		var tomb string
		return args, nil, nil, expectation{code: exitOK, what: "exit 0, deletes " + recText(orec),
			check: func(out []byte) error {
				oi, err := Decode[storage.ObjectInfo](out)
				if err != nil {
					return err
				}
				if oi.Bucket != b || oi.Key != key || !oi.Deleted || oi.ETag == "" || slices.Contains(o.seen, oi.ETag) {
					return fmt.Errorf("rm returned %+v", oi)
				}
				tomb = oi.ETag
				return nil
			},
			apply: func() {
				mb.used = addUsage(mb.used, -int64(len(o.data)), -1)
				o.live, o.etag = false, tomb
				o.seen = append(o.seen, tomb)
			}}
	case op < 81:
		name, value := m.pick(tagNames), m.pick(tagValues)
		var t *mTag
		if mb != nil {
			t = mb.tags[key][name]
		}
		var trec *rec
		if t != nil {
			trec = &t.rec
		}
		cargs, cond, bad := m.conditionArgs(trec)
		args = append(append([]string{"tag", "set", "--json"}, cargs...), "--", obj, name, value)
		switch {
		case bad:
			return args, nil, nil, expectation{code: exitUsage, what: "exit 2: client refuses the condition"}
		}
		if code, what := missing(mb, o); code != 0 {
			return args, nil, nil, expectation{code: code, what: what}
		}
		if name == "" || strings.HasPrefix(name, "~") {
			return args, nil, nil, expectation{code: exitFailure, what: "exit 1: server rejects the tag name"}
		}
		if condCode(trec, cond) != exitOK {
			return args, nil, nil, expectation{code: exitPrecondition, what: "exit 4: tag precondition fails against " + recText(trec)}
		}
		var learned string
		return args, nil, nil, expectation{code: exitOK, what: "exit 0, tag set over " + recText(trec),
			check: func(out []byte) error {
				ti, err := Decode[storage.TagInfo](out)
				if err != nil {
					return err
				}
				if ti.Name != name || ti.Value != value || ti.ETag == "" || (trec != nil && slices.Contains(trec.seen, ti.ETag)) {
					return fmt.Errorf("tag set returned %+v", ti)
				}
				learned = ti.ETag
				return nil
			},
			apply: func() {
				if mb.tags[key] == nil {
					mb.tags[key] = map[string]*mTag{}
				}
				if t == nil {
					t = &mTag{}
					mb.tags[key][name] = t
				}
				t.set(learned)
				t.value = value
			}}
	case op < 86:
		args = []string{"tag", "ls", "--json", "--", obj}
		if code, what := missing(mb, o); code != 0 {
			return args, nil, nil, expectation{code: code, what: what}
		}
		var want []storage.TagInfo
		for name, t := range mb.tags[key] {
			want = append(want, storage.TagInfo{Name: name, Value: t.value, ETag: t.etag})
		}
		sortTags(want)
		return args, nil, nil, expectation{code: exitOK, what: fmt.Sprintf("exit 0, %d tags (tags outlive deletes)", len(want)), check: func(out []byte) error {
			got, err := Decode[[]storage.TagInfo](out)
			if err != nil {
				return err
			}
			sortTags(got)
			if len(got) != len(want) || (len(got) > 0 && !slices.Equal(got, want)) {
				return fmt.Errorf("tags %+v, want %+v", got, want)
			}
			return nil
		}}
	case op < 93:
		principal, perm := m.pick(principals), m.pick(perms)
		var a *mACL
		if mb != nil {
			a = mb.acls[principal]
		}
		var arec *rec
		if a != nil {
			arec = &a.rec
		}
		cargs, cond, bad := m.conditionArgs(arec)
		args = append(append([]string{"acl", "set", "--json"}, cargs...), "--", b, principal, perm)
		canon, okPerm := canonicalPerm(perm)
		switch {
		case bad || strings.TrimSpace(perm) != perm:
			return args, nil, nil, expectation{code: exitUsage, what: "exit 2: client refuses the input"}
		case mb == nil:
			return args, nil, nil, expectation{code: exitNotFound, what: "exit 3: no such bucket"}
		case principal == "" || !okPerm:
			return args, nil, nil, expectation{code: exitFailure, what: "exit 1: server rejects principal or permission"}
		case condCode(arec, cond) != exitOK:
			return args, nil, nil, expectation{code: exitPrecondition, what: "exit 4: acl precondition fails against " + recText(arec)}
		}
		var learned string
		return args, nil, nil, expectation{code: exitOK, what: "exit 0, permission " + strconv.Quote(canon),
			check: func(out []byte) error {
				ai, err := Decode[storage.ACLInfo](out)
				if err != nil {
					return err
				}
				if ai.Principal != principal || ai.Perm != canon || ai.ETag == "" || (arec != nil && slices.Contains(arec.seen, ai.ETag)) {
					return fmt.Errorf("acl set returned %+v", ai)
				}
				learned = ai.ETag
				return nil
			},
			apply: func() {
				if a == nil {
					a = &mACL{}
					mb.acls[principal] = a
				}
				a.set(learned)
				a.perm = canon
			}}
	case op < 96:
		principal := m.pick(principals)
		args = []string{"acl", "get", "--json", "--", b, principal}
		if mb == nil {
			return args, nil, nil, expectation{code: exitNotFound, what: "exit 3: no such bucket"}
		}
		a := mb.acls[principal]
		if a == nil {
			return args, nil, nil, expectation{code: exitNotFound, what: "exit 3: no entry"}
		}
		want := storage.ACLInfo{Principal: principal, Perm: a.perm, ETag: a.etag}
		return args, nil, nil, expectation{code: exitOK, what: "exit 0, " + strconv.Quote(a.perm), check: equals("acl", want)}
	default:
		set := m.r.IntN(2) == 0
		qb, qo := quotaBytesSet[m.r.IntN(len(quotaBytesSet))], quotaObjsSet[m.r.IntN(len(quotaObjsSet))]
		if set {
			args = []string{"quota", "set", "--json", "--bytes", strconv.FormatUint(qb, 10), "--objects", strconv.FormatUint(qo, 10), "--", b}
		} else {
			args = []string{"quota", "get", "--json", "--", b}
		}
		if mb == nil {
			return args, nil, nil, expectation{code: exitNotFound, what: "exit 3: no such bucket"}
		}
		limits := mb.quota
		if set {
			limits = occ.Usage{Bytes: qb, Objects: qo}
		}
		want := storage.QuotaInfo{Bucket: b, LimitBytes: limits.Bytes, LimitObjects: limits.Objects, UsedBytes: mb.used.Bytes, UsedObjects: mb.used.Objects}
		return args, nil, nil, expectation{code: exitOK, what: fmt.Sprintf("exit 0, %+v", want),
			check: equals("quota", want),
			apply: func() { mb.quota = limits }}
	}
}

func sortTags(ts []storage.TagInfo) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
}

func missing(mb *mBucket, o *mObject) (int, string) {
	switch {
	case mb == nil:
		return exitNotFound, "exit 3: no such bucket"
	case o == nil || !o.live:
		return exitNotFound, "exit 3: no live object"
	}
	return 0, ""
}

func recText(r *rec) string {
	switch {
	case r == nil:
		return "a record never written"
	case !r.live:
		return "a deleted record (tombstone " + short(r.etag) + ")"
	}
	return "live etag " + short(r.etag)
}

func short(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

// RunModel generates o.Steps objctl invocations from seed over a small space
// of buckets, adversarial keys, values, tags, ACLs and quotas, runs each one
// sequentially through run, and checks every exit code and output against an
// oracle that learns ETags only from objctl's own output. Every step, with the
// oracle's expectation and verdict, goes to tr. It returns the first mismatch.
func RunModel(seed int64, run Runner, o ModelOptions, tr *Transcript) error {
	m := &model{r: rand.New(rand.NewPCG(uint64(seed), 0x6f626a63746c)), o: o, buckets: map[string]*mBucket{}}
	tr.Note("model seed=%d steps=%d", seed, o.Steps)
	codes := map[int]int{}
	for i := 0; i < o.Steps; i++ {
		args, stdin, files, e := m.next()
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(o.Dir, name), data, 0o644); err != nil {
				return err
			}
		}
		full := append(slices.Clone(o.Globals), args...)
		r := run(full, stdin)
		codes[r.Code]++
		var err error
		if r.Code != e.code {
			err = fmt.Errorf("exit %d, want %d (stderr %s)", r.Code, e.code, showBytes(r.Stderr, 300))
		} else if e.code == exitOK && e.check != nil {
			err = e.check(r.Stdout)
		}
		if err == nil && e.code == exitOK && e.apply != nil {
			e.apply()
		}
		verdict := "ok"
		if err != nil {
			verdict = "MISMATCH: " + err.Error()
		}
		tr.Step(full, stdin, r, e.what, verdict)
		if err != nil {
			return fmt.Errorf("step %d: objctl %s: %w", i+1, CommandLine(full), err)
		}
	}
	tr.Note("model seed=%d done: exit codes %v", seed, codes)
	if codes[exitOK] == 0 {
		return errors.New("no step succeeded")
	}
	return nil
}
