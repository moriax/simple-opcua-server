// Package opcuaserver serves a list of tags over OPC UA.
//
// The tags come from a CSV file or from a slice built in code. Each one becomes
// a Variable node in a browsable folder hierarchy, readable and writable by any
// client and by the hosting program through Set and Get.
//
//	tags, _, err := opcuaserver.LoadTags("tags.csv", ".", false)
//	srv, err := opcuaserver.New(tags)
//	err = srv.Start()
//	defer srv.Stop()
//	err = srv.Set("t|MIX.MIXER1.BatchId", "BATCH-1")
package opcuaserver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
)

// Defaults used by New when an option does not override them.
const (
	DefaultHost           = "127.0.0.1"
	DefaultPort           = 4840
	DefaultNamespaceURI   = "urn:simpleopcuaserver:tags"
	DefaultNamespaceIndex = 2
	DefaultSeparator      = "."
	DefaultSaveInterval   = 5 * time.Second

	// SecurityPolicy is the only signed and encrypted policy offered. The older
	// Basic128Rsa15 and Basic256 policies are deprecated and broken.
	SecurityPolicy = "Basic256Sha256"
)

// AuthMode is a kind of user identity token the server accepts.
type AuthMode string

const (
	// AuthAnonymous lets any client in without credentials.
	AuthAnonymous AuthMode = "anonymous"
	// AuthUserName advertises a UserName token on the secure endpoints. The
	// credentials are not verified; there is no user database behind them.
	AuthUserName AuthMode = "username"
)

// ErrNotStarted is returned by operations that need a running server.
var ErrNotStarted = errors.New("opcuaserver: server is not running")

type config struct {
	host           string
	advertise      []string
	port           int
	namespaceURI   string
	namespaceIndex int
	separator      string
	nodeIDs        NodeIDFormat
	auth           []AuthMode

	security   bool
	certPath   string
	keyPath    string
	appURI     string
	commonName string

	persist      bool
	valuesPath   string
	saveInterval time.Duration

	logger          *Logger
	softwareVersion string
}

// Option configures a Server. Options are applied by New in the order given.
type Option func(*config)

// WithHost sets the address the listener binds to. It is also the host in the
// first advertised endpoint URL. The default is 127.0.0.1, which keeps the
// server reachable only from this machine.
func WithHost(host string) Option { return func(c *config) { c.host = host } }

// WithPort sets the TCP port. The default is 4840.
func WithPort(port int) Option { return func(c *config) { c.port = port } }

// WithAdvertise adds endpoint URLs for host names the server does not bind to,
// for clients that reach it under another name. A client whose endpoint URL is
// not advertised is handed an empty ServerEndpoints list by CreateSession and
// abandons the session.
func WithAdvertise(hosts ...string) Option {
	return func(c *config) { c.advertise = append(c.advertise, hosts...) }
}

// WithNamespace sets the URI of the namespace holding the tags.
func WithNamespace(uri string) Option { return func(c *config) { c.namespaceURI = uri } }

// WithNamespaceIndex sets the namespace index the tags appear under. The
// default is 2, because index 1 conventionally belongs to the server's own
// ApplicationUri.
func WithNamespaceIndex(i int) Option { return func(c *config) { c.namespaceIndex = i } }

// WithSeparator sets the character separating the levels of a tag path.
func WithSeparator(s string) Option { return func(c *config) { c.separator = s } }

// WithNodeIDFormat selects whether a tag's NodeID keeps the tag file's prefix.
func WithNodeIDFormat(f NodeIDFormat) Option { return func(c *config) { c.nodeIDs = f } }

// WithAuth sets the identity tokens the endpoints offer. The default is
// AuthAnonymous.
func WithAuth(modes ...AuthMode) Option { return func(c *config) { c.auth = modes } }

// WithoutSecurity offers only the unsecured endpoint and uses no certificate.
func WithoutSecurity() Option { return func(c *config) { c.security = false } }

// WithCertificate points at the PEM certificate and key to use. They are
// generated, self-signed, if either file is missing.
func WithCertificate(certPath, keyPath string) Option {
	return func(c *config) {
		c.security = true
		c.certPath, c.keyPath = certPath, keyPath
	}
}

// WithApplicationURI sets the server's ApplicationUri. It must match the URI
// subject alternative name of the certificate, so it only takes effect for a
// certificate this server generates.
func WithApplicationURI(uri string) Option { return func(c *config) { c.appURI = uri } }

// WithPersistence keeps tag values in path, writing them whenever a value
// changes and at most every interval.
func WithPersistence(path string, interval time.Duration) Option {
	return func(c *config) {
		c.persist = true
		c.valuesPath = path
		if interval > 0 {
			c.saveInterval = interval
		}
	}
}

