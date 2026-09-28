package templatecontract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type manifest struct {
	SchemaVersion                       int               `json:"schemaVersion"`
	BuildProfile                        string            `json:"buildProfile"`
	TemplateVersion                     string            `json:"templateVersion"`
	MinimumSandboxImageContractRevision int               `json:"minimumSandboxImageContractRevision"`
	OpenAPISpec                         string            `json:"openapiSpec"`
	AgentInstructions                   string            `json:"agentInstructions"`
	Commands                            map[string]string `json:"commands"`
}

func TestTemplateContract(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	manifestBytes, err := os.ReadFile(filepath.Join(root, ".superverse", "template.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var contract manifest
	if err := json.Unmarshal(manifestBytes, &contract); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if contract.SchemaVersion != 1 || contract.BuildProfile != "go-react-v1" || contract.TemplateVersion != "1.6.2" || contract.MinimumSandboxImageContractRevision != 3 {
		t.Fatalf("unexpected template identity: %+v", contract)
	}
	if baseline := strings.TrimSpace(readFile(t, filepath.Join(root, "DEX_SERVER_BASELINE"))); baseline != "server/v0.13.2" {
		t.Fatalf("unexpected Dex Server baseline: %q", baseline)
	}
	if baseline := strings.TrimSpace(readFile(t, filepath.Join(root, "DEX_CLI_BASELINE"))); baseline != "cli-v0.13.8" {
		t.Fatalf("unexpected Dex CLI baseline: %q", baseline)
	}
	goModule := readFile(t, filepath.Join(root, "go.mod"))
	if !strings.Contains(goModule, "github.com/superdurable/dex/sdk-go v0.13.1") {
		t.Fatal("template must pin Dex Go SDK v0.13.1")
	}
	for _, path := range []string{contract.OpenAPISpec, contract.AgentInstructions} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Errorf("manifest path %q: %v", path, err)
		}
	}
	makefile := readFile(t, filepath.Join(root, "Makefile"))
	agents := readFile(t, filepath.Join(root, contract.AgentInstructions))
	readme := readFile(t, filepath.Join(root, "README.md"))
	for name, contents := range map[string]string{"AGENTS.md": agents, "README.md": readme} {
		if strings.Contains(contents, "/opt/superverse/dex-skills") {
			t.Errorf("%s must discover Dex Skills through the coding-agent host", name)
		}
	}
	if !strings.Contains(agents, "`dex-app-builder` skill") {
		t.Error("AGENTS.md must require the installed dex-app-builder skill")
	}
	for _, command := range contract.Commands {
		target := strings.TrimPrefix(command, "make ")
		if !strings.Contains(makefile, "\n"+target+":") && !strings.HasPrefix(makefile, target+":") {
			t.Errorf("Makefile does not define %q", target)
		}
		if !strings.Contains(agents, command) {
			t.Errorf("AGENTS.md does not mention %q", command)
		}
	}
	for _, removedPath := range []string{".gitmodules", ".agents"} {
		if _, err := os.Stat(filepath.Join(root, removedPath)); !os.IsNotExist(err) {
			t.Errorf("removed project-local skill path %q still exists", removedPath)
		}
	}

	dependencyUpdater := readFile(t, filepath.Join(root, "scripts", "update-dex-dependencies.py"))
	for _, required := range []string{
		"github.com/superdurable/dex/sdk-go",
		"DEX_SERVER_BASELINE",
		"DEX_CLI_BASELINE",
		"templateVersion",
		"GITHUB_OUTPUT",
	} {
		if !strings.Contains(dependencyUpdater, required) {
			t.Errorf("Dex dependency updater does not mention %q", required)
		}
	}
	updateWorkflow := readFile(t, filepath.Join(root, ".github", "workflows", "update-dex-dependencies.yml"))
	for _, required := range []string{
		"schedule:",
		"workflow_dispatch:",
		"pull-requests: write",
		"automation/update-dex-dependencies",
		"gh pr create",
		"gh workflow run ci.yml",
	} {
		if !strings.Contains(updateWorkflow, required) {
			t.Errorf("Dex dependency update workflow does not mention %q", required)
		}
	}
	ciWorkflow := readFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	for _, required := range []string{
		"workflow_dispatch:",
		"scripts/check-template-version.py",
		"gh release create",
		"contents: write",
	} {
		if !strings.Contains(ciWorkflow, required) {
			t.Errorf("Template CI does not mention %q", required)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}
