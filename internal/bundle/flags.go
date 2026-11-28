package bundle

import "github.com/spf13/pflag"

// valueFlags are the flags recorded with their value (design §5): enum,
// numeric, duration and boolean flags. Every other flag is recorded by name.
var valueFlags = map[string]bool{
	"all-namespaces":  true,
	"all-controllers": true,
	"since":           true,
	"tail":            true,
	"timeout":         true,
	"request-timeout": true,
	"max-size":        true,
	"no-endpoints":    true,
}

// RecordFlags returns the flags that were set on fs, sorted by long name.
// Only the flags in the design's value list carry their value.
func RecordFlags(fs *pflag.FlagSet) []FlagValue {
	var out []FlagValue
	fs.Visit(func(f *pflag.Flag) {
		fv := FlagValue{Name: f.Name}
		if valueFlags[f.Name] {
			v := f.Value.String()
			fv.Value = &v
		}
		out = append(out, fv)
	})
	return out
}
