package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nutgood/frameio-icloud/internal/config"
	"github.com/nutgood/frameio-icloud/internal/frameio"
	"github.com/nutgood/frameio-icloud/internal/paths"
)

// runAuth handles two modes:
//   - default: interactive OAuth via Adobe IMS. Spins up a localhost HTTPS
//     listener on :12345, prints the authorize URL, captures the redirect,
//     exchanges the code, writes tokens.json. Also prints the Frame.io
//     hierarchy at the end so the user knows what to put in config.
//   - -discover: skip OAuth, just print the hierarchy using existing
//     tokens.json. For when you add a new project later.
func runAuth(args []string) {
	fs := flag.NewFlagSet("auth", flag.ExitOnError)
	clientID := fs.String("client-id", os.Getenv("FRAMEIO_CLIENT_ID"), "OAuth client ID from Adobe Developer Console")
	clientSecret := fs.String("client-secret", os.Getenv("FRAMEIO_CLIENT_SECRET"), "OAuth client secret from Adobe Developer Console")
	port := fs.Int("port", 12345, "local port for the OAuth redirect listener")
	scopes := fs.String("scopes", "openid email profile offline_access additional_info.roles", "OAuth scopes")
	discoverOnly := fs.Bool("discover", false, "skip OAuth; just print Frame.io accounts/workspaces/projects")
	apply := fs.Bool("apply", false, "with -discover and exactly one account/workspace/project, write the IDs into config.json")
	_ = fs.Parse(args)

	p, err := paths.Default()
	if err != nil {
		log.Fatalf("paths: %v", err)
	}
	if err := p.EnsureDirs(); err != nil {
		log.Fatalf("ensure dirs: %v", err)
	}

	if *discoverOnly {
		runDiscover(p, *apply)
		return
	}
	if *clientID == "" || *clientSecret == "" {
		log.Fatal("-client-id and -client-secret (or FRAMEIO_CLIENT_ID / FRAMEIO_CLIENT_SECRET) required")
	}

	redirectURI := fmt.Sprintf("https://localhost:%d/callback", *port)
	store, err := frameio.LoadTokenStore(p.Tokens)
	if err != nil {
		log.Fatalf("load %s: %v", p.Tokens, err)
	}
	store.ClientID = *clientID
	store.ClientSecret = *clientSecret
	store.RedirectURI = redirectURI
	if err := store.Save(); err != nil {
		log.Fatalf("save initial store: %v", err)
	}

	stateBytes := make([]byte, 16)
	_, _ = rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	authURL := store.AuthorizeURL(state, strings.Fields(*scopes))
	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Println("Open the following URL in your browser and grant access:")
	fmt.Println()
	fmt.Println("  " + authURL)
	fmt.Println()
	fmt.Printf("After login, the browser will redirect to %s.\n", redirectURI)
	fmt.Println("The redirect uses a self-signed cert — accept the browser warning once.")
	fmt.Println("This process will exit automatically once tokens are saved.")
	fmt.Println("--------------------------------------------------------------------------------")

	done := make(chan struct{})
	var flowErr error

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			flowErr = fmt.Errorf("oauth: %s: %s", e, q.Get("error_description"))
			http.Error(w, flowErr.Error(), http.StatusBadRequest)
			close(done)
			return
		}
		if q.Get("state") != state {
			flowErr = fmt.Errorf("oauth: state mismatch")
			http.Error(w, flowErr.Error(), http.StatusBadRequest)
			close(done)
			return
		}
		code := q.Get("code")
		if code == "" {
			flowErr = fmt.Errorf("oauth: no code in callback")
			http.Error(w, flowErr.Error(), http.StatusBadRequest)
			close(done)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := store.ExchangeCode(ctx, code); err != nil {
			flowErr = err
			http.Error(w, err.Error(), http.StatusInternalServerError)
			close(done)
			return
		}
		fmt.Fprintln(w, "OK — tokens saved. You can close this tab.")
		close(done)
	})

	tlsCert, err := selfSignedLoopbackCert()
	if err != nil {
		log.Fatalf("self-signed cert: %v", err)
	}
	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", *port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{tlsCert}},
	}
	go func() {
		if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()
	<-done
	_ = srv.Close()
	if flowErr != nil {
		log.Fatalf("auth failed: %v", flowErr)
	}
	log.Printf("tokens written to %s (access token expires %s UTC)", p.Tokens, store.ExpiresAt.UTC().Format(time.RFC3339))
	fmt.Println()
	runDiscover(p, *apply)
}

