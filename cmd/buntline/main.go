// Command buntline runs the harness: a browser UI served on localhost by
// default, or a headless one-shot run with -p.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/KonMam/buntline/internal/agent"
	"github.com/KonMam/buntline/internal/config"
	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/module/checkpoints"
	"github.com/KonMam/buntline/internal/module/commands"
	"github.com/KonMam/buntline/internal/module/core"
	"github.com/KonMam/buntline/internal/module/diagnostics"
	"github.com/KonMam/buntline/internal/module/files"
	gitmod "github.com/KonMam/buntline/internal/module/git"
	"github.com/KonMam/buntline/internal/module/hooks"
	"github.com/KonMam/buntline/internal/module/mcpclient"
	"github.com/KonMam/buntline/internal/module/memory"
	"github.com/KonMam/buntline/internal/module/ollama"
	"github.com/KonMam/buntline/internal/module/search"
	"github.com/KonMam/buntline/internal/module/subagents"
	"github.com/KonMam/buntline/internal/module/tasks"
	"github.com/KonMam/buntline/internal/module/vision"
	"github.com/KonMam/buntline/internal/module/webfetch"
	"github.com/KonMam/buntline/internal/module/worktrees"
	"github.com/KonMam/buntline/internal/provider"
	"github.com/KonMam/buntline/internal/secrets"
	"github.com/KonMam/buntline/internal/server"
	"github.com/KonMam/buntline/internal/session"
	"github.com/KonMam/buntline/internal/tools"
	"github.com/KonMam/buntline/web"
)

// version is stamped by the release build via
// -ldflags "-X main.version=v1.2.3"; source builds report "dev".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "buntline:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	var (
		prompt      = flag.String("p", "", "run headless: execute one prompt, stream events as JSONL to stdout, exit")
		autoApprove = flag.Bool("auto-approve", false, "headless only: approve side-effectful tools automatically (default: deny)")
		noOpen      = flag.Bool("no-open", false, "do not open the browser on startup")
		baseURL     = flag.String("base-url", cfg.BaseURL, "OpenAI-compatible API base URL")
		model       = flag.String("model", cfg.Model, "model name")
		addr        = flag.String("addr", cfg.Addr, "listen address for the UI server")
		workdir     = flag.String("workdir", "", "working directory the agent operates in (default: cwd)")
		dataDir     = flag.String("data-dir", cfg.DataDir, "directory for harness state (module toggles, checkpoints)")
		sessionsDir = flag.String("sessions-dir", cfg.SessionsDir, "directory for session storage")
		worktree    = flag.String("worktree", "", "create an isolated git worktree of the working directory and operate there; parallel sessions in one repo never collide")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println("buntline", version)
		return nil
	}
	cfg.BaseURL, cfg.Model, cfg.Addr = *baseURL, *model, *addr
	cfg.DataDir, cfg.SessionsDir = *dataDir, *sessionsDir
	cfg.Version = version

	if *workdir != "" {
		cfg.Workdir = *workdir
	}
	if cfg.Workdir == "" {
		cfg.Workdir, _ = os.Getwd()
	}
	if abs, err := resolveDir(cfg.Workdir); err != nil {
		return fmt.Errorf("workdir: %w", err)
	} else {
		cfg.Workdir = abs
	}

	// --worktree isolates this run in a fresh git worktree of the
	// working directory, so it cannot collide with other sessions in
	// the same repo. Works for both server and headless runs.
	if *worktree != "" {
		wt := &worktrees.Module{}
		path, _, err := wt.Create(context.Background(), cfg.Workdir, *worktree)
		if err != nil {
			return fmt.Errorf("worktree: %w", err)
		}
		fmt.Fprintf(os.Stderr, "buntline: working in worktree %s\n", path)
		cfg.Workdir = path
	}

	if !tools.RipgrepAvailable() {
		hint := "apt install ripgrep (or dnf/pacman equivalent)"
		if runtime.GOOS == "darwin" {
			hint = "brew install ripgrep"
		}
		fmt.Fprintln(os.Stderr, "warning: ripgrep (rg) not found; the grep tool will fail and glob will use a slower fallback. Install: "+hint)
	}

	if *prompt != "" {
		// Headless has no setup UI: with no explicit endpoint, fall back to
		// the app-managed default (the model starred in the Models view),
		// resolving its key like the server does: env first, then the
		// secrets store. Nothing resolved is a hard error, never a guess.
		if cfg.BaseURL == "" {
			prof, ok := config.AppDefaultProfile()
			if !ok {
				return fmt.Errorf("no model is configured: start the UI once to set one up, or pass -base-url and -model")
			}
			cfg.BaseURL, cfg.APIKey = prof.BaseURL, prof.APIKey
			if cfg.APIKey == "" && prof.KeyRef != "" {
				cfg.APIKey = secrets.Get(prof.KeyRef)
			}
			if cfg.Model == "" {
				cfg.Model = prof.Model
			}
		}
		prov := provider.NewOpenAICompat(cfg.BaseURL, cfg.APIKey)
		return runHeadless(cfg, prov, *prompt, *autoApprove)
	}
	return runServer(cfg, !*noOpen)
}

