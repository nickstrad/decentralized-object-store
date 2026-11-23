package main

import (
	"context"
	"fmt"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
	"net/http/httptest"
	"os"
	"strings"
)

// Each row is an independent expected result for an existing entity. A matching
// validator is substituted only after the fixture has been committed.
func httpModelChecks() {
	type row struct {
		name, header, value string
		want                int
	}
	rows := []row{
		{"empty", "If-Match", "", 412}, {"space", "If-Match", " \t ", 412},
		{"empty-tag", "If-Match", `""`, 412}, {"bare", "If-Match", "garbage", 400},
		{"unclosed", "If-Match", `"abc`, 400}, {"quotes", "If-Match", `""""`, 400},
		{"unknown", "If-Match", `"unknown"`, 412}, {"weak", "If-Match", `W/ETAG`, 412},
		{"star", "If-Match", "*", 200}, {"match", "If-Match", "ETAG", 200},
		{"list-match", "If-Match", `"unknown", ETAG`, 200},
		{"quoted-comma-list", "If-Match", `"a,b", ETAG`, 200},
		{"list-no-match", "If-Match", `"a", "b"`, 412},
		{"list-weak", "If-Match", `"unknown", W/ETAG`, 412},
		{"empty-elements", "If-Match", `, , ETAG,`, 200},
		{"star-list", "If-Match", `*, ETAG`, 400},
		{"none-star", "If-None-Match", "*", 412}, {"none-empty", "If-None-Match", "", 400},
		{"none-space", "If-None-Match", " \t ", 400}, {"none-weak", "If-None-Match", `W/ETAG`, 400},
		{"none-list", "If-None-Match", `"a", "b"`, 400},
	}
	rt, cleanup := realNode()
	defer cleanup()
	h := httpapi.NewHandler(rt, httpapi.Limits{}, node.StatusRows()...)
	do := func(method, path, body, header, value string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if header != "" {
			r.Header[header] = []string{value}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	require(do("PUT", "/b", "", "", "").Code < 300, "create fixture bucket")
	for _, target := range []string{"object", "delete", "tag", "acl"} {
		for _, r := range rows {
			if filter := os.Getenv("CHECK_HTTP_CASE"); filter != "" && filter != target+"-"+r.name {
				continue
			}
			check("C31", "http-model-"+target+"-"+r.name, 0, func() string {
				path, body, method := "/b/k", "changed", "PUT"
				switch target {
				case "delete":
					method = "DELETE"
				case "tag":
					path = "/b/k?tag=t"
				case "acl":
					path = "/b?acl=p"
					body = "rw"
				}
				initial := "initial"
				if target == "acl" {
					initial = "r"
				}
				fixture := do("PUT", path, initial, "", "")
				require(fixture.Code == 200, "fixture=%d %s", fixture.Code, fixture.Body.String())
				var before, after string
				snapshot := func() *string {
					s := ""
					must(rt.Inspect(context.Background(), func(n *node.Node) { s = fmt.Sprint(n.OCC().Digest()) }))
					return &s
				}
				before = *snapshot()
				v := strings.ReplaceAll(r.value, "ETAG", fixture.Header().Get("ETag"))
				got := do(method, path, body, r.header, v)
				want := r.want
				if want == 200 && method == "DELETE" {
					want = 204
				}
				require(got.Code == want, "%s %s %s=%q status=%d want=%d body=%s", method, path, r.header, v, got.Code, want, got.Body.String())
				after = *snapshot()
				if want >= 400 {
					require(before == after, "refused precondition mutated metadata")
				} else {
					require(before != after, "successful mutation did not change metadata")
				}
				if target == "object" || target == "delete" {
					g := do("GET", "/b/k", "", "", "")
					expected := initial
					if want < 300 {
						expected = body
					}
					if method == "DELETE" && want < 300 {
						require(g.Code == 404, "delete not applied")
					} else {
						require(g.Code == 200 && g.Body.String() == expected, "wrong readback")
					}
				}
				return fmt.Sprintf("%s %s header=%s value=%q status=%d, mutation matches model", method, path, r.header, v, got.Code)
			})
		}
	}
}
