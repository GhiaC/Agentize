package toolruntime

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

func fixtureCatalog(t *testing.T, schema string, policy Policy) *Catalog {
	t.Helper()
	document := `{"tools":[{"name":"records","description":"Read or change records","input_schema":` + schema + `}]}`
	c, err := LoadCatalog(fstest.MapFS{"root/tools.json": &fstest.MapFile{Data: []byte(document)}}, map[string]Policy{"records": policy})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testPolicy() Policy {
	return Policy{Version: "1", Profiles: []string{"research"}, Scopes: []string{"records:read"}, Effects: Effects{Read: true, Inference: InferenceNone}, TimeoutMS: 1000, MaxResultBytes: 65536}
}

const recordsSchema = `{"type":"object","properties":{"action":{"type":"string","enum":["read","write","prompt"]},"value":{"type":"integer","minimum":1,"maximum":10},"filter":{"type":"object"}},"required":["action"],"additionalProperties":false}`

func TestCatalogRejectsDriftAndUntrustedSchemas(t *testing.T) {
	valid := `{"tools":[{"name":"records","description":"Records","input_schema":{"type":"object"}}]}`
	for _, tc := range []struct {
		name, document string
		policies       map[string]Policy
	}{
		{"missing policy", valid, nil},
		{"orphan policy", valid, map[string]Policy{"records": testPolicy(), "missing": testPolicy()}},
		{"missing effects", valid, map[string]Policy{"records": {Version: "1", Profiles: []string{"research"}, Scopes: []string{"read"}, TimeoutMS: 1000, MaxResultBytes: 1000}}},
		{"unknown active inference", valid, map[string]Policy{"records": {Version: "1", Profiles: []string{"research"}, Scopes: []string{"read"}, Effects: Effects{Read: true, Inference: InferenceUnknown}, TimeoutMS: 1000, MaxResultBytes: 1000}}},
		{"duplicate name", `{"tools":[{"name":"records","description":"A","input_schema":{"type":"object"}},{"name":"records","description":"B","input_schema":{"type":"object"}}]}`, map[string]Policy{"records": testPolicy()}},
		{"duplicate JSON key", `{"tools":[],"tools":[]}`, nil},
		{"bad schema", strings.Replace(valid, `"type":"object"`, `"type":"not-a-type"`, 1), map[string]Policy{"records": testPolicy()}},
		{"bad required", strings.Replace(valid, `"type":"object"`, `"type":"object","required":"action"`, 1), map[string]Policy{"records": testPolicy()}},
		{"remote ref", strings.Replace(valid, `"type":"object"`, `"type":"object","properties":{"x":{"$ref":"https://schema.invalid/input"}}`, 1), map[string]Policy{"records": testPolicy()}},
		{"local ref", strings.Replace(valid, `"type":"object"`, `"type":"object","$ref":"file:///private/input.json"`, 1), map[string]Policy{"records": testPolicy()}},
		{"external metaschema", strings.Replace(valid, `"type":"object"`, `"type":"object","$schema":"https://schema.invalid/meta"`, 1), map[string]Policy{"records": testPolicy()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadCatalog(fstest.MapFS{"root/tools.json": &fstest.MapFile{Data: []byte(tc.document)}}, tc.policies)
			if err == nil {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
}

func TestCatalogVersionAndDefinitionsAreImmutable(t *testing.T) {
	policy := testPolicy()
	c := fixtureCatalog(t, recordsSchema, policy)
	version := c.Version()
	policy.Profiles[0] = "admin"
	d, _ := c.Definition("records")
	d.Policy.Profiles[0] = "admin"
	d.InputSchema[0] = '!'
	again, _ := c.Definition("records")
	if again.Policy.Profiles[0] != "research" || again.InputSchema[0] != '{' || c.Version() != version {
		t.Fatal("mutable catalog metadata escaped")
	}
	c2 := fixtureCatalog(t, " \n"+recordsSchema+"\n ", testPolicy())
	if c2.Version() != version {
		t.Fatal("whitespace changed catalog identity")
	}
	p := testPolicy()
	p.Version = "2"
	if fixtureCatalog(t, recordsSchema, p).Version() == version {
		t.Fatal("policy changes did not change catalog identity")
	}
}

func TestCatalogAliasRequiresIdenticalContract(t *testing.T) {
	document := `{"tools":[{"name":"records","description":"Records","input_schema":{"type":"object"}},{"name":"legacy_records","description":"Legacy records","input_schema":{"type":"object"}}]}`
	canonical := testPolicy()
	alias := testPolicy()
	alias.AliasOf = "records"
	tree := fstest.MapFS{"root/tools.json": &fstest.MapFile{Data: []byte(document)}}
	if _, err := LoadCatalog(tree, map[string]Policy{"records": canonical, "legacy_records": alias}); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCatalog(tree, map[string]Policy{"records": canonical, "legacy_records": alias})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	binding := Binding{Handler: func(context.Context, Call) (any, error) { calls++; return true, nil }}
	r, err := New(c, map[string]Binding{"records": binding}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(context.Background(), principal(), Invocation{Name: "legacy_records", Profile: "research"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("alias did not use canonical binding")
	}
	if _, err := New(c, map[string]Binding{"records": binding, "legacy_records": binding}, nil); err == nil {
		t.Fatal("independent alias handler accepted")
	}
	alias.Scopes = []string{"admin"}
	if _, err := LoadCatalog(tree, map[string]Policy{"records": canonical, "legacy_records": alias}); err == nil {
		t.Fatal("alias privilege drift accepted")
	}
}
