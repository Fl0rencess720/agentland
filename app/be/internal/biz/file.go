package biz

type FileRepo interface{}

type fileUseCase struct {
	repo FileRepo
}

func NewFileUsecase(repo FileRepo) FileUseCase {
	return &fileUseCase{repo: repo}
}
