package data

import "github.com/Fl0rencess720/agentland/app/be/internal/biz"

type deploymentRepo struct{}

func NewDeploymentRepo() biz.DeploymentRepo {
	return &deploymentRepo{}
}
