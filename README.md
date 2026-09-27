# simple-opcua-server

An OPC UA server that publishes a list of tags, as a single binary and as a Go
library. Every tag starts at its data type's default value, clients can read and
write it, and the values can be persisted across restarts.

Built for testing, simulation and integration work: it offers an unsecured
endpoint next to signed and encrypted ones, accepts any anonymous client, and
binds to a local address by default.

```bash
go install github.com/moriax/simple-opcua-server/cmd/simple-opcua-server@latest
```

## Quick start

```bash
simple-opcua-server -config config/AddressSpace.example.csv
```

```
Loaded 59 tags from config/AddressSpace.example.csv
Generated a self-signed certificate
  cert.pem
  key.pem
  cert.der
  thumbprint (SHA-1) B767BC72D4A9E505F947E7CEBFB73011AEAD27C5
  subject             CN=SimpleOPCUAServer@myhost,O=simpleopcuaserver
  application URI     urn:myhost:simpleopcuaserver
  valid for           localhost, myhost, 127.0.0.1, ::1
  expires             2036-09-18T10:00:33Z
  A client must trust this certificate before it can use a secure endpoint.
Address space: 59 variables in 12 folders, namespace 2 (urn:simpleopcuaserver:tags)
Example NodeID: ns=2;s=t|MIX.MIXER1.BatchId
Listening on opc.tcp://127.0.0.1:4840
  opc.tcp://127.0.0.1:4840
    None             None
    Sign             Basic256Sha256
    SignAndEncrypt   Basic256Sha256
Accepting anonymous clients. Logging at info. Press Ctrl+C to stop.
```

Point a client at `opc.tcp://127.0.0.1:4840`, pick any of the three endpoints
with **Anonymous** authentication, and browse to
`Objects → urn:simpleopcuaserver:tags`.

The binary looks for `config/AddressSpace.csv` relative to the working directory
and then next to the executable, falling back to the example file shipped here,
so a fresh checkout runs without any setup.

## Using it as a library

```bash
go get github.com/moriax/simple-opcua-server
```

```go
package main

import (
	"log"

	opcuaserver "github.com/moriax/simple-opcua-server"
)

func main() {
	tags, _, err := opcuaserver.LoadTags("config/AddressSpace.example.csv", ".", false)
	if err != nil {
		log.Fatal(err)
	}

	srv, err := opcuaserver.New(tags,
		opcuaserver.WithPort(4840),
		opcuaserver.WithoutSecurity(),
	)
	if err != nil {
		log.Fatal(err)
	}

	if err := srv.Start(); err != nil {
		log.Fatal(err)
	}
	defer srv.Stop()

	srv.Set("t|MIX.MIXER1.BatchId", "BATCH-0001")
	srv.Set("t|MIX.MIXER1.FUNCTION.Temperature", 21.5)

	value, _ := srv.Get("t|MIX.MIXER1.BatchId")
	log.Println(value)

	select {}
}
```

`go run ./examples/embed` is a working program that does this and feeds a tag
from a ticker.

### API

| Call | Purpose |
| --- | --- |
| `New(tags, opts...)` | Build the address space. Nothing is listening yet. |
| `Start()` | Begin listening. Returns as soon as the server accepts connections. |
| `Set(tag, value)` | Write a tag value. |
| `Get(tag)` | Read a tag value back. |
| `Stop()` | Shut down, saving values one last time when persistence is on. |

A tag is named either by its NodeID identifier, as the tag file writes it
(`t|MIX.MIXER1.BatchId`), or by its dotted path (`MIX.MIXER1.BatchId`).

`Set` converts the value to the tag's declared data type and refuses a
conversion that would change it, so `Set("...MyTestTagInt16", 42)` stores an
`int16` while `40000` is rejected as out of range. It also ignores the tag's
access level: a tag that is read-only to *clients* is still writable by the
program hosting the server. `Set` works before `Start` too, so values can be
seeded before the first client connects.

Tags do not have to come from a file:

```go
tags := []opcuaserver.Tag{{
	FullPath: "SYS.Uptime",
	Path:     []string{"SYS", "Uptime"},
	Name:     "Uptime",
	DataType: opcuaserver.TypeDouble,
	Read:     true,
	Write:    true,
}}
```

