package models

import "mime/multipart"

type FileUploadReq struct {
	File    *multipart.FileHeader `form:"file" binding:"required"`
	Purpose string                `form:"purpose" binding:"required"`
}

type FileUploadResp struct {
	FileID   string `json:"file_id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type"`
}

type FileMetadataResp struct {
	FileID      string `json:"file_id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	MimeType    string `json:"mime_type"`
	DownloadURL string `json:"download_url"`
}