// WithoutPersistence starts every run from the tags' default values.
func WithoutPersistence() Option { return func(c *config) { c.persist = false } }

// WithLogger sends the server's log to l. Use NewLogger to make one.
func WithLogger(l *Logger) Option { return func(c *config) { c.logger = l } }

// WithSoftwareVersion sets the version reported in the server's build info.
func WithSoftwareVersion(v string) Option { return func(c *config) { c.softwareVersion = v } }

// Server serves a set of tags over OPC UA.
type Server struct {
	cfg  config
	tags []Tag

	srv *server.Server
	ns  *server.NodeNameSpace

	keyPair  *KeyPair
	build    *BuildResult
	restored int
	warnings []string

	// nodes maps both a tag's NodeID identifier and its dotted path to its
	// node, so that Set and Get accept either.
	nodes map[string]*ua.NodeID
	types map[string]DataType

	mu        sync.Mutex
	started   bool
	cancel    context.CancelFunc
	persister *Persister
	persisted chan struct{}
}

// New builds the address space for tags. The server is not listening until
// Start is called; Set and Get work before then too.
func New(tags []Tag, opts ...Option) (*Server, error) {
	if len(tags) == 0 {
		return nil, errors.New("opcuaserver: no tags given")
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}

	cfg := config{
		host:            DefaultHost,
		port:            DefaultPort,
		namespaceURI:    DefaultNamespaceURI,
		namespaceIndex:  DefaultNamespaceIndex,
		separator:       DefaultSeparator,
		nodeIDs:         NodeIDRaw,
		auth:            []AuthMode{AuthAnonymous},
		security:        true,
		certPath:        "cert.pem",
		keyPath:         "key.pem",
		commonName:      "SimpleOPCUAServer@" + hostname,
		persist:         false,
		saveInterval:    DefaultSaveInterval,
		softwareVersion: "dev",
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.appURI == "" {
		cfg.appURI = "urn:" + hostname + ":simpleopcuaserver"
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.logger == nil {
		cfg.logger, _, _ = NewLogger(LogOff, "")
	}

	s := &Server{cfg: cfg, tags: slices.Clone(tags)}

	initial, err := s.restore()
	if err != nil {
		return nil, err
	}

	srvOpts, err := s.serverOptions(hostname)
	if err != nil {
		return nil, err
	}
	s.srv = server.New(srvOpts...)

	// Registered before Start, which is what makes this replace gopcua's own
	// GetEndpoints: RegisterHandler keeps the handler already in place.
	s.srv.RegisterHandler(id.GetEndpointsRequest_Encoding_DefaultBinary,
		GetEndpointsHandler(s.srv, cfg.logger))

	// gopcua only creates namespace 0, so the tags would land at index 1. Pad
	// with the namespaces a server conventionally carries, index 1 being the
	// server's own ApplicationUri, until the tags reach the requested index.
	for i := len(s.srv.Namespaces()); i < cfg.namespaceIndex; i++ {
		uri := cfg.appURI
		if i > 1 {
			uri = fmt.Sprintf("urn:simpleopcuaserver:reserved:%d", i)
		}
		server.NewNodeNameSpace(s.srv, uri)
	}

	s.ns = server.NewNodeNameSpace(s.srv, cfg.namespaceURI)
	if int(s.ns.ID()) != cfg.namespaceIndex {
		return nil, fmt.Errorf("opcuaserver: tags landed in namespace %d, not the requested %d",
			s.ns.ID(), cfg.namespaceIndex)
	}

	s.build, err = Build(s.srv, s.ns, s.tags, BuildOptions{
		Separator: cfg.separator,
		NodeIDs:   cfg.nodeIDs,
		Initial:   initial,
	})
	if err != nil {
		return nil, err
	}
	s.warnings = append(s.warnings, s.build.Warnings...)
	s.index()

	return s, nil
}

func (c *config) validate() error {
	switch {
	case c.separator == "":
		return errors.New("opcuaserver: separator must not be empty")
	case c.port < 1 || c.port > 65535:
		return fmt.Errorf("opcuaserver: port must be between 1 and 65535, got %d", c.port)
	// Namespace 0 is reserved for the OPC UA namespace itself.
	case c.namespaceIndex < 1 || c.namespaceIndex > 255:
		return fmt.Errorf("opcuaserver: namespace index must be between 1 and 255, got %d", c.namespaceIndex)
	case c.persist && c.saveInterval < time.Second:
		return fmt.Errorf("opcuaserver: save interval must be at least 1s, got %s", c.saveInterval)
	case len(c.auth) == 0:
		return errors.New("opcuaserver: at least one auth mode is required")
	}
	for _, m := range c.auth {
		if m != AuthAnonymous && m != AuthUserName {
			return fmt.Errorf("opcuaserver: unknown auth mode %q", m)
		}
	}
	if _, err := ParseNodeIDFormat(string(c.nodeIDs)); err != nil {
		return fmt.Errorf("opcuaserver: %w", err)
	}
	return nil
}

// restore reads persisted values, if persistence is on.
func (s *Server) restore() (map[string]any, error) {
	if !s.cfg.persist {
		return nil, nil
	}
	stored, err := LoadValues(s.cfg.valuesPath)
	if err != nil {
		return nil, err
	}
	initial, warnings := RestoreValues(stored, s.tags, s.cfg.nodeIDs)
	s.warnings = append(s.warnings, warnings...)
	s.restored = len(initial)
	return initial, nil
}

// serverOptions turns the configuration into gopcua server options.
func (s *Server) serverOptions(hostname string) ([]server.Option, error) {
	// Only the first endpoint is bound; the rest are advertised.
	hosts := []string{s.cfg.host}
	for _, h := range s.cfg.advertise {
		h = strings.TrimSpace(h)
		if h != "" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}

	opts := []server.Option{
		server.EndPoint(s.cfg.host, s.cfg.port),
		server.EnableSecurity("None", ua.MessageSecurityModeNone),
		server.ServerName("SimpleOPCUAServer"),
		server.ProductName("simpleopcuaserver"),
		server.SoftwareVersion(s.cfg.softwareVersion),
		server.SetLogger(s.cfg.logger),
	}
	for _, h := range hosts[1:] {
		opts = append(opts, server.EndPoint(h, s.cfg.port))
	}

	for _, m := range s.cfg.auth {
		if m == AuthUserName {
			opts = append(opts, server.EnableAuthMode(ua.UserTokenTypeUserName))
			continue
		}
		opts = append(opts, server.EnableAuthMode(ua.UserTokenTypeAnonymous))
	}

	if !s.cfg.security {
		return opts, nil
	}

	pair, err := LoadOrCreateKeyPair(s.cfg.certPath, s.cfg.keyPath, s.cfg.appURI,
		s.cfg.commonName, certHosts(hosts, hostname))
	if err != nil {
		return nil, err
	}
	s.keyPair = pair
	if why := pair.NonConformant(); why != "" {
		s.warnings = append(s.warnings,
			fmt.Sprintf("%s is not a valid OPC UA application instance certificate: %s", pair.CertPath, why))
	}

	return append(opts,
		server.Certificate(pair.CertDER),
		server.PrivateKey(pair.Key),
		server.EnableSecurity(SecurityPolicy, ua.MessageSecurityModeSign),
		server.EnableSecurity(SecurityPolicy, ua.MessageSecurityModeSignAndEncrypt),
	), nil
}

// index records how Set and Get find a tag.
func (s *Server) index() {
	nsID := s.ns.ID()
	s.nodes = make(map[string]*ua.NodeID, len(s.tags)*2)
	s.types = make(map[string]DataType, len(s.tags)*2)

	for _, t := range s.tags {
		key := t.NodeID(s.cfg.nodeIDs)
		s.nodes[key] = ua.NewStringNodeID(nsID, key)
		s.types[key] = t.DataType
	}
	// The dotted path is accepted too, as long as it does not shadow a NodeID.
	for _, t := range s.tags {
		if _, taken := s.nodes[t.FullPath]; taken {
			continue
		}
		key := t.NodeID(s.cfg.nodeIDs)
		s.nodes[t.FullPath] = s.nodes[key]
		s.types[t.FullPath] = t.DataType
	}
}

// Start begins listening. It returns once the server is accepting connections,
// so the caller keeps running; use Stop to shut down.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("opcuaserver: server is already running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := s.srv.Start(ctx); err != nil {
		cancel()
		return fmt.Errorf("opcuaserver: cannot start: %w", err)
	}
	s.cancel = cancel
	s.started = true

	if s.cfg.persist {
		s.persister = NewPersister(s.cfg.valuesPath, s.ns, s.tags, s.cfg.nodeIDs,
			s.cfg.saveInterval, s.cfg.logger)
		s.persister.MarkSaved()
		s.persisted = make(chan struct{})
		go func() {
			defer close(s.persisted)
			s.persister.Run(ctx)
		}()
	}

	s.cfg.logger.LogEndpoints(s.srv)
	return nil
}

