// Command simple-opcua-server serves an OPC UA address space defined by a CSV
// tag file. It is a thin wrapper around the opcuaserver package, which can be
// embedded in another Go program instead.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	opcuaserver "github.com/moriax/simple-opcua-server"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	defaultConfig  = "config/AddressSpace.csv"
	exampleConfig  = "config/AddressSpace.example.csv"
	defaultCert    = "cert.pem"
	defaultKey     = "key.pem"
	defaultValues  = "values.json"
	defaultLogFile = ""
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "simple-opcua-server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", defaultConfig, "path to the CSV tag file")
		host        = flag.String("host", opcuaserver.DefaultHost, "local address to bind and to advertise in the endpoint URL")
		advertise   = flag.String("advertise", "", "extra host names to advertise endpoints for, comma separated; the server still binds only to -host")
		port        = flag.Int("port", opcuaserver.DefaultPort, "TCP port to listen on")
		namespaceNS = flag.String("namespace", opcuaserver.DefaultNamespaceURI, "URI of the namespace holding the tags")
		namespaceIX = flag.Int("namespace-index", opcuaserver.DefaultNamespaceIndex, "namespace index the tags should appear under")
		nodeIDs     = flag.String("nodeid", "raw", `tag NodeID identifier: "raw" keeps the tag file's prefix (t|MIX.MIXER1.BatchId), "path" drops it`)
		separator   = flag.String("separator", opcuaserver.DefaultSeparator, "character separating the levels of a tag path")
		strict      = flag.Bool("strict", false, "fail on any problem in the tag file instead of warning and continuing")
		certPath    = flag.String("cert", defaultCert, "path to the server certificate, generated on first use")
		keyPath     = flag.String("key", defaultKey, "path to the server private key, generated on first use")
		appURI      = flag.String("appuri", "", "server ApplicationUri (default urn:<hostname>:simpleopcuaserver)")
		noSecurity  = flag.Bool("no-security", false, "offer only the unsecured endpoint and use no certificate")
		authModes   = flag.String("auth", "anonymous", "user identity tokens to accept, comma separated: anonymous, username")
		valuesPath  = flag.String("values", defaultValues, "path to the file tag values are persisted in")
		noPersist   = flag.Bool("no-persist", false, "do not persist tag values; every restart begins at the defaults")
		saveEvery   = flag.Duration("save-interval", opcuaserver.DefaultSaveInterval, "how often changed tag values are written to disk")
		logLevel    = flag.String("log", "info", "log level: off, error, warn, info, debug or trace")
		logFile     = flag.String("logfile", defaultLogFile, "also append the log to this file")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("simple-opcua-server %s\n", version)
		return nil
	}

	nodeIDFormat, err := opcuaserver.ParseNodeIDFormat(*nodeIDs)
	if err != nil {
		return err
	}
	level, err := opcuaserver.ParseLogLevel(*logLevel)
	if err != nil {
		return err
	}
	logger, logCloser, err := opcuaserver.NewLogger(level, *logFile)
	if err != nil {
		return err
	}
	if logCloser != nil {
		defer logCloser.Close()
	}
	auth, err := parseAuthModes(*authModes)
	if err != nil {
		return err
	}

	tagPath, err := resolveConfig(*configPath)
	if err != nil {
		return err
	}
	tags, warnings, err := opcuaserver.LoadTags(tagPath, *separator, *strict)
	if err != nil {
		return err
	}
	printWarnings(warnings)
	fmt.Printf("Loaded %d tags from %s\n", len(tags), tagPath)

	opts := []opcuaserver.Option{
		opcuaserver.WithHost(*host),
		opcuaserver.WithPort(*port),
		opcuaserver.WithAdvertise(strings.Split(*advertise, ",")...),
		opcuaserver.WithNamespace(*namespaceNS),
		opcuaserver.WithNamespaceIndex(*namespaceIX),
		opcuaserver.WithSeparator(*separator),
		opcuaserver.WithNodeIDFormat(nodeIDFormat),
		opcuaserver.WithAuth(auth...),
		opcuaserver.WithLogger(logger),
		opcuaserver.WithSoftwareVersion(version),
	}
	if *appURI != "" {
		opts = append(opts, opcuaserver.WithApplicationURI(*appURI))
	}

	storePath := *valuesPath
	if *noPersist {
		opts = append(opts, opcuaserver.WithoutPersistence())
	} else {
		storePath = resolveOrCreate(*valuesPath)
		opts = append(opts, opcuaserver.WithPersistence(storePath, *saveEvery))
	}

	if *noSecurity {
		opts = append(opts, opcuaserver.WithoutSecurity())
	} else {
		cp, kp := resolveKeyPair(*certPath, *keyPath)
		opts = append(opts, opcuaserver.WithCertificate(cp, kp))
	}

	srv, err := opcuaserver.New(tags, opts...)
	if err != nil {
		return err
	}
	printWarnings(srv.Warnings())

	if pair := srv.Certificate(); pair != nil {
		if pair.Created {
			fmt.Printf("Generated a self-signed certificate\n  %s\n  %s\n  %s\n", pair.CertPath, pair.KeyPath, pair.DERPath)
		} else {
			fmt.Printf("Using the certificate at %s\n", pair.CertPath)
		}
		for _, line := range pair.Describe() {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println("  A client must trust this certificate before it can use a secure endpoint.")
	}
	if n := srv.RestoredValues(); n > 0 {
		fmt.Printf("Restored %d tag value(s) from %s\n", n, storePath)
	}

	as := srv.AddressSpace()
	fmt.Printf("Address space: %d variables in %d folders, namespace %d (%s)\n",
		as.Variables, as.Folders, srv.NamespaceIndex(), *namespaceNS)
	if id, err := srv.NodeID(tags[0].NodeID(nodeIDFormat)); err == nil {
		fmt.Printf("Example NodeID: %s\n", id)
	}

	if err := srv.Start(); err != nil {
		return err
	}
	defer srv.Stop()

	fmt.Printf("Listening on opc.tcp://%s:%d\n", *host, *port)
	printEndpoints(srv)
	fmt.Printf("Accepting %s. Logging at %s", strings.Join(authModeNames(auth), " and "), level)
	if *logFile != "" {
		fmt.Printf(" to %s", *logFile)
	}
	fmt.Println(". Press Ctrl+C to stop.")
	if !*noPersist {
		fmt.Printf("Persisting tag values to %s every %s\n", storePath, *saveEvery)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	fmt.Println("\nShutting down.")
	return srv.Stop()
}

func printWarnings(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
}

// printEndpoints lists each advertised URL with the security it offers.
func printEndpoints(srv *opcuaserver.Server) {
	byURL := map[string][]string{}
	for _, ep := range srv.Endpoints() {
		byURL[ep.EndpointURL] = append(byURL[ep.EndpointURL],
			fmt.Sprintf("%-16s %s", opcuaserver.SecurityModeName(ep.SecurityMode), opcuaserver.PolicyName(ep.SecurityPolicyURI)))
	}
	for _, u := range srv.EndpointURLs() {
		fmt.Printf("  %s\n", u)
		for _, line := range byURL[u] {
			fmt.Printf("    %s\n", line)
		}
	}
}

// parseAuthModes turns the -auth flag into auth modes.
func parseAuthModes(s string) ([]opcuaserver.AuthMode, error) {
	var out []opcuaserver.AuthMode
	for _, name := range strings.Split(s, ",") {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "":
			continue
		case "anonymous", "anon":
			out = append(out, opcuaserver.AuthAnonymous)
		case "username", "user":
			out = append(out, opcuaserver.AuthUserName)
		default:
			return nil, fmt.Errorf("unknown -auth value %q, want anonymous or username", strings.TrimSpace(name))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-auth must name at least one token type")
	}
	return out, nil
}

func authModeNames(modes []opcuaserver.AuthMode) []string {
	names := make([]string, 0, len(modes))
	for _, m := range modes {
		if m == opcuaserver.AuthUserName {
			names = append(names, "username clients (credentials are not checked)")
			continue
		}
		names = append(names, "anonymous clients")
	}
	return names
}

// findFile looks for a relative path in the working directory and then next to
// the executable, so the binary can be run from anywhere with its files beside
// it. Absolute paths are used as given.
func findFile(p string) (string, bool) {
	if fileExists(p) {
		return p, true
	}
	if filepath.IsAbs(p) {
		return p, false
	}
	if exe, err := os.Executable(); err == nil {
		alt := filepath.Join(filepath.Dir(exe), p)
		if fileExists(alt) {
			return alt, true
		}
	}
	return p, false
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// resolveConfig finds the tag file, falling back to the example that ships with
// the repository so that a fresh checkout runs without any setup.
func resolveConfig(p string) (string, error) {
	if found, ok := findFile(p); ok {
		return found, nil
	}
	if p == defaultConfig {
		if found, ok := findFile(exampleConfig); ok {
			fmt.Fprintf(os.Stderr, "note: %s not found, using the example tag file %s\n", p, found)
			return found, nil
		}
	}
	return "", fmt.Errorf("tag file %s not found (looked in the working directory and next to the executable)", p)
}

// resolveOrCreate returns where a file lives, or where to create it.
func resolveOrCreate(p string) string {
	if found, ok := findFile(p); ok {
		return found
	}
	return createPath(p)
}

// resolveKeyPair reuses an existing certificate wherever it is found; otherwise
// the pair is created next to the executable, so that running from different
// working directories does not keep producing new certificates to trust.
func resolveKeyPair(certPath, keyPath string) (string, string) {
	c, cOK := findFile(certPath)
	k, kOK := findFile(keyPath)
	if cOK && kOK {
		return c, k
	}
	return createPath(certPath), createPath(keyPath)
}

// createPath picks where to create a file that does not exist yet: next to the
// executable when that is writable.
func createPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	exe, err := os.Executable()
	if err != nil {
		return p
	}
	dir := filepath.Dir(exe)
	if !writable(dir) {
		return p
	}
	return filepath.Join(dir, p)
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
