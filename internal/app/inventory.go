package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/term"
	"io"
	"os"
	"strings"

	"github.com/buttercrab/up/internal/inventory"
	"github.com/buttercrab/up/internal/report"
	"github.com/buttercrab/up/internal/updaters"
)

func runInventory(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("up inventory", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "emit the versioned inventory as JSON")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: up inventory [--json]\n\nInteractive shortcut to Inventory inside up. Use plain up for the main menu.\nWith --json or redirected output, export current-PATH metadata for scripts.\nInstalled package lists and update availability are not checked.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "up inventory: expected no positional arguments")
		return 2
	}
	if !*asJSON && isTerminal(stdout) && term.IsTerminal(int(os.Stdin.Fd())) {
		return runShell(ctx, stdout, stderr, shellOptions{jobs: 4}, true)
	}
	result, err := updaters.Inventory(ctx)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		fmt.Fprintf(stderr, "up inventory: %v\n", err)
		if ctx.Err() != nil {
			return 130
		}
		return 1
	}
	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(result)
	} else {
		_, err = io.WriteString(stdout, inventoryReport(result))
	}
	if err != nil {
		fmt.Fprintf(stderr, "up inventory: write report: %v\n", err)
		return 1
	}
	return 0
}

func inventoryReport(r inventory.Report) string {
	var text strings.Builder
	fmt.Fprintln(&text, "Installation inventory (current PATH)")
	counts := map[inventory.Support]int{}
	for _, item := range r.Installations {
		counts[item.UpdateSupport]++
	}
	fmt.Fprintf(&text, "%d supported · %d without an updater · %d blocked\n", counts[inventory.Supported], counts[inventory.Unsupported], counts[inventory.Blocked])
	for _, item := range r.Installations {
		support := string(item.UpdateSupport)
		if item.UpdateSupport == inventory.Unsupported {
			support = "not supported"
		}
		fmt.Fprintf(&text, "\n%s (%s): %s\n", inventoryCell(item.Name), inventoryCell(item.ID), support)
		fmt.Fprintf(&text, "  Executable: %s\n", inventoryCell(item.Executable))
		owner := item.Owner
		if owner == "" {
			owner = "unknown"
		}
		fmt.Fprintf(&text, "  Owner: %s\n", inventoryCell(owner))
		if item.PrefixKnown {
			fmt.Fprintf(&text, "  Prefix: %s\n", inventoryCell(item.Prefix))
		}
		if item.PlannedExecutable != "" && item.PlannedExecutable != item.Executable {
			fmt.Fprintf(&text, "  Upgrade entry point: %s\n", inventoryCell(item.PlannedExecutable))
		}
		for _, runtime := range item.Runtimes {
			fmt.Fprintf(&text, "  Runtime (%s): %s\n", inventoryCell(runtime.ID), inventoryCell(runtime.Executable))
		}
		if item.Reason != "" {
			fmt.Fprintf(&text, "  %s\n", inventoryCell(item.Reason))
		}
	}
	if len(r.Missing) > 0 {
		var names []string
		for _, item := range r.Missing {
			names = append(names, inventoryCell(item.ID))
		}
		fmt.Fprintf(&text, "\nNot found on PATH: %s\n", strings.Join(names, ", "))
	}
	fmt.Fprintln(&text, "\nInstalled state only; update availability was not checked.")
	return text.String()
}

func inventoryCell(s string) string {
	return strings.NewReplacer("\t", "\\t", "\r", "\\r", "\n", "\\n").Replace(report.Text(s))
}
