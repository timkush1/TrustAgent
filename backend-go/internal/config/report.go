package config

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// WriteReport prints the configuration the way -check-config shows it: the dotenv file, every key
// that is not at its default (secrets masked, never printed), and the warnings.
func WriteReport(w io.Writer, c *Config) error {
	envFile := c.EnvFile
	if envFile == "" {
		envFile = "none"
	}
	if _, err := fmt.Fprintf(w, "TrustAgent proxy configuration (dotenv: %s)\n\n", envFile); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tSOURCE\tVALUE") //nolint:errcheck // tabwriter errors surface at Flush
	set := 0
	for _, e := range c.entries {
		if e.Source == SourceDefault {
			continue
		}
		set++
		name := e.Key.Name
		if e.Var != "" && e.Var != name {
			name += " (as " + e.Var + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", name, e.Source, e.DisplayValue()) //nolint:errcheck // see Flush
	}
	if set == 0 {
		fmt.Fprintln(tw, "(every key is at its default)\t\t") //nolint:errcheck // see Flush
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(c.Diagnostics) == 0 {
		_, err := fmt.Fprintln(w, "\nNo warnings.")
		return err
	}
	if _, err := fmt.Fprintf(w, "\n%d warning(s):\n", len(c.Diagnostics)); err != nil {
		return err
	}
	for _, d := range c.Diagnostics {
		if _, err := fmt.Fprintln(w, "  "+d.String()); err != nil {
			return err
		}
	}
	return nil
}
