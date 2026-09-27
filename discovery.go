package opcuaserver

import (
	"fmt"
	"strings"
	"time"

	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
)

// transportProfile is the only transport this server speaks.
const transportProfile = "http://opcfoundation.org/UA-Profile/Transport/uatcp-uasc-uabinary"

// GetEndpointsHandler replaces gopcua's GetEndpoints service.
//
// The library compares the EndpointUrl a client asks for against the advertised
// URLs as exact strings, so a client that dials "opc.tcp://host:4840/" gets an
// empty endpoint list from a server advertising "opc.tcp://host:4840". Clients
// report that empty list as Bad_SecurityPolicyRejected, because from their side
// no endpoint met their security requirements. gopcua's own session service
// already trims the trailing slash before matching; this does the same, and
// falls back to returning every endpoint.
//
// OPC UA Part 4 §5.4.4 makes the requested URL a hint: a server uses it to
// decide which of its addresses to report and "should return a suitable default
// URL if it does not recognize the HostName in the URL". Returning nothing is
// never the right answer.
func GetEndpointsHandler(srv *server.Server, logger *Logger) server.Handler {
	return func(sc *uasc.SecureChannel, r ua.Request, reqID uint32) (ua.Response, error) {
		req, ok := r.(*ua.GetEndpointsRequest)
		if !ok {
			return nil, fmt.Errorf("GetEndpoints: unexpected request type %T", r)
		}

		all := srv.Endpoints()
		eps := all

		// A client may restrict the answer to transports it speaks.
		if len(req.ProfileURIs) > 0 && !containsFold(req.ProfileURIs, transportProfile) {
			logger.Info("GetEndpoints: client asked for transport profiles %v, this server only offers %s",
				req.ProfileURIs, transportProfile)
			eps = nil
		} else if want := trimURL(req.EndpointURL); want != "" {
			var matched []*ua.EndpointDescription
			for _, ep := range all {
				if trimURL(ep.EndpointURL) == want {
					matched = append(matched, ep)
				}
			}
			if len(matched) > 0 {
				eps = matched
			} else {
				// The client reached us on an address we do not advertise, for
				// example through a gateway or a port forward. Report what we
				// have rather than nothing.
				logger.Info("GetEndpoints: client asked for %q, which is not an advertised URL; returning all %d endpoint(s)",
					req.EndpointURL, len(all))
			}
		}

		logger.Debug("GetEndpoints: requested %q, returning %d endpoint(s)", req.EndpointURL, len(eps))

		return &ua.GetEndpointsResponse{
			ResponseHeader: &ua.ResponseHeader{
				Timestamp:          time.Now(),
				RequestHandle:      req.RequestHeader.RequestHandle,
				ServiceResult:      ua.StatusOK,
				ServiceDiagnostics: &ua.DiagnosticInfo{},
				StringTable:        []string{},
				AdditionalHeader:   ua.NewExtensionObject(nil),
			},
			Endpoints: eps,
		}, nil
	}
}

// trimURL normalises an endpoint URL for comparison: case and a trailing slash
// carry no meaning in one.
func trimURL(s string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/"))
}

func containsFold(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(strings.TrimSpace(s), needle) {
			return true
		}
	}
	return false
}
