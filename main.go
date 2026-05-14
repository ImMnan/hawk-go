package main

import (
	"log/slog"

	"github.com/immnan/hawk/pkg"
)

func main() {
	pkg.InitLogger()
	slog.Info("initializing hawk")
	pkg.Run()

}
