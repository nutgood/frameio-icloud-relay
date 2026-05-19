// Package service is the long-running relay: it consumes Frame.io C2C
// upload webhooks, downloads each file, imports it into Photos.app
// (which iCloud Photos sync then uploads to iCloud), and deletes the
// file from Frame.io. A reconcile poll catches anything webhooks miss.
//
// This was originally the body of cmd/frameio-relay/main.go in the
// frameio-immich-relay project; it's been split into a package so the
// single frameio-icloud binary can host it alongside the auth/install/
// status subcommands.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nutgood/frameio-icloud/internal/frameio"
	"github.com/nutgood/frameio-icloud/internal/ipc"
	"github.com/nutgood/frameio-icloud/internal/paths"
	"github.com/nutgood/frameio-icloud/internal/photos"
	"github.com/nutgood/frameio-icloud/internal/pushover"
)

// Options is the runtime configuration for a service. Fully resolved by
// the caller (CLI flags + config file merged) before construction.
type Options struct {
	Paths        *paths.Paths
	AccountID    string
	WorkspaceID  string
	FolderID     string
	PublicURL    string
	WebhookAddr  string
	PollInterval time.Duration
	StuckTimeout time.Duration
	DryRun       bool
	Pushover     *pushover.Client
	Photos       *photos.Importer
}

// Service holds the running relay's state. Construct via New, then Run.
type Service struct {
	opts Options

	client   *frameio.Client
	state    *State
	notifier *notifier
	authedAs string

	mu          sync.Mutex
	inflight    map[string]struct{}
	recentImps  []ipc.Event
	recentErrs  []ipc.Event
	startedAt   time.Time
	webhookSrv  *http.Server
	webhookLive bool
}

// New constructs a Service. tokens is the authenticated Frame.io token
// store; opts.AccountID is filled in by auto-discovery if empty.
func New(tokens *frameio.TokenStore, opts Options) *Service {
	if opts.WebhookAddr == "" {
		opts.WebhookAddr = ":9000"
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = 60 * time.Second
	}
	if opts.Photos == nil {
		opts.Photos = photos.New()
	}
	return &Service{
		opts:     opts,
		client:   frameio.NewClient(tokens, opts.AccountID),
		notifier: newNotifier(opts.Pushover, 30*time.Second),
		inflight: map[string]struct{}{},
	}
}

// Run starts the service and blocks until ctx is done. Performs:
//
//  1. Auth check (`/v4/me`).
//  2. Auto-discovery of account/workspace/folder if any are unset.
//  3. Webhook registration when PublicURL is set.
//  4. Photos.app permission check.
//  5. Local-disk reconcile (orphaned downloads).
//  6. Status IPC socket.
//  7. Reconcile poll loop (synchronous; blocks on ctx).
func (s *Service) Run(ctx context.Context) error {
	if err := s.opts.Paths.EnsureDirs(); err != nil {
		return fmt.Errorf("ensure dirs: %w", err)
	}
	st, err := LoadState(s.opts.Paths.State)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	s.state = st
	s.startedAt = time.Now()

	name, err := s.client.Me(ctx)
	if err != nil {
		return fmt.Errorf("auth check: %w", err)
	}
	s.authedAs = name
	log.Printf("authenticated as %s", name)

	if err := autoDiscover(ctx, s.client, &s.opts.AccountID, &s.opts.WorkspaceID, &s.opts.FolderID); err != nil {
		return fmt.Errorf("auto-discover: %w", err)
	}
	if s.opts.AccountID == "" || s.opts.FolderID == "" {
		return errors.New("missing Frame.io account or folder (discovery did not find one; set via `frameio-icloud config set`)")
	}
	s.client.AccountID = s.opts.AccountID

	// Photos.app reachability — fail fast with a helpful error rather
	// than dropping every webhook on the floor.
	if !s.opts.DryRun {
		if err := s.opts.Photos.Check(ctx); err != nil {
			s.notifier.OnError(fmt.Errorf("photos check failed at startup: %w", err))
			log.Printf("WARN: %v — imports will fail until Photos.app is reachable", err)
		}
	}

	// Webhook registration.
	if s.opts.PublicURL != "" {
		if s.opts.WorkspaceID == "" {
			return errors.New("public_url is set but workspace is unknown (set via `frameio-icloud config set frameio.workspace <id>`)")
		}
		if st.WebhookID == "" {
			secret, id, err := s.client.RegisterWebhook(ctx, s.opts.WorkspaceID, s.opts.PublicURL, []string{"file.upload.completed"})
			if err != nil {
				return fmt.Errorf("register webhook: %w", err)
			}
			st.WebhookID = id
			st.WebhookSecret = secret
			st.WebhookWorkspace = s.opts.WorkspaceID
			st.WebhookURL = s.opts.PublicURL
			if err := SaveState(s.opts.Paths.State, st); err != nil {
				return fmt.Errorf("save state: %w", err)
			}
			log.Printf("registered webhook %s → %s", id, s.opts.PublicURL)
		} else {
			log.Printf("reusing webhook %s (cached secret)", st.WebhookID)
		}
	} else {
		log.Printf("no public_url configured — running in polling-only mode")
	}

	// Goroutines: webhook server, status IPC, then poll loop in main.
	if st.WebhookSecret != "" {
		go s.runWebhookServer(ctx, s.opts.WebhookAddr, st.WebhookSecret)
	}
	go func() {
		if err := ipc.Serve(ctx, s.opts.Paths.Socket, s.snapshot); err != nil {
			log.Printf("ipc serve: %v", err)
		}
	}()

	s.notifier.OnStartup(name)

	// Startup local sweep, then poll loop.
	if err := s.reconcileLocal(ctx); err != nil {
		log.Printf("local reconcile: %v", err)
	}
	s.runPollLoop(ctx, s.opts.PollInterval)
	log.Printf("shutting down")
	return nil
}

