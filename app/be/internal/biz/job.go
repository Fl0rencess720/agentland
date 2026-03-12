package biz

type JobRepo interface{}

type jobUseCase struct {
	repo JobRepo
}

func NewJobUsecase(repo JobRepo) JobUseCase {
	return &jobUseCase{repo: repo}
}
