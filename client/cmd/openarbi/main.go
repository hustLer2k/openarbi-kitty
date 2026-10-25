// OpenArbi runs native Codex in the current terminal.
// The cloud session outlives the terminal.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
	"github.com/hustLer2k/openarbi-kitty/client/internal/terminaltext"
	"golang.org/x/term"
)

func main() {
	if err := run(); err != nil {
		if _, safe := errors.AsType[*shareholderNotDispatchedError](err); safe {
			fmt.Fprintln(os.Stderr, shareholderNotDispatchedJSON)
			os.Exit(3)
		}
		fmt.Fprintln(os.Stderr, "OpenArbi:", terminaltext.Sanitize(err.Error()))
		os.Exit(1)
	}
}
func run() (resultErr error) {
	flags := flag.NewFlagSet("openarbi", flag.ContinueOnError)
	server := flags.String("server", "", "OpenArbi API origin (HTTPS outside localhost)")
	testToken := flags.Bool("test-token-env", false, "read OPENARBI_TEST_TOKEN instead of OS credentials (isolated testing only)")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: openarbi [flags] [create|rooms|attach|queue|stop|resume|members|shareholder|login|logout]\n\nWithout a subcommand, opens the native room launcher.\nQueue server-side follow-ups with: openarbi queue ROOM \"MESSAGE\".\nNative Codex Tab follow-ups are local to that client.\nLogin reads a token without echo and validates it before saving to the OS credential store.\nClosing the terminal leaves cloud work running.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	command := ""
	if flags.NArg() > 0 {
		command = flags.Arg(0)
	}
	// Every error before handing a shareholder request to its command is known
	// to precede publication, including missing OS credentials and invalid URL.
	shareholderPreflight := command == "shareholder"
	defer func() {
		if shareholderPreflight && resultErr != nil {
			resultErr = &shareholderNotDispatchedError{cause: resultErr}
		}
	}()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	vault := client.Credentials{}
	// Normalize the credential key so a trailing slash does not split logins.
	preferences, preferencesErr := client.UserPreferences()
	origin, err := client.ResolveServer(*server, os.Getenv("OPENARBI_URL"), preferences)
	if err != nil {
		return err
	}
	if preferencesErr != nil && *server == "" && os.Getenv("OPENARBI_URL") == "" {
		return preferencesErr
	}
	switch command {
	case "logout":
		if flags.NArg() != 1 {
			return errors.New("logout takes no arguments")
		}
		return vault.Delete(origin)
	case "login":
		if flags.NArg() != 1 {
			return errors.New("login takes no arguments")
		}
		fmt.Fprint(os.Stderr, "Access token: ")
		var token string
		if term.IsTerminal(int(os.Stdin.Fd())) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return errors.New("could not read token")
			}
			token = string(b)
			clear(b)
		} else {
			scanner := bufio.NewScanner(os.Stdin)
			scanner.Buffer(make([]byte, 1024), 65536)
			if !scanner.Scan() {
				return errors.New("could not read token from stdin")
			}
			token = strings.TrimSpace(scanner.Text())
		}
		api, err := client.New(origin, token, nil)
		if err != nil {
			return err
		}
		if err = saveLogin(ctx, api, origin, token, vault, preferences); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Signed in. Credentials saved in the OS store; API origin remembered.")
		return nil
	case "", "create", "rooms", "attach", "queue", "stop", "resume", "members", "shareholder":
	default:
		return fmt.Errorf("unknown subcommand %q", command)
	}
	var token string
	if *testToken {
		token = os.Getenv("OPENARBI_TEST_TOKEN")
		if token == "" {
			return errors.New("OPENARBI_TEST_TOKEN is required with --test-token-env")
		}
	} else {
		token, err = vault.Get(origin)
		if err != nil {
			return err
		}
	}
	api, err := client.New(origin, token, nil)
	if err != nil {
		return err
	}
	if command == "shareholder" {
		shareholderPreflight = false
		return runShareholder(ctx, api, flags.Args()[1:], os.Stdin, os.Stdout)
	}
	return runRooms(ctx, api, origin, command, flags.Args()[min(1, flags.NArg()):])
}

type credentialWriter interface{ Set(string, string) error }

func saveLogin(ctx context.Context, api *client.Client, origin, token string, vault credentialWriter, preferences client.Preferences) error {
	if _, err := api.Capabilities(ctx); err != nil {
		return err
	}
	if err := vault.Set(origin, token); err != nil {
		return err
	}
	if err := preferences.SaveServer(origin); err != nil {
		return fmt.Errorf("credentials saved, but the default API origin was not saved: %w", err)
	}
	return nil
}
