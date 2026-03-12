package data

import "github.com/Fl0rencess720/agentland/app/be/internal/biz"

type jobRepo struct{}

func NewJobRepo() biz.JobRepo {
	return &jobRepo{}
}
