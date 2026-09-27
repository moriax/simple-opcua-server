// Command embed shows how to run the OPC UA server inside another Go program.
//
//	go run ./examples/embed
package main

import (
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	opcuaserver "github.com/moriax/simple-opcua-server"
)

func main() {
	// Tags can come from a CSV file...
	tags, warnings, err := opcuaserver.LoadTags("config/AddressSpace.example.csv", ".", false)
	if err != nil {
		log.Fatal(err)
	}
	for _, w := range warnings {
		log.Printf("warning: %s", w)
	}

	// ...or be built in code:
	tags = append(tags,
		opcuaserver.Tag{
			FullPath: "SYS.Uptime", Path: []string{"SYS", "Uptime"}, Name: "Uptime",
			DataType: opcuaserver.TypeDouble, Read: true, Write: true,
		},
	)

	srv, err := opcuaserver.New(tags,
		opcuaserver.WithHost("127.0.0.1"),
		opcuaserver.WithPort(4840),
		opcuaserver.WithoutSecurity(),
		opcuaserver.WithoutPersistence(),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Values can be seeded before the server accepts a single connection.
	if err := srv.Set("t|MIX.MIXER1.BatchId", "BATCH-0001"); err != nil {
		log.Fatal(err)
	}

	if err := srv.Start(); err != nil {
		log.Fatal(err)
	}
	defer srv.Stop()

	for _, u := range srv.EndpointURLs() {
		fmt.Println("listening on", u)
	}
	fmt.Println("press Ctrl+C to stop")

	// Feed the address space from whatever the program is actually doing.
	started := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case now := <-ticker.C:
			if err := srv.Set("SYS.Uptime", now.Sub(started).Seconds()); err != nil {
				log.Printf("set uptime: %s", err)
			}
			// Set converts to the tag's declared type, so a plain float works
			// on a Double tag and a plain int on an Int16 one.
			if err := srv.Set("t|MIX.MIXER1.FUNCTION.Temperature", 20+5*math.Sin(float64(now.Unix())/10)); err != nil {
				log.Printf("set temperature: %s", err)
			}
		case <-stop:
			fmt.Println("\nstopping")
			return
		}
	}
}
