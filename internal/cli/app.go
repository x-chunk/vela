// Package cli implements the vela command line client.
//
// Every command talks to the server through the public Go client
// (go.xchunk.org/vela/client), so the CLI exercises exactly the API that
// other tools use. All process interaction (stdio, environment, terminals,
// external programs) goes through App so that commands can be tested
// in-process.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"go.xchunk.org/vela/client"
	"go.xchunk.org/vela/internal/cli/config"
	"go.xchunk.org/vela/internal/cli/output"
	"go.xchunk.org/vela/internal/version"
)

// App holds the process environment and the global flags.
type App struct {
	In        io.Reader
	Out       io.Writer
	Err       io.Writer
	LookupEnv func(string) (string, bool)
	// InIsTerminal, OutIsTerminal and ErrIsTerminal report whether the
	// standard streams are terminals.
	InIsTerminal  func() bool
	OutIsTerminal func() bool
	ErrIsTerminal func() bool
	// ReadSecret reads a line from the terminal without echo.
	ReadSecret func() (string, error)
	// Run starts an external program (browser opener, clipboard tool,
	// editor). stdin may be nil.
	Run func(ctx context.Context, stdin io.Reader, interactive bool, name string, args ...string) error
	// LookPath finds an external program.
	LookPath func(string) (string, error)
	Now      func() time.Time
	// HTTPClient overrides the HTTP client (tests).
	HTTPClient *http.Client
	// RetryBackoff overrides the client's retry delay (tests).
	RetryBackoff func(int) time.Duration

	flagProfile string
	flagURL     string
	flagConfig  string
	jsonOut     bool
	quiet       bool
	verbose     bool
	noColor     bool
	timeout     time.Duration

	printer *output.Printer
	stdin   *bufio.Reader
}

// NewApp returns an App wired to the real process.
func NewApp() *App {
	isTerm := func(f *os.File) func() bool {
		return func() bool { return term.IsTerminal(int(f.Fd())) } // #nosec G115 -- file descriptors fit in int
	}
	return &App{
		In:            os.Stdin,
		Out:           os.Stdout,
		Err:           os.Stderr,
		LookupEnv:     os.LookupEnv,
		InIsTerminal:  isTerm(os.Stdin),
		OutIsTerminal: isTerm(os.Stdout),
		ErrIsTerminal: isTerm(os.Stderr),
		ReadSecret: func() (string, error) {
			b, err := term.ReadPassword(int(os.Stdin.Fd())) // #nosec G115 -- file descriptors fit in int
			return string(b), err
		},
		Run:      runProgram,
		LookPath: exec.LookPath,
		Now:      time.Now,
	}
}

func runProgram(ctx context.Context, stdin io.Reader, interactive bool, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed tools or the user's own $EDITOR
	if interactive {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	} else {
		cmd.Stdin = stdin
	}
	return cmd.Run()
}

// Main runs the CLI with the process arguments and returns the exit code.
func Main(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return NewApp().Execute(ctx, args)
}

// Execute runs one command line and returns the exit code.
func (a *App) Execute(ctx context.Context, args []string) int {
	root := a.newRootCommand()
	root.SetArgs(args)
	root.SetIn(a.In)
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return exitOK
	}
	if a.printer == nil {
		a.printer = &output.Printer{Out: a.Out, Err: a.Err, JSON: a.jsonOut}
	}
	return a.reportError(err)
}

func (a *App) env(key string) string {
	v, _ := a.LookupEnv(key)
	return v
}

