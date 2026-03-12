package data

import "github.com/Fl0rencess720/agentland/app/be/internal/biz"

type fileRepo struct{}

func NewFileRepo() biz.FileRepo {
	return &fileRepo{}
}
