package main

import (
	"fmt"
	"log"
	"os"

	"github.com/balaji-balu/ieo/internal/install"
)

func main() {
	if len(os.Args) < 2 {
		log.Println("Usage: installer <install|uninstall|status> [--version vX.Y.Z]")
		os.Exit(1)
	}

	version := install.ParseVersionFlag(os.Args)

	switch os.Args[1] {
	case "install":
		install.RunInstall(version)
	case "uninstall":
		install.RunUninstall()
	case "status":
		install.RunStatus()
	default:
		fmt.Println("Unknown command:", os.Args[1])
	}
}
