package opcuaserver

import (
	"context"
	"testing"

	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
)

func endpointsHandler(t *testing.T) server.Handler {
	t.Helper()
	srv := server.New(
		server.EndPoint("127.0.0.1", 0),
		server.EnableSecurity("None", ua.MessageSecurityModeNone),
		server.EnableAuthMode(ua.UserTokenTypeAnonymous),
	)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })

	logger, _, err := NewLogger(LogOff, "")
	if err != nil {
		t.Fatal(err)
	}
	return GetEndpointsHandler(srv, logger)
}

func getEndpoints(t *testing.T, h server.Handler, req *ua.GetEndpointsRequest) *ua.GetEndpointsResponse {
	t.Helper()
	if req.RequestHeader == nil {
		req.RequestHeader = &ua.RequestHeader{RequestHandle: 42}
	}
	resp, err := h(nil, req, 1)
	if err != nil {
		t.Fatal(err)
	}
	ge, ok := resp.(*ua.GetEndpointsResponse)
	if !ok {
		t.Fatalf("handler returned %T, want *ua.GetEndpointsResponse", resp)
	}
	return ge
}

// A client that dials "opc.tcp://host:port/" must get the same endpoints as one
// that dials "opc.tcp://host:port". gopcua compares the two as exact strings and
// answers the first with an empty list, which clients report as
// Bad_SecurityPolicyRejected.
func TestGetEndpointsIgnoresTrailingSlash(t *testing.T) {
	h := endpointsHandler(t)

	base := getEndpoints(t, h, &ua.GetEndpointsRequest{})
	if len(base.Endpoints) == 0 {
		t.Fatal("no endpoints advertised at all")
	}
	want := len(base.Endpoints)
	url := base.Endpoints[0].EndpointURL

	for _, req := range []string{url, url + "/", url + "///", "OPC.TCP" + url[len("opc.tcp"):]} {
		got := getEndpoints(t, h, &ua.GetEndpointsRequest{EndpointURL: req})
		if len(got.Endpoints) != want {
			t.Errorf("GetEndpoints(%q) returned %d endpoints, want %d", req, len(got.Endpoints), want)
		}
	}
}

// Reached through a gateway or a port forward, the client's URL is one the
// server never advertised. Returning nothing strands it; returning what we have
// lets it connect.
func TestGetEndpointsFallsBackForUnknownHost(t *testing.T) {
	h := endpointsHandler(t)
	want := len(getEndpoints(t, h, &ua.GetEndpointsRequest{}).Endpoints)

	got := getEndpoints(t, h, &ua.GetEndpointsRequest{EndpointURL: "opc.tcp://gateway.example:4840/"})
	if len(got.Endpoints) != want {
		t.Errorf("unknown host returned %d endpoints, want the full list of %d", len(got.Endpoints), want)
	}
}

func TestGetEndpointsHonoursProfileURIs(t *testing.T) {
	h := endpointsHandler(t)
	want := len(getEndpoints(t, h, &ua.GetEndpointsRequest{}).Endpoints)

	got := getEndpoints(t, h, &ua.GetEndpointsRequest{ProfileURIs: []string{transportProfile}})
	if len(got.Endpoints) != want {
		t.Errorf("matching profile returned %d endpoints, want %d", len(got.Endpoints), want)
	}

	got = getEndpoints(t, h, &ua.GetEndpointsRequest{
		ProfileURIs: []string{"http://opcfoundation.org/UA-Profile/Transport/https-uabinary"},
	})
	if len(got.Endpoints) != 0 {
		t.Errorf("a transport this server does not speak returned %d endpoints, want 0", len(got.Endpoints))
	}
}

func TestGetEndpointsEchoesRequestHandle(t *testing.T) {
	h := endpointsHandler(t)
	resp := getEndpoints(t, h, &ua.GetEndpointsRequest{
		RequestHeader: &ua.RequestHeader{RequestHandle: 4711},
	})
	if resp.ResponseHeader.RequestHandle != 4711 {
		t.Errorf("RequestHandle = %d, want 4711", resp.ResponseHeader.RequestHandle)
	}
	if resp.ResponseHeader.ServiceResult != ua.StatusOK {
		t.Errorf("ServiceResult = %v, want Good", resp.ResponseHeader.ServiceResult)
	}
}

func TestGetEndpointsRejectsWrongRequestType(t *testing.T) {
	h := endpointsHandler(t)
	if _, err := h(nil, &ua.ReadRequest{}, 1); err == nil {
		t.Error("a non-GetEndpoints request should be an error")
	}
}

func TestTrimURL(t *testing.T) {
	cases := map[string]string{
		"opc.tcp://H:4840/":   "opc.tcp://h:4840",
		"opc.tcp://h:4840///": "opc.tcp://h:4840",
		" opc.tcp://h:4840 ":  "opc.tcp://h:4840",
		"":                    "",
	}
	for in, want := range cases {
		if got := trimURL(in); got != want {
			t.Errorf("trimURL(%q) = %q, want %q", in, got, want)
		}
	}
}
