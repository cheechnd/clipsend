package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"clipsend/internal/direct"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func executeCLI(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	root := newRootCommand()
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	// Cobra's completion protocol writes a diagnostic to stderr even on success.
	// Completion is a quiet, local query; keep only its machine-readable stdout.
	completing := len(args) > 0 && (args[0] == "__complete" || args[0] == "__completeNoDesc")
	if completing {
		root.SetErr(io.Discard)
		if len(args) > 1 {
			args = append([]string{args[0]}, append(legacyFlagPlacement(root, args[1:len(args)-1]), args[len(args)-1])...)
		}
	} else {
		args = legacyFlagPlacement(root, args)
	}
	root.InitDefaultCompletionCmd(args...)
	completion, _, _ := root.Find([]string{"completion"})
	completion.RunE = func(cmd *cobra.Command, args []string) error { return cmd.Help() }
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	// Alias-first help historically returned flag.ErrHelp (exit 1). Keep that
	// existing status while Cobra's new command-level help exits successfully.
	if err == nil && !completing && legacyAliasHelp(root, args) {
		return errors.New("flag: help requested")
	}
	return err
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "clipsend <alias> [flags] [files...]",
		Short:         "Send text, images, and files to paired devices",
		Long:          "ClipSend — send text, images, and files to your paired iPhone or iPad.\n\nScan the pairing QR code in the ClipSend mobile app, then approve the device here.\nSends expire after two minutes. Both devices must be on the same LAN and this\ncomputer must remain awake. Options go after the device and before the first file.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs, // An unknown root word is the shorthand send alias.
	}
	root.SetVersionTemplate("clipsend {{.Version}}\n")
	root.Flags().Bool("list", false, "list paired devices (same as devices)")
	var quick sendOptions
	addSendFlags(root, &quick)
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		quick.TextSet = cmd.Flags().Changed("text")
		return withConfig(func(dir string) error {
			return sendTransfer(cmd.Context(), dir, canonicalDeviceAlias(dir, args[0]), quick, args[1:], cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	root.ValidArgsFunction = completeSend

	var sendOpts sendOptions
	send := &cobra.Command{
		Use: "send <alias> [flags] [files...]", Short: "Send text, the clipboard, stdin, or files to a paired device",
		Args:              usageArgs(1, -1, "clipsend send <alias> [--host IP] [--port PORT] [--stdin | --text TEXT]"),
		ValidArgsFunction: completeSend,
		RunE: func(cmd *cobra.Command, args []string) error {
			sendOpts.TextSet = cmd.Flags().Changed("text")
			return withConfig(func(dir string) error {
				return sendTransfer(cmd.Context(), dir, canonicalDeviceAlias(dir, args[0]), sendOpts, args[1:], cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			})
		},
	}
	addSendFlags(send, &sendOpts)
	root.AddCommand(send)

	var pairOpts pairOptions
	pair := &cobra.Command{
		Use: "pair <alias> [flags]", Short: "Pair a device by scanning a private QR code",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("usage: clipsend pair <alias> [--host IP] [--port PORT] [--qr PATH]")
			}
			if len(args) != 1 {
				return errors.New("unexpected pairing arguments")
			}
			return nil
		},
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConfig(func(dir string) error {
				return pairDevice(cmd.Context(), dir, args[0], pairOpts, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			})
		},
	}
	addNetworkFlags(pair.Flags(), &pairOpts.Host, &pairOpts.Port)
	pair.Flags().StringVar(&pairOpts.QR, "qr", "", "also write a private QR PNG to this path")
	pair.Flags().BoolVar(&pairOpts.Code, "code", false, "also print pairing JSON for manual entry")
	_ = pair.MarkFlagFilename("qr")
	noNetworkFlagCompletions(pair)
	root.AddCommand(pair)

	for _, spec := range []struct {
		name, use, short string
		count            int
	}{
		{"devices", "devices", "List paired devices", 0},
		{"unpair", "unpair <alias>", "Revoke a paired device's access", 1},
		{"rename", "rename <alias> <new-alias>", "Change a paired device's local alias", 2},
	} {
		command := &cobra.Command{
			Use: spec.use, Short: spec.short,
			Args:              usageArgs(spec.count, spec.count, "clipsend "+spec.use),
			ValidArgsFunction: cobra.NoFileCompletions,
			RunE: func(cmd *cobra.Command, args []string) error {
				return withConfig(func(dir string) error {
					return deviceCommand(dir, cmd.Name(), append([]string{cmd.Name()}, args...), cmd.OutOrStdout())
				})
			},
		}
		if spec.name == "unpair" || spec.name == "rename" {
			command.ValidArgsFunction = completeFirstAlias
		}
		root.AddCommand(command)
	}
	root.AddCommand(&cobra.Command{
		Use: "relay <private-config.json>", Short: "Import private notification service settings",
		Args: usageArgs(1, 1, "clipsend relay <private-config.json>"),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return nil, cobra.ShellCompDirectiveDefault
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConfig(func(dir string) error {
				c, err := readRelaySettings(args[0])
				if err != nil {
					return err
				}
				if err = saveRelaySettings(dir, c); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Notification service configured.")
				return nil
			})
		},
	})
	root.AddCommand(&cobra.Command{
		Use: "doctor", Short: "Check local configuration and network information",
		Args:              cobra.ArbitraryArgs, // Historically doctor ignores extra arguments.
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConfig(func(dir string) error { return doctor(dir, cmd.OutOrStdout()) })
		},
	})
	var uiHost string
	var uiPort int
	ui := &cobra.Command{
		Use: "ui", Short: "Run the internal desktop JSONL and socket service",
		Args:              usageArgs(0, 0, "clipsend ui [--host IP] [--port PORT]"),
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return uiCommand(cmd.Context(), uiHost, uiPort, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	addNetworkFlags(ui.Flags(), &uiHost, &uiPort)
	noNetworkFlagCompletions(ui)
	root.AddCommand(ui)
	// Cobra supplies completion bash|zsh|fish|powershell and the hidden __complete
	// protocol. Do not maintain shell-specific parsers alongside its generators.
	return root
}

func withConfig(fn func(string) error) error {
	dir, err := nativeConfigDir()
	if err != nil {
		return err
	}
	return fn(dir)
}

func usageArgs(minimum, maximum int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < minimum || (maximum >= 0 && len(args) > maximum) {
			return errors.New("usage: " + usage)
		}
		return nil
	}
}

