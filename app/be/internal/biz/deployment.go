package biz

type DeploymentRepo interface{}

type deploymentUseCase struct {
	repo DeploymentRepo
}

func NewDeploymentUsecase(repo DeploymentRepo) DeploymentUseCase {
	return &deploymentUseCase{repo: repo}
}
