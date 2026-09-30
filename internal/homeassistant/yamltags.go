package homeassistant

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// HA's YAML loader resolves !include, !include_dir_*, !env_var and !secret
// while parsing, so YAML text that HA parses can read files and environment
// variables on the HA host. The only custom tag a blueprint needs is !input.
var standardYAMLTags = map[string]bool{
	"": true, "!!str": true, "!!int": true, "!!float": true, "!!bool": true, "!!null": true,
	"!!map": true, "!!seq": true, "!!binary": true, "!!timestamp": true, "!!merge": true,
	"!!set": true, "!!omap": true, "!!pairs": true,
}

// CheckYAMLTags returns an error for any tag outside the YAML core schema,
// other than those in allowed. yaml.v3 expands %TAG handles and verbatim tags
// before this sees them, so "!<!include>" and "%TAG !e! !" forms are caught too.
func CheckYAMLTags(n *yaml.Node, allowed ...string) error {
	if n == nil {
		return nil
	}
	if !standardYAMLTags[n.Tag] && !slices.Contains(allowed, n.Tag) {
		return invalidArg("line %d: the YAML tag %s is not allowed here", n.Line, n.Tag)
	}
	for _, c := range n.Content {
		if err := CheckYAMLTags(c, allowed...); err != nil {
			return err
		}
	}
	return nil
}

// checkBlueprintYAML parses blueprint YAML the way HA will and rejects it
// unless it is a single document whose only custom tag is !input.
func checkBlueprintYAML(src string) error {
	dec := yaml.NewDecoder(strings.NewReader(src))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return invalidArg("blueprint YAML does not parse: %v", err)
	}
	if err := CheckYAMLTags(&doc, "!input"); err != nil {
		return fmt.Errorf("%w; blueprints may only use !input", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return invalidArg("blueprint YAML must be a single document")
	}
	return nil
}
