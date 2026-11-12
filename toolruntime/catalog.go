// Package toolruntime defines transport-independent tool contracts. It does not
// construct an agent, call a model, or start background workers.
package toolruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/xeipuuv/gojsonschema"
)

const (
	InferenceNone     Inference = "none"
	InferenceDirect   Inference = "direct"
	InferenceDeferred Inference = "deferred"
	InferenceUnknown  Inference = "unknown"
)

type Inference string

type Effects struct {
	Read           bool      `json:"read,omitempty"`
	Write          bool      `json:"write,omitempty"`
	ExternalHTTP   bool      `json:"external_http,omitempty"`
	ExternalAction bool      `json:"external_action,omitempty"`
	Inference      Inference `json:"inference"`
}

// Policy is reviewed application metadata, never supplied by a tool caller.
// An empty Profiles list deliberately makes a capability discovery-only.
type Policy struct {
	Version        string   `json:"version"`
	Profiles       []string `json:"profiles"`
	Scopes         []string `json:"scopes"`
	Effects        Effects  `json:"effects"`
	Context        []string `json:"context,omitempty"`
	TimeoutMS      int      `json:"timeout_ms"`
	MaxResultBytes int      `json:"max_result_bytes"`
	AliasOf        string   `json:"alias_of,omitempty"`
	Unavailable    string   `json:"unavailable,omitempty"`
}

type Definition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Group       string          `json:"group"`
	Policy      Policy          `json:"policy"`
}

type catalogEntry struct {
	definition Definition
	schema     *gojsonschema.Schema
}

type Catalog struct {
	entries map[string]catalogEntry
	names   []string
	version string
}

var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)

