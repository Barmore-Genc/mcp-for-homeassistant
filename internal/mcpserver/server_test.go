package mcpserver

import (
	"regexp"
	"slices"
	"testing"
)

func TestInstructionsNameOfferedTools(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		cs, _ := stateConnect(t, readOnly)
		instr := cs.InitializeResult().Instructions
		tools := automationToolNames(t, cs)
		mentioned := regexp.MustCompile(`ha_[a-z_]+`).FindAllString(instr, -1)
		if len(mentioned) == 0 {
			t.Fatalf("read-only %v: instructions name no tools:\n%s", readOnly, instr)
		}
		for _, name := range mentioned {
			if !slices.Contains(tools, name) {
				t.Errorf("read-only %v: instructions mention %s, which is not offered", readOnly, name)
			}
		}
	}
}
