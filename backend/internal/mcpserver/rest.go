package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// restCaller runs a REST operation in-process: a synthetic request through
// api.RequireAuth(mux), carrying the Authorization header of the MCP call. A
// tool built on it has no logic of its own, so validation, the materializer
// trigger, status codes and error messages are the REST handler's, and the
// two surfaces cannot drift. No network hop is involved.
type restCaller struct {
	h http.Handler
}

// Confirmation is embedded in the input of every destructive tool. A call
// without confirm=true is rejected before anything is dispatched.
type Confirmation struct {
	Confirm bool `json:"confirm,omitempty" jsonschema:"must be true: this tool is destructive and is rejected without it"`
}

// confirmed is implemented by Confirmation (and so by every input embedding
// it), which is how the generic wrapper finds out whether the caller agreed.
func (c Confirmation) confirmed() bool { return c.Confirm }

type confirmer interface{ confirmed() bool }

// requireConfirm is the one place the confirmation convention is enforced.
func requireConfirm(tool string, in any) error {
	if c, ok := in.(confirmer); ok && c.confirmed() {
		return nil
	}
	return fmt.Errorf("confirmation required: %s is destructive; call it again with confirm=true to proceed (nothing was changed)", tool)
}

// destructiveSuffix is appended to the description of every destructive tool.
const destructiveSuffix = " DESTRUCTIVE: requires the argument confirm=true; without it the call is rejected and nothing changes."

var pathParam = regexp.MustCompile(`\{([a-z_]+)\}`)

// restOpt tunes how a tool's arguments map onto the REST request.
type restOpt func(*restSpec)

type restSpec struct {
	aliases map[string]string // path parameter -> argument name
	query   map[string]bool   // arguments sent as query parameters
}

// alias names the argument that fills path parameter param.
func alias(param, arg string) restOpt {
	return func(s *restSpec) { s.aliases[param] = arg }
}

// asQuery sends the named arguments as query parameters even on a write.
func asQuery(args ...string) restOpt {
	return func(s *restSpec) {
		for _, a := range args {
			s.query[a] = true
		}
	}
}

// addREST registers one tool for one REST route ("METHOD /path/{param}").
// In is the tool's argument struct: its json fields are the path parameters,
// the query parameters (every field of a GET, plus asQuery ones) and the JSON
// body (everything else). destructive adds the confirm convention.
func addREST[In any](s *mcp.Server, rc *restCaller, name, route, desc string, destructive bool, opts ...restOpt) {
	spec := &restSpec{aliases: map[string]string{}, query: map[string]bool{}}
	for _, o := range opts {
		o(spec)
	}
	method, pattern, ok := strings.Cut(route, " ")
	if !ok {
		panic("mcpserver: bad route " + route)
	}
	if destructive {
		desc += destructiveSuffix
	}
	desc += " Mirrors " + route + "."
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc},
		func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			if destructive {
				if err := requireConfirm(name, in); err != nil {
					return nil, nil, err
				}
			}
			var hdr http.Header
			if req != nil && req.Extra != nil {
				hdr = req.Extra.Header
			}
			out, err := rc.call(ctx, hdr, method, pattern, spec, in)
			return nil, out, err
		})
}

// call dispatches one request and shapes the response for MCP.
func (rc *restCaller) call(ctx context.Context, hdr http.Header, method, pattern string, spec *restSpec, in any) (any, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	args := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	delete(args, "confirm")

	path := pattern
	for _, m := range pathParam.FindAllStringSubmatch(pattern, -1) {
		param := m[1]
		arg := param
		if a, ok := spec.aliases[param]; ok {
			arg = a
		}
		var v string
		if err := json.Unmarshal(args[arg], &v); err != nil || v == "" {
			return nil, fmt.Errorf("%s is required", arg)
		}
		delete(args, arg)
		path = strings.ReplaceAll(path, "{"+param+"}", url.PathEscape(v))
	}

	q := url.Values{}
	for k, v := range args {
		if method == http.MethodGet || spec.query[k] {
			delete(args, k)
			if s := queryValue(v); s != "" {
				q.Set(k, s)
			}
		}
	}
	target := path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}

	var body *bytes.Reader
	if method != http.MethodGet && method != http.MethodDelete {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	r, err := http.NewRequestWithContext(ctx, method, "http://mcp.internal"+target, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	if a := hdr.Get("Authorization"); a != "" {
		r.Header.Set("Authorization", a)
	}
	rec := httptest.NewRecorder()
	rc.h.ServeHTTP(rec, r)
	return shapeResponse(rec)
}

// queryValue renders a JSON argument as a query-string value; null and empty
// strings are dropped (the REST handlers treat an absent parameter as unset).
func queryValue(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	if string(v) == "null" {
		return ""
	}
	return string(v)
}

// shapeResponse turns the recorded REST response into a tool result: an error
// status becomes a tool error carrying the handler's message, 204 becomes
// {"ok": true}, an object passes through, and an array is wrapped as
// {"items": [...]} (structured tool output is an object).
func shapeResponse(rec *httptest.ResponseRecorder) (any, error) {
	body := bytes.TrimSpace(rec.Body.Bytes())
	if rec.Code >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		msg := http.StatusText(rec.Code)
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return nil, fmt.Errorf("%s (HTTP %d)", msg, rec.Code)
	}
	if len(body) == 0 {
		return map[string]any{"ok": true}, nil
	}
	switch body[0] {
	case '{':
		return json.RawMessage(body), nil
	case '[':
		return map[string]json.RawMessage{"items": body}, nil
	}
	return nil, errors.New("unexpected response from the REST handler")
}