// LoadCatalog reads the existing knowledge schemas and an explicit policy for
// every name. It never resolves schema references over HTTP or the filesystem.
func LoadCatalog(tree fs.FS, policies map[string]Policy) (*Catalog, error) {
	if tree == nil {
		return nil, fmt.Errorf("tool catalog: knowledge filesystem is required")
	}
	c := &Catalog{entries: make(map[string]catalogEntry)}
	err := fs.WalkDir(tree, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("tool catalog: cannot walk knowledge tree")
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("tool catalog: symlinks are not supported")
		}
		if d.IsDir() || d.Name() != "tools.json" {
			return nil
		}
		data, err := fs.ReadFile(tree, name)
		if err != nil {
			return fmt.Errorf("tool catalog: cannot read tools document")
		}
		if len(data) > 4<<20 {
			return fmt.Errorf("tool catalog: tools document is too large")
		}
		var document struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"input_schema"`
			} `json:"tools"`
		}
		if err := decodeStrict(data, &document); err != nil {
			return fmt.Errorf("tool catalog: invalid tools document %s", name)
		}
		for _, tool := range document.Tools {
			if !toolNamePattern.MatchString(tool.Name) || strings.TrimSpace(tool.Description) == "" {
				return fmt.Errorf("tool catalog: invalid name or missing description in %s", name)
			}
			if _, exists := c.entries[tool.Name]; exists {
				return fmt.Errorf("tool catalog: duplicate tool %s", tool.Name)
			}
			policy, ok := policies[tool.Name]
			if !ok {
				return fmt.Errorf("tool catalog: missing policy for %s", tool.Name)
			}
			if err := validatePolicy(policy); err != nil {
				return fmt.Errorf("tool catalog: invalid policy for %s: %w", tool.Name, err)
			}
			var raw map[string]any
			if err := decodeStrict(tool.InputSchema, &raw); err != nil || raw["type"] != "object" {
				return fmt.Errorf("tool catalog: %s requires an object input schema", tool.Name)
			}
			if hasSchemaReference(raw) {
				return fmt.Errorf("tool catalog: schema references are not supported for %s", tool.Name)
			}
			loader := gojsonschema.NewSchemaLoader()
			loader.AutoDetect, loader.Validate, loader.Draft = false, true, gojsonschema.Draft7
			schema, err := loader.Compile(gojsonschema.NewGoLoader(raw))
			if err != nil {
				return fmt.Errorf("tool catalog: invalid JSON schema for %s", tool.Name)
			}
			canonical, err := json.Marshal(raw)
			if err != nil {
				return fmt.Errorf("tool catalog: cannot canonicalize schema")
			}
			def := Definition{Name: tool.Name, Description: tool.Description, InputSchema: canonical, Group: path.Dir(name), Policy: clonePolicy(policy)}
			c.entries[tool.Name] = catalogEntry{definition: def, schema: schema}
			c.names = append(c.names, tool.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(c.names) == 0 {
		return nil, fmt.Errorf("tool catalog: no tools found")
	}
	for name := range policies {
		if _, ok := c.entries[name]; !ok {
			return nil, fmt.Errorf("tool catalog: policy has no schema: %s", name)
		}
	}
	sort.Strings(c.names)
	for _, name := range c.names {
		alias := c.entries[name].definition.Policy.AliasOf
		if alias == "" {
			continue
		}
		target, ok := c.entries[alias]
		if !ok || alias == name || target.definition.Policy.AliasOf != "" {
			return nil, fmt.Errorf("tool catalog: invalid alias %s", name)
		}
		x, y := c.entries[name].definition, target.definition
		if !bytes.Equal(x.InputSchema, y.InputSchema) {
			return nil, fmt.Errorf("tool catalog: alias schema differs for %s", name)
		}
		x.Policy.AliasOf, y.Policy.AliasOf = "", ""
		a, _ := json.Marshal(x.Policy)
		b, _ := json.Marshal(y.Policy)
		if !bytes.Equal(a, b) {
			return nil, fmt.Errorf("tool catalog: alias policy differs for %s", name)
		}
	}
	encoded, _ := json.Marshal(c.Definitions())
	sum := sha256.Sum256(encoded)
	c.version = hex.EncodeToString(sum[:])
	return c, nil
}

func validatePolicy(p Policy) error {
	if p.Version == "" {
		return fmt.Errorf("version is required")
	}
	switch p.Effects.Inference {
	case InferenceNone, InferenceDirect, InferenceDeferred, InferenceUnknown:
	default:
		return fmt.Errorf("inference policy is required")
	}
	if len(p.Profiles) > 0 {
		if p.Unavailable != "" || p.Effects.Inference == InferenceUnknown || len(p.Scopes) == 0 || (!p.Effects.Read && !p.Effects.Write) {
			return fmt.Errorf("active capability requires reviewed effects and scopes")
		}
		if p.TimeoutMS < 1 || p.TimeoutMS > 60_000 || p.MaxResultBytes < 1 || p.MaxResultBytes > 1<<20 {
			return fmt.Errorf("active capability requires bounded execution and output")
		}
	} else if p.Unavailable == "" {
		return fmt.Errorf("discovery-only capability requires a reason")
	}
	for _, items := range [][]string{p.Profiles, p.Scopes, p.Context} {
		seen := make(map[string]bool)
		for _, item := range items {
			if strings.TrimSpace(item) == "" || seen[item] {
				return fmt.Errorf("empty or duplicate policy field")
			}
			seen[item] = true
		}
	}
	for _, key := range p.Context {
		if key != "account_id" && key != "conversation_id" && key != "system_id" && key != "system_version_id" && key != "workspace_id" {
			return fmt.Errorf("unknown context requirement")
		}
	}
	return nil
}

// LoadKnowledge uses canonical knowledge schemas and their reviewed execution
// manifest. Adding a schema without a policy is an error, not implicit access.
func LoadKnowledge(tree fs.FS) (*Catalog, error) {
	data, err := fs.ReadFile(tree, "tool-policies.json")
	if err != nil {
		return nil, fmt.Errorf("tool catalog: execution manifest is required")
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("tool catalog: execution manifest is too large")
	}
	var manifest struct {
		Version int               `json:"version"`
		Tools   map[string]Policy `json:"tools"`
	}
	if err := decodeStrict(data, &manifest); err != nil || manifest.Version != 1 {
		return nil, fmt.Errorf("tool catalog: invalid execution manifest")
	}
	return LoadCatalog(tree, manifest.Tools)
}

// hasSchemaReference rejects all URI-bearing schema keywords before the schema
// library runs. The current canonical knowledge schemas do not need references.
func hasSchemaReference(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			if k == "$ref" || k == "$id" || k == "$schema" {
				return true
			}
			if hasSchemaReference(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasSchemaReference(child) {
				return true
			}
		}
	}
	return false
}

func clonePolicy(p Policy) Policy {
	p.Profiles = append([]string(nil), p.Profiles...)
	p.Scopes = append([]string(nil), p.Scopes...)
	p.Context = append([]string(nil), p.Context...)
	sort.Strings(p.Profiles)
	sort.Strings(p.Scopes)
	sort.Strings(p.Context)
	return p
}

func (c *Catalog) Version() string { return c.version }

func (c *Catalog) Definition(name string) (Definition, bool) {
	e, ok := c.entries[name]
	if !ok {
		return Definition{}, false
	}
	d := e.definition
	d.InputSchema = append(json.RawMessage(nil), d.InputSchema...)
	d.Policy = clonePolicy(d.Policy)
	return d, true
}

func (c *Catalog) Definitions() []Definition {
	out := make([]Definition, 0, len(c.names))
	for _, name := range c.names {
		d, _ := c.Definition(name)
		out = append(out, d)
	}
	return out
}
