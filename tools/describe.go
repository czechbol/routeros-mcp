package tools

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/czechbol/routeros-mcp/server"
)

//go:embed openapi/*.json
var openapiFS embed.FS

const (
	openapiDir = "openapi"
	pathSep    = "/"
)

// ErrShardNotFound signals a request for a top-level menu we do not have
// shipped in the embedded OpenAPI catalogue.
var ErrShardNotFound = errors.New("no embedded shard for menu")

type openapiIndex struct {
	SpecVersion string   `json:"spec_version"`
	OpenAPI     string   `json:"openapi"`
	Menus       []string `json:"menus"`
}

type openapiShard struct {
	Menu  string                             `json:"menu"`
	Paths map[string]map[string]operationDoc `json:"paths"`
}

type operationDoc struct {
	Summary     string         `json:"summary,omitempty"`
	OperationID string         `json:"operationId,omitempty"`
	RequestBody map[string]any `json:"requestBody,omitempty"`
	Responses   map[string]any `json:"responses,omitempty"`
}

var (
	embeddedIndex     openapiIndex
	errEmbeddedIndex  error
	embeddedIndexOnce sync.Once

	shardCache   = make(map[string]*openapiShard)
	shardCacheMu sync.RWMutex

	liveSpecMu     sync.RWMutex
	liveSpecActive *server.LiveSpec
)

// SetLiveSpec installs an OpenAPI document fetched at startup (Sprint 3 dynamic
// fetch). ros_describe will prefer this spec over the embedded shards. Pass
// nil to fall back to the embedded data.
func SetLiveSpec(spec *server.LiveSpec) {
	liveSpecMu.Lock()
	defer liveSpecMu.Unlock()
	liveSpecActive = spec
}

func activeLiveSpec() *server.LiveSpec {
	liveSpecMu.RLock()
	defer liveSpecMu.RUnlock()
	return liveSpecActive
}

func loadEmbeddedIndex() (openapiIndex, error) {
	embeddedIndexOnce.Do(func() {
		raw, err := openapiFS.ReadFile(openapiDir + pathSep + "index.json")
		if err != nil {
			errEmbeddedIndex = fmt.Errorf("read embedded openapi index: %w", err)
			return
		}
		errEmbeddedIndex = json.Unmarshal(raw, &embeddedIndex)
	})
	return embeddedIndex, errEmbeddedIndex
}

// lookupOperations returns the operation map for normalised (a leading-slash
// RouterOS path) using the live spec if present, otherwise the embedded shards.
func lookupOperations(normalised string) (map[string]operationDoc, string, error) {
	if live := activeLiveSpec(); live != nil {
		if rawOps, ok := live.Paths[normalised]; ok {
			ops, err := decodeLiveOps(rawOps)
			if err != nil {
				return nil, live.SpecVersion, err
			}
			return ops, live.SpecVersion, nil
		}
		// Live spec didn't have the path — fall through to embedded (a path
		// may exist in 7.22.3 but not in an older live version, or vice versa).
	}
	idx, err := loadEmbeddedIndex()
	if err != nil {
		return nil, "", fmt.Errorf("load openapi index: %w", err)
	}
	menu := strings.SplitN(strings.TrimLeft(normalised, pathSep), pathSep, 2)[0]
	shard, err := loadShard(menu)
	if err != nil {
		return nil, idx.SpecVersion, fmt.Errorf("no description for path %q: %w", normalised, err)
	}
	ops, ok := shard.Paths[normalised]
	if !ok {
		return nil, idx.SpecVersion, fmt.Errorf(
			"%w: %q (version %s); try ros_list_paths",
			server.ErrPathNotInCatalogue, normalised, idx.SpecVersion,
		)
	}
	return ops, idx.SpecVersion, nil
}

func decodeLiveOps(rawOps map[string]any) (map[string]operationDoc, error) {
	out := make(map[string]operationDoc, len(rawOps))
	for method, raw := range rawOps {
		buf, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("re-encode live op %s: %w", method, err)
		}
		var op operationDoc
		if err := json.Unmarshal(buf, &op); err != nil {
			return nil, fmt.Errorf("decode live op %s: %w", method, err)
		}
		out[method] = op
	}
	return out, nil
}

func loadShard(menu string) (*openapiShard, error) {
	shardCacheMu.RLock()
	if s, ok := shardCache[menu]; ok {
		shardCacheMu.RUnlock()
		return s, nil
	}
	shardCacheMu.RUnlock()

	name := openapiDir + pathSep + menu + ".json"
	raw, err := openapiFS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("%w %q: %w", ErrShardNotFound, menu, err)
	}
	var s openapiShard
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode shard %s: %w", menu, err)
	}
	shardCacheMu.Lock()
	shardCache[menu] = &s
	shardCacheMu.Unlock()
	return &s, nil
}

// DescribeIn parameters for ros_describe.
type DescribeIn struct {
	Path   string `json:"path"             jsonschema:"RouterOS menu path to describe, e.g. ip/firewall/filter, ip/address. Leading slash optional."`
	Op     string `json:"op,omitempty"     jsonschema:"specific operation: get, put, patch, post, delete. Empty returns all operations."`
	Format string `json:"format,omitempty" jsonschema:"response format: json or markdown (default markdown)"`
}

