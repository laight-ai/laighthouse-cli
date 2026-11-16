package main

import (
	"os"

	"github.com/laight-ai/laighthouse-cli/cli"
)

// goreleaser가 ldflags로 채운다.
var version = "dev"

func main() {
	os.Exit(cli.Main(cli.Options{Name: "laighthouse-cli", Version: version}, os.Args[1:]))
}
