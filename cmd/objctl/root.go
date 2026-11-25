package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/nickstrad/object-store/internal/objctl"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
)

// config is everything the command tree touches outside argv: the
// environment a Command runs in, and the variable lookup for --addr.
type config struct {
	objctl.Env
	getenv func(string) string
}

const rootLong = `objctl is a command-line client for the object store's HTTP API.

Objects are addressed as one argument, bucket/key: the bucket is everything
before the first slash (bucket names cannot contain one) and the key is every
byte after it, so keys may hold further slashes, dots, spaces, '?', '#', '%'
or any other byte. Put a lone -- before arguments that start with a dash.

Output is human-readable text; --json prints exactly one JSON document per
invocation, the encoding of the object store's own entity types (ObjectInfo,
TagInfo, ACLInfo, QuotaInfo, BucketSpec, MigrationInfo, MemberInfo, Health).
--verbose traces every HTTP request and response on stderr.

Conditional writes: --if-match takes an entity tag (quotes optional) or *, and
may be repeated to match any of several tags; --if-none-match takes only *.

Exit status:
  0  success
  1  the request failed (server error, conflict, quota exceeded, network)
  2  invalid usage, or an input the client refuses before sending it
  3  not found (no such bucket, object, ACL entry or migration; HTTP 404)
  4  precondition failed (--if-match / --if-none-match did not hold; HTTP 412)

Environment:
  OBJCTL_ADDR  default for --addr (otherwise 127.0.0.1:8080)`