Further options: `WithHost`, `WithAdvertise`, `WithNamespace`,
`WithNamespaceIndex`, `WithSeparator`, `WithNodeIDFormat`, `WithAuth`,
`WithCertificate`, `WithApplicationURI`, `WithPersistence`,
`WithoutPersistence`, `WithoutSecurity`, `WithLogger`, `WithSoftwareVersion`.
Accessors: `Tags`, `NodeID`, `NamespaceIndex`, `EndpointURLs`, `Endpoints`,
`Certificate`, `AddressSpace`, `RestoredValues`, `Warnings`.

## Options

| Flag | Default | Meaning |
| --- | --- | --- |
| `-config` | `config/AddressSpace.csv` | Path to the CSV tag file |
| `-host` | `127.0.0.1` | Address to bind and to advertise in the endpoint URL |
| `-advertise` | | Extra host names to advertise endpoints for, comma separated |
| `-port` | `4840` | TCP port |
| `-namespace` | `urn:simpleopcuaserver:tags` | URI of the namespace holding the tags |
| `-namespace-index` | `2` | Namespace index the tags appear under |
| `-nodeid` | `raw` | `raw` keeps the tag file's prefix in the NodeID, `path` drops it |
| `-separator` | `.` | Character separating the levels of a tag path |
| `-strict` | off | Fail on any problem in the tag file instead of warning and continuing |
| `-cert` | `cert.pem` | Server certificate, generated on first use |
| `-key` | `key.pem` | Server private key, generated on first use |
| `-appuri` | `urn:<hostname>:simpleopcuaserver` | Server ApplicationUri |
| `-no-security` | off | Offer only the unsecured endpoint and use no certificate |
| `-auth` | `anonymous` | Identity tokens to accept: `anonymous`, `username` or both |
| `-values` | `values.json` | File tag values are persisted in |
| `-no-persist` | off | Do not persist values; every restart begins at the defaults |
| `-save-interval` | `5s` | How often changed values are written to disk |
| `-log` | `info` | Log level: `off`, `error`, `warn`, `info`, `debug` or `trace` |
| `-logfile` | | Also append the log to this file |
| `-version` | | Print the version and exit |

`-host` is what keeps the server local. It is the only address the listener
binds to; passing something else (for example `0.0.0.0`) exposes the server on
the network, with no security in front of it.

## Tag file

A CSV file with a header row. `config/AddressSpace.example.csv` is a complete
one:

```csv
Tag Name,Data Type,AccessRights,Simulated
t|MIX.MIXER1.BatchId,IO_String,RW,FALSE
t|MIX.MIXER1.Status,IO_Int16,RW,FALSE
t|MIX.MIXER1.FUNCTION.Temperature,IO_Double,RW,FALSE
```

* **Tag Name** — anything before a `|` is a source-system prefix and is
  stripped, so `t|MIX.MIXER1.BatchId` becomes the tag path `MIX.MIXER1.BatchId`.
  Each level of the path becomes a folder in the address space.
* **Data Type** — an `IO_` prefix is optional. Recognised: `Boolean`, `SByte`,
  `Byte`, `Int16`, `UInt16`, `Int32`, `UInt32`, `Int64`, `UInt64`, `Float`,
  `Double`, `String`, `DateTime`, plus the usual PLC aliases (`BOOL`, `DINT`,
  `REAL`, `LREAL`, `WORD`, `DWORD`, …). An unrecognised type falls back to
  `String` with a warning, or aborts startup under `-strict`.
* **AccessRights** — `RW`, `R`/`RO` or `W`. Empty means `RW`.
* **Simulated** — parsed but not acted on; no values change on their own.

Columns are matched by header name, so their order does not matter and extra
columns are ignored. A file with no recognisable header is read positionally as
name, type, access, simulated. Blank lines and lines starting with `#` are
skipped. CRLF endings and a missing final newline are both fine.

Real exports are messy, and the parser reports rather than refuses: a duplicate
tag keeps its first definition, an unknown data type becomes a `String`. The
example file deliberately contains both defects so that the handling stays
covered by the tests. `-strict` turns every such warning into a startup failure.

`config/*.csv` is gitignored apart from the example, because a plant's tag list
is usually not something to publish.

### Address space layout

`t|MIX.MIXER1.FUNCTION.Speed,IO_Int16,RW,FALSE` produces:

```
Objects
└── urn:simpleopcuaserver:tags     ns=2;i=85
    └── MIX                        ns=2;s=MIX                          FolderType
        └── MIXER1                 ns=2;s=MIX.MIXER1                   FolderType
            └── FUNCTION           ns=2;s=MIX.MIXER1.FUNCTION          FolderType
                └── Speed          ns=2;s=t|MIX.MIXER1.FUNCTION.Speed  Int16, AccessLevel 3
```

A tag's NodeID is `ns=<index>;s=<identifier>`, so you can address any tag
without browsing. The identifier is the first CSV column verbatim, prefix
included, which matches the identifiers a source system that exported the file
already uses. Pass `-nodeid path` to strip the prefix instead. Folders are not
tags and always use the plain dotted path.

The tags sit at namespace index 2 because index 1 conventionally belongs to the
server's own ApplicationUri; `-namespace-index` moves them.

The BrowseName is the last path segment and the Description carries the full
path, both without the prefix.

On a first run the initial values are the type default — `0`, `false` or `""` —
with StatusCode `Good` and source and server timestamps set at startup. After
that they are whatever was last written.

## Persistence

Values written by a client or through `Set` survive a restart. They are kept in
`values.json` next to the executable, written whenever a tag changes and once
more on shutdown, so a value written a moment before Ctrl+C is not lost.
`-values` moves the file, `-save-interval` changes how often it is written, and
`-no-persist` turns the whole thing off.

The file is plain JSON, sorted by NodeID so that successive saves diff cleanly,
and safe to read or edit while the server is stopped:

```json
{
  "version": 1,
  "saved": "2026-09-21T09:29:48Z",
  "values": {
    "t|MIX.MIXER1.BatchId": { "type": "String", "value": "BATCH-0001" },
    "t|MIX.MIXER1.Status": { "type": "Int16", "value": 0 }
  }
}
```

Each value records the data type it was saved under. On startup a value is
restored only if its tag is still in the tag file and still has that type;
anything else is reported and the tag falls back to its default. So editing the
tag file is safe, and **to start clean, delete `values.json`** or pass
`-no-persist`.

Values are written atomically, so an interrupted save leaves the previous file
intact rather than a truncated one. 64 bit integers keep their exact value, and
a `NaN` or infinity written to a Float or Double tag is stored as a string
because JSON numbers cannot express one.

## Security

Three endpoints are offered on the same URL:

| Security mode | Policy |
| --- | --- |
| `None` | `None` |
| `Sign` | `Basic256Sha256` |
| `SignAndEncrypt` | `Basic256Sha256` |

The deprecated `Basic128Rsa15` and `Basic256` policies are deliberately not
offered. All three endpoints accept **anonymous** clients; there is no user
authentication, and client certificates are accepted without being checked
against a trust list.

On first run the server generates a self-signed certificate and key and reuses
them afterwards, so clients only have to trust it once. They are written next to
the executable, or to the working directory if that is not writable, and the
startup output names the exact paths.

### Trusting the certificate

**A client refuses a secure endpoint until it trusts this server's
certificate**, usually reporting `BadCertificateUntrusted`. The server prints
the certificate's SHA-1 thumbprint at startup, and clients name the file they
store a certificate in after exactly that, which is how you find it among the
others. The certificate is also written as `cert.der`, the form a trust store
wants:

* **UaExpert** — press *Trust Server Certificate* in the connect dialog.
* **Anything following the OPC UA PKI layout** — the rejected certificate
  arrives as `<thumbprint>.der` under `.../rejected/certs`. Move it to
  `.../trusted/certs`, or copy `cert.der` there under that name, and restart the
  client. Siemens SIMATIC IT Unified keeps these under
  `...\config\opcuaclientcerts\<connection id>\`.

Moving the file is usually not enough on its own: most clients read their trust
list once at startup, so the client, driver or runtime has to be restarted
afterwards rather than just reconnected.

The certificate is an end entity certificate — `CA:FALSE`, no `keyCertSign` —
as an OPC UA application instance certificate has to be. A certificate marked
`CA:TRUE` is filed by a client as a certificate authority rather than as this
server's own certificate, and then rejected for having no revocation list, no
matter which folder it is put in. The server warns on startup if it finds such a
certificate; deleting `cert.pem`, `cert.der` and `key.pem` replaces it.

`-cert` and `-key` point at your own PEM files instead. `-no-security` drops
back to a single unsecured endpoint and touches no certificate at all.

### User identity

`-auth` selects the identity tokens the endpoints offer. `anonymous`, the
default, lets any client in. Adding `username` also advertises a UserName token
on the secure endpoints, for clients that insist on supplying credentials — but
**the credentials are not checked**, because there is no user database behind
them. It makes such a client connect; it does not authenticate anyone.

## Logging

`-log` controls how much the server reports, and `-logfile` copies the same
output to a file:

```bash
simple-opcua-server -log debug -logfile opcua.log
```

| Level | What it adds |
| --- | --- |
| `off` | Nothing |
| `error`, `warn` | Failures only |
| `info` | Every endpoint advertised at startup, and each connection accepted |
| `debug` | Every service request, plus the protocol handshake |
| `trace` | The above, plus a dump of every message the codec decodes |

`debug` is the level to use when a client cannot connect. It records the two
things that decide whether a connection succeeds — the URL the client dialled
and the security policy it asked for:

```
WIRE  uacp 1: recv &uacp.Hello{..., EndpointURL:"opc.tcp://127.0.0.1:4840"}
INFO  registered connection: 127.0.0.1:40902
WIRE  uasc 3: setting securityPolicy to ...SecurityPolicy#Basic256Sha256
WIRE  handleOpenSecureChannelRequest: Got OPN Request
DEBUG Handling *ua.GetEndpointsRequest
```

A client that fetches the endpoint list and then disconnects without ever
opening a secure channel rejected the server on its own, using the endpoint list
logged at startup. Watch the size of the `GetEndpointsResponse`: a response of a
few dozen bytes is an empty endpoint list, and a client that receives one
reports it as a rejected security policy.

## Discovery and endpoint URLs

The server binds only to `-host`, but a client identifies an endpoint by the URL
it dialled. `-advertise` adds further URLs for the same listener, for a client
that reaches the server under another name:

```bash
simple-opcua-server -host 127.0.0.1 -advertise localhost
```

This matters beyond cosmetics. `CreateSession` answers with the endpoints
matching the URL the client asked for, and a client must compare that list
against the one `GetEndpoints` gave it — that comparison is what detects a
man-in-the-middle downgrading security. A client dialling a URL the server never
advertises receives an empty list and abandons the session.

Port 4840 is also where a Local Discovery Server normally listens, so other OPC
UA software on the machine may try to register itself here and log a fault when
this server turns out not to be one. `-port` moves the server out of the way.

`GetEndpoints` returns every endpoint whose transport the client speaks,
regardless of the URL it asked for, because OPC UA Part 4 §5.4.4 treats that URL
as a hint about which addresses to report rather than a filter.

This server replaces gopcua's implementation of that service. gopcua compares
the requested URL against the advertised ones as exact strings, so a client
dialling `opc.tcp://host:4840/` receives an empty list from a server advertising
`opc.tcp://host:4840`. Clients turn that empty list into
`Bad_SecurityPolicyRejected`, since from their side no endpoint met their
security requirements — a confusing error for a trailing slash. gopcua's own
session service already trims the slash before matching, so only discovery was
affected.

## Building

Go 1.23 or newer. `build.sh` (or `build.ps1` on Windows) cross-compiles static,
dependency-free binaries for Linux and Windows on amd64 and arm64 into `dist/`,
with a copy of the example tag file alongside:

```bash
./build.sh
```

For a single host:

```bash
go build -o simple-opcua-server ./cmd/simple-opcua-server
```

```bash
go test ./...
```

## Known limitations

* **Timestamps are lost on write.** The server stores the `DataValue` a client
  sends verbatim. A client that writes only a value, with no source timestamp,
  will read that tag back without timestamps until the server restarts. This is
  how `gopcua`'s write path behaves and cannot be changed from outside the
  library. `Set` always stamps the values it writes.
* **No client authentication.** Anyone who can reach the port can read and write
  every tag, on the secure endpoints too. Encryption here protects the traffic,
  not the server.
* **No simulation.** The `Simulated` column is parsed but values only ever
  change because a client or the hosting program wrote them.
* **No concurrency guarantees on a single node.** `gopcua` does not lock an
  individual node across a simultaneous read and write of the same tag from two
  different sessions. This is a pre-existing upstream issue; it has not caused a
  problem in practice for a local test server, but it is a reason not to put
  this on a network. Saving values reads every node on a timer, so it shares
  that same unsynchronised read.

## Built on

[gopcua](https://github.com/gopcua/opcua), MIT licensed.

## License

MIT, see [LICENSE](LICENSE).