// DescribedParameter is a single field surfaced from the OpenAPI request body.
type DescribedParameter struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Format      string `json:"format,omitempty"`
	Description string `json:"description,omitempty"`
	Default     any    `json:"default,omitempty"`
	Enum        []any  `json:"enum,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// DescribedOperation summarises a single HTTP method on a RouterOS path.
type DescribedOperation struct {
	Method     string               `json:"method"`
	Summary    string               `json:"summary,omitempty"`
	Parameters []DescribedParameter `json:"parameters,omitempty"`
}

// DescribeOut is the structured response for ros_describe.
type DescribeOut struct {
	Path        string               `json:"path"`
	SpecVersion string               `json:"spec_version"`
	Operations  []DescribedOperation `json:"operations"`
}

// RegisterDescribeTool wires the ros_describe tool onto srv.
func RegisterDescribeTool(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ros_describe",
		Description: "Describe a RouterOS REST path: list its operations (get/put/patch/post/delete) and the parameters each accepts. Backed by the bundled OpenAPI spec. Use BEFORE calling ros_add/ros_set/ros_exec to learn valid field names, types, enums, and required fields. Note: ros_add uses PUT, ros_set uses PATCH, ros_remove uses DELETE, ros_exec uses POST. Most CRUD paths expose GET+PUT only.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  ptr(false),
		},
	}, describe)
}

func describe(
	_ context.Context, _ *mcp.CallToolRequest, in DescribeIn,
) (*mcp.CallToolResult, DescribeOut, error) {
	if in.Path == "" {
		return server.ToolError("path is required; example: ip/address"), DescribeOut{}, nil
	}
	if in.Format == "" {
		in.Format = formatMarkdown
	}

	normalised := pathSep + strings.TrimLeft(in.Path, pathSep)
	ops, specVersion, err := lookupOperations(normalised)
	if err != nil {
		return server.ToolError("%v", err), DescribeOut{}, nil
	}

	out := DescribeOut{Path: normalised, SpecVersion: specVersion}
	wantOp := strings.ToLower(strings.TrimSpace(in.Op))
	methods := make([]string, 0, len(ops))
	for m := range ops {
		if wantOp == "" || strings.EqualFold(m, wantOp) {
			methods = append(methods, m)
		}
	}
	sort.Strings(methods)
	if wantOp != "" && len(methods) == 0 {
		available := make([]string, 0, len(ops))
		for m := range ops {
			available = append(available, strings.ToUpper(m))
		}
		sort.Strings(available)
		return server.ToolError(
			"no %q operation for %s; available: %s", wantOp, normalised, strings.Join(available, ", "),
		), out, nil
	}

	for _, m := range methods {
		op := ops[m]
		out.Operations = append(out.Operations, DescribedOperation{
			Method:     strings.ToUpper(m),
			Summary:    op.Summary,
			Parameters: extractParameters(op.RequestBody),
		})
	}

	return server.Render(in.Format, out, func() string { return renderDescribe(out) }), out, nil
}

// extractParameters pulls field descriptors from
// requestBody.content."application/json".schema (often an allOf wrapper).
func extractParameters(body map[string]any) []DescribedParameter {
	schema := requestBodySchema(body)
	if schema == nil {
		return nil
	}
	properties, required := flattenSchema(schema)
	if len(properties) == 0 {
		return nil
	}
	out := make([]DescribedParameter, 0, len(properties))
	for name, raw := range properties {
		out = append(out, paramFromProp(name, raw, required[name]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func requestBodySchema(body map[string]any) map[string]any {
	content, _ := body["content"].(map[string]any)
	app, _ := content["application/json"].(map[string]any)
	schema, _ := app["schema"].(map[string]any)
	return schema
}

func paramFromProp(name string, raw any, required bool) DescribedParameter {
	prop, ok := raw.(map[string]any)
	if !ok {
		return DescribedParameter{Name: name, Required: required}
	}
	p := DescribedParameter{Name: name, Required: required}
	if v, ok := prop["type"].(string); ok {
		p.Type = v
	}
	if v, ok := prop["format"].(string); ok {
		p.Format = v
	}
	if v, ok := prop["description"].(string); ok {
		p.Description = v
	}
	if v, ok := prop["default"]; ok {
		p.Default = v
	}
	if v, ok := prop["enum"].([]any); ok {
		p.Enum = v
	}
	return p
}

func flattenSchema(schema map[string]any) (map[string]any, map[string]bool) {
	props := map[string]any{}
	required := map[string]bool{}
	mergeSchema(schema, props, required)
	return props, required
}

func mergeSchema(schema map[string]any, props map[string]any, required map[string]bool) {
	if p, ok := schema["properties"].(map[string]any); ok {
		maps.Copy(props, p)
	}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	// $ref resolution not implemented (would need a resolver wired to the full
	// component map); upstream specs heavily favour inline schemas / allOf.
	for _, key := range []string{"allOf", "oneOf", "anyOf"} {
		subs, _ := schema[key].([]any)
		for _, sub := range subs {
			if m, ok := sub.(map[string]any); ok {
				mergeSchema(m, props, required)
			}
		}
	}
}

func renderDescribe(out DescribeOut) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s (RouterOS %s)\n\n", out.Path, out.SpecVersion)
	for _, op := range out.Operations {
		fmt.Fprintf(&b, "## %s\n", op.Method)
		if op.Summary != "" {
			fmt.Fprintf(&b, "_%s_\n\n", op.Summary)
		}
		if len(op.Parameters) == 0 {
			b.WriteString("_no documented parameters_\n\n")
			continue
		}
		b.WriteString("| name | type | required | description |\n")
		b.WriteString("|------|------|----------|-------------|\n")
		for _, p := range op.Parameters {
			fmt.Fprintf(
				&b, "| `%s` | %s%s | %s | %s |\n",
				p.Name, p.Type, formatEnum(p.Enum), tickbox(p.Required), oneLine(p.Description),
			)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func formatEnum(enum []any) string {
	if len(enum) == 0 {
		return ""
	}
	parts := make([]string, 0, len(enum))
	for _, e := range enum {
		parts = append(parts, fmt.Sprintf("%v", e))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func tickbox(b bool) string {
	if b {
		return "yes"
	}
	return ""
}

func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "|", "/")
}
