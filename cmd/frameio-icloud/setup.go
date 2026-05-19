package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"

	"github.com/nutgood/frameio-icloud-relay/internal/config"
	"github.com/nutgood/frameio-icloud-relay/internal/frameio"
	"github.com/nutgood/frameio-icloud-relay/internal/launchd"
	"github.com/nutgood/frameio-icloud-relay/internal/paths"
	"github.com/nutgood/frameio-icloud-relay/internal/photos"
	"github.com/nutgood/frameio-icloud-relay/internal/pushover"
)

// runSetup is the interactive end-to-end onboarding flow. Everything a
// brand-new user needs to do to get a working LaunchAgent on this Mac
// happens inside this function: OAuth, hierarchy pick, optional public
// URL, optional Pushover, Photos.app permission check, LaunchAgent
// install. Each step is a huh form so the wizard composes cleanly.
//
// Ctrl+C exits cleanly at any prompt — partial config is preserved
// (anything we wrote before the abort survives in config.json).
func runSetup(_ []string) {
	p, err := paths.Default()
	if err != nil {
		exitf("paths: %v", err)
	}
	if err := p.EnsureDirs(); err != nil {
		exitf("ensure dirs: %v", err)
	}
	cfg, err := config.Load(p.Config)
	if err != nil {
		exitf("config: %v", err)
	}

	if err := step1Welcome(p); err != nil {
		exitWizard(err)
	}
	if err := step2Auth(p); err != nil {
		exitWizard(err)
	}
	if err := step3Hierarchy(p, cfg); err != nil {
		exitWizard(err)
	}
	if err := step4PublicURL(p, cfg); err != nil {
		exitWizard(err)
	}
	if err := step5Pushover(p, cfg); err != nil {
		exitWizard(err)
	}
	if err := step6PhotosCheck(); err != nil {
		exitWizard(err)
	}
	if err := step7Install(p); err != nil {
		exitWizard(err)
	}
	step8Summary(p, cfg)
}

// step1Welcome introduces the wizard. Single Note with a "let's go"
// button; lets the user back out cleanly before any side effects.
func step1Welcome(p *paths.Paths) error {
	return huh.NewForm(huh.NewGroup(
		huh.NewNote().
			Title("frameio-icloud setup").
			Description(strings.Join([]string{
				"This wizard configures the relay on this Mac:",
				"",
				"  1. Authenticate with Frame.io (Adobe IMS OAuth)",
				"  2. Pick your account / workspace / project",
				"  3. (Optional) configure a public webhook URL",
				"  4. (Optional) configure Pushover notifications",
				"  5. Verify Photos.app integration",
				"  6. Install the LaunchAgent so the service runs on login",
				"",
				"Config will be written to:",
				"  " + p.Config,
				"",
				"Press Enter to continue, Ctrl+C to quit.",
			}, "\n")).
			Next(true).
			NextLabel("Begin"),
	)).Run()
}