// Stop shuts the server down, saving tag values one last time when persistence
// is on. It is safe to call on a server that was never started.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return nil
	}
	s.started = false

	s.cancel()
	if s.persisted != nil {
		<-s.persisted
		s.persisted = nil
	}
	return s.srv.Close()
}

// Set writes a tag value. The tag is named by its NodeID identifier, as it
// appears in the tag file, or by its dotted path. The value is converted to the
// tag's data type, and rejected if it does not fit.
//
// Unlike a client write, Set ignores the tag's access level: a tag that is
// read-only to clients is still writable by the program hosting the server.
func (s *Server) Set(tag string, value any) error {
	nodeID, dt, err := s.lookup(tag)
	if err != nil {
		return err
	}
	v, err := coerce(dt, value)
	if err != nil {
		return fmt.Errorf("opcuaserver: tag %q: %w", tag, err)
	}
	variant, err := ua.NewVariant(v)
	if err != nil {
		return fmt.Errorf("opcuaserver: tag %q: cannot encode %T: %w", tag, v, err)
	}

	node := s.ns.Node(nodeID)
	if node == nil {
		return fmt.Errorf("opcuaserver: tag %q has no node", tag)
	}

	now := time.Now()
	dv := &ua.DataValue{
		EncodingMask:    ua.DataValueValue | ua.DataValueStatusCode | ua.DataValueSourceTimestamp | ua.DataValueServerTimestamp,
		Value:           variant,
		Status:          ua.StatusOK,
		SourceTimestamp: now,
		ServerTimestamp: now,
	}
	if err := node.SetAttribute(ua.AttributeIDValue, dv); err != nil {
		return fmt.Errorf("opcuaserver: tag %q: %w", tag, err)
	}

	// Subscribers only exist once the server is running, and notifying before
	// then reaches machinery that Start sets up.
	s.mu.Lock()
	running := s.started
	s.mu.Unlock()
	if running {
		s.ns.ChangeNotification(nodeID)
	}
	return nil
}

