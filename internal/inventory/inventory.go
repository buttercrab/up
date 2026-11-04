// Package inventory defines the read-only installation catalogue and report.
// Detection is limited to the current PATH; it is not a machine-wide software
// census, a package inventory, or an assertion that any update is available.
package inventory

const SchemaVersion = 1
const CurrentPath = "current_path"

type Support string

const (
	Supported     Support = "supported"
	Unsupported   Support = "not_supported"
	Blocked       Support = "blocked"
	OwnerUnknown          = "unknown"
	OwnerHomebrew         = "homebrew"
	OwnerRustup           = "rustup"
)

type Report struct {
	SchemaVersion int            `json:"schema_version"`
	Scope         string         `json:"scope"`
	Installations []Installation `json:"installations"`
	Missing       []MissingTool  `json:"missing"`
}

// Executable is the entrypoint found on PATH. ResolvedExecutable is its current
// physical target, if readable. PlannedExecutable is a separately validated
// upgrade entrypoint, not a promise that an upgrade is currently available.
type Installation struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Executable         string    `json:"executable"`
	ResolvedExecutable string    `json:"resolved_executable,omitempty"`
	Owner              string    `json:"owner"`
	Prefix             string    `json:"prefix,omitempty"`
	PrefixKnown        bool      `json:"prefix_known"`
	Runtimes           []Runtime `json:"runtimes,omitempty"`
	UpdateSupport      Support   `json:"update_support"`
	Reason             string    `json:"reason,omitempty"`
	Resources          []string  `json:"resources,omitempty"`
	PlannedExecutable  string    `json:"planned_executable,omitempty"`
}

type Runtime struct {
	ID                 string `json:"id"`
	Executable         string `json:"executable"`
	ResolvedExecutable string `json:"resolved_executable,omitempty"`
	Owner              string `json:"owner"`
}

type MissingTool struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Commands []string `json:"commands"`
}

type Definition struct {
	ID            string
	Name          string
	Commands      []string
	UpdateSupport Support
}

// Catalog is the authoritative capability declaration. Only the first matching
// command for each definition is detected, using the current PATH. Alternate
// versions, GUI-only applications, and project dependencies are not scanned.
func Catalog() []Definition {
	return []Definition{
		{ID: "brew", Name: "Homebrew", Commands: []string{"brew"}, UpdateSupport: Supported},
		{ID: "rustup", Name: "Rustup", Commands: []string{"rustup"}, UpdateSupport: Supported},
		{ID: "npm", Name: "npm", Commands: []string{"npm"}, UpdateSupport: Supported},
		{ID: "cargo", Name: "Cargo", Commands: []string{"cargo"}, UpdateSupport: Unsupported},
		{ID: "uv", Name: "uv", Commands: []string{"uv"}, UpdateSupport: Unsupported},
		{ID: "pipx", Name: "pipx", Commands: []string{"pipx"}, UpdateSupport: Unsupported},
		{ID: "bun", Name: "Bun", Commands: []string{"bun"}, UpdateSupport: Unsupported},
		{ID: "go", Name: "Go", Commands: []string{"go"}, UpdateSupport: Unsupported},
		{ID: "vscode", Name: "Visual Studio Code", Commands: []string{"code", "code-insiders"}, UpdateSupport: Unsupported},
		{ID: "cursor", Name: "Cursor", Commands: []string{"cursor"}, UpdateSupport: Unsupported},
		{ID: "mas", Name: "Mac App Store CLI", Commands: []string{"mas"}, UpdateSupport: Unsupported},
		{ID: "gcloud", Name: "Google Cloud CLI", Commands: []string{"gcloud"}, UpdateSupport: Unsupported},
		{ID: "softwareupdate", Name: "macOS Software Update", Commands: []string{"softwareupdate"}, UpdateSupport: Unsupported},
	}
}

func SupportedIDs() []string {
	var ids []string
	for _, definition := range Catalog() {
		if definition.UpdateSupport == Supported {
			ids = append(ids, definition.ID)
		}
	}
	return ids
}