// step2Auth handles the OAuth bit: detect existing valid tokens and
// offer to skip; otherwise prompt for Adobe Developer Console
// credentials, kick off the browser flow, and wait for the callback.
func step2Auth(p *paths.Paths) error {
	existing, _ := frameio.LoadTokenStore(p.Tokens)
	hasTokens := existing != nil && existing.RefreshToken != "" && existing.ClientID != ""

	reAuth := !hasTokens
	if hasTokens {
		if err := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title("Existing Frame.io tokens found").
				Description(fmt.Sprintf("Tokens for client_id=%s are already saved at %s.\nRe-authenticate, or keep these?", obscure(existing.ClientID), p.Tokens)).
				Affirmative("Re-authenticate").
				Negative("Keep existing").
				Value(&reAuth),
		)).Run(); err != nil {
			return err
		}
	}
	if !reAuth {
		return nil
	}

	var clientID, clientSecret string
	clientID = os.Getenv("FRAMEIO_CLIENT_ID")
	clientSecret = os.Getenv("FRAMEIO_CLIENT_SECRET")
	if err := huh.NewForm(huh.NewGroup(
		huh.NewNote().
			Title("Adobe Developer Console").
			Description(strings.Join([]string{
				"You need an OAuth Web App credential from:",
				"  https://developer.adobe.com/console",
				"",
				"Create a project, Add API → Frame.io V4 API, pick OAuth Web App,",
				"and set the redirect URI to:",
				"  https://localhost:12345/callback",
				"",
				"Copy the Client ID and Client Secret into the next two fields.",
			}, "\n")).
			Next(true).
			NextLabel("Got it"),
		huh.NewInput().
			Title("Client ID").
			Value(&clientID).
			Validate(nonEmpty("client ID")),
		huh.NewInput().
			Title("Client Secret").
			EchoMode(huh.EchoModePassword).
			Value(&clientSecret).
			Validate(nonEmpty("client secret")),
	)).Run(); err != nil {
		return err
	}

	// Launch the local HTTPS callback server + open the browser.
	authURL, done, shutdown, err := startOAuthCallback(p, clientID, clientSecret, defaultOAuthPort)
	if err != nil {
		return err
	}
	defer shutdown()

	if err := huh.NewForm(huh.NewGroup(
		huh.NewNote().
			Title("Browser authentication").
			Description(strings.Join([]string{
				"We've opened your default browser to the Adobe sign-in URL.",
				"If it didn't open automatically, paste this URL manually:",
				"",
				"  " + authURL,
				"",
				"After you grant access, the browser will redirect to a localhost",
				"URL using a self-signed cert. Click through the browser warning",
				"the first time — the cert is generated fresh and discarded on exit.",
				"",
				"Press Enter once you've completed the browser sign-in (we'll wait",
				"for the callback to land if you press it too early).",
			}, "\n")).
			Next(true).
			NextLabel("I've signed in"),
	)).Run(); err != nil {
		return err
	}

	// Background-launch the browser. Best-effort — if `open` fails the
	// user has the URL on screen and can paste it manually.
	_ = exec.Command("open", authURL).Start()

	var oauthErr error
	if err := spinner.New().
		Title("Waiting for browser callback…").
		Action(func() {
			select {
			case oauthErr = <-done:
			case <-time.After(5 * time.Minute):
				oauthErr = errors.New("oauth callback timeout after 5 minutes — re-run `frameio-icloud setup`")
			}
		}).
		Run(); err != nil {
		return err
	}
	if oauthErr != nil {
		return fmt.Errorf("oauth: %w", oauthErr)
	}
	return nil
}

// step3Hierarchy lists accounts/workspaces/projects and asks the user
// to pick one of each (or auto-picks if there's only one). Writes the
// resulting IDs to config.
func step3Hierarchy(p *paths.Paths, cfg *config.Config) error {
	store, err := frameio.LoadTokenStore(p.Tokens)
	if err != nil {
		return fmt.Errorf("tokens: %w", err)
	}

	var h Hierarchy
	if err := spinner.New().
		Title("Discovering Frame.io accounts / workspaces / projects…").
		Action(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			h, err = gatherHierarchy(ctx, store)
		}).
		Run(); err != nil {
		return err
	}
	if err != nil {
		return err
	}
	if len(h.Accounts) == 0 {
		return errors.New("no Frame.io accounts on this user — check your Adobe project access")
	}

	// Account.
	var accountID string
	if len(h.Accounts) == 1 {
		accountID = h.Accounts[0].ID
		_ = noteAuto("Account", h.Accounts[0].DisplayName, accountID).Run()
	} else {
		opts := make([]huh.Option[string], 0, len(h.Accounts))
		for _, a := range h.Accounts {
			opts = append(opts, huh.NewOption(a.DisplayName+"  ("+shortID(a.ID)+")", a.ID))
		}
		if err := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Pick a Frame.io account").
				Options(opts...).
				Value(&accountID),
		)).Run(); err != nil {
			return err
		}
	}
	acct := findAccount(h, accountID)

	// Workspace.
	if len(acct.Workspaces) == 0 {
		return fmt.Errorf("account %s has no workspaces", acct.DisplayName)
	}
	var workspaceID string
	if len(acct.Workspaces) == 1 {
		workspaceID = acct.Workspaces[0].ID
		_ = noteAuto("Workspace", acct.Workspaces[0].Name, workspaceID).Run()
	} else {
		opts := make([]huh.Option[string], 0, len(acct.Workspaces))
		for _, w := range acct.Workspaces {
			opts = append(opts, huh.NewOption(w.Name+"  ("+shortID(w.ID)+")", w.ID))
		}
		if err := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Pick a workspace").
				Options(opts...).
				Value(&workspaceID),
		)).Run(); err != nil {
			return err
		}
	}
	ws := findWorkspace(acct, workspaceID)

	// Project (we store its root folder ID).
	if len(ws.Projects) == 0 {
		return fmt.Errorf("workspace %s has no projects (create a Camera-to-Cloud project in Frame.io first)", ws.Name)
	}
	var folderID string
	if len(ws.Projects) == 1 {
		folderID = ws.Projects[0].RootFolderID
		_ = noteAuto("Project", ws.Projects[0].Name, folderID).Run()
	} else {
		opts := make([]huh.Option[string], 0, len(ws.Projects))
		for _, prj := range ws.Projects {
			opts = append(opts, huh.NewOption(prj.Name+"  ("+shortID(prj.ID)+")", prj.RootFolderID))
		}
		if err := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Pick the project to relay from").
				Description("This is the project where camera uploads land.").
				Options(opts...).
				Value(&folderID),
		)).Run(); err != nil {
			return err
		}
	}

	cfg.FrameioAccount = accountID
	cfg.FrameioWorkspace = workspaceID
	cfg.FrameioFolder = folderID
	return config.Save(p.Config, cfg)
}