func runDiscover(p *paths.Paths, apply bool) {
	store, err := frameio.LoadTokenStore(p.Tokens)
	if err != nil {
		log.Fatalf("load %s: %v", p.Tokens, err)
	}
	if store.RefreshToken == "" {
		log.Fatalf("tokens file %s is empty — run `frameio-icloud auth` first (without -discover)", p.Tokens)
	}
	client := frameio.NewClient(store, "")
	ctx := context.Background()
	accounts, err := client.ListAccounts(ctx)
	if err != nil {
		log.Fatalf("list accounts: %v", err)
	}
	if len(accounts) == 0 {
		fmt.Println("No Frame.io accounts found for this user.")
		return
	}
	fmt.Println("Discovered Frame.io hierarchy:")
	fmt.Println()
	type triple struct{ account, workspace, folder string }
	var single *triple
	for _, a := range accounts {
		fmt.Printf("  Account: %s\n    id: %s\n", a.DisplayName, a.ID)
		workspaces, err := client.ListWorkspaces(ctx, a.ID)
		if err != nil {
			fmt.Printf("    (list workspaces failed: %v)\n", err)
			continue
		}
		for _, w := range workspaces {
			fmt.Printf("    Workspace: %s\n      id: %s\n", w.Name, w.ID)
			projects, err := client.ListProjects(ctx, a.ID, w.ID)
			if err != nil {
				fmt.Printf("      (list projects failed: %v)\n", err)
				continue
			}
			for _, prj := range projects {
				fmt.Printf("      Project: %s\n        id: %s\n        root_folder_id: %s\n", prj.Name, prj.ID, prj.RootFolderID)
				if len(accounts) == 1 && len(workspaces) == 1 && len(projects) == 1 {
					single = &triple{a.ID, w.ID, prj.RootFolderID}
				}
			}
		}
	}
	fmt.Println()
	if single == nil {
		fmt.Println("Multiple accounts/workspaces/projects — set the chosen IDs with:")
		fmt.Println("  frameio-icloud config set frameio.account   <id>")
		fmt.Println("  frameio-icloud config set frameio.workspace <id>")
		fmt.Println("  frameio-icloud config set frameio.folder    <root_folder_id>")
		return
	}
	if !apply {
		fmt.Println("Exactly one account / workspace / project found. To write into config:")
		fmt.Println("  frameio-icloud auth -discover -apply")
		fmt.Printf("\n  frameio.account   = %s\n", single.account)
		fmt.Printf("  frameio.workspace = %s\n", single.workspace)
		fmt.Printf("  frameio.folder    = %s\n", single.folder)
		return
	}
	cfg, err := config.Load(p.Config)
	if err != nil {
		log.Fatalf("config load: %v", err)
	}
	cfg.FrameioAccount = single.account
	cfg.FrameioWorkspace = single.workspace
	cfg.FrameioFolder = single.folder
	if err := config.Save(p.Config, cfg); err != nil {
		log.Fatalf("config save: %v", err)
	}
	fmt.Printf("config updated: %s\n", p.Config)
}

// selfSignedLoopbackCert mints an ephemeral RSA cert valid for 127.0.0.1
// / localhost so the loopback OAuth redirect can use HTTPS (Adobe IMS
// requires it). Browser warns on first visit; cert is discarded on exit.
func selfSignedLoopbackCert() (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "frameio-icloud localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}
