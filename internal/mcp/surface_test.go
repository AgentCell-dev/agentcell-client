package mcp

import (
	"reflect"
	"testing"

	"github.com/agentcell/agentcell-client/contract"
	"github.com/agentcell/agentcell-client/internal/cli"
)

type probeRequest struct {
	Value string `json:"value" cli:"value,required" description:"Probe value"`
}

func (probeRequest) Operation() string { return "probe" }

func TestAddingOneDefinitionAddsCLIAndMCP(t *testing.T) {
	definitions := append([]contract.Definition{}, contract.Definitions...)
	definitions = append(definitions, contract.Definition{Name: "probe", Summary: "Parity probe", RequestType: reflect.TypeFor[probeRequest](), ResponseType: reflect.TypeFor[contract.PSResponse]()})
	commands, tools := cli.Commands(definitions), Tools(definitions)
	if commands[len(commands)-1].Name != "probe" {
		t.Fatal("CLI did not gain probe from definition")
	}
	if tools[len(tools)-1].Name != "probe" {
		t.Fatal("MCP did not gain probe from definition")
	}
	if len(commands) != len(tools) {
		t.Fatalf("surface counts drifted: CLI=%d MCP=%d", len(commands), len(tools))
	}
}

func TestProductionSurfacesHaveExactParity(t *testing.T) {
	commands, tools := cli.Commands(contract.Definitions), Tools(contract.Definitions)
	if len(commands) != len(tools) {
		t.Fatalf("CLI=%d MCP=%d", len(commands), len(tools))
	}
	for i := range commands {
		if commands[i].Name != tools[i].Name {
			t.Fatalf("index %d: CLI=%q MCP=%q", i, commands[i].Name, tools[i].Name)
		}
	}
}
