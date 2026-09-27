# simple-opcua-server

An OPC UA server that publishes a list of tags, usable both as a single binary
and as a Go library.

You give it a CSV file of tag names and data types — the kind a SCADA, MES or
HMI system exports — and it serves them as a browsable OPC UA address space that
any client can read from and write to. Useful for testing an OPC UA client
without the real plant behind it, for simulating a machine during integration
work, or for putting an OPC UA interface in front of a Go program.

```bash
go install github.com/moriax/simple-opcua-server/cmd/simple-opcua-server@latest
simple-opcua-server
```

Then connect a client to `opc.tcp://127.0.0.1:4840` and browse to
`Objects → urn:simpleopcuaserver:tags`.

## Features

- **Tags from a CSV file.** Dotted tag names become a folder hierarchy, one
  Variable node per tag, each addressable directly as `ns=2;s=<identifier>`.
  Messy exports are handled: duplicate rows, unknown data types and CRLF
  endings are reported rather than refused.
- **Read and write.** Values start at the data type's default and can be
  changed by any client, or by the hosting program.
- **Persistence.** Values survive a restart, kept in a readable JSON file that
  records the data type each one was saved under.
- **Security, or none.** Unsecured, `Sign` and `SignAndEncrypt` endpoints with
  `Basic256Sha256`, against a self-signed certificate generated on first run.
  One flag turns security off entirely.
- **Embeddable.** `New` / `Start` / `Set` / `Get` / `Stop` let another Go
  program host the server and feed the tags from its own data.
- **One binary, no dependencies.** Static builds for Linux and Windows on amd64
  and arm64. Nothing to install on the target machine.
- **Local by default.** Binds to `127.0.0.1` unless told otherwise.
- **Diagnosable.** Log levels from `off` to a full protocol trace, because most
  OPC UA connection failures are invisible from the client side.

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

	srv, err := opcuaserver.New(tags, opcuaserver.WithPort(4840))
	if err != nil {
		log.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		log.Fatal(err)
	}
	defer srv.Stop()

	srv.Set("t|MIX.MIXER1.BatchId", "BATCH-0001")
	value, _ := srv.Get("t|MIX.MIXER1.BatchId")
	log.Println(value)

	select {}
}
```

| Call | Purpose |
| --- | --- |
| `New(tags, opts...)` | Build the address space. Nothing is listening yet. |
| `Start()` | Begin listening. Returns as soon as the server accepts connections. |
| `Set(tag, value)` | Write a tag value. |
| `Get(tag)` | Read a tag value back. |
| `Stop()` | Shut down, saving values one last time when persistence is on. |

A tag is named by its NodeID identifier as the tag file writes it
(`t|MIX.MIXER1.BatchId`) or by its dotted path (`MIX.MIXER1.BatchId`). `Set`
converts the value to the tag's declared data type and refuses a conversion
that would change it. Tags can also be built in code instead of read from a
file.

`go run ./examples/embed` is a working program that does this and feeds a tag
from a ticker.

## Tag file

```csv
Tag Name,Data Type,AccessRights,Simulated
t|MIX.MIXER1.BatchId,IO_String,RW,FALSE
t|MIX.MIXER1.Status,IO_Int16,RW,FALSE
t|MIX.MIXER1.FUNCTION.Temperature,IO_Double,RW,FALSE
```

Anything before a `|` is a source-system prefix. Each level of the dotted path
becomes a folder, so the last line above is reachable as
`MIX → MIXER1 → FUNCTION → Temperature` and addressable as
`ns=2;s=t|MIX.MIXER1.FUNCTION.Temperature`.

`config/AddressSpace.example.csv` is a complete example and is what the server
falls back to when no tag file of your own is present. Real tag files are
gitignored, since a plant's tag list is usually not something to publish.

## Documentation

**[docs/reference.md](docs/reference.md)** covers everything else: every
command line flag, the tag file format in full, persistence, security and how
to get a client to trust the certificate, logging, and the endpoint URL
behaviour that decides whether a client can connect at all.

## Building

Go 1.23 or newer.

```bash
go build -o simple-opcua-server ./cmd/simple-opcua-server
go test ./...
```

`./build.sh` (or `build.ps1` on Windows) cross-compiles static binaries for
Linux and Windows on amd64 and arm64 into `dist/`.

## Limitations

This is a simulator and a test server, not a production OPC UA server.

- **No user authentication.** Anyone who can reach the port can read and write
  every tag. Encryption protects the traffic, not the server.
- **Values do not change on their own.** The `Simulated` column is parsed but
  not acted on; something has to write them.
- **No locking on a single node.** An upstream limitation of the OPC UA library
  used here, and another reason to keep this off a network.

## Built on

[gopcua](https://github.com/gopcua/opcua), MIT licensed.

## License

MIT, see [LICENSE](LICENSE).