// Get reads a tag value back, named the same way as for Set.
func (s *Server) Get(tag string) (any, error) {
	nodeID, _, err := s.lookup(tag)
	if err != nil {
		return nil, err
	}
	dv := s.ns.Attribute(nodeID, ua.AttributeIDValue)
	if dv == nil || dv.Value == nil {
		return nil, fmt.Errorf("opcuaserver: tag %q has no value", tag)
	}
	if dv.Status != ua.StatusOK {
		return nil, fmt.Errorf("opcuaserver: tag %q: %s", tag, dv.Status)
	}
	return dv.Value.Value(), nil
}

func (s *Server) lookup(tag string) (*ua.NodeID, DataType, error) {
	nodeID, ok := s.nodes[tag]
	if !ok {
		return nil, "", fmt.Errorf("opcuaserver: unknown tag %q", tag)
	}
	return nodeID, s.types[tag], nil
}

// Tags returns the tags the server was built from.
func (s *Server) Tags() []Tag { return slices.Clone(s.tags) }

// NamespaceIndex is the index the tags live under.
func (s *Server) NamespaceIndex() uint16 { return s.ns.ID() }

// NodeID is the full NodeID of a tag, as a client addresses it.
func (s *Server) NodeID(tag string) (string, error) {
	nodeID, _, err := s.lookup(tag)
	if err != nil {
		return "", err
	}
	return nodeID.String(), nil
}

// EndpointURLs lists the endpoint URLs advertised, without repeating one per
// security mode.
func (s *Server) EndpointURLs() []string {
	var urls []string
	for _, ep := range s.srv.Endpoints() {
		if !slices.Contains(urls, ep.EndpointURL) {
			urls = append(urls, ep.EndpointURL)
		}
	}
	return urls
}

// Endpoints returns the full endpoint descriptions, which is what a client
// picks its security from.
func (s *Server) Endpoints() []*ua.EndpointDescription { return s.srv.Endpoints() }

// Certificate is the certificate the secure endpoints use, or nil when security
// is off. A client has to trust it before it can use a secure endpoint.
func (s *Server) Certificate() *KeyPair { return s.keyPair }

// AddressSpace reports what was built: how many variables and folders.
func (s *Server) AddressSpace() BuildResult { return *s.build }

// RestoredValues is how many tag values were read back from the value file.
func (s *Server) RestoredValues() int { return s.restored }

// Warnings lists recoverable problems found while building the server, such as
// a value that could not be restored.
func (s *Server) Warnings() []string { return slices.Clone(s.warnings) }