func (a *App) newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "vela",
		Short: "Manage files on a Vela store",
		Long: "vela manages files on a Vela store: upload, share, organize and delete them.\n\n" +
			"Remote paths are written without a scheme (edu/math/q1.pdf); a trailing slash\n" +
			"denotes a directory. Run \"vela login\" first.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			_, noColorEnv := a.LookupEnv("NO_COLOR")
			a.printer = &output.Printer{
				Out:   a.Out,
				Err:   a.Err,
				JSON:  a.jsonOut,
				Quiet: a.quiet || a.jsonOut,
				Color: !a.noColor && !noColorEnv && a.env("TERM") != "dumb" && a.OutIsTerminal(),
				Now:   a.Now,
			}
			return nil
		},
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError(err) })
	pf := root.PersistentFlags()
	pf.StringVar(&a.flagProfile, "profile", "", "config profile to use (env VELA_PROFILE)")
	pf.StringVar(&a.flagURL, "url", "", "server URL, overriding the profile (env VELA_URL)")
	pf.StringVar(&a.flagConfig, "config", "", "config file (env VELA_CONFIG; default $XDG_CONFIG_HOME/vela/config.toml)")
	pf.BoolVar(&a.jsonOut, "json", false, "machine-readable JSON output")
	pf.BoolVarP(&a.quiet, "quiet", "q", false, "print results only, no progress or hints")
	pf.BoolVarP(&a.verbose, "verbose", "v", false, "trace HTTP requests (without secrets)")
	pf.BoolVar(&a.noColor, "no-color", false, "disable colors (also NO_COLOR)")
	pf.DurationVar(&a.timeout, "timeout", 0, "overall timeout for the command, e.g. 30s (default none)")

	root.AddGroup(
		&cobra.Group{ID: "files", Title: "Files:"},
		&cobra.Group{ID: "share", Title: "Sharing:"},
		&cobra.Group{ID: "account", Title: "Account:"},
	)
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("files", a.lsCommand(), a.statCommand(), a.putCommand(), a.newCommand(), a.getCommand(), a.catCommand())
	add("share", a.urlCommand(), a.openCommand())
	add("account", a.loginCommand(), a.logoutCommand(), a.whoamiCommand(), a.versionCommand())
	root.SetHelpCommandGroupID("account")
	root.SetCompletionCommandGroupID("account")
	return root
}

// ctx returns the command context with the --timeout applied.
func (a *App) ctx(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	if a.timeout > 0 {
		return context.WithTimeout(cmd.Context(), a.timeout)
	}
	return context.WithCancel(cmd.Context())
}

func (a *App) configPath() (string, error) {
	if a.flagConfig != "" {
		return a.flagConfig, nil
	}
	if p := a.env("VELA_CONFIG"); p != "" {
		return p, nil
	}
	return config.DefaultPath()
}

func (a *App) loadConfig() (*config.File, string, error) {
	path, err := a.configPath()
	if err != nil {
		return nil, "", err
	}
	f, err := config.Load(path)
	return f, path, err
}

func (a *App) settings() (config.Settings, error) {
	f, _, err := a.loadConfig()
	if err != nil {
		return config.Settings{}, err
	}
	return config.Resolve(f, a.flagProfile, a.flagURL, a.LookupEnv)
}

// userAgent identifies the CLI to the server: vela-cli/<version> (<os>/<arch>).
func userAgent() string {
	return fmt.Sprintf("vela-cli/%s (%s/%s)", version.Get().Version, runtime.GOOS, runtime.GOARCH)
}

func (a *App) newClient(url, key string) (*client.Client, error) {
	hc := a.HTTPClient
	if hc == nil {
		hc = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 2 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   8,
			ForceAttemptHTTP2:     true,
		}}
	}
	opts := []client.Option{client.WithHTTPClient(hc), client.WithUserAgent(userAgent())}
	if a.verbose {
		opts = append(opts, client.WithTrace(a.Err))
	}
	if a.RetryBackoff != nil {
		opts = append(opts, client.WithBackoff(a.RetryBackoff))
	}
	return client.New(url, key, opts...)
}

// client returns a client for the resolved profile.
func (a *App) client() (*client.Client, config.Settings, error) {
	s, err := a.settings()
	if err != nil {
		return nil, s, err
	}
	if s.URL == "" || s.Key == "" {
		return nil, s, &cliError{code: exitAuth, slug: "not_logged_in",
			msg: "not logged in", hint: "run \"vela login\", or set VELA_URL and VELA_KEY"}
	}
	c, err := a.newClient(s.URL, s.Key)
	return c, s, err
}

// confirm asks a yes/no question on the terminal. Without a terminal it
// refuses rather than guessing: destructive commands in scripts must say -y.
func (a *App) confirm(question string, yes bool) error {
	if yes {
		return nil
	}
	if !a.InIsTerminal() {
		return usageError(fmt.Errorf("%s: confirmation required, but stdin is not a terminal; pass -y to confirm", question))
	}
	_, _ = fmt.Fprintf(a.Err, "%s [y/N] ", question)
	line, err := a.readLine()
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return &cliError{code: exitError, slug: "aborted", msg: "aborted"}
}

func (a *App) readLine() (string, error) {
	if a.stdin == nil {
		a.stdin = bufio.NewReader(a.In)
	}
	line, err := a.stdin.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
