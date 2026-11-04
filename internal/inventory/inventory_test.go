package inventory

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestCatalogCapabilitiesAndCallerOwnership(t *testing.T) {
	seen := make(map[string]bool)
	for _, definition := range Catalog() {
		if seen[definition.ID] || definition.ID == "" || len(definition.Commands) == 0 {
			t.Fatalf("invalid catalogue definition: %+v", definition)
		}
		seen[definition.ID] = true
	}
	for _, id := range []string{"brew", "rustup", "npm", "cargo", "uv", "pipx", "bun", "go", "vscode", "cursor", "mas", "gcloud", "softwareupdate"} {
		if !seen[id] {
			t.Errorf("missing %s", id)
		}
	}
	if !slices.Equal(SupportedIDs(), []string{"brew", "rustup", "npm"}) {
		t.Fatalf("unexpected execution capabilities: %v", SupportedIDs())
	}
	one := Catalog()
	one[0].Commands[0] = "mutated"
	if Catalog()[0].Commands[0] != "brew" {
		t.Fatal("caller mutated shared catalogue state")
	}
}

func TestJSONSeparatesMissingUnsupportedAndUnknownMetadata(t *testing.T) {
	report := Report{SchemaVersion: SchemaVersion, Scope: CurrentPath,
		Installations: []Installation{{ID: "uv", Name: "uv", Executable: "/tools/uv", Owner: OwnerUnknown, UpdateSupport: Unsupported}},
		Missing:       []MissingTool{{ID: "pipx", Name: "pipx", Commands: []string{"pipx"}}},
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"schema_version":1`, `"scope":"current_path"`, `"prefix_known":false`, `"update_support":"not_supported"`, `"owner":"unknown"`, `"missing":[`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("missing JSON contract %s: %s", field, data)
		}
	}
}