// newRoot builds the objctl command tree. Every leaf turns its flags and
// arguments into an objctl.Command and hands it to do; nothing else runs.
func newRoot(c config, do func(objctl.Command) error) *cobra.Command {
	var (
		cur         objctl.Command
		ifMatch     []string
		ifNoneMatch string
	)
	addr := objctl.DefaultAddr
	if v := c.getenv(objctl.AddrEnv); v != "" {
		addr = v
	}
	usage := func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("%q needs a subcommand: %w", cmd.CommandPath(), objctl.ErrUsage)
		}
		return fmt.Errorf("unknown command %q for %q: %w", args[0], cmd.CommandPath(), objctl.ErrUsage)
	}
	root := &cobra.Command{
		Use:           "objctl",
		Short:         "Command-line client for the object store HTTP API",
		Long:          rootLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          usage,
	}
	root.SetIn(c.Stdin)
	root.SetOut(c.Stdout)
	root.SetErr(c.Stderr)
	pf := root.PersistentFlags()
	pf.StringVar(&cur.Addr, "addr", addr, "node HTTP address, host:port or http://host:port ($"+objctl.AddrEnv+")")
	pf.DurationVar(&cur.Timeout, "timeout", objctl.DefaultTimeout, "deadline for the whole command")
	pf.BoolVar(&cur.JSON, "json", false, "print one JSON document instead of text")
	pf.BoolVarP(&cur.Verbose, "verbose", "v", false, "trace HTTP requests and responses on stderr")

	condFlags := func(fs *pflag.FlagSet) {
		fs.StringArrayVar(&ifMatch, "if-match", nil, "require the current entity tag to be this one (repeatable) or *")
		fs.StringVar(&ifNoneMatch, "if-none-match", "", "require that no live record exists (only * is accepted)")
	}
	leaf := func(verb objctl.Verb, use, short string, nargs int, build func(cmd *cobra.Command, args []string) error) *cobra.Command {
		words := strings.Fields(string(verb))
		return &cobra.Command{
			Use:   words[len(words)-1] + " " + use,
			Short: short,
			Args:  cobra.ExactArgs(nargs),
			RunE: func(cmd *cobra.Command, args []string) error {
				cur.Verb = verb
				if err := build(cmd, args); err != nil {
					return err
				}
				if err := cur.Validate(); err != nil {
					return err
				}
				return do(cur)
			},
		}
	}
	object := func(arg string) error {
		var err error
		cur.Bucket, cur.Key, err = objctl.SplitObject(arg)
		return err
	}
	bucket := func(arg string) error {
		cur.Bucket = arg
		return objctl.CheckBucket(arg)
	}
	cond := func(cmd *cobra.Command) error {
		var err error
		cur.Cond, err = objctl.ParseCondition(ifMatch, ifNoneMatch, cmd.Flags().Changed("if-none-match"))
		return err
	}
	group := func(name, short string, subs ...*cobra.Command) *cobra.Command {
		g := &cobra.Command{Use: name, Short: short, Args: cobra.ArbitraryArgs, RunE: usage}
		g.AddCommand(subs...)
		return g
	}

	health := leaf(objctl.VerbHealth, "", "Show the node's health summary", 0, func(*cobra.Command, []string) error { return nil })
	members := leaf(objctl.VerbMembers, "", "List the node's gossip members", 0, func(*cobra.Command, []string) error { return nil })

	mb := leaf(objctl.VerbMakeBucket, "BUCKET", "Create a bucket", 1, func(_ *cobra.Command, a []string) error { return bucket(a[0]) })
	mb.Flags().StringVar(&cur.Region, "region", "", "pin the bucket to a region (enables migration)")
	mb.Flags().Uint64Var(&cur.QuotaBytes, "quota-bytes", 0, "byte quota (0 = unlimited)")
	mb.Flags().Uint64Var(&cur.QuotaObjects, "quota-objects", 0, "object-count quota (0 = unlimited)")

	ls := leaf(objctl.VerbList, "BUCKET", "List live objects", 1, func(_ *cobra.Command, a []string) error {
		return bucket(a[0])
	})
	ls.Flags().StringVar(&cur.Prefix, "prefix", "", "only keys starting with this prefix")
	ls.Flags().IntVar(&cur.Limit, "limit", 0, fmt.Sprintf("at most this many objects (0 = server maximum, %d)", httpapi.MaxListLimit))

	put := leaf(objctl.VerbPut, "BUCKET/KEY", "Write an object from --file or stdin", 1, func(cmd *cobra.Command, a []string) error {
		if err := cond(cmd); err != nil {
			return err
		}
		return object(a[0])
	})
	put.Flags().StringVarP(&cur.File, "file", "f", "", "read the object from this file (default stdin)")
	put.Flags().StringVar(&cur.ContentType, "content-type", "", "content type to store with the object")
	condFlags(put.Flags())

	get := leaf(objctl.VerbGet, "BUCKET/KEY", "Read an object to stdout or --output", 1, func(_ *cobra.Command, a []string) error {
		return object(a[0])
	})
	get.Flags().StringVarP(&cur.Output, "output", "o", "", "write the object to this file (default stdout); with --json, stdout gets its ObjectInfo")

	head := leaf(objctl.VerbHead, "BUCKET/KEY", "Show an object's metadata", 1, func(_ *cobra.Command, a []string) error { return object(a[0]) })
	head.Aliases = []string{"stat"}

	rm := leaf(objctl.VerbRemove, "BUCKET/KEY", "Delete an object", 1, func(cmd *cobra.Command, a []string) error {
		if err := cond(cmd); err != nil {
			return err
		}
		return object(a[0])
	})
	condFlags(rm.Flags())

	tagSet := leaf(objctl.VerbTagSet, "BUCKET/KEY NAME VALUE", "Set a tag on an object", 3, func(cmd *cobra.Command, a []string) error {
		cur.Name, cur.Value = a[1], a[2]
		if err := cond(cmd); err != nil {
			return err
		}
		return object(a[0])
	})
	condFlags(tagSet.Flags())
	tagLs := leaf(objctl.VerbTagList, "BUCKET/KEY", "List an object's tags", 1, func(_ *cobra.Command, a []string) error { return object(a[0]) })

	aclSet := leaf(objctl.VerbACLSet, "BUCKET PRINCIPAL PERM", "Set a principal's permission letters (r, w, a)", 3, func(cmd *cobra.Command, a []string) error {
		cur.Principal, cur.Perm = a[1], a[2]
		if err := cond(cmd); err != nil {
			return err
		}
		return bucket(a[0])
	})
	condFlags(aclSet.Flags())
	aclGet := leaf(objctl.VerbACLGet, "BUCKET PRINCIPAL", "Show a principal's permission", 2, func(_ *cobra.Command, a []string) error {
		cur.Principal = a[1]
		return bucket(a[0])
	})

	quotaSet := leaf(objctl.VerbQuotaSet, "BUCKET", "Set a bucket's byte and object limits", 1, func(_ *cobra.Command, a []string) error { return bucket(a[0]) })
	quotaSet.Flags().Uint64Var(&cur.QuotaBytes, "bytes", 0, "byte limit (0 = unlimited)")
	quotaSet.Flags().Uint64Var(&cur.QuotaObjects, "objects", 0, "object-count limit (0 = unlimited)")
	quotaGet := leaf(objctl.VerbQuotaGet, "BUCKET", "Show a bucket's limits and usage", 1, func(_ *cobra.Command, a []string) error { return bucket(a[0]) })

	migStart := leaf(objctl.VerbMigrateStart, "BUCKET REGION", "Start migrating a region-pinned bucket", 2, func(_ *cobra.Command, a []string) error {
		cur.To = a[1]
		return bucket(a[0])
	})
	migStatus := leaf(objctl.VerbMigrateStatus, "ID", "Show a migration's state", 1, func(_ *cobra.Command, a []string) error {
		cur.ID = a[0]
		return nil
	})
	migStatus.Flags().BoolVar(&cur.Wait, "wait", false, "poll until the migration completes or is compensated (bounded by --timeout)")

	root.AddCommand(health, members, mb, ls, put, get, head, rm,
		group("tag", "Set and list object tags", tagSet, tagLs),
		group("acl", "Set and show bucket permissions", aclSet, aclGet),
		group("quota", "Set and show bucket quotas", quotaSet, quotaGet),
		group("migrate", "Start and follow bucket migrations", migStart, migStatus))
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return objctl.Usagef("%v", err) })
	return root
}

// execute runs root on args and reports whether a leaf handed over a Command.
// A nil args slice is replaced by an empty one so cobra never reads os.Args.
func execute(root *cobra.Command, args []string) error {
	if args == nil {
		args = []string{}
	}
	root.SetArgs(args)
	return root.Execute()
}

func usageErr(err error) error {
	if errors.Is(err, objctl.ErrUsage) {
		return err
	}
	return objctl.Usagef("%v", err)
}

// run executes argv and returns the process exit code.
func run(ctx context.Context, args []string, c config) int {
	code := objctl.ExitOK
	root := newRoot(c, func(cmd objctl.Command) error {
		code = objctl.Run(ctx, cmd, c.Env)
		return nil
	})
	if err := execute(root, args); err != nil {
		fmt.Fprintf(c.Stderr, "objctl: %v\nRun 'objctl --help' for usage.\n", usageErr(err))
		return objctl.ExitUsage
	}
	return code
}

func osOpen(name string) (io.ReadCloser, error)    { return os.Open(name) }
func osCreate(name string) (io.WriteCloser, error) { return os.Create(name) }
