package main

import (
	"os"
	//"log"
	 "github.com/joho/godotenv"
	"github.com/balaji-balu/ieo/cmd/edgectl/cmd"
)

func init() {
    envPath := os.Getenv("EDGECTL_ENV_PATH")
	//log.Println("inside cli", envPath)
    if envPath != "" {
        godotenv.Load(envPath)
    } else {
        godotenv.Load(".env")
    }
}

func main() {
	cmd.Execute()
}