func runServer(cfg config.Config, open bool) error {
	store, err := session.NewStore(cfg.SessionsDir)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// srv and the files module reference each other (the module resolves
	// session workdirs through the server); wire via the lookup closure.
	var srv *server.Server
	lookup := func(id string) (string, error) { return srv.WorkdirFor(id) }
	registry, err := module.NewRegistry(
		filepath.Join(cfg.DataDir, "modules.json"),
		&files.Module{Lookup: lookup},
		ollama.New(cfg.BaseURL),
		&webfetch.Module{},
		&commands.Module{Lookup: lookup},
		&memory.Module{},
		&subagents.Module{},
		&hooks.Module{},
		checkpoints.New(cfg.DataDir, lookup),
		&diagnostics.Module{},
		&gitmod.Module{Lookup: lookup},
		&search.Module{Cfg: cfg.Search},
		&vision.Module{Cfg: cfg.Vision},
		&tasks.Module{},
		&worktrees.Module{Lookup: lookup},
		mcpclient.New(cfg.MCPServers),
	)
	if err != nil {
		return err
	}
	// Core modules are part of the harness itself, not toggleable
	// features: the tool surface the agent loop is built around (file
	// tools, bash, ask_user, session search) plus the subagents control
	// tools. The store shows them in a read-only section. Every module
	// above is a feature the user can switch off and unload.
	registry.Core(
		&core.Files{},
		&core.Bash{},
		&core.Search{},
		&core.Ask{},
		&core.SessionSearch{},
	)
	srv = server.New(cfg, store, web.Handler(), registry, log)

	url := "http://" + cfg.Addr
	log.Info("buntline listening", "url", url, "model", cfg.Model, "base_url", cfg.BaseURL, "workdir", cfg.Workdir)
	if open {
		openBrowser(url)
	}

	// Graceful exit: background bash tasks run in their own process
	// groups, so they would outlive an unhandled termination. Tear the
	// sessions down before leaving.
	httpSrv := &http.Server{Addr: cfg.Addr, Handler: srv.Handler()}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	select {
	case err := <-errCh:
		srv.Shutdown()
		return err
	case <-ctx.Done():
		srv.Shutdown()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

// policyApprover implements headless approval policy: deny by default,
// allow everything with -auto-approve. Either way the decision lands in the
// stream like any other.
type policyApprover struct{ allow bool }

func (p policyApprover) RequestApproval(context.Context, agent.ApprovalRequest) (agent.Decision, error) {
	if p.allow {
		return agent.DecisionAllow, nil
	}
	return agent.DecisionDeny, nil
}

func runHeadless(cfg config.Config, prov provider.Provider, prompt string, autoApprove bool) error {
	enc := json.NewEncoder(os.Stdout)
	hooksMod := &hooks.Module{}
	a := agent.New(agent.Config{
		Provider: prov,
		Model:    cfg.Model,
		Tools:    tools.Default(cfg.Workdir),
		Approver: policyApprover{allow: autoApprove},
		// Hooks apply in headless too: a policy that only holds when a
		// human is watching is not a policy.
		Interceptors: []agent.ToolInterceptor{hooksMod.Interceptor("headless", cfg.Workdir)},
		SystemPrompt: config.SystemPrompt(cfg.Workdir),
		Emit: func(ev agent.Event) {
			// Deltas are noise in a pipeline; the message events carry
			// the full content.
			if ev.Type == agent.EventTextDelta || ev.Type == agent.EventThinkingDelta {
				return
			}
			_ = enc.Encode(ev)
		},
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.Run(ctx, prompt)
}

// resolveDir returns the absolute, symlink-resolved path so tool path
// confinement compares real paths (macOS: /tmp and friends are symlinks
// into /private).
func resolveDir(p string) (string, error) {
	info, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	_ = cmd.Start()
}