func (s *Service) runPollLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	if err := s.reconcile(ctx); err != nil {
		log.Printf("initial reconcile: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.reconcile(ctx); err != nil {
				log.Printf("reconcile: %v", err)
			}
		}
	}
}

func (s *Service) claim(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.inflight[id]; busy {
		return false
	}
	s.inflight[id] = struct{}{}
	return true
}

func (s *Service) release(id string) {
	s.mu.Lock()
	delete(s.inflight, id)
	s.mu.Unlock()
}

// recordImport / recordError feed the status IPC's ring buffers.
const recentLimit = 20

func (s *Service) recordImport(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recentImps = append(s.recentImps, ipc.Event{At: time.Now(), Message: msg})
	if len(s.recentImps) > recentLimit {
		s.recentImps = s.recentImps[len(s.recentImps)-recentLimit:]
	}
}

func (s *Service) recordError(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recentErrs = append(s.recentErrs, ipc.Event{At: time.Now(), Message: msg})
	if len(s.recentErrs) > recentLimit {
		s.recentErrs = s.recentErrs[len(s.recentErrs)-recentLimit:]
	}
}

// reconcileLocal walks the downloads dir for orphans (typically left over
// from a crash between Photos import and Frame.io delete). Files that
// were already imported re-trigger the Frame.io delete and get cleaned
// up; files whose IDs aren't in the imported-set are re-imported.
func (s *Service) reconcileLocal(ctx context.Context) error {
	if s.opts.DryRun {
		return nil
	}
	var paths []string
	err := filepath.WalkDir(s.opts.Paths.Downloads, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".part") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	log.Printf("local reconcile: %d orphan file(s) on disk", len(paths))
	for _, p := range paths {
		if err := s.reconcileLocalFile(ctx, p); err != nil {
			log.Printf("local reconcile %s: %v", p, err)
		}
	}
	return nil
}

func (s *Service) reconcileLocalFile(ctx context.Context, path string) error {
	id := "local:" + path
	if !s.claim(id) {
		return nil
	}
	defer s.release(id)
	if err := s.opts.Photos.Import(ctx, path); err != nil {
		s.notifier.OnImportFailed()
		s.recordError(fmt.Sprintf("photos import %s: %v", filepath.Base(path), err))
		return err
	}
	s.notifier.OnImported()
	s.recordImport("orphan: " + filepath.Base(path))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("local reconcile: remove %s: %v", path, err)
	}
	return nil
}

func (s *Service) reconcile(ctx context.Context) error {
	files, err := s.walk(ctx, s.opts.FolderID)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.IsReady() {
			if err := s.process(ctx, f); err != nil {
				log.Printf("[%s] reconcile process: %v", f.ID, err)
			}
			continue
		}
		if err := s.maybeReapStuck(ctx, f); err != nil {
			log.Printf("[%s] reap stuck: %v", f.ID, err)
		}
	}
	return nil
}