// step4PublicURL asks whether webhook-driven mode should be enabled,
// and if so, captures the public HTTPS URL. Polling-only mode is the
// natural default — works without any inbound network exposure.
func step4PublicURL(p *paths.Paths, cfg *config.Config) error {
	enable := cfg.PublicURL != ""
	if err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Webhook delivery (sub-second latency)").
			Description(strings.Join([]string{
				"If this Mac has a public HTTPS URL routing to it (Tailscale Funnel,",
				"Cloudflare Tunnel, ngrok, etc.), Frame.io can push events the moment",
				"each upload completes. Skip to run in polling-only mode (a reconcile",
				"pass every 60s).",
				"",
				"You can change this later with `frameio-icloud config set public_url …`.",
			}, "\n")).
			Affirmative("Configure URL").
			Negative("Skip (polling-only)").
			Value(&enable),
	)).Run(); err != nil {
		return err
	}
	if !enable {
		cfg.PublicURL = ""
		return config.Save(p.Config, cfg)
	}
	url := cfg.PublicURL
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Public webhook URL").
			Description("Must be HTTPS and route to this Mac's :9000/webhook.").
			Placeholder("https://your-tunnel.example.com/webhook").
			Value(&url).
			Validate(func(s string) error {
				s = strings.TrimSpace(s)
				if !strings.HasPrefix(s, "https://") {
					return errors.New("must start with https://")
				}
				if !strings.HasSuffix(s, "/webhook") {
					return errors.New("must end with /webhook")
				}
				return nil
			}),
	)).Run(); err != nil {
		return err
	}
	cfg.PublicURL = strings.TrimSpace(url)
	return config.Save(p.Config, cfg)
}

// step5Pushover optionally captures Pushover credentials and offers an
// inline test push so the user knows they entered them right.
func step5Pushover(p *paths.Paths, cfg *config.Config) error {
	enable := cfg.PushoverToken != "" && cfg.PushoverUserKey != ""
	if err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Pushover notifications").
			Description(strings.Join([]string{
				"The relay coalesces upload events into burst notifications:",
				"  · 'Received webhook, importing photos…' when a burst starts",
				"  · 'Imported N pictures' 30s after the burst ends",
				"  · Operator-visible errors push immediately",
				"",
				"You'll need an application token (from pushover.net/apps/build) and",
				"your user key (from pushover.net/).",
			}, "\n")).
			Affirmative("Configure Pushover").
			Negative("Skip").
			Value(&enable),
	)).Run(); err != nil {
		return err
	}
	if !enable {
		cfg.PushoverToken = ""
		cfg.PushoverUserKey = ""
		return config.Save(p.Config, cfg)
	}
	token := cfg.PushoverToken
	userKey := cfg.PushoverUserKey
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Pushover application token").
			EchoMode(huh.EchoModePassword).
			Value(&token).
			Validate(nonEmpty("app token")),
		huh.NewInput().
			Title("Pushover user key").
			EchoMode(huh.EchoModePassword).
			Value(&userKey).
			Validate(nonEmpty("user key")),
	)).Run(); err != nil {
		return err
	}
	cfg.PushoverToken = strings.TrimSpace(token)
	cfg.PushoverUserKey = strings.TrimSpace(userKey)
	if err := config.Save(p.Config, cfg); err != nil {
		return err
	}

	var sendTest bool
	if err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Send a test push now?").
			Description("Verifies the credentials end-to-end. You should see the push on your devices within a few seconds.").
			Affirmative("Send").
			Negative("Skip").
			Value(&sendTest),
	)).Run(); err != nil {
		return err
	}
	if !sendTest {
		return nil
	}
	var sendErr error
	if err := spinner.New().
		Title("Sending test push…").
		Action(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sendErr = pushover.New(cfg.PushoverToken, cfg.PushoverUserKey).
				Send(ctx, pushover.Message{Title: "Frame.io", Body: "Setup wizard: test push (you can ignore this)"})
		}).
		Run(); err != nil {
		return err
	}
	result := "✅ test push sent — check your devices."
	if sendErr != nil {
		result = "⚠️  Pushover rejected the credentials:\n  " + sendErr.Error() + "\n\nYou can fix this later with `frameio-icloud config set pushover.token …`."
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Pushover").Description(result).Next(true),
	)).Run()
}

