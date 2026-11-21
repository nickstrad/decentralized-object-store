package occ_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
)

func TestTypedMetadataDocument(t *testing.T) {
	s, _ := newTestStore(t, "a", 1, 1)
	if _, err := s.PutTag("b", "o", "color", "red", occ.IfNoneMatch()); err != nil {
		t.Fatal(err)
	}
	if v, _, err := s.Tag("b", "o", "color"); err != nil || v != "red" {
		t.Fatalf("tag %q %v", v, err)
	}
	if _, err := s.PutACL("b", "alice", occ.PermRead|occ.PermWrite, occ.IfNoneMatch()); err != nil {
		t.Fatal(err)
	}
	if p, _, err := s.ACL("b", "alice"); err != nil || p != occ.PermRead|occ.PermWrite {
		t.Fatalf("acl %v %v", p, err)
	}
	if _, err := s.PutACL("b", "alice", 0x80, occ.Unconditional()); !errors.Is(err, env.ErrInvalidArgument) {
		t.Fatal(err)
	}
	if _, _, err := s.ACL("b", "nobody"); !errors.Is(err, occ.ErrNotFound) {
		t.Fatal(err)
	}
	mustPut(t, s, occ.ACLKey("b", "bad"), "xy", occ.IfNoneMatch())
	if _, _, err := s.ACL("b", "bad"); !errors.Is(err, occ.ErrMalformed) {
		t.Fatal(err)
	}
	m := occ.ObjectMeta{Size: 42, Hash: [32]byte{1, 2}, Created: -7}
	if _, err := s.PutObjectMeta("b", "o", m, occ.IfNoneMatch()); err != nil {
		t.Fatal(err)
	}
	if got, _, err := s.ObjectMeta("b", "o"); err != nil || got != m {
		t.Fatalf("object meta %v %v", got, err)
	}
	if _, _, err := s.ObjectMeta("b", "none"); !errors.Is(err, occ.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.PutManifest("b", "o", []byte{9, 9}, occ.IfNoneMatch()); err != nil {
		t.Fatal(err)
	}
	if got, _, err := s.Manifest("b", "o"); err != nil || !slices.Equal(got, []byte{9, 9}) {
		t.Fatal(err)
	}
	if _, err := s.SetRoute("b", "region-2"); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Route("b"); err != nil || r != "region-2" {
		t.Fatal(err)
	}
	if _, err := s.Route("nobucket"); !errors.Is(err, occ.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.SetContentType("b", "o", "text/plain"); err != nil {
		t.Fatal(err)
	}
	if ct, err := s.ContentType("b", "o"); err != nil || ct != "text/plain" {
		t.Fatal(err)
	}
	if _, err := s.PutObjectMeta("b", "p", occ.ObjectMeta{Size: 8}, occ.IfNoneMatch()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObjectMeta("bb", "q", occ.ObjectMeta{Size: 1000}, occ.IfNoneMatch()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObjectMeta("b", "gone", occ.ObjectMeta{Size: 1000}, occ.IfNoneMatch()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(occ.ObjectKey("b", "gone"), occ.IfMatch(s.Record(occ.ObjectKey("b", "gone")).ETag())); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObjectMeta("b", "p", occ.ObjectMeta{Size: 20}, occ.Unconditional()); err != nil {
		t.Fatal(err)
	}
	u, err := s.RecountObjects("b")
	if err != nil || u != (occ.Usage{Bytes: 42 + 20, Objects: 2}) {
		t.Fatalf("recount = %v %v (siblings count at their largest size, tombstones not at all, other buckets not at all)", u, err)
	}
	mustPut(t, s, occ.ObjectKey("b", "junk"), "x", occ.IfNoneMatch())
	if _, err := s.RecountObjects("b"); !errors.Is(err, occ.ErrMalformed) {
		t.Fatal(err)
	}
	if _, _, err := s.ObjectMeta("b", "junk"); !errors.Is(err, occ.ErrMalformed) {
		t.Fatal(err)
	}
}

func TestParseKey(t *testing.T) {
	rows := []struct {
		key   string
		kind  occ.Kind
		parts []string
	}{
		{occ.TagKey("b", "o", "n"), occ.KindTag, []string{"b", "o", "n"}},
		{occ.ACLKey("b", "p"), occ.KindACL, []string{"b", "p"}},
		{occ.QuotaKey("b"), occ.KindQuota, []string{"b"}},
		{occ.ObjectKey("b", "with/slash"), occ.KindObject, []string{"b", "with/slash"}},
		{occ.ManifestKey("", ""), occ.KindManifest, []string{"", ""}},
		{occ.RouteKey("b"), occ.KindRoute, []string{"b"}},
		{occ.ContentTypeKey("b", "o"), occ.KindContentType, []string{"b", "o"}},
	}
	for _, r := range rows {
		t.Run(r.kind.String(), func(t *testing.T) {
			k, parts, err := occ.ParseKey(r.key)
			if err != nil || k != r.kind || !slices.Equal(parts, r.parts) {
				t.Fatalf("ParseKey = %v %v %v", k, parts, err)
			}
		})
	}
	if occ.Kind(0).String() != "kind(0)" {
		t.Fatal("unknown kind name")
	}
	if !slices.Equal([]string{occ.ObjectKey("b", "x")}, func() []string {
		d := occ.NewDoc()
		s, _ := newTestStore(t, "a", 1, 1)
		mustPut(t, s, occ.ObjectKey("b", "x"), "", occ.IfNoneMatch())
		mustPut(t, s, occ.ObjectKey("bc", "x"), "", occ.IfNoneMatch())
		d.Merge(s.Doc())
		return d.KeysWithPrefix(occ.BucketPrefix(occ.KindObject, "b"))
	}()) {
		t.Fatal("bucket prefix must not match a longer bucket name")
	}
	for name, bad := range map[string]string{
		"empty":           "",
		"unknown kind":    "\x09\x01b",
		"truncated part":  "\x03\x05ab",
		"too few parts":   "\x02\x01b",
		"trailing bytes":  occ.QuotaKey("b") + "x",
		"non-minimal len": "\x03\x81\x00",
	} {
		if _, _, err := occ.ParseKey(bad); !errors.Is(err, occ.ErrMalformed) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := occ.DecodeObjectMeta([]byte{0x80, 0}); !errors.Is(err, occ.ErrMalformed) {
		t.Fatal(err)
	}
}

func TestErrorCodesRoundTrip(t *testing.T) {
	table, err := occ.Codes()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range occ.CodeEntries() {
		wrapped := errors.Join(errors.New("context"), e.Err)
		code, msg := table.Encode(wrapped)
		if code != e.Code {
			t.Fatalf("%v encoded as %d, want %d", e.Err, code, e.Code)
		}
		back := table.Decode(code, msg)
		if !errors.Is(back, e.Err) || !errors.Is(back, env.ErrRemote) {
			t.Fatalf("%v did not survive the wire: %v", e.Err, back)
		}
	}
	ce := &occ.ConflictError{Key: "k", Siblings: make([]occ.Version, 2)}
	code, _ := table.Encode(ce)
	if code != occ.CodeConflict {
		t.Fatalf("conflict error encoded as %d", code)
	}
	if !errors.Is(ce, occ.ErrConflict) {
		t.Fatal("ConflictError must match ErrConflict")
	}
	var target *occ.ConflictError
	if !errors.As(errors.Join(errors.New("x"), ce), &target) || target.Key != "k" || len(target.Siblings) != 2 {
		t.Fatal("errors.As on ConflictError")
	}
	if _, ok := occ.IsConflict(errors.New("plain")); ok {
		t.Fatal("IsConflict on a plain error")
	}
}