func (s *Service) maybeReapStuck(ctx context.Context, f frameio.File) error {
	if s.opts.StuckTimeout <= 0 || s.opts.DryRun {
		return nil
	}
	if f.CreatedAt.IsZero() {
		return nil
	}
	age := time.Since(f.CreatedAt)
	if age < s.opts.StuckTimeout {
		return nil
	}
	if !s.claim(f.ID) {
		return nil
	}
	defer s.release(f.ID)
	log.Printf("[%s] %s stuck in status=%q for %s; deleting", f.ID, f.Name, f.Status, age.Round(time.Second))
	return s.client.DeleteFile(ctx, f.ID)
}

func (s *Service) walk(ctx context.Context, rootID string) ([]frameio.File, error) {
	var out []frameio.File
	stack := []string{rootID}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		children, err := s.client.ListFolderChildren(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", id, err)
		}
		for _, c := range children {
			switch c.Type {
			case "folder", "version_stack":
				stack = append(stack, c.ID)
			case "file":
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// process downloads f, imports into Photos.app, then deletes from
// Frame.io. Persists "imported" between the Photos step and the delete
// step so a crash in between doesn't cause a duplicate import.
func (s *Service) process(ctx context.Context, f frameio.File) error {
	if !s.claim(f.ID) {
		return nil
	}
	defer s.release(f.ID)

	// If we already imported this in a prior process lifetime, skip
	// ahead to the Frame.io delete.
	if s.state.HasImported(f.ID) {
		if !s.opts.DryRun {
			if err := s.client.DeleteFile(ctx, f.ID); err != nil {
				return fmt.Errorf("delete (post-import retry): %w", err)
			}
			s.state.ForgetImported(f.ID)
			_ = SaveState(s.opts.Paths.State, s.state)
			log.Printf("[%s] deleted from frame.io (retry)", f.ID)
		}
		return nil
	}

	if f.MediaLinks.Original == nil || f.MediaLinks.Original.DownloadURL == "" {
		fresh, err := s.client.GetFile(ctx, f.ID)
		if err != nil {
			return fmt.Errorf("refetch: %w", err)
		}
		f = fresh
	}
	if !f.IsReady() {
		return fmt.Errorf("not ready (status=%s)", f.Status)
	}

	dst, tmp := s.localPath(f)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	skipDownload := false
	if info, err := os.Stat(dst); err == nil && info.Size() == f.FileSize && f.FileSize > 0 {
		log.Printf("[%s] %s — local copy exists; skipping download", f.ID, f.Name)
		skipDownload = true
	}
	if !skipDownload {
		log.Printf("[%s] %s (%s, %d bytes) → %s", f.ID, f.Name, f.MediaType, f.FileSize, dst)
		body, _, err := s.client.Download(ctx, f)
		if err != nil {
			return fmt.Errorf("download: %w", err)
		}
		n, err := writeAndClose(tmp, body)
		if err != nil {
			return fmt.Errorf("write: %w", err)
		}
		if f.FileSize > 0 && n != f.FileSize {
			_ = os.Remove(tmp)
			return fmt.Errorf("short write: %d != %d", n, f.FileSize)
		}
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
	}

	if s.opts.DryRun {
		log.Printf("[%s] dry-run: skipping Photos import + Frame.io delete", f.ID)
		return nil
	}

	if err := s.opts.Photos.Import(ctx, dst); err != nil {
		s.notifier.OnImportFailed()
		s.recordError(fmt.Sprintf("%s: photos: %v", f.Name, err))
		return fmt.Errorf("photos: %w", err)
	}
	log.Printf("[%s] imported %s into Photos.app", f.ID, f.Name)
	s.notifier.OnImported()
	s.recordImport(f.Name)

	// Persist "imported" BEFORE the Frame.io delete so a crash in between
	// the two doesn't lose the fact that Photos.app already has it.
	s.state.MarkImported(f.ID)
	if err := SaveState(s.opts.Paths.State, s.state); err != nil {
		log.Printf("warn: persist imported set: %v", err)
	}

	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		log.Printf("[%s] warn: remove local copy: %v", f.ID, err)
	}
	if err := s.client.DeleteFile(ctx, f.ID); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	s.state.ForgetImported(f.ID)
	_ = SaveState(s.opts.Paths.State, s.state)
	log.Printf("[%s] deleted from frame.io", f.ID)
	return nil
}

func (s *Service) localPath(f frameio.File) (dst, tmp string) {
	t := f.CreatedAt
	if t.IsZero() {
		t = time.Now().UTC()
	}
	name := f.Name
	if name == "" {
		name = f.ID
	}
	dst = filepath.Join(s.opts.Paths.Downloads, t.Format("2006"), t.Format("01-02"), name)
	return dst, dst + ".part"
}

func (s *Service) runWebhookServer(ctx context.Context, addr, secret string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "POST" {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		body, err := frameio.ReadWebhookBody(req, 1<<20)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sig := req.Header.Get(frameio.WebhookSignatureHeader)
		ts := req.Header.Get(frameio.WebhookTimestampHeader)
		if err := frameio.WebhookVerify(secret, sig, ts, body, 5*time.Minute); err != nil {
			log.Printf("webhook: verify failed: %v", err)
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		var evt frameio.WebhookEvent
		if err := json.Unmarshal(body, &evt); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("webhook: %s resource=%s/%s", evt.Type, evt.Resource.Type, evt.Resource.ID)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")

		if evt.Type == "file.upload.completed" && evt.Resource.Type == "file" && evt.Resource.ID != "" {
			s.notifier.OnWebhook()
			go func(id string) {
				bg, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				f, err := s.client.GetFile(bg, id)
				if err != nil {
					log.Printf("webhook process: get %s: %v", id, err)
					s.recordError("webhook get: " + err.Error())
					return
				}
				if err := s.process(bg, f); err != nil {
					log.Printf("webhook process: %s: %v", id, err)
					s.recordError(id + ": " + err.Error())
				}
			}(evt.Resource.ID)
		}
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	s.mu.Lock()
	s.webhookSrv = srv
	s.webhookLive = true
	s.mu.Unlock()
	log.Printf("webhook server listening on %s (path /webhook)", addr)
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("webhook server: %v", err)
	}
}

// snapshot is the ipc.Provider passed to ipc.Serve.
func (s *Service) snapshot() ipc.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	inflight := make([]string, 0, len(s.inflight))
	for id := range s.inflight {
		inflight = append(inflight, id)
	}
	imps := append([]ipc.Event(nil), s.recentImps...)
	errs := append([]ipc.Event(nil), s.recentErrs...)
	open, burstStart, imp, fail := s.notifier.snapshot()
	st := ipc.Status{
		PID:            os.Getpid(),
		StartedAt:      s.startedAt,
		AuthUser:       s.authedAs,
		AuthExpiresAt:  s.client.Tokens.ExpiresAt,
		WebhookURL:     s.opts.PublicURL,
		WebhookActive:  s.webhookLive,
		PollInterval:   s.opts.PollInterval.String(),
		PollingOnly:    s.opts.PublicURL == "",
		InFlight:       inflight,
		RecentImports:  imps,
		RecentErrors:   errs,
		BurstOpen:      open,
		BurstCount:     imp,
		BurstFailed:    fail,
		BurstStartedAt: burstStart,
	}
	return st
}

// autoDiscover fills in any of the IDs that are empty so long as there's
// exactly one reasonable choice at each level.
func autoDiscover(ctx context.Context, client *frameio.Client, account, workspace, folder *string) error {
	if *account == "" {
		accounts, err := client.ListAccounts(ctx)
		if err != nil {
			return fmt.Errorf("list accounts: %w", err)
		}
		switch len(accounts) {
		case 0:
			return errors.New("no Frame.io accounts on this user")
		case 1:
			*account = accounts[0].ID
			log.Printf("discovered account: %q (%s)", accounts[0].DisplayName, *account)
		default:
			return fmt.Errorf("%d accounts present; set frameio.account explicitly", len(accounts))
		}
	}
	if *workspace == "" {
		workspaces, err := client.ListWorkspaces(ctx, *account)
		if err != nil {
			return fmt.Errorf("list workspaces: %w", err)
		}
		switch len(workspaces) {
		case 0:
			return fmt.Errorf("no workspaces in account %s", *account)
		case 1:
			*workspace = workspaces[0].ID
			log.Printf("discovered workspace: %q (%s)", workspaces[0].Name, *workspace)
		default:
			return fmt.Errorf("%d workspaces; set frameio.workspace explicitly", len(workspaces))
		}
	}
	if *folder == "" {
		projects, err := client.ListProjects(ctx, *account, *workspace)
		if err != nil {
			return fmt.Errorf("list projects: %w", err)
		}
		switch len(projects) {
		case 0:
			return fmt.Errorf("no projects in workspace %s", *workspace)
		case 1:
			*folder = projects[0].RootFolderID
			log.Printf("discovered project root_folder_id=%s (%q)", *folder, projects[0].Name)
		default:
			return fmt.Errorf("%d projects; set frameio.folder explicitly", len(projects))
		}
	}
	return nil
}

func writeAndClose(path string, body io.ReadCloser) (int64, error) {
	defer body.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(f, body)
	closeErr := f.Close()
	if copyErr != nil {
		return n, copyErr
	}
	return n, closeErr
}