// step6PhotosCheck runs the Photos.app reachability probe and, if it
// passes, imports a tiny test PNG so macOS surfaces the Automation-
// permission prompt now rather than on the first real upload.
func step6PhotosCheck() error {
	var doCheck bool = true
	if err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Verify Photos.app integration").
			Description(strings.Join([]string{
				"We'll import a tiny test image into Photos.app via AppleScript.",
				"macOS will prompt for Automation permission the first time — allow",
				"it, or imports will fail until you do.",
				"",
				"You can delete the test image from Photos afterwards.",
			}, "\n")).
			Affirmative("Run check").
			Negative("Skip").
			Value(&doCheck),
	)).Run(); err != nil {
		return err
	}
	if !doCheck {
		return nil
	}
	importer := photos.New()
	var probeErr error
	if err := spinner.New().
		Title("Pinging Photos.app…").
		Action(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			probeErr = importer.Check(ctx)
		}).
		Run(); err != nil {
		return err
	}
	if probeErr != nil {
		return huh.NewForm(huh.NewGroup(
			huh.NewNote().
				Title("Photos.app probe failed").
				Description("Error:\n  " + probeErr.Error() + "\n\nRun `frameio-icloud test-photos` after granting Automation permission.").
				Next(true),
		)).Run()
	}
	tmp, err := writeTestPNG()
	if err != nil {
		return err
	}
	defer os.Remove(tmp)

	var importErr error
	if err := spinner.New().
		Title("Importing test image into Photos.app…").
		Action(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			importErr = importer.Import(ctx, tmp)
		}).
		Run(); err != nil {
		return err
	}
	msg := "✅ Imported test image into Photos.app. iCloud Photos sync will upload it shortly."
	if importErr != nil {
		msg = "⚠️  Import failed:\n  " + importErr.Error() + "\n\nLikely cause: Automation permission not granted yet.\nFix in System Settings → Privacy & Security → Automation."
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Photos.app").Description(msg).Next(true),
	)).Run()
}

// step7Install offers to install the LaunchAgent. Skipping is fine —
// the user can run `frameio-icloud install` later.
func step7Install(p *paths.Paths) error {
	var doInstall bool = true
	if err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Install the LaunchAgent").
			Description(strings.Join([]string{
				"Copies this binary to:",
				"  " + p.InstalledBinary,
				"Writes the plist to:",
				"  " + p.Plist,
				"Bootstraps it into your gui/$UID domain so it starts on every login.",
				"",
				"Skip to install later with `frameio-icloud install`.",
			}, "\n")).
			Affirmative("Install now").
			Negative("Skip").
			Value(&doInstall),
	)).Run(); err != nil {
		return err
	}
	if !doInstall {
		return nil
	}
	src, err := os.Executable()
	if err != nil {
		return err
	}
	var installErr error
	if err := spinner.New().
		Title("Installing LaunchAgent…").
		Action(func() {
			installErr = launchd.Install(p, src)
		}).
		Run(); err != nil {
		return err
	}
	if installErr != nil {
		return installErr
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewNote().
			Title("Installed").
			Description("LaunchAgent loaded. The service is running.\nUse `frameio-icloud status` to verify.").
			Next(true),
	)).Run()
}

// step8Summary prints a non-interactive recap. Not a huh form — by this
// point we want plain output that survives in the user's terminal
// scrollback. Includes anything they'll need to copy/paste later.
func step8Summary(p *paths.Paths, cfg *config.Config) {
	fmt.Println()
	fmt.Println("Setup complete.")
	fmt.Println()
	fmt.Printf("  Config:    %s\n", p.Config)
	fmt.Printf("  Tokens:    %s\n", p.Tokens)
	fmt.Printf("  Logs:      %s\n", p.LogOut)
	fmt.Printf("  Socket:    %s\n", p.Socket)
	fmt.Println()
	fmt.Printf("  Frame.io account:   %s\n", cfg.FrameioAccount)
	fmt.Printf("  Frame.io workspace: %s\n", cfg.FrameioWorkspace)
	fmt.Printf("  Frame.io folder:    %s\n", cfg.FrameioFolder)
	if cfg.PublicURL != "" {
		fmt.Printf("  Webhook URL:        %s\n", cfg.PublicURL)
	} else {
		fmt.Println("  Webhook URL:        (polling-only mode)")
	}
	if cfg.PushoverToken != "" {
		fmt.Println("  Pushover:           configured")
	} else {
		fmt.Println("  Pushover:           disabled")
	}
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  frameio-icloud status   # confirm the service is alive")
	fmt.Println("  frameio-icloud logs     # tail service output")
}

