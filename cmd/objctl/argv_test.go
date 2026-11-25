package main

import (
	"io"
	"strconv"
	"strings"

	"github.com/nickstrad/object-store/internal/objctl"
	"github.com/nickstrad/object-store/internal/storage"
)

// argv renders c as the canonical argument list that parses back to c: verb
// words, every global option spelled out, the verb's non-default flags in
// --name=value form, then -- and the positional arguments.
func argv(c objctl.Command) []string {
	out := strings.Fields(string(c.Verb))
	out = append(out, "--addr="+c.Addr, "--timeout="+c.Timeout.String())
	if c.JSON {
		out = append(out, "--json")
	}
	if c.Verbose {
		out = append(out, "--verbose")
	}
	flag := func(name, v string) {
		if v != "" {
			out = append(out, "--"+name+"="+v)
		}
	}
	num := func(name string, v uint64) {
		if v != 0 {
			flag(name, strconv.FormatUint(v, 10))
		}
	}
	cond := func(cond storage.Condition) {
		ifMatch, none := conditionArgs(cond)
		for _, v := range ifMatch {
			out = append(out, "--if-match="+v)
		}
		if none {
			out = append(out, "--if-none-match=*")
		}
	}
	obj := c.Bucket + "/" + c.Key
	var pos []string
	switch c.Verb {
	case objctl.VerbMakeBucket:
		flag("region", c.Region)
		num("quota-bytes", c.QuotaBytes)
		num("quota-objects", c.QuotaObjects)
		pos = []string{c.Bucket}
	case objctl.VerbList:
		flag("prefix", c.Prefix)
		if c.Limit != 0 {
			flag("limit", strconv.Itoa(c.Limit))
		}
		pos = []string{c.Bucket}
	case objctl.VerbPut:
		flag("file", c.File)
		flag("content-type", c.ContentType)
		cond(c.Cond)
		pos = []string{obj}
	case objctl.VerbGet:
		flag("output", c.Output)
		pos = []string{obj}
	case objctl.VerbHead, objctl.VerbTagList:
		pos = []string{obj}
	case objctl.VerbRemove:
		cond(c.Cond)
		pos = []string{obj}
	case objctl.VerbTagSet:
		cond(c.Cond)
		pos = []string{obj, c.Name, c.Value}
	case objctl.VerbACLSet:
		cond(c.Cond)
		pos = []string{c.Bucket, c.Principal, c.Perm}
	case objctl.VerbACLGet:
		pos = []string{c.Bucket, c.Principal}
	case objctl.VerbQuotaSet:
		num("bytes", c.QuotaBytes)
		num("objects", c.QuotaObjects)
		pos = []string{c.Bucket}
	case objctl.VerbQuotaGet:
		pos = []string{c.Bucket}
	case objctl.VerbMigrateStart:
		pos = []string{c.Bucket, c.To}
	case objctl.VerbMigrateStatus:
		if c.Wait {
			out = append(out, "--wait")
		}
		pos = []string{c.ID}
	}
	return append(append(out, "--"), pos...)
}

// conditionArgs renders c back to --if-match / --if-none-match values that
// objctl.ParseCondition turns into c again: tags are always quoted, "*" never.
func conditionArgs(c storage.Condition) (ifMatch []string, ifNoneMatch bool) {
	switch {
	case c.IfNoneMatch:
		return nil, true
	case c.IfMatch == "*":
		return []string{"*"}, false
	}
	for _, t := range c.IfMatchAny {
		ifMatch = append(ifMatch, `"`+t+`"`)
	}
	return ifMatch, false
}

// parse turns argv into the Command it denotes. ok is false when argv asked
// only for help or shell completion; err wraps objctl.ErrUsage when argv is
// invalid.
func parse(args []string, getenv func(string) string) (cmd objctl.Command, ok bool, err error) {
	c := config{Env: objctl.Env{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}, getenv: getenv}
	root := newRoot(c, func(x objctl.Command) error { cmd, ok = x, true; return nil })
	if err := execute(root, args); err != nil {
		return objctl.Command{}, false, usageErr(err)
	}
	return cmd, ok, nil
}