// coerce converts a Go value to the type a tag's data type calls for, refusing
// a conversion that would change the value.
func coerce(dt DataType, v any) (any, error) {
	if v == nil {
		return nil, errors.New("value must not be nil")
	}

	switch dt {
	case TypeBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%s tag needs a bool, got %T", dt, v)
		}
		return b, nil

	case TypeString:
		switch x := v.(type) {
		case string:
			return x, nil
		case fmt.Stringer:
			return x.String(), nil
		}
		return nil, fmt.Errorf("%s tag needs a string, got %T", dt, v)

	case TypeDateTime:
		t, ok := v.(time.Time)
		if !ok {
			return nil, fmt.Errorf("%s tag needs a time.Time, got %T", dt, v)
		}
		return t, nil

	case TypeFloat, TypeDouble:
		f, err := toFloat(v)
		if err != nil {
			return nil, fmt.Errorf("%s tag: %w", dt, err)
		}
		if dt == TypeFloat {
			return float32(f), nil
		}
		return f, nil
	}

	i, err := toInt(v)
	if err != nil {
		return nil, fmt.Errorf("%s tag: %w", dt, err)
	}
	lo, hi, ok := intRange(dt)
	if !ok {
		return nil, fmt.Errorf("cannot convert %T to %s", v, dt)
	}
	if i < lo || i > hi {
		return nil, fmt.Errorf("%v is out of range for %s", v, dt)
	}

	switch dt {
	case TypeSByte:
		return int8(i), nil
	case TypeByte:
		return uint8(i), nil
	case TypeInt16:
		return int16(i), nil
	case TypeUInt16:
		return uint16(i), nil
	case TypeInt32:
		return int32(i), nil
	case TypeUInt32:
		return uint32(i), nil
	case TypeInt64:
		return i, nil
	case TypeUInt64:
		return uint64(i), nil
	}
	return nil, fmt.Errorf("cannot convert %T to %s", v, dt)
}

func toInt(v any) (int64, error) {
	switch x := v.(type) {
	case int:
		return int64(x), nil
	case int8:
		return int64(x), nil
	case int16:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case int64:
		return x, nil
	case uint:
		if uint64(x) > math.MaxInt64 {
			return 0, fmt.Errorf("%d does not fit in an int64", x)
		}
		return int64(x), nil
	case uint8:
		return int64(x), nil
	case uint16:
		return int64(x), nil
	case uint32:
		return int64(x), nil
	case uint64:
		if x > math.MaxInt64 {
			return 0, fmt.Errorf("%d does not fit in an int64", x)
		}
		return int64(x), nil
	case float32:
		return floatToInt(float64(x))
	case float64:
		return floatToInt(x)
	}
	return 0, fmt.Errorf("needs a number, got %T", v)
}

func floatToInt(f float64) (int64, error) {
	if f != math.Trunc(f) {
		return 0, fmt.Errorf("%v is not a whole number", f)
	}
	if math.IsNaN(f) || f < math.MinInt64 || f > math.MaxInt64 {
		return 0, fmt.Errorf("%v does not fit in an integer", f)
	}
	return int64(f), nil
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float32:
		return float64(x), nil
	case float64:
		return x, nil
	}
	i, err := toInt(v)
	if err != nil {
		return 0, err
	}
	return float64(i), nil
}

// intRange is the inclusive range a data type can hold, expressed in int64.
// UInt64 is capped at MaxInt64 because that is as far as int64 reaches; larger
// values have to be passed as a uint64 already of the right type.
func intRange(dt DataType) (lo, hi int64, ok bool) {
	switch dt {
	case TypeSByte:
		return math.MinInt8, math.MaxInt8, true
	case TypeByte:
		return 0, math.MaxUint8, true
	case TypeInt16:
		return math.MinInt16, math.MaxInt16, true
	case TypeUInt16:
		return 0, math.MaxUint16, true
	case TypeInt32:
		return math.MinInt32, math.MaxInt32, true
	case TypeUInt32:
		return 0, math.MaxUint32, true
	case TypeInt64:
		return math.MinInt64, math.MaxInt64, true
	case TypeUInt64:
		return 0, math.MaxInt64, true
	}
	return 0, 0, false
}

// SecurityModeName is a readable name for an OPC UA message security mode.
func SecurityModeName(m ua.MessageSecurityMode) string {
	switch m {
	case ua.MessageSecurityModeNone:
		return "None"
	case ua.MessageSecurityModeSign:
		return "Sign"
	case ua.MessageSecurityModeSignAndEncrypt:
		return "SignAndEncrypt"
	default:
		return m.String()
	}
}

// PolicyName strips the well-known prefix from a security policy URI.
func PolicyName(uri string) string {
	const prefix = "http://opcfoundation.org/UA/SecurityPolicy#"
	return strings.TrimPrefix(uri, prefix)
}
