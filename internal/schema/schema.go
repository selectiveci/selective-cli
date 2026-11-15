package schema

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type Document struct {
	Binary    string      `json:"binary"`
	Version   string      `json:"version"`
	Commands  []Command   `json:"commands"`
	ExitCodes []ExitCode  `json:"exit_codes"`
	Errors    []ErrorCode `json:"errors"`
	Events    []Event     `json:"events"`
}

type Command struct {
	Path        string   `json:"path"`
	Use         string   `json:"use"`
	Short       string   `json:"short"`
	Long        string   `json:"long,omitempty"`
	Example     string   `json:"example,omitempty"`
	Runnable    bool     `json:"runnable"`
	Args        []Arg    `json:"args"`
	Flags       []Flag   `json:"flags"`
	ExitCodes   []int    `json:"exit_codes,omitempty"`
	Events      []string `json:"events,omitempty"`
	Mutates     bool     `json:"mutates,omitempty"`
	Destructive bool     `json:"destructive,omitempty"`
	WaitDefault string   `json:"wait_default,omitempty"`
	SecretFlags []string `json:"secret_flags,omitempty"`
	MCP         string   `json:"mcp,omitempty"`
	SendsPaths  bool     `json:"sends_paths,omitempty"`
}

type Arg struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Variadic bool   `json:"variadic"`
}

type Flag struct {
	Name       string `json:"name"`
	Shorthand  string `json:"shorthand,omitempty"`
	Type       string `json:"type"`
	Default    string `json:"default"`
	Usage      string `json:"usage"`
	Required   bool   `json:"required"`
	Persistent bool   `json:"persistent"`
}

func Build(root *cobra.Command, version string) (Document, error) {
	doc := Document{
		Binary:    root.Name(),
		Version:   version,
		Commands:  []Command{},
		ExitCodes: ExitCodes(),
		Errors:    Errors(),
		Events:    Events(),
	}
	var problems []error
	for _, c := range descendants(root) {
		node, err := describe(root, c)
		if err != nil {
			problems = append(problems, err)
		}
		doc.Commands = append(doc.Commands, node)
	}
	return doc, errors.Join(problems...)
}

func Lookup(doc Document, path []string) (Command, bool) {
	want := strings.Join(path, " ")
	for _, c := range doc.Commands {
		if c.Path == want {
			return c, true
		}
	}
	return Command{}, false
}

func descendants(c *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	for _, sub := range c.Commands() {
		if sub.Hidden || sub.Name() == "completion" {
			continue
		}
		out = append(out, sub)
		out = append(out, descendants(sub)...)
	}
	return out
}

func describe(root, c *cobra.Command) (Command, error) {
	node := Command{
		Path:     strings.TrimPrefix(c.CommandPath(), root.Name()+" "),
		Use:      c.Use,
		Short:    c.Short,
		Long:     c.Long,
		Example:  c.Example,
		Runnable: c.Runnable(),
		Args:     positionals(c.Use),
		Flags:    flags(c),
		MCP:      c.Annotations["mcp"],
	}
	return node, annotate(&node, c)
}

func positionals(use string) []Arg {
	args := []Arg{}
	fields := strings.Fields(use)
	if len(fields) == 0 {
		return args
	}
	for _, f := range fields[1:] {
		if f == "[flags]" || f == "[options]" {
			continue
		}
		arg := Arg{Required: !strings.HasPrefix(f, "[")}
		f = strings.Trim(f, "[]")
		arg.Variadic = strings.HasSuffix(f, "...")
		arg.Name = strings.TrimSuffix(f, "...")
		args = append(args, arg)
	}
	return args
}

func flags(c *cobra.Command) []Flag {
	out := []Flag{}
	visit := func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		out = append(out, Flag{
			Name:       f.Name,
			Shorthand:  f.Shorthand,
			Type:       f.Value.Type(),
			Default:    f.DefValue,
			Usage:      f.Usage,
			Required:   len(f.Annotations[cobra.BashCompOneRequiredFlag]) > 0 && f.Annotations[cobra.BashCompOneRequiredFlag][0] == "true",
			Persistent: c.PersistentFlags().Lookup(f.Name) != nil || c.InheritedFlags().Lookup(f.Name) != nil,
		})
	}
	c.LocalFlags().VisitAll(visit)
	c.InheritedFlags().VisitAll(visit)
	return out
}