// ----------------------------------------------------------------------
// OAuth callback machinery for the wizard.
//
// startOAuthCallback returns:
//   - the authorize URL to show the user
//   - a one-shot channel that fires with the OAuth result (nil = success)
//   - a shutdown func the caller MUST defer to free the listener
//
// We deliberately don't block — the wizard renders the URL via huh,
// then awaits the channel inside a spinner.
// ----------------------------------------------------------------------

func startOAuthCallback(p *paths.Paths, clientID, clientSecret string, port int) (string, <-chan error, func(), error) {
	redirectURI := fmt.Sprintf("https://localhost:%d/callback", port)
	store, err := frameio.LoadTokenStore(p.Tokens)
	if err != nil {
		return "", nil, nil, fmt.Errorf("load tokens: %w", err)
	}
	store.ClientID = clientID
	store.ClientSecret = clientSecret
	store.RedirectURI = redirectURI
	if err := store.Save(); err != nil {
		return "", nil, nil, fmt.Errorf("save tokens: %w", err)
	}

	stateBytes := make([]byte, 16)
	_, _ = rand.Read(stateBytes)
	stateTok := hex.EncodeToString(stateBytes)
	authURL := store.AuthorizeURL(stateTok, strings.Fields(defaultOAuthScopes))

	done := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			err := fmt.Errorf("%s: %s", e, q.Get("error_description"))
			http.Error(w, err.Error(), http.StatusBadRequest)
			done <- err
			return
		}
		if q.Get("state") != stateTok {
			err := errors.New("state mismatch")
			http.Error(w, err.Error(), http.StatusBadRequest)
			done <- err
			return
		}
		code := q.Get("code")
		if code == "" {
			err := errors.New("no code in callback")
			http.Error(w, err.Error(), http.StatusBadRequest)
			done <- err
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := store.ExchangeCode(ctx, code); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			done <- err
			return
		}
		fmt.Fprintln(w, "OK — tokens saved. You can close this tab and return to the terminal.")
		done <- nil
	})

	tlsCert, err := selfSignedLoopbackCert()
	if err != nil {
		return "", nil, nil, fmt.Errorf("self-signed cert: %w", err)
	}
	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{tlsCert}},
	}
	go func() {
		if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			// non-fatal: if the listener died, the spinner will time
			// out and the wizard will report it.
			select {
			case done <- fmt.Errorf("listen: %w", err):
			default:
			}
		}
	}()
	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	return authURL, done, shutdown, nil
}

// ----------------------------------------------------------------------
// Small helpers.
// ----------------------------------------------------------------------

func noteAuto(kind, name, id string) *huh.Form {
	return huh.NewForm(huh.NewGroup(
		huh.NewNote().
			Title(fmt.Sprintf("Auto-selected %s", kind)).
			Description(fmt.Sprintf("%s\n  id: %s\n\nOnly one available; nothing to pick.", name, id)).
			Next(true),
	))
}

func findAccount(h Hierarchy, id string) Account {
	for _, a := range h.Accounts {
		if a.ID == id {
			return a
		}
	}
	return Account{}
}

func findWorkspace(a Account, id string) Workspace {
	for _, w := range a.Workspaces {
		if w.ID == id {
			return w
		}
	}
	return Workspace{}
}

func nonEmpty(field string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New(field + " is required")
		}
		return nil
	}
}

// shortID truncates a UUID to its first segment for friendlier picker
// labels — full IDs end up in config so the abbreviation never hurts.
func shortID(id string) string {
	if i := strings.Index(id, "-"); i > 0 {
		return id[:i] + "…"
	}
	if len(id) > 8 {
		return id[:8] + "…"
	}
	return id
}

// obscure trims a client ID to a recognisable prefix without exposing
// the whole thing in the "tokens already saved" prompt.
func obscure(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8] + "…"
}

// exitWizard converts huh's "user aborted" / Ctrl+C errors into a clean
// exit instead of a stack trace. Anything else gets fmt'd to stderr.
func exitWizard(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, huh.ErrUserAborted) {
		fmt.Fprintln(os.Stderr, "setup aborted — partial config preserved.")
		os.Exit(1)
	}
	exitf("%v", err)
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "setup: "+format+"\n", args...)
	os.Exit(1)
}
