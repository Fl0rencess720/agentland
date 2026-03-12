//go:build wireinject
// +build wireinject

package main

import (
	"github.com/google/wire"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/data"
	"github.com/Fl0rencess720/agentland/app/be/internal/service"
)

type App struct {
	HTTPServer *service.HTTPServer
}

func NewApp(httpServer *service.HTTPServer) *App {
	return &App{HTTPServer: httpServer}
}

func wireApp() *App {
	panic(wire.Build(
		NewApp,
		service.ProviderSet,
		biz.ProviderSet,
		data.ProviderSet,
	))
}
