// Command quecto-harness serves a local coding agent that talks to a model
// server you host. It is one binary with the web UI compiled in.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"quecto-harness/internal/agent"
	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
	"quecto-harness/internal/perms"
	"quecto-harness/internal/server"
	"quecto-harness/internal/service"
	"quecto-harness/internal/store"
)

// version, commit and date are set with -ldflags at build time.
var (
	version = "0.1.0"
	commit  = "none"
	date    = "unknown"
)

func main() {
	log.SetFlags(log.LstdFlags)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Quecto-harness: "+err.Error())
		os.Exit(1)
	}
}

func run(argv []string) error {
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") {
		switch argv[0] {
		case "version":
			fmt.Printf("quecto-harness %s (%s, built %s, %s)\n", version, commit, date, runtime.Version())
			return nil
		case "open":
			return openSaved(argv[1:])
		case "service":
			return serviceCmd(argv[1:])
		default:
			return fmt.Errorf("No command %q: try serve, open, service or version", argv[0])
		}
	}
	return serve(argv)
}

type flags struct {
	listen string
	noOpen bool
	home   string
}

func parseFlags(name string, argv []string) (flags, error) {
	var f flags
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&f.listen, "listen", "", "Host:port to bind, overriding config.json for this process")
	fs.BoolVar(&f.noOpen, "no-open", false, "Do not open a browser after binding")
	fs.StringVar(&f.home, "home", os.Getenv("QUECTO_HARNESS_HOME"), "State directory, default ~/.quecto-harness")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		return f, err
	}
	return f, nil
}

const usage = `quecto-harness: A local coding agent that talks to a model server you host.

Usage:
  quecto-harness [flags]                  Serve
  quecto-harness open                     Open the browser at the saved URL
  quecto-harness service install [flags]  Write and load the OS unit
  quecto-harness service uninstall        Unload and remove the OS unit
  quecto-harness service status           Report unit presence, run state, URL
  quecto-harness version                  Print version, commit, Go version

Flags:
`

func paths(home string) (config.Paths, error) {
	if home != "" {
		return config.DiscoverPathsIn(home), nil
	}
	return config.DiscoverPaths()
}

func serve(argv []string) error {
	f, err := parseFlags("quecto-harness", argv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	p, err := paths(f.home)
	if err != nil {
		return err
	}
	if err := p.EnsureDirs(); err != nil {
		return err
	}
	cfg, err := config.Open(p)
	if err != nil {
		return err
	}
	for _, note := range cfg.Notes() {
		fmt.Fprintln(os.Stderr, "Config.json: "+note)
	}

	sessions, err := store.Open(p.Sessions)
	if err != nil {
		return err
	}
	blobs, err := store.OpenFiles(p.Files)
	if err != nil {
		return err
	}
	pastes, err := store.OpenUploads(p.Uploads)
	if err != nil {
		return err
	}
	token, err := loadToken(p.Token)
	if err != nil {
		return err
	}

	listen := cfg.Config().Listen
	if f.listen != "" {
		listen = f.listen
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("Listen on %s: %s: another process may be using the port, pass --listen to choose another", listen, err)
	}
	defer ln.Close()
	if !isLoopback(listen) {
		fmt.Fprintf(os.Stderr, "Listening on %s: the session token is the only access control, bind to 127.0.0.1 unless you meant this\n", listen)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := llm.New(cfg.Config().ServerURL, cfg.Config().ServerToken)
	hub := server.NewHub()
	broker := perms.New(cfg, func(sessionID string, r perms.Request) {
		hub.Publish(sessionID, agent.PermissionEvent(r))
	})
	engine := agent.NewEngine(ctx, cfg, sessions, blobs, client, broker, hub)
	srv := server.New(cfg, sessions, blobs, pastes, engine, broker, client, hub, token, listen, version)

	go srv.Detect(ctx)

	url := "http://" + displayHost(listen) + "/?token=" + token
	if err := config.WriteAtomic(p.URL, []byte(url+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Println(url)
	if !f.noOpen {
		if err := OpenBrowser(url); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
		}
	}

	httpSrv := &http.Server{Handler: srv.Handler()}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}

	stop()
	broker.CancelAll()
	engine.Wait(3 * time.Second)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutdownCtx)
	if err := os.Remove(p.URL); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("Remove %s: %v", p.URL, err)
	}
	return nil
}

// loadToken reuses the token on disk so a tab left open across a restart keeps
// working. Deleting the file is the only way to rotate it.
func loadToken(path string) (string, error) {
	if raw, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(raw)); t != "" {
			return t, nil
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("Generate a session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	if err := config.WriteAtomic(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func isLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// displayHost turns a wildcard bind into an address a browser can open.
func displayHost(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return "127.0.0.1:" + port
	}
	return net.JoinHostPort(host, port)
}

// OpenBrowser starts the platform opener and never waits on it.
func OpenBrowser(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, url)
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		cmd.Stdout, cmd.Stderr = devnull, devnull
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("Open a browser: %s not found: visit %s yourself", name, url)
	}
	go cmd.Wait()
	return nil
}

func openSaved(argv []string) error {
	f, err := parseFlags("quecto-harness open", argv)
	if err != nil {
		return err
	}
	p, err := paths(f.home)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(p.URL)
	if err != nil {
		return fmt.Errorf("No saved URL at %s: start quecto-harness first, or check service status", p.URL)
	}
	return OpenBrowser(strings.TrimSpace(string(raw)))
}

func serviceCmd(argv []string) error {
	if len(argv) == 0 {
		return errors.New("Service needs a subcommand: install, uninstall or status")
	}
	sub, rest := argv[0], argv[1:]
	f, err := parseFlags("quecto-harness service "+sub, rest)
	if err != nil {
		return err
	}
	p, err := paths(f.home)
	if err != nil {
		return err
	}
	switch sub {
	case "install":
		if err := p.EnsureDirs(); err != nil {
			return err
		}
		spec, err := service.NewSpec(p.Home, f.listen)
		if err != nil {
			return err
		}
		if err := service.Install(spec); err != nil {
			return err
		}
		if runtime.GOOS == "linux" {
			fmt.Println("If you want this running without a login session: loginctl enable-linger $USER")
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if raw, err := os.ReadFile(p.URL); err == nil {
				fmt.Printf("Installed and running, open %s\n", strings.TrimSpace(string(raw)))
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		fmt.Printf("Installed, but no URL appeared: check %s/harness.err\n", p.Log)
		return nil
	case "uninstall":
		if err := service.Uninstall(p.Home); err != nil {
			return err
		}
		fmt.Printf("Removed the unit. Config, token and sessions are still in %s\n", p.Home)
		return nil
	case "status":
		st, err := service.Report(p.Home)
		if err != nil {
			return err
		}
		fmt.Printf("Unit      %s\n", st.Unit)
		state := "Not installed"
		switch {
		case st.Running:
			state = "Running"
			if st.Detail != "" {
				state = "Running (" + st.Detail + ")"
			}
		case st.Installed:
			state = "Installed, not running"
		}
		fmt.Printf("State     %s\n", state)
		fmt.Printf("URL       %s\n", st.URL)
		fmt.Printf("Token     %s (delete to rotate)\n", p.Token)
		fmt.Printf("Logs      %s/harness.log\n", p.Log)
		switch {
		case st.Running:
			os.Exit(0)
		case st.Installed:
			os.Exit(3)
		default:
			os.Exit(4)
		}
		return nil
	}
	return fmt.Errorf("No service subcommand %q: try install, uninstall or status", sub)
}