func addNetworkFlags(flags *pflag.FlagSet, host *string, port *int) {
	flags.StringVar(host, "host", "", "local IPv4 address")
	flags.IntVar(port, "port", defaultPort, "local HTTPS port (allow it through your LAN firewall)")
}
func addSendFlags(cmd *cobra.Command, options *sendOptions) {
	addNetworkFlags(cmd.Flags(), &options.Host, &options.Port)
	cmd.Flags().BoolVar(&options.Stdin, "stdin", false, "read text from standard input instead of clipboard")
	cmd.Flags().StringVar(&options.Text, "text", "", "send this text instead of reading the clipboard")
	cmd.MarkFlagsMutuallyExclusive("text", "stdin")
	_ = cmd.RegisterFlagCompletionFunc("text", cobra.NoFileCompletions)
	noNetworkFlagCompletions(cmd)
}
func noNetworkFlagCompletions(cmd *cobra.Command) {
	for _, name := range []string{"host", "port"} {
		_ = cmd.RegisterFlagCompletionFunc(name, cobra.NoFileCompletions)
	}
}

func pairedCompletions(prefix string) []string {
	dir, err := nativeConfigDir()
	if err != nil {
		return nil
	}
	aliases, err := direct.ReadAliases(dir)
	if err != nil {
		return nil
	}
	var matches []string
	for _, alias := range aliases {
		if safeLabel(alias) != alias {
			continue
		}
		name, typed := []rune(alias), []rune(prefix)
		if len(typed) <= len(name) && strings.EqualFold(string(name[:len(typed)]), prefix) {
			// Shells may filter again case-sensitively. Preserve the typed prefix.
			matches = append(matches, prefix+string(name[len(typed):]))
		}
	}
	return matches
}
func completeSend(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return pairedCompletions(prefix), cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveDefault
}
func completeFirstAlias(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return pairedCompletions(prefix), cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// The original flag package accepted single-dash long options, and stopped at
// the first file after the required alias. Cobra/pflag handles parsing, values,
// and help; this small argv adapter preserves that boundary and spelling.
func legacyFlagPlacement(root *cobra.Command, original []string) []string {
	args := append([]string(nil), original...)
	if len(args) == 0 {
		return args
	}
	if args[0] == "--list" {
		args[0] = "devices"
		return args
	}
	cmd, aliasIndex := root, 0
	switch args[0] {
	case "send", "pair":
		cmd, _, _ = root.Find(args[:1])
		aliasIndex = 1
	case "devices", "unpair", "rename", "relay", "doctor", "ui", "help", "completion", "__complete", "__completeNoDesc":
		return args
	}
	if len(args) <= aliasIndex || strings.HasPrefix(args[aliasIndex], "-") {
		return args
	}
	cmd.InitDefaultHelpFlag()
	for i := aliasIndex + 1; i < len(args); i++ {
		word := args[i]
		if word == "--" {
			break
		}
		if word == "-" || !strings.HasPrefix(word, "-") {
			args = append(args[:i], append([]string{"--"}, args[i:]...)...)
			break
		}
		name := strings.SplitN(strings.TrimLeft(word, "-"), "=", 2)[0]
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			continue
		} // Cobra diagnoses unknown flags.
		if !strings.HasPrefix(word, "--") {
			args[i] = "-" + word
		}
		if flag.NoOptDefVal == "" && !strings.Contains(word, "=") {
			i++
		}
	}
	return args
}

func legacyAliasHelp(root *cobra.Command, args []string) bool {
	if len(args) < 2 {
		return false
	}
	cmd, aliasIndex := root, 0
	switch args[0] {
	case "send", "pair":
		cmd, _, _ = root.Find(args[:1])
		aliasIndex = 1
	case "devices", "unpair", "rename", "relay", "doctor", "ui", "help", "completion":
		return false
	}
	if len(args) <= aliasIndex || strings.HasPrefix(args[aliasIndex], "-") {
		return false
	}
	help := cmd.Flags().Lookup("help")
	return help != nil && help.Changed && help.Value.String() == "true"
}

// The socket protocol takes a stored alias. Resolve spelling at the CLI boundary
// so case-insensitive completion also works when an existing UI owns the send.
func canonicalDeviceAlias(dir, alias string) string {
	aliases, err := direct.ReadAliases(dir)
	if err == nil {
		for _, candidate := range aliases {
			if strings.EqualFold(candidate, alias) {
				return candidate
			}
		}
	}
	return alias
}
