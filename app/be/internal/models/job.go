package models

type JobStatusResp struct {
	JobID    string   `json:"job_id"`
	Type     string   `json:"type"`
	Status   string   `json:"status"`
	Progress int      `json:"progress"`
	Logs     []string `json:"logs"`
	Result   any      `json:"result"`
}
