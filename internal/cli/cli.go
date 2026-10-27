// Package cli implements arbot, the Safenet arbitration bot's command-line
// tool, as a function that cmd/arbot and end-to-end tests call.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"

	"github.com/safe-research/safenet-arbitration-bot/internal/config"
)

// env is what a command runs with.
type env struct {
	progname string
	cfg      config.Config
	stdout   io.Writer
	stderr   io.Writer
}

// commands are the subcommands of arbot, by name.
var commands = map[string]struct {
	run         func(ctx context.Context, e *env, args []string) error
	description string
}{
	"calls":      {callsCommand, "list the calls that the Safe of a Safenet request makes, as the checks judge them"},
	"charter":    {charter, "fetch the Safenet Arbitration Charter"},
	"events":     {eventsCommand, "list the events that a transaction logged, on the Safe's chain of a Safenet request"},
	"executions": {executionsCommand, "list the Safe transactions that the Safe of a Safenet request executed"},
	"classify":   {classify, "classify a Safenet request with deterministic Charter checks"},
	"index":      {indexCommand, "index the arbitrator's rulings into the rulings corpus"},
	"info":       {info, "show a Safenet request, its proposal, votes, and arbitration"},
	"pending":    {pending, "list the disputed Safenet requests awaiting arbitration"},
	"safe":       {safeCommand, "show the settings of the Safe of a Safenet request"},
	"tags":       {tags, "list the tags of a ruling in the rulings corpus"},
	"transfers":  {transfers, "list the transfers out of, or into, the Safe of a Safenet request"},
}

// usageError is an error in the command-line arguments, for which arbot exits
// with status 2. An empty message means that the error and the usage have been
// printed already.
type usageError string

func (e usageError) Error() string {
	return string(e)
}

// Run runs arbot with the command-line arguments args, which start with the
// program name like os.Args, writing its output to stdout and its errors to
// stderr. It returns the exit status: 0 on success, 1 on failure, and 2 for
// invalid arguments.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	e := &env{progname: filepath.Base(args[0]), stdout: stdout, stderr: stderr}
	err := e.run(ctx, args[1:])
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return 0
	}
	code := 1
	if usage, ok := errors.AsType[usageError](err); ok {
		if usage == "" {
			return 2
		}
		code = 2
	}
	fmt.Fprintf(stderr, "%s: %v\n", e.progname, err)
	return code
}

func (e *env) run(ctx context.Context, args []string) error {
	flags := e.flagSet(e.progname)
	configPath := flags.String("config", "", "path to the configuration file (default: ./arbot.config.json, then $XDG_CONFIG_HOME/arbot/config.json)")
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "Usage: %s [flags] <command> [arguments]\n\nCommands:\n", e.progname)
		for _, name := range slices.Sorted(maps.Keys(commands)) {
			fmt.Fprintf(out, "  %-10s %s\n", name, commands[name].description)
		}
		fmt.Fprintf(out, "\nFlags:\n")
		flags.PrintDefaults()
	}
	if err := parse(flags, args, -1); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	e.cfg = cfg

	if flags.NArg() == 0 {
		return usageError("missing command")
	}
	command, ok := commands[flags.Arg(0)]
	if !ok {
		return usageError(fmt.Sprintf("unknown command %q", flags.Arg(0)))
	}
	return command.run(ctx, e, flags.Args()[1:])
}

// flagSet returns a flag set for a command, which writes errors and usage to
// stderr.
func (e *env) flagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(e.stderr)
	return flags
}

// parse parses args with flags, and checks that n positional arguments remain,
// unless n is negative. On an invalid argument, the flag set prints the error
// and the usage, and parse returns an empty usageError. It returns flag.ErrHelp
// if the usage was requested.
func parse(flags *flag.FlagSet, args []string, n int) error {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError("")
	}
	if n >= 0 && flags.NArg() != n {
		flags.Usage()
		return usageError("")
	}
	return nil
}
