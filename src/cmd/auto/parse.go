package main

import (
	"fmt"
	"strings"
)

// parsedArgs holds the positional and flag values extracted from a subcommand's
// arguments. Flags may appear anywhere, before or after positionals, matching
// the original argparse-based CLI.
type parsedArgs struct {
	positional []string
	values     map[string]string
	bools      map[string]bool
}

// parseArgs splits args into positionals, value flags, and bool flags. valueFlags
// and boolFlags are the recognised long-option names (without the leading --).
func parseArgs(args []string, valueFlags, boolFlags map[string]bool) (*parsedArgs, error) {
	out := &parsedArgs{values: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			out.positional = append(out.positional, arg)
			continue
		}
		name, inlineVal, hasInline := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		switch {
		case boolFlags[name]:
			out.bools[name] = true
		case valueFlags[name] && hasInline:
			out.values[name] = inlineVal
		case valueFlags[name]:
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --%s requires a value", name)
			}
			i++
			out.values[name] = args[i]
		default:
			return nil, fmt.Errorf("unknown flag --%s", name)
		}
	}
	return out, nil
}

// requireName returns the first positional as the process name or an error.
func (p *parsedArgs) requireName() (string, error) {
	if len(p.positional) == 0 {
		return "", fmt.Errorf("missing process name")
	}
	return p.positional[0], nil
}
